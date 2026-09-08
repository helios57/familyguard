#!/usr/bin/env bash
#
# Cross-compiles fgctl for every platform a parent might run it on, and writes checksums.
#
# Exit status is three-valued, like tests/e2e/run.sh:
#   0  every target built
#   1  at least one target failed to build
#   2  could NOT be run — no Go toolchain, or not run from the backend module
#
# The distinction matters because a build script that cannot start and exits 0 produces an empty
# output directory that looks exactly like a successful build of nothing.
set -u -o pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${1:-$HERE/dist}"

notmeasured() { echo "NOT MEASURED: $*" >&2; exit 2; }

command -v go >/dev/null 2>&1 || notmeasured "no go on PATH"
[ -f "$HERE/go.mod" ] || notmeasured "$HERE is not the backend module"

# The version is stamped into the binary so a stale copy on someone's PATH is visible rather than
# guessed at. Taken from the git tag when there is one, because the tag is what the release gate
# checks against the Android versionName -- see .github/workflows/release.yml.
VERSION="$(git -C "$HERE" describe --tags --always --dirty 2>/dev/null || echo dev)"

mkdir -p "$OUT" || notmeasured "cannot create $OUT"
rc=0
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  ext=""; [ "$os" = "windows" ] && ext=".exe"
  name="fgctl-${os}-${arch}${ext}"
  # CGO off so every artefact is a single static file with nothing to install alongside it.
  if CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
       -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
       -o "$OUT/$name" ./cmd/fgctl; then
    printf '  %-24s %s\n' "$target" "$name"
  else
    printf '  %-24s FAILED\n' "$target"
    rc=1
  fi
done

# Checksums, because the Windows binary is the one most likely to travel by a route that can corrupt
# or substitute it -- a chat attachment, a USB stick, a download over someone else's network.
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$OUT" && sha256sum fgctl-* > SHA256SUMS) && echo "  checksums              SHA256SUMS"
fi

echo "fgctl ${VERSION} → $OUT"
exit "$rc"
