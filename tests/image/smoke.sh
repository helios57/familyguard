#!/usr/bin/env bash
#
# The container image, checked against the things the Kubernetes manifest assumes about it.
#
# The Go suites prove the server is correct. None of them prove it is *deployable*: they run a binary
# the host built, as the developer's own user, on a writable filesystem, with the host's CA bundle
# and the host's zoneinfo. Every one of those differs in the cluster, and each difference has the
# same failure shape — a pod that comes up, answers /healthz, and fails the first time it needs the
# thing that is missing. A sign-in that dies on "x509: certificate signed by unknown authority" and a
# bedtime that never starts because `Europe/Zurich` cannot be resolved are both discovered by a
# parent, weeks later.
#
# So this runs the real image, under the same restrictions the manifest imposes, against a real
# PostgreSQL, and asserts what the manifest is entitled to assume.
#
#   0  every assertion ran and passed
#   1  an assertion ran and failed
#   2  it could not be run — no docker, base images absent. NOT a pass.
set -u -o pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

IMAGE="${SMOKE_IMAGE:-familyguard-control-plane:smoke}"
PG_IMAGE="${E2E_PG_IMAGE:-postgres:18.6}"

# Read out of the Dockerfile rather than repeated here. Two copies of a base-image reference drift
# the moment one is bumped, and the drift is silent in the direction that matters: this script would
# pre-pull the *old* base, the build would pull the new one anyway, and the pre-pull's promise —
# "NOT MEASURED rather than a red when the registry is out of reach" — would quietly stop covering
# the image actually being built. The first FROM is the builder, the last is the final stage.
BUILD_IMAGE="$(awk '$1 == "FROM" { print $2; exit }' "$ROOT/backend/Dockerfile" 2>/dev/null)"
BASE_IMAGE="$(awk '$1 == "FROM" { last = $2 } END { print last }' "$ROOT/backend/Dockerfile" 2>/dev/null)"

NET="familyguard-smoke-$$"
PG="familyguard-smoke-pg-$$"
APP="familyguard-smoke-app-$$"
WORK=""

# Test values. Nothing here is a credential to anything: the database lives for the length of this
# script, and the signing key signs sessions no client will ever present.
PG_PASSWORD="smoke-not-a-secret"
SESSION_KEY="smoke-signing-key-not-a-secret-0123456789"

notmeasured() { echo "NOT MEASURED: $*" >&2; exit 2; }

cleanup() {
	docker rm -f "$APP" "$PG" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
	[ -n "$WORK" ] && rm -rf "$WORK" || true
}
trap cleanup EXIT

PASSED=0
FAILED=0
check() { # check <name> ; reads the assertion's status from $?
	local rc=$? name="$1"
	if [ "$rc" -eq 0 ]; then
		printf '  ok    %s\n' "$name"
		PASSED=$((PASSED + 1))
	else
		printf '  FAIL  %s\n' "$name"
		FAILED=$((FAILED + 1))
	fi
}

# ---- preconditions --------------------------------------------------------
command -v docker >/dev/null 2>&1 || notmeasured "docker is not installed"
docker info >/dev/null 2>&1 || notmeasured "docker is installed but not usable by this user"
command -v curl >/dev/null 2>&1 || notmeasured "curl is not installed"
[ -n "$BUILD_IMAGE" ] && [ -n "$BASE_IMAGE" ] ||
	notmeasured "could not read both FROM lines out of $ROOT/backend/Dockerfile"

# The e2e harness refuses to pull, so that a red suite is never really a rate-limited registry. The
# same reasoning gives the opposite rule here: two of these three images are *build inputs*, which
# BuildKit fetches during `docker build` whether this script pulls them or not — and BuildKit's cache
# is not the image store, so `docker image inspect golang:1.26-alpine` says "absent" for an image
# that a build a minute earlier used successfully. Measured, and it is exactly the false red this
# check was meant to prevent. So: pull what is missing, and report NOT MEASURED only when the
# registry is genuinely out of reach.
for img in "$PG_IMAGE" "$BUILD_IMAGE" "$BASE_IMAGE"; do
	docker image inspect "$img" >/dev/null 2>&1 && continue
	echo "pulling $img…"
	docker pull -q "$img" >/dev/null 2>&1 ||
		notmeasured "image $img is neither present locally nor pullable"
done

WORK="$(mktemp -d)" || notmeasured "could not create a work directory"

# ---- build ----------------------------------------------------------------
# A build failure is a FAIL, not a 2: the Dockerfile is the artifact under test, and "the thing we
# are testing is broken" is exactly what a 1 means.
echo "building $IMAGE…"
if ! docker build -q -t "$IMAGE" --build-arg VERSION=smoke "$ROOT/backend" >"$WORK/build.log" 2>&1; then
	tail -n 30 "$WORK/build.log" >&2
	echo "the image did not build" >&2
	exit 1
fi

echo
echo "---- the image itself ----"

# The final stage is pinned by content, not by a name its publisher can repoint. With a tag, two
# builds of the same commit can produce two different images, and the property this base is chosen
# for — that there is nothing to execute — is a property of the content, which a tag does not name.
#
# Read out of the Dockerfile rather than out of the built image, because the base reference survives
# nowhere in the result: `docker inspect` on the final image names no parent at all, and the layers
# it lists are the same layers whatever reference pulled them. So the Dockerfile IS the authority
# here, and this asserts what it says.
case "$BASE_IMAGE" in
*@sha256:*) true ;;
*)
	echo "    the final FROM is $BASE_IMAGE, which is a tag someone else can move"
	false
	;;
esac
check "the final stage is pinned by digest, not a moving tag"

# An image with a shell is an image where a remote-code-execution bug has somewhere to go. Three
# names are tried rather than one: distroless has no /bin/sh, but a base-image swap that quietly
# reintroduced busybox would still pass a check that only looked for bash.
no_shell=yes
for sh in /bin/sh /bin/bash /bin/busybox /usr/bin/env; do
	if docker run --rm --entrypoint "$sh" "$IMAGE" -c true >/dev/null 2>&1; then
		echo "    $sh runs inside the image"
		no_shell=no
	fi
done
[ "$no_shell" = yes ]
check "the image contains no shell"

# `runAsNonRoot: true` in the manifest compares a *number*. An image whose user is the name
# "nonroot" is refused by the kubelet with CreateContainerConfigError, which reads like a manifest
# problem and is an image problem.
user="$(docker inspect -f '{{.Config.User}}' "$IMAGE" 2>/dev/null)"
case "$user" in
[0-9]*:[0-9]* | [0-9]*)
	[ "${user%%:*}" -ne 0 ]
	;;
*)
	echo "    image user is $(printf '%q' "$user"), which the kubelet cannot resolve to a uid"
	false
	;;
esac
check "the image runs as a numeric non-root uid ($user)"

# The CA bundle. The JWKS fetch is lazy, so a missing bundle would not surface until the first parent
# signed in — the one moment nobody is watching a log. `docker cp` reads it without a shell.
docker create --name "$APP-probe" "$IMAGE" >/dev/null 2>&1 &&
	docker cp "$APP-probe:/etc/ssl/certs/ca-certificates.crt" "$WORK/ca.crt" >/dev/null 2>&1 &&
	docker cp "$APP-probe:/server" "$WORK/server" >/dev/null 2>&1
copied=$?
docker rm -f "$APP-probe" >/dev/null 2>&1 || true
[ "$copied" -eq 0 ] && [ -s "$WORK/ca.crt" ]
check "the image carries a non-empty CA bundle"

# tzdata, read out of the binary rather than out of the filesystem. Bedtime is computed in the
# family's own zone (`time.LoadLocation`), and a static image need not carry /usr/share/zoneinfo at
# all — so the zone database is compiled in (`_ "time/tzdata"`), and this is what proves it still is.
#
# NOT `Europe/Zurich`, which is the obvious spelling and was this line until it was calibrated. That
# string is also in `migrations/0001_init.sql` (the default of `policies.timezone`), and store.go
# embeds the migrations — so the binary carries it whether or not it carries a zone database.
# Measured: with `_ "time/tzdata"` removed, `Europe/Zurich` still matched (3 hits → 1) and the check
# stayed green. These two zones appear nowhere in this repository, so a hit can only have come from
# the compiled-in database; the same experiment took them 2 → 0.
[ -s "$WORK/server" ] &&
	command grep -qa 'Antarctica/Vostok' "$WORK/server" &&
	command grep -qa 'Pacific/Chatham' "$WORK/server"
check "the binary carries an embedded IANA zone database"

echo
echo "---- refusing a configuration that would produce an insecure server ----"

# Also the proof that the binary can execute at all in an image with no libc: a dynamically linked
# binary here prints an exec-format or loader error instead, and this line would not match.
refusal="$(docker run --rm "$IMAGE" 2>&1)"
printf '%s' "$refusal" | command grep -q 'DATABASE_URL is required'
check "an unconfigured container refuses to start, by name"

printf '%s' "$refusal" | command grep -q 'SESSION_SIGNING_KEY is required'
check "…and reports every problem at once, not just the first"

echo
echo "---- running, under the manifest's own restrictions ----"

docker network create "$NET" >/dev/null 2>&1 || notmeasured "could not create a docker network"

docker run -d --name "$PG" --network "$NET" \
	-e POSTGRES_PASSWORD="$PG_PASSWORD" -e POSTGRES_DB=postgres \
	"$PG_IMAGE" >/dev/null 2>&1 || notmeasured "could not start postgres"

ready=""
for _ in $(seq 1 60); do
	if docker exec "$PG" pg_isready -U postgres -q >/dev/null 2>&1; then
		ready=yes
		break
	fi
	sleep 0.5
done
[ -n "$ready" ] || notmeasured "postgres did not become ready within 30s"

# Every flag here mirrors a line in the Kubernetes manifest. Running with them is what turns
# "the securityContext looks right" into "the securityContext has been shown to work".
docker run -d --name "$APP" --network "$NET" \
	--read-only \
	--cap-drop ALL \
	--security-opt no-new-privileges=true \
	-p 127.0.0.1::8080 \
	-e DATABASE_URL="postgres://postgres:$PG_PASSWORD@$PG:5432/postgres?sslmode=disable" \
	-e PUBLIC_URL="https://guard.example" \
	-e SESSION_SIGNING_KEY="$SESSION_KEY" \
	-e OAUTH_CLIENT_ID="smoke.apps.googleusercontent.com" \
	-e OAUTH_CLIENT_SECRET="smoke-not-a-secret" \
	-e BOOTSTRAP_PARENT_EMAILS="parent@example.com" \
	"$IMAGE" >/dev/null 2>&1 || notmeasured "could not start the control plane container"

# The port is read inside the wait loop rather than once before it, and an empty answer is a 2 only
# while the container is still alive.
#
# `docker port` prints nothing for a container that has already exited. Reading it up front and
# calling the empty answer "could not measure" therefore hands the *product's* failure to the reader
# as a harness problem — and this is the one assertion whose whole job is to catch a server that
# cannot start under the restrictions the manifest imposes. Measured during calibration: a server
# that writes to its root filesystem at startup died before the port read, and this section reported
# NOT MEASURED with every assertion below it unrun, instead of one red line naming the cause.
PORT=""
up=""
for _ in $(seq 1 60); do
	[ -n "$PORT" ] || PORT="$(docker port "$APP" 8080/tcp 2>/dev/null | head -n1 | sed 's/.*://')"
	if [ -n "$PORT" ] && curl -fsS -m 2 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
		up=yes
		break
	fi
	# A container that has already exited will never come up, and waiting the full 30s for it
	# throws away the logs that say why.
	[ "$(docker inspect -f '{{.State.Running}}' "$APP" 2>/dev/null)" = "true" ] || break
	sleep 0.5
done

# The one genuinely unmeasurable shape, kept apart from the failure above: the container is alive
# and docker still will not say which host port it is on. Nothing about the image has been shown
# either way, so that is a 2. A container that has exited is not this case — it is a red.
if [ -z "$up" ] && [ -z "$PORT" ] &&
	[ "$(docker inspect -f '{{.State.Running}}' "$APP" 2>/dev/null)" = "true" ]; then
	notmeasured "the container is running but docker never reported its mapped port"
fi

# Port 0 is never a listening port, so the assertions below fail fast and honestly rather than
# reaching some unrelated service on a port left over from an earlier line.
BASE="http://127.0.0.1:${PORT:-0}"

if [ -z "$up" ]; then
	echo "    the container never answered /healthz; its log follows" >&2
	docker logs "$APP" 2>&1 | tail -n 30 >&2
fi
[ -n "$up" ]
check "it starts and serves /healthz with a read-only root filesystem and no capabilities"

# The migrations ran, inside the container, against a database it reached over the network. This is
# the assertion that separates "the process started" from "the deployment works": /readyz pings the
# pool, and the pool is only open because Open, Migrate and Bootstrap all succeeded first.
curl -fsS -m 5 "$BASE/readyz" 2>/dev/null | command grep -q '"database":"ok"'
check "/readyz reports the database it actually pinged"

# The uid the kernel sees, not the one the image asked for — read out of /proc, because docker's
# rendering of that number is what made this assertion pass against a container running as root.
#
# `docker top "$APP"` prints its UID column through the host's `ps`, which resolves 0 to the *name*
# `root`. So `[ "$uid" != "0" ]` was true for a root container and this line printed
#
#     ok    the running process is uid root, not root
#
# — a control that passed having evaluated nothing. Calibrated in both directions: with `USER 0:0`
# in the Dockerfile the old spelling stayed green while /proc reads 0; with the real
# `USER 65532:65532` both read 65532. (`docker top "$APP" -eo pid,uid` also reads 0 — naming pid is
# what satisfies the daemon's "Couldn't find PID field in ps output", which is why a bare `-o uid`
# was ruled out earlier — but /proc is the kernel's own answer and needs no ps to render it.)
#
# The emptiness check is still the point: an unreadable /proc must not read as "not root", and a
# container that has already exited has no pid, so it fails here rather than reporting a uid.
#
# Recorded rather than papered over: under rootless docker or a userns remap the host's uid is not
# the container's. This host runs neither — measured, a container asking for 65532 appears as 65532
# in the host's own /proc — and in the cluster the enforcing control is the manifest's
# `runAsNonRoot`, which the image-user assertion above is what feeds.
hostpid="$(docker inspect -f '{{.State.Pid}}' "$APP" 2>/dev/null)"
uid="$(awk '/^Uid:/ { print $2 }' "/proc/${hostpid:-0}/status" 2>/dev/null)"
[ -n "$uid" ] && [ "$uid" != "0" ]
check "the running process is uid ${uid:-<none>}, not root"

# SIGTERM must reach the Go process, not a shell wrapper that ignores it. `docker stop` sends TERM
# and waits; a process that ignores it is killed after the timeout, so the elapsed time *is* the
# measurement — and the log line proves the graceful path ran rather than the process merely dying.
started="$(date +%s)"
docker stop -t 25 "$APP" >/dev/null 2>&1
elapsed=$(($(date +%s) - started))
[ "$elapsed" -lt 20 ] && docker logs "$APP" 2>&1 | command grep -q 'shutting down'
check "SIGTERM reaches pid 1 and shuts down gracefully (${elapsed}s)"

# Exit 0. A graceful shutdown that still exits non-zero makes every rollout look like a crash, and a
# CrashLoopBackOff on a pod that is working is the kind of noise that gets alerting muted.
[ "$(docker inspect -f '{{.State.ExitCode}}' "$APP" 2>/dev/null)" = "0" ]
check "…and exits 0"

echo
printf '%d passed, %d failed\n' "$PASSED" "$FAILED"
[ "$FAILED" -eq 0 ] || exit 1
exit 0
