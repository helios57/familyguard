#!/usr/bin/env bash
#
# Rebuilds the real APKs that backend/internal/apk's tests parse.
#
#   0  the fixtures were rebuilt and each one is what it claims to be
#   1  a fixture came out wrong
#   2  it could not be run at all — NOT a pass
#
# They are checked in as binaries, which is a thing worth justifying. The parser under test reads
# aapt2's binary XML and Android's signing block: two formats defined by what the tools emit, not by
# a specification this repo could encode. A fixture synthesised in Go would be a copy of this
# reader's own assumptions, and it would agree with them however wrong they were. So the fixtures
# come out of the same toolchain that builds the shipping app, and this script is how they are
# reproduced when that toolchain moves.
#
# What each one is for:
#
#   fixture-v1.apk   v2-signed, versionCode 1 — the ordinary case, and the debug signing every
#                    developer build here uses
#   fixture-v2.apk   v2-signed, versionCode 2 — the same package one version on, which is what makes
#                    an upgrade test an upgrade rather than a reinstall
#   fixture-v3.apk   v3-signed, same key — the scheme the RELEASE build uses (v3-only), so without
#                    it the parser would be exercised only on the scheme production does not ship
#   fixture-othersigner.apk
#                    the SAME package and version code as fixture-v1, signed by a DIFFERENT key.
#                    This is the one the security property is measured with: the catalog pins a
#                    package to the signer that first registered it, and Android would refuse to
#                    install a differently-signed build over the one on the phone. Nothing else in
#                    the fixture set can produce that state — v3 shares v1's key, so an upload of it
#                    is refused for having the same version code, which passes a signer test for
#                    entirely the wrong reason. Measured: it did.
#
# The negative cases are derived in the test from these bytes rather than checked in: an unsigned
# APK is fixture-v1 rewritten by a zip writer that does not know about the signing block, and a
# truncated one is a prefix. Both are exactly reproducible from a fixture that IS checked in, so
# storing them would add binaries without adding evidence.
#
# Usage:  tests/apk/regenerate-fixtures.sh

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ANDROID="$ROOT/android-dpc"
OUT="$ROOT/backend/internal/apk/testdata"

result() { # result <PASS|FAIL|NOT MEASURED> <reason>
	printf '\nRESULT\t%s\t%s\n' "$1" "$2"
	case "$1" in
	PASS) exit 0 ;;
	FAIL) exit 1 ;;
	*) exit 2 ;;
	esac
}

command -v java >/dev/null 2>&1 || result "NOT MEASURED" "no java on PATH; apksigner cannot run"
[ -x "$ANDROID/gradlew" ] || result "NOT MEASURED" "no gradle wrapper at $ANDROID/gradlew"

SDK="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}}"
# The newest build-tools present, so this does not pin a version that a machine may not have. The
# APK formats are stable; what moves is where the tool lives.
BT="$(ls -d "$SDK"/build-tools/* 2>/dev/null | sort -V | tail -1)"
[ -n "$BT" ] && [ -x "$BT/apksigner" ] && [ -x "$BT/aapt2" ] ||
	result "NOT MEASURED" "no build-tools with apksigner and aapt2 under $SDK"

KEYSTORE="$HOME/.android/debug.keystore"
[ -f "$KEYSTORE" ] || result "NOT MEASURED" "no debug keystore at $KEYSTORE; nothing can be re-signed"

command -v keytool >/dev/null 2>&1 || result "NOT MEASURED" "no keytool on PATH; the second signing key cannot be made"

printf 'building the fixture application, both revisions\n'
(cd "$ANDROID" && ./gradlew --console=plain --no-daemon :fixture-app:assembleV1Debug :fixture-app:assembleV2Debug) ||
	result "NOT MEASURED" "the fixture application would not build"

mkdir -p "$OUT" || result "NOT MEASURED" "could not create $OUT"
cp "$ANDROID/fixture-app/build/outputs/apk/v1/debug/fixture-app-v1-debug.apk" "$OUT/fixture-v1.apk" ||
	result "NOT MEASURED" "the v1 APK is not where the build says it is"
cp "$ANDROID/fixture-app/build/outputs/apk/v2/debug/fixture-app-v2-debug.apk" "$OUT/fixture-v2.apk" ||
	result "NOT MEASURED" "the v2 APK is not where the build says it is"

# v3, from the same key. `--v2-signing-enabled false` is what makes this fixture distinct: with both
# schemes present the parser would read v3 and the test could not tell that from reading v2.
printf 'signing a v3-only copy\n'
cp "$OUT/fixture-v1.apk" "$OUT/fixture-v3.apk" || result "NOT MEASURED" "could not copy the v1 APK"
"$BT/apksigner" sign \
	--ks "$KEYSTORE" --ks-pass pass:android --ks-key-alias androiddebugkey --key-pass pass:android \
	--v1-signing-enabled false --v2-signing-enabled false --v3-signing-enabled true \
	--min-sdk-version 29 \
	"$OUT/fixture-v3.apk" ||
	result "NOT MEASURED" "apksigner could not produce a v3-only copy"

# The other-signer copy. Its key is generated here into a temporary keystore and thrown away: a
# second key checked into the repo would be a private key in a public repository, and the fixture
# only needs the key to be DIFFERENT, never to be the same difference twice.
printf 'signing a copy with a second, unrelated key\n'
OTHERKS="$(mktemp -d)/other.keystore"
trap 'rm -rf "$(dirname "$OTHERKS")"' EXIT
keytool -genkeypair -keystore "$OTHERKS" -storepass notasecret -keypass notasecret \
	-alias other -keyalg RSA -keysize 2048 -validity 10000 \
	-dname "CN=FamilyGuard fixture second signer, OU=tests, O=familyguard, C=CH" >/dev/null 2>&1 ||
	result "NOT MEASURED" "keytool could not generate the second signing key"

cp "$OUT/fixture-v1.apk" "$OUT/fixture-othersigner.apk" || result "NOT MEASURED" "could not copy the v1 APK"
# The copy still carries v1's signature, and apksigner appends rather than replaces unless the old
# block is gone. `zip -d` removes the v1 (JAR) signature files; the v2/v3 block lives outside the
# central directory and apksigner rewrites it wholesale, so re-signing is enough for those.
"$BT/apksigner" sign \
	--ks "$OTHERKS" --ks-pass pass:notasecret --ks-key-alias other --key-pass pass:notasecret \
	--v1-signing-enabled false --v2-signing-enabled true --v3-signing-enabled false \
	--min-sdk-version 29 \
	"$OUT/fixture-othersigner.apk" ||
	result "NOT MEASURED" "apksigner could not sign with the second key"

# ---- and now read each one back, because a copy is not a fixture until it says the right thing ----
fail=0
check() { # check <file> <expected versionCode> <expected scheme line>
	local file="$1" want_code="$2" want_scheme="$3" badging verify code
	badging="$("$BT/aapt2" dump badging "$file" 2>/dev/null | head -1)"
	code="$(printf '%s' "$badging" | sed -n "s/.*versionCode='\([0-9]*\)'.*/\1/p")"
	if [ "$code" != "$want_code" ]; then
		printf 'FAIL %s: versionCode is %q, expected %q\n' "$(basename "$file")" "$code" "$want_code"
		fail=1
	fi
	verify="$("$BT/apksigner" verify -v "$file" 2>/dev/null | command grep -E "^Verified using $want_scheme scheme")"
	case "$verify" in
	*': true') printf 'ok   %s: versionCode=%s, %s\n' "$(basename "$file")" "$code" "$verify" ;;
	*)
		printf 'FAIL %s: not verified using the %s scheme (%s)\n' "$(basename "$file")" "$want_scheme" "${verify:-no such line}"
		fail=1
		;;
	esac
}
check "$OUT/fixture-v1.apk" 1 v2
check "$OUT/fixture-v2.apk" 2 v2
check "$OUT/fixture-v3.apk" 1 v3
check "$OUT/fixture-othersigner.apk" 1 v2

# The whole point of the other-signer fixture is that its certificate differs from v1's. Read both
# out of apksigner and compare: a copy that was not actually re-signed is byte-different from v1
# (the block was rewritten) and would pass every check above while measuring nothing.
cert_of() { "$BT/apksigner" verify --print-certs "$1" 2>/dev/null | sed -n 's/.*certificate SHA-256 digest: //p' | head -1; }
V1CERT="$(cert_of "$OUT/fixture-v1.apk")"
OTHERCERT="$(cert_of "$OUT/fixture-othersigner.apk")"
if [ -z "$V1CERT" ] || [ -z "$OTHERCERT" ]; then
	printf 'FAIL could not read a certificate digest back (v1=%q other=%q)\n' "$V1CERT" "$OTHERCERT"
	fail=1
elif [ "$V1CERT" = "$OTHERCERT" ]; then
	printf 'FAIL fixture-othersigner.apk carries the SAME certificate as fixture-v1.apk (%s)\n' "$V1CERT"
	fail=1
else
	printf 'ok   fixture-othersigner.apk: certificate %s differs from v1 %s\n' "$OTHERCERT" "$V1CERT"
fi

# And its package and version code must MATCH v1, or the catalog would refuse it for being a
# different app rather than for being differently signed.
pkg_of() { "$BT/aapt2" dump badging "$1" 2>/dev/null | sed -n "s/^package: name='\([^']*\)'.*/\1/p"; }
if [ "$(pkg_of "$OUT/fixture-v1.apk")" != "$(pkg_of "$OUT/fixture-othersigner.apk")" ]; then
	printf 'FAIL fixture-othersigner.apk is a different package than fixture-v1.apk\n'
	fail=1
fi

# The v3 copy must NOT also carry a v2 block, or it stops being a v3 fixture without anything saying
# so — the parser prefers v3, so the test would keep passing while covering half of what it names.
if "$BT/apksigner" verify -v "$OUT/fixture-v3.apk" 2>/dev/null | command grep -q '^Verified using v2 scheme.*: true'; then
	printf 'FAIL fixture-v3.apk also verifies under v2; it does not isolate the v3 path\n'
	fail=1
fi

[ "$fail" -eq 0 ] || result "FAIL" "at least one fixture is not what it claims to be"
printf '\n'
sha256sum "$OUT"/fixture-*.apk
result "PASS" "four fixtures rebuilt and read back from aapt2 and apksigner"
