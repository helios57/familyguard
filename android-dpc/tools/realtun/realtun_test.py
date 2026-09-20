#!/usr/bin/env python3
"""Run the ad filter against a real kernel, a real resolver and a real DNS client.

Every other test of this filter feeds it bytes the repo wrote.  That shows the parsers agree with
the spec and says nothing about whether the packets the filter *emits* are ones a network stack
will accept -- and that failure is silent.  A wrong checksum, a wrong length, a wrong address: the
peer drops the packet without a word, the query goes unanswered, the app retries, and what a parent
sees is a game that takes ten seconds to open.

So this puts a real TUN device in a network namespace, runs dnsmasq as the resolver and dig as the
client, and runs the Kotlin filter in between.  Nothing is stubbed but the transport between the
tun device and the JVM, which is a length-prefixed pipe rather than the descriptor itself; the
bytes are identical.

It carries its own calibration.  The same query is run twice -- once with the rule loaded and once
with an empty rule file -- and BOTH arms must behave differently.  A test that only asserted
NXDOMAIN would pass just as well if the tunnel dropped every packet it did not understand, which is
exactly the bug worth catching.

    sudo unshare -n python3 tools/realtun/realtun_test.py --classpath <path>

Exit 0 both arms behaved, 1 measured wrong, 2 could not measure.
"""

from __future__ import annotations

import argparse
import fcntl
import os
import re
import select
import shutil
import struct
import subprocess
import sys
import threading
import time

TUN_NAME = "fgtun0"
TUN_ADDRESS = "10.99.99.1"
TUN_PREFIX = 24
# The address dig is pointed at.  Any address inside the tun's subnet reaches the tunnel, because a
# tun device is NOARP and the kernel sends on-link traffic straight out of it.
FAKE_RESOLVER = "10.99.99.53"

UPSTREAM_HOST = "127.0.0.1"
UPSTREAM_PORT = 5353

# The two names the arms turn on.  dnsmasq answers BOTH, so a blocked answer can only have come
# from the filter -- never from the resolver simply not knowing the name.
BLOCKED_NAME = "ads.example.com"
BLOCKED_ANSWER = "198.51.100.7"
ALLOWED_NAME = "content.example.org"
ALLOWED_ANSWER = "203.0.113.9"

TUNSETIFF = 0x400454CA
IFF_TUN = 0x0001
IFF_NO_PI = 0x1000


class NotMeasured(Exception):
    """Something the test needs was missing.  Not a finding about the filter."""


def run(*argv: str, check: bool = True) -> subprocess.CompletedProcess:
    result = subprocess.run(argv, capture_output=True, text=True)
    if check and result.returncode != 0:
        raise NotMeasured(f"{' '.join(argv)} failed rc={result.returncode}: {result.stderr.strip()}")
    return result


def open_tun() -> int:
    try:
        fd = os.open("/dev/net/tun", os.O_RDWR)
    except OSError as exc:
        raise NotMeasured(f"cannot open /dev/net/tun: {exc}") from exc
    request = struct.pack("16sH", TUN_NAME.encode(), IFF_TUN | IFF_NO_PI)
    try:
        fcntl.ioctl(fd, TUNSETIFF, request)
    except OSError as exc:
        os.close(fd)
        raise NotMeasured(f"TUNSETIFF failed (needs CAP_NET_ADMIN in this namespace): {exc}") from exc
    run("ip", "link", "set", "lo", "up")
    run("ip", "addr", "add", f"{TUN_ADDRESS}/{TUN_PREFIX}", "dev", TUN_NAME)
    run("ip", "link", "set", TUN_NAME, "up")
    return fd


def start_dnsmasq() -> subprocess.Popen:
    binary = shutil.which("dnsmasq") or "/usr/sbin/dnsmasq"
    if not os.path.exists(binary):
        raise NotMeasured("dnsmasq is not installed; there is no resolver to forward to")
    process = subprocess.Popen(
        [
            binary, "--keep-in-foreground", "--no-daemon", "--user=root",
            f"--port={UPSTREAM_PORT}", f"--listen-address={UPSTREAM_HOST}", "--bind-interfaces",
            "--no-resolv", "--no-hosts", "--cache-size=0",
            f"--address=/{BLOCKED_NAME}/{BLOCKED_ANSWER}",
            f"--address=/{ALLOWED_NAME}/{ALLOWED_ANSWER}",
        ],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    # Prove it is actually answering before anything else is measured, so "dig got nothing" can
    # never be read as a finding about the filter when the resolver never came up.
    deadline = time.time() + 10
    while time.time() < deadline:
        if process.poll() is not None:
            raise NotMeasured(f"dnsmasq exited: {(process.stdout.read() or '').strip()}")
        probe = subprocess.run(
            ["dig", "+time=1", "+tries=1", "+short", f"@{UPSTREAM_HOST}", "-p", str(UPSTREAM_PORT), ALLOWED_NAME],
            capture_output=True, text=True,
        )
        if ALLOWED_ANSWER in probe.stdout:
            return process
        time.sleep(0.2)
    process.kill()
    raise NotMeasured("dnsmasq never answered its own probe")


class Tunnel:
    """Pumps packets between the tun device and the harness process."""

    def __init__(self, tun_fd: int, classpath: str, rules_path: str) -> None:
        self.tun_fd = tun_fd
        self.stop = threading.Event()
        self.harness = subprocess.Popen(
            [
                "java", "-cp", classpath,
                "io.github.helios57.familyguard.filter.TunLoopHarness",
                rules_path, UPSTREAM_HOST, str(UPSTREAM_PORT),
            ],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.log: list[str] = []
        self.threads = [
            threading.Thread(target=self._tun_to_harness, daemon=True),
            threading.Thread(target=self._harness_to_tun, daemon=True),
            threading.Thread(target=self._drain_stderr, daemon=True),
        ]
        for thread in self.threads:
            thread.start()
        self._await_ready()

    def _await_ready(self) -> None:
        deadline = time.time() + 30
        while time.time() < deadline:
            if any("harness: ready" in line for line in self.log):
                return
            if self.harness.poll() is not None:
                raise NotMeasured("the harness exited before it was ready:\n" + "\n".join(self.log))
            time.sleep(0.1)
        raise NotMeasured("the harness never reported ready:\n" + "\n".join(self.log))

    def _drain_stderr(self) -> None:
        for raw in self.harness.stderr:
            self.log.append(raw.decode(errors="replace").rstrip())

    def _tun_to_harness(self) -> None:
        # select() rather than a bare blocking read, because the tun fd OUTLIVES this tunnel: both
        # arms share one device.  A thread parked in os.read() cannot be told to stop, so the first
        # arm's reader was still on the fd when the second arm started, the two raced for each
        # packet, and the loser wrote into a dead harness's stdin and swallowed the query.  That
        # cost one NO-REPLY in the calibration arm -- reported as "the filter did not resolve it",
        # which is a finding about this file.  It is also intermittent, so it reads as a flaky
        # filter rather than a broken harness.
        while not self.stop.is_set():
            readable, _, _ = select.select([self.tun_fd], [], [], 0.2)
            if not readable:
                continue
            try:
                packet = os.read(self.tun_fd, 65535)
            except OSError:
                return
            if not packet:
                return
            try:
                self.harness.stdin.write(struct.pack(">H", len(packet)) + packet)
                self.harness.stdin.flush()
            except (BrokenPipeError, ValueError):
                return

    def _harness_to_tun(self) -> None:
        stream = self.harness.stdout
        while not self.stop.is_set():
            header = stream.read(2)
            if len(header) < 2:
                return
            (length,) = struct.unpack(">H", header)
            payload = stream.read(length)
            if len(payload) < length:
                return
            try:
                os.write(self.tun_fd, payload)
            except OSError:
                return

    def close(self) -> None:
        """Stop pumping and PROVE the reader let go of the shared tun fd before returning."""
        self.stop.set()
        self.harness.kill()
        self.harness.wait(timeout=5)
        for thread in self.threads:
            thread.join(timeout=5)
        still_running = [t.name for t in self.threads if t.is_alive()]
        if still_running:
            # Never fall through: a surviving reader steals packets from the next arm, and the
            # symptom lands on the filter.
            raise NotMeasured(f"a pump thread outlived its tunnel: {still_running}")


def dig(name: str) -> tuple[str, str]:
    """Ask the tunnel for a name.  Returns (status, first answer)."""
    result = subprocess.run(
        ["dig", "+time=3", "+tries=1", f"@{FAKE_RESOLVER}", name, "A"],
        capture_output=True, text=True,
    )
    status = re.search(r"status:\s*([A-Z]+)", result.stdout)
    answers = re.findall(rf"^{re.escape(name)}\.\s+\d+\s+IN\s+A\s+(\S+)", result.stdout, re.M)
    return (status.group(1) if status else "NO-REPLY"), (answers[0] if answers else "")


def arm(label: str, tun_fd: int, classpath: str, rules: str, tmpdir: str) -> dict[str, tuple[str, str]]:
    rules_path = os.path.join(tmpdir, f"rules-{label}.txt")
    with open(rules_path, "w", encoding="utf-8") as handle:
        handle.write(rules)
    tunnel = Tunnel(tun_fd, classpath, rules_path)
    try:
        return {BLOCKED_NAME: dig(BLOCKED_NAME), ALLOWED_NAME: dig(ALLOWED_NAME)}
    finally:
        tunnel.close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--classpath", required=True, help="file holding the unit-test classpath")
    parser.add_argument("--tmpdir", default="/tmp")
    arguments = parser.parse_args()

    with open(arguments.classpath, encoding="utf-8") as handle:
        classpath = handle.read().strip()
    if not classpath:
        raise NotMeasured("the classpath file is empty")
    if not shutil.which("dig"):
        raise NotMeasured("dig is not installed; there is no client to measure with")

    tun_fd = open_tun()
    resolver = start_dnsmasq()
    try:
        filtering = arm("filtering", tun_fd, classpath, f"||{BLOCKED_NAME}^\n", arguments.tmpdir)
        open_arm = arm("open", tun_fd, classpath, "! no rules at all\n", arguments.tmpdir)
    finally:
        resolver.kill()
        os.close(tun_fd)

    print("  with the rule loaded:")
    for name, (status, answer) in filtering.items():
        print(f"    {name:26} {status:10} {answer}")
    print("  with an empty rule file (calibration):")
    for name, (status, answer) in open_arm.items():
        print(f"    {name:26} {status:10} {answer}")

    failures = []
    if filtering[BLOCKED_NAME][0] != "NXDOMAIN":
        failures.append(f"{BLOCKED_NAME} was not blocked: {filtering[BLOCKED_NAME]}")
    if filtering[ALLOWED_NAME][1] != ALLOWED_ANSWER:
        failures.append(f"{ALLOWED_NAME} did not resolve through the tunnel: {filtering[ALLOWED_NAME]}")
    # The calibration arm.  Without it, a tunnel that simply dropped everything it did not
    # understand would pass the two assertions above.
    if open_arm[BLOCKED_NAME][1] != BLOCKED_ANSWER:
        failures.append(
            f"CALIBRATION FAILED: with no rules, {BLOCKED_NAME} still did not resolve "
            f"({open_arm[BLOCKED_NAME]}). The NXDOMAIN above proves nothing about the filter."
        )
    if open_arm[ALLOWED_NAME][1] != ALLOWED_ANSWER:
        failures.append(f"CALIBRATION FAILED: {ALLOWED_NAME} did not resolve with no rules either")

    if failures:
        print("\nFAILED:")
        for failure in failures:
            print(f"  - {failure}")
        return 1
    print("\nOK — the rule blocked, the calibration arm resolved the same name, and a real")
    print("     kernel accepted every packet this filter wrote.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except NotMeasured as error:
        print(f"NOT MEASURED: {error}", file=sys.stderr)
        sys.exit(2)
