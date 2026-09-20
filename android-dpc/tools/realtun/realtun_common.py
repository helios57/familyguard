#!/usr/bin/env python3
"""The parts both real-TUN tests need: a tun device, and a pipe to the Kotlin filter.

Shared rather than copied because the sharp edge here was found once and must stay fixed once: the
reader thread holds a file descriptor that OUTLIVES the tunnel it belongs to, so stopping it has to
be proved rather than asked for.  A second copy of that loop is a second place for it to regress,
and the symptom lands on the filter rather than on the harness.
"""

from __future__ import annotations

import fcntl
import os
import select
import struct
import subprocess
import threading
import time

TUN_NAME = "fgtun0"
TUN_ADDRESS = "10.99.99.1"
TUN_PREFIX = 24

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
    # Below 1500 on purpose, and the same number TcpFlow segments to: a packet the filter writes
    # travels inside the phone's real connection, and one that needs fragmenting on the way out is
    # one the peer may never see.
    run("ip", "link", "set", TUN_NAME, "mtu", "1400")
    return fd


class Tunnel:
    """Pumps packets between the tun device and the harness process."""

    def __init__(self, tun_fd: int, classpath: str, rules_path: str, *, upstream: tuple[str, int],
                 mode: str = "DNS_ONLY") -> None:
        self.tun_fd = tun_fd
        self.stop = threading.Event()
        self.harness = subprocess.Popen(
            [
                "java", "-cp", classpath,
                "io.github.helios57.familyguard.filter.TunLoopHarness",
                rules_path, upstream[0], str(upstream[1]), mode,
            ],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.log: list[str] = []
        # Counted on both sides of the pipe, because "the filter did nothing" and "nothing reached
        # the filter" look identical from the client and have completely different causes.
        self.from_tun = 0
        self.to_tun = 0
        self.threads = [
            threading.Thread(target=self._tun_to_harness, name="tun-to-harness", daemon=True),
            threading.Thread(target=self._harness_to_tun, name="harness-to-tun", daemon=True),
            threading.Thread(target=self._drain_stderr, name="harness-stderr", daemon=True),
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
        # select() rather than a bare blocking read, because the tun fd OUTLIVES this tunnel: the
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
            self.from_tun += 1
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
                self.to_tun += 1
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
