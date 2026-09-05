#!/usr/bin/env bash
#
# `deploy/` renders, and the rendered output still says what the rest of the repository assumes.
#
# The plan recorded this as calibrated once, by hand, in Phase 7.4 — which means it was true on the
# day it was written and nothing has re-checked it since. A manifest directory is exactly the kind
# of artifact that rots without a symptom: nothing in a Go test compiles it, nothing in CI applied
# it, and the failure surfaces in a cluster rather than in a suite.
#
# What it asserts is deliberately not "does kustomize exit 0". A directory that renders can still be
# wrong in ways that are silent until a phone is affected, so each assertion below is a property
# something else in this repository depends on:
#
#   - the DPC download and the QR checksum come from files mounted read-only into the control plane;
#   - the dump the backup writes must be readable by the postgres that will restore it, which means
#     ONE image tag across both, not two that happen to agree today;
#   - a floating tag turns an unrelated pod restart into a silent upgrade of the process holding the
#     session signing key;
#   - a Secret in the render is a credential in git.
#
#   0  every assertion ran and passed
#   1  an assertion ran and failed
#   2  it could not be run — no renderer available. NOT a pass.
set -u -o pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
DEPLOY="$ROOT/deploy"

notmeasured() { echo "NOT MEASURED: $*" >&2; exit 2; }

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
[ -d "$DEPLOY" ] || notmeasured "$DEPLOY does not exist"

# Either renderer is fine; they implement the same thing. `kubectl kustomize` is checked first only
# because it is the one more machines already have.
RENDER=""
if command -v kubectl >/dev/null 2>&1 && kubectl kustomize --help >/dev/null 2>&1; then
	RENDER="kubectl kustomize"
elif command -v kustomize >/dev/null 2>&1; then
	RENDER="kustomize build"
else
	notmeasured "neither 'kubectl kustomize' nor 'kustomize' is available to render $DEPLOY"
fi
command -v python3 >/dev/null 2>&1 || notmeasured "python3 is not installed; the rendered YAML cannot be inspected"

OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT

echo "== rendering deploy/ with: $RENDER"
if ! $RENDER "$DEPLOY" > "$OUT" 2>/tmp/render.err; then
	echo "  FAIL  deploy/ does not render"
	sed 's/^/        /' /tmp/render.err
	echo
	echo "1 assertion ran, 1 failed"
	exit 1
fi
false; [ -s "$OUT" ] && true
check "deploy/ renders, and the render is not empty"

# Everything below reads the SAME rendered bytes. Re-rendering per assertion would let a
# non-deterministic input pass one check and fail another for reasons neither reports.
inspect() { python3 "$HERE/inspect.py" "$OUT" "$@"; }

inspect expected-kinds
check "the render still contains every object the deployment needs"

inspect no-secrets
check "no Secret is rendered — credentials are not in this repository"

inspect nonroot
check "every pod template runs as a non-root uid"

inspect pinned
check "no image is on a floating tag"

inspect same-postgres
check "the backup job and the database run the identical postgres image"

inspect apk-mount-readonly
check "the control plane mounts the APK directory read-only"

inspect catalog-dir-writable
check "the catalog directory is writable and is not the DPC's own"

inspect probes
check "the control plane declares startup, readiness and liveness probes"

inspect db-recreate
check "the database uses strategy Recreate, never RollingUpdate"

inspect backup-verifies
check "the backup job restores what it dumped before keeping it"

echo
echo "$((PASSED + FAILED)) assertions ran, $FAILED failed"
[ "$FAILED" -eq 0 ] || exit 1
exit 0
