#!/usr/bin/env bash
#
# FR-19 on a real device: a parent's adb reaches the phone's own adbd through the control plane.
#
#   0  it ran, and `adb shell` through the relay answered as the phone
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# The relay itself is proven without a device (tests/e2e/debug_tunnel_test.go, the test standing in
# for the phone). What only a device can show is the phone's half: the command arriving with its
# parameters, the DPC connecting to adbd on its own loopback, dialling the server's upgrade over the
# emulator's NAT, splicing, and the notice the child sees while it lasts.
#
# Unlike self-update.sh this layer does NOT cost the device its adb: the test switches Allow
# debugging on before the phone enrols, so the first policy the DPC applies leaves adb running —
# which is also the only state in which this feature can work on a real phone.
#
# The APK is the DEBUG build for the same reason as self-update.sh: its network security config is
# the one that permits a cleartext control plane on 10.0.2.2.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

E2E="$ROOT/tests/e2e/run.sh"
TEST_NAME="TestRemoteADBReachesARealPhonesAdbd"
LOG=""
cleanup() {
	[ -n "$LOG" ] && rm -f "$LOG"
	return 0
}
trap cleanup EXIT

case "${1:-}" in
-h | --help)
	sed -n '2,20p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

[ -x "$E2E" ] || result "NOT MEASURED" "no e2e runner at $E2E"
require_one_device

DEBUG_APK="$ANDROID/app/build/outputs/apk/debug/app-debug.apk"
TEST_APK="$ANDROID/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk"

say "building the DPC and the instrumentation"
(cd "$ANDROID" && ./gradlew --console=plain :app:assembleDebug :app:assembleDebugAndroidTest)
[ $? -eq 0 ] || result "NOT MEASURED" "the DPC did not build"

# Before installing, not only before enrolling: sys.boot_completed turns 1 while the package
# manager is still coming up, and an install in that window dies inside the platform — measured
# 2026-09-23 on a cold-booted API 37 AVD: `NullPointerException … PackageManagerInternal.freeStorage`
# from StorageManagerService.allocateBytes, and the same command succeeded a minute later.
wait_for_unlocked_user "before installing"

say "installing the DPC and the instrumentation"
adb install -r -d "$DEBUG_APK" | tail -n1
[ "${PIPESTATUS[0]}" -eq 0 ] || result "NOT MEASURED" "could not install the DPC on the device"
adb install -r -d "$TEST_APK" | tail -n1
[ "${PIPESTATUS[0]}" -eq 0 ] || result "NOT MEASURED" "could not install the instrumentation APK"

ensure_device_owner
allow_local_network
wait_for_unlocked_user "before the enrollment instrumentation"

LOG="$(mktemp)" || result "NOT MEASURED" "could not create a log file"
say "running $TEST_NAME"
E2E_ANDROID=1 \
	E2E_ANDROID_ADB="$ADB" \
	E2E_ANDROID_SERIAL="${ANDROID_SERIAL:-}" \
	"$E2E" -run "^${TEST_NAME}\$" -v 2>&1 | tee "$LOG"
rc="${PIPESTATUS[0]}"

if ! command grep -q -- "--- PASS: $TEST_NAME" "$LOG"; then
	if command grep -q -- "--- SKIP: $TEST_NAME" "$LOG"; then
		result "NOT MEASURED" "$TEST_NAME skipped itself; it was not given a device"
	fi
	if [ "$rc" -eq 0 ]; then
		result "NOT MEASURED" "the suite exited 0 without running $TEST_NAME; the filter matched nothing"
	fi
fi
case "$rc" in
0) result "PASS" "adb shell through the control plane answered as the phone, and the phone showed the notice while it did" ;;
1) result "FAIL" "$TEST_NAME failed" ;;
*) result "NOT MEASURED" "the e2e harness could not run (exit $rc) — its own reason is the last NOT MEASURED line above" ;;
esac
