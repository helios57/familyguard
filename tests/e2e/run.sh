#!/usr/bin/env bash
#
# End-to-end suite for the FamilyGuard control plane.
#
# Starts PostgreSQL in Docker, builds the real server binary, and hands both to the Go suite, which
# drives the server as a black box over HTTP. Nothing is stubbed: the OIDC verifier verifies real
# RS256 signatures against a real JWKS endpoint, and every request crosses a socket.
#
# Exit status is three-valued on purpose:
#   0  the suite ran and passed
#   1  the suite ran and something failed
#   2  the suite could NOT be run — no docker, no image, database never came up, build failed.
#      This is not a pass. A harness that cannot start and exits 0 is the most expensive kind of
#      green there is, because it looks exactly like coverage.
set -u -o pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

# Pinned to the same image `deploy/postgres.yaml` runs. A suite that proves the migrations apply on
# a different major version than the deployment proves it about a database nobody runs.
PG_IMAGE="${E2E_PG_IMAGE:-postgres:18.6}"
PG_PASSWORD="e2e-not-a-secret"
CONTAINER="familyguard-e2e-$$"
WORK=""

notmeasured() { echo "NOT MEASURED: $*" >&2; exit 2; }

cleanup() {
  # Both are best-effort teardown of things this script created; failing to remove a temp directory
  # must not turn a passing suite red, which is the one place `|| true` is honest.
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  [ -n "$WORK" ] && rm -rf "$WORK" || true
}
trap cleanup EXIT

# ---- preconditions --------------------------------------------------------
# Each is checked before anything is started, so a missing tool is reported as "not measured"
# rather than as a test failure.

command -v docker >/dev/null 2>&1 || notmeasured "docker is not installed"
docker info >/dev/null 2>&1 || notmeasured "docker is installed but not usable by this user"

GO="${E2E_GO:-$HOME/go/go1.26.5/bin/go}"
[ -x "$GO" ] || GO="$(command -v go || true)"
[ -n "$GO" ] || notmeasured "no go toolchain found"
GOVER="$("$GO" env GOVERSION 2>/dev/null || echo unknown)"
case "$GOVER" in
  go1.2[6-9]*|go1.[3-9][0-9]*) : ;;
  *) notmeasured "go toolchain is $GOVER; this module needs 1.26 or newer" ;;
esac

# The image must already be present. Pulling here would make a red suite indistinguishable from a
# rate-limited registry, and this machine caches the image deliberately.
docker image inspect "$PG_IMAGE" >/dev/null 2>&1 || \
  notmeasured "image $PG_IMAGE is not present locally (docker pull it first)"

# A browser, for the guard that measures the console's rendered layout on a 360 px phone. Resolved
# here rather than inside the suite so that the answer to "which browser measured this" is recorded
# in one place, and so that its absence is NOT MEASURED rather than a suite that quietly drops the
# only check that can see a tap target.
CHROME="${E2E_CHROME:-}"
if [ -z "$CHROME" ]; then
  for candidate in google-chrome google-chrome-stable chromium chromium-browser; do
    CHROME="$(command -v "$candidate" 2>/dev/null || true)"
    [ -n "$CHROME" ] && break
  done
fi
[ -n "$CHROME" ] && [ -x "$CHROME" ] || \
  notmeasured "no chrome/chromium found (set E2E_CHROME); the rendered mobile layout cannot be measured"
echo "browser: $CHROME ($("$CHROME" --version 2>/dev/null || echo 'version unknown'))"

# The real APKs the catalog tests register. Built by tests/apk/regenerate-fixtures.sh and committed,
# because a suite that generates them would need Gradle and the Android SDK to test a Go parser.
# Their absence is NOT MEASURED rather than a skip: the catalog is the half of FR-16 that decides
# what installs itself on a child's phone, and a suite that quietly drops it still says PASS.
APK_FIXTURES="$ROOT/backend/internal/apk/testdata"
for fixture in fixture-v1.apk fixture-v2.apk fixture-v3.apk fixture-othersigner.apk; do
  [ -s "$APK_FIXTURES/$fixture" ] || \
    notmeasured "$APK_FIXTURES/$fixture is missing; run tests/apk/regenerate-fixtures.sh"
done

WORK="$(mktemp -d)" || notmeasured "could not create a work directory"

# ---- build ----------------------------------------------------------------
# The binary under test is the real one. Building it here rather than inside the suite keeps the
# module boundary clean: the e2e module imports nothing from the backend, so it cannot accidentally
# check the server against the server's own constants.

echo "building the control plane…"
if ! (cd "$ROOT/backend" && "$GO" build -o "$WORK/family-guard" ./cmd/server); then
  notmeasured "the control plane did not build"
fi

# ---- postgres -------------------------------------------------------------

echo "starting postgres ($PG_IMAGE)…"
if ! docker run -d --rm --name "$CONTAINER" \
      -e POSTGRES_PASSWORD="$PG_PASSWORD" \
      -e POSTGRES_DB=postgres \
      -P \
      "$PG_IMAGE" >/dev/null; then
  notmeasured "could not start the postgres container"
fi

PG_PORT="$(docker port "$CONTAINER" 5432/tcp 2>/dev/null | head -n1 | sed 's/.*://')"
[ -n "${PG_PORT:-}" ] || notmeasured "could not read the mapped postgres port"

# Readiness is polled inside the container, not against the mapped port: the port is bound by
# docker-proxy the moment the container starts, so a TCP connect succeeds long before postgres will
# answer a query.
#
# `-h 127.0.0.1` is load bearing, and this is the second readiness bug on the same line. Without it
# pg_isready uses the unix socket, and the socket is served during the entrypoint's *initdb* phase by
# a temporary server that is about to be shut down. Measured on postgres:17-alpine — the image this
# harness used before it was aligned with the deployment — polling both at
# 20 Hz: the socket reports ready at **6.02 s**, stops being ready at **6.87 s**, and the real server
# answers TCP at **7.32 s**. A 0.85 s window in which the old check said yes about a server that no
# longer existed by the time the suite ran — and the suite's first act is CREATE DATABASE, so it
# surfaced as `psql "CREATE DATABASE …": exit status 2`, an infrastructure-shaped red that looks like
# Docker being flaky and is really a probe measuring the wrong process. The temporary server is
# started with listen_addresses empty, so TCP is exactly what the init phase cannot fake.
ready=""
for _ in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -h 127.0.0.1 -U postgres -q >/dev/null 2>&1; then
    ready=yes
    break
  fi
  sleep 0.5
done
[ -n "$ready" ] || notmeasured "postgres did not become ready within 30s"

# ---- run ------------------------------------------------------------------

export E2E_SERVER_BIN="$WORK/family-guard"
export E2E_PG_CONTAINER="$CONTAINER"
export E2E_PG_HOST=127.0.0.1
export E2E_PG_PORT="$PG_PORT"
export E2E_PG_USER=postgres
export E2E_PG_PASSWORD="$PG_PASSWORD"
export E2E_CHROME="$CHROME"
export E2E_APK_FIXTURES="$APK_FIXTURES"

echo "running the suite…"
(cd "$HERE" && "$GO" test -count=1 -timeout 15m "$@" ./...)
rc=$?

if [ "$rc" -eq 0 ]; then
  echo "E2E PASS"
else
  echo "E2E FAIL (go test exit $rc)"
  rc=1
fi
exit "$rc"
