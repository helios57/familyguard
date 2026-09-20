#!/usr/bin/env python3
"""Run the SNI half of the ad filter against a real kernel, a real TLS client and a real TLS server.

The DNS half (realtun_test.py) proves the filter can answer a query.  This proves the harder and
more valuable claim: that it can TERMINATE a TCP connection an app opened, read the server name out
of the ClientHello, and then either reset the connection or splice it through to the real
destination with the handshake intact.  Nothing about that is checkable from a fixture -- a
sequence number one out, a window that never reopens, or a byte of the hello lost while deciding
all produce a connection that simply hangs, and a fixture-driven test cannot tell a hang from a
pass because it is the peer's stack that notices, not ours.

So: `curl` is the app.  A real Python TLS server is the destination.  A real TUN device carries the
packets and a real Linux TCP stack judges every one of them.

### The routing, which is the only non-obvious part

A transparent proxy has to dial the address its client was dialling -- and that address is routed
INTO the tunnel, so the filter's own connection would come straight back to it.  Android solves
this per-uid: the tunnel's own sockets are exempt via `VpnService.protect()`, which is a uid/netid
decision in the kernel.  This reproduces that shape with a uid-scoped DNAT:

    uid CLIENT (curl)  -> 203.0.113.9:443 -> the main route -> fgtun0 -> the filter
    uid 0 (the filter) -> 203.0.113.9:443 -> OUTPUT DNAT    -> 127.0.0.1:443 -> the test server

so `curl` really opens a connection to 203.0.113.9:443, the filter really opens its own socket to
203.0.113.9:443, and the two reach different places for the same reason they do on a phone.

**The server address must NOT be a local address, and that is measured rather than assumed.** The
first version of this file assigned 203.0.113.9 to `lo` so the filter could reach the server
without NAT.  Every SYN-ACK the filter wrote was then dropped by the kernel as a MARTIAN SOURCE --
a packet arriving on an interface carrying a source address the machine owns -- and the symptom was
perfect: tcpdump showed the SYN-ACK on the wire with `cksum correct`, and `curl` retransmitted its
SYN three times and timed out.  Nothing in the filter was wrong.  `ip route get from <addr> iif
fgtun0` is the one-line discriminator: it answers `Invalid argument` for a local source and resolves
for a non-local one, with a main-table route present for both.

    sudo unshare -n python3 tools/realtun/realtun_sni_test.py --classpath <path>

Exit 0 both arms behaved, 1 measured wrong, 2 could not measure.
"""

from __future__ import annotations

import argparse
import http.server
import os
import shutil
import ssl
import subprocess
import sys
import threading

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from realtun_common import NotMeasured, Tunnel, open_tun, run  # noqa: E402

# TEST-NET-3, reserved for documentation, so nothing here can collide with a real destination.
SERVER_ADDRESS = "203.0.113.9"
SERVER_PORT = 443
ROUTED_SUBNET = "203.0.113.0/24"

# Where the test server really listens.  The filter's own connections are redirected here; the
# client's are not.
REAL_SERVER = "127.0.0.1"

# The uid curl runs as.  Anything that is not 0 works; this is the ordinary desktop user, which
# exists in the namespace because a network namespace shares the user database.
CLIENT_UID = 1000

# Both names resolve to the same server, so a blocked result can only have come from the SNI the
# filter read -- never from one of them being unreachable.
BLOCKED_NAME = "ads.example.com"
ALLOWED_NAME = "content.example.org"

BODY = "served-by-the-real-destination"

# Told apart rather than lumped together: a RESET is the filter deciding, and a TIMEOUT is the
# filter failing to decide. Reporting both as "blocked" would make a tunnel that carries nothing
# look like a tunnel that blocks correctly -- which is the calibration arm's whole job to catch,
# and it should not need to.
CURL_STATUS = {7: "connection refused", 28: "TIMED OUT", 35: "TLS failed", 56: "reset by peer"}

# The filter's own upstream DNS, unused by this test but required to start the tunnel.
UPSTREAM_HOST = "127.0.0.1"
UPSTREAM_PORT = 5353


def configure_routing() -> None:
    # In the MAIN table, so it serves two purposes at once: it sends the client into the tunnel,
    # and it gives the kernel a reverse path for 203.0.113.9 when the filter injects packets FROM
    # that address.  Without the second, loose reverse-path filtering discards every reply.
    run("ip", "route", "add", ROUTED_SUBNET, "dev", "fgtun0")
    run("sysctl", "-q", "-w", "net.ipv4.conf.all.route_localnet=1")
    # Only the filter's own sockets. `--uid-owner 0` is this test's stand-in for
    # `VpnService.protect()`: without it the rule would catch curl too, the client would reach the
    # server directly, and both arms would pass while the filter saw nothing at all.
    run(
        "iptables", "-t", "nat", "-A", "OUTPUT",
        "-p", "tcp", "-d", SERVER_ADDRESS, "--dport", str(SERVER_PORT),
        "-m", "owner", "--uid-owner", "0",
        "-j", "DNAT", "--to-destination", f"{REAL_SERVER}:{SERVER_PORT}",
    )


def make_certificate(tmpdir: str) -> tuple[str, str]:
    if not shutil.which("openssl"):
        raise NotMeasured("openssl is not installed; there is no way to make a server certificate")
    key = os.path.join(tmpdir, "server.key")
    cert = os.path.join(tmpdir, "server.crt")
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
        "-keyout", key, "-out", cert, "-subj", "/CN=realtun-test")
    return cert, key


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self) -> None:  # noqa: N802  (the base class names it)
        payload = BODY.encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *_args) -> None:
        pass


def start_server(cert: str, key: str) -> http.server.HTTPServer:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)
    server = http.server.HTTPServer((REAL_SERVER, SERVER_PORT), Handler, bind_and_activate=False)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    server.server_bind()
    server.server_activate()
    threading.Thread(target=server.serve_forever, daemon=True).start()

    # Prove it answers before anything else is measured, as uid 0 over the loopback path.  Without
    # this, "curl got nothing" could mean the server never came up and would be read as a finding
    # about the filter.
    probe = subprocess.run(
        ["curl", "-sS", "-k", "--max-time", "5", f"https://{REAL_SERVER}/"],
        capture_output=True, text=True,
    )
    if BODY not in probe.stdout:
        server.shutdown()
        raise NotMeasured(f"the test server never answered its own probe: {probe.stderr.strip()}")
    return server


def fetch(name: str) -> tuple[int, str]:
    """Ask for [name] as the client uid.  Returns (curl exit status, body)."""
    result = subprocess.run(
        [
            "setpriv", "--reuid", str(CLIENT_UID), "--regid", str(CLIENT_UID), "--clear-groups",
            "curl", "-sS", "-k", "--max-time", "10",
            "--resolve", f"{name}:{SERVER_PORT}:{SERVER_ADDRESS}",
            f"https://{name}/",
        ],
        capture_output=True, text=True,
    )
    return result.returncode, result.stdout.strip()


def arm(label: str, tun_fd: int, classpath: str, rules: str, tmpdir: str) -> dict[str, tuple[int, str]]:
    rules_path = os.path.join(tmpdir, f"sni-rules-{label}.txt")
    with open(rules_path, "w", encoding="utf-8") as handle:
        handle.write(rules)
    tunnel = Tunnel(
        tun_fd, classpath, rules_path,
        upstream=(UPSTREAM_HOST, UPSTREAM_PORT), mode="FULL",
    )
    try:
        results = {BLOCKED_NAME: fetch(BLOCKED_NAME), ALLOWED_NAME: fetch(ALLOWED_NAME)}
    finally:
        log = list(tunnel.log) + [f"packets: tun->filter {tunnel.from_tun}, filter->tun {tunnel.to_tun}"]
        tunnel.close()
    if os.environ.get("REALTUN_VERBOSE"):
        print(f"  -- harness log ({label}) --")
        for line in log:
            print(f"     {line}")
    return results


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--classpath", required=True, help="file holding the unit-test classpath")
    parser.add_argument("--tmpdir", default="/tmp")
    arguments = parser.parse_args()

    with open(arguments.classpath, encoding="utf-8") as handle:
        classpath = handle.read().strip()
    if not classpath:
        raise NotMeasured("the classpath file is empty")
    for tool in ("curl", "setpriv", "iptables"):
        if not shutil.which(tool):
            raise NotMeasured(f"{tool} is not installed; there is no client to measure with")

    tun_fd = open_tun()
    configure_routing()
    cert, key = make_certificate(arguments.tmpdir)
    server = start_server(cert, key)
    try:
        filtering = arm("filtering", tun_fd, classpath, f"||{BLOCKED_NAME}^\n", arguments.tmpdir)
        open_arm = arm("open", tun_fd, classpath, "! no rules at all\n", arguments.tmpdir)
    finally:
        server.shutdown()
        os.close(tun_fd)

    def show(title: str, results: dict[str, tuple[int, str]]) -> None:
        print(f"  {title}")
        for name, (status, body) in results.items():
            verdict = CURL_STATUS.get(status, f"curl error {status}") if status != 0 else (body or "empty")
            print(f"    {name:26} curl={status:<3} {verdict}")

    show("with the rule loaded:", filtering)
    show("with an empty rule file (calibration):", open_arm)

    failures = []
    if filtering[BLOCKED_NAME][0] == 0:
        failures.append(f"{BLOCKED_NAME} was not blocked: curl succeeded and got {filtering[BLOCKED_NAME][1]!r}")
    if filtering[ALLOWED_NAME][1] != BODY:
        failures.append(
            f"{ALLOWED_NAME} did not reach the real destination through the tunnel: "
            f"{filtering[ALLOWED_NAME]}. The block above proves nothing until this passes -- a "
            f"filter that reset everything would satisfy the first assertion."
        )
    # The calibration arm.  Same client, same server, same code path, one rule less.
    if open_arm[BLOCKED_NAME][1] != BODY:
        failures.append(
            f"CALIBRATION FAILED: with no rules, {BLOCKED_NAME} still did not connect "
            f"({open_arm[BLOCKED_NAME]}). The reset above cannot be attributed to the rule."
        )
    if open_arm[ALLOWED_NAME][1] != BODY:
        failures.append(f"CALIBRATION FAILED: {ALLOWED_NAME} did not connect with no rules either")

    if failures:
        print("\nFAILED:")
        for failure in failures:
            print(f"  - {failure}")
        return 1
    print("\nOK — the SNI was read out of a real ClientHello, the named host was reset, the other")
    print("     host completed a real TLS handshake with a real server through the filter, and the")
    print("     same name connected once the rule was removed.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except NotMeasured as error:
        print(f"NOT MEASURED: {error}", file=sys.stderr)
        sys.exit(2)
