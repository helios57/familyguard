#!/usr/bin/env bash
#
# FR-21 on a real device: a pause from the control plane suspends what a child can open, leaves the
# dialer alone, and lifting it gives everything back.
#
#   0  it ran, and the package manager said suspended=true under the pause and false after it
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# The pause is proven on the server (tests/e2e/pause_test.go) and in both engines (the shared
# vectors). What only a device can show is the platform's answer: the DPC, as device owner, calling
# setPackagesSuspended — read back from `dumpsys package`, which is what the launcher obeys.
#
# Like remote-adb.sh this layer keeps adb: the test switches Allow debugging on before the phone
# enrols. The app it pauses is a preinstalled one with a launcher entry (DeskClock on the Google
# APIs images); the fixture app, which has no components at all, is the negative control a pause must
# leave alone (FR-3.12).
#
# Emulator: on familyguard37 (API 37) `-gpu swiftshader_indirect` alone aborts SurfaceFlinger in a loop
# ("Assertion failed: !rcEnc->featureInfo()->hasReadColorBufferDma"), which takes device_policy and
# StorageManager down with it and reads like a provisioning failure. Measured 2026-09-27. Launch with
#   emulator -avd familyguard37 -no-window -no-audio -no-boot-anim -no-snapshot -wipe-data \
#            -gpu swiftshader_indirect -feature Minigbm
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

E2E="$ROOT/tests/e2e/run.sh"
TEST_NAME="TestAPauseSuspendsAppsOnARealPhone"
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
(cd "$ANDROID" && ./gradlew --console=plain :app:assembleDebug :app:assembleDebugAndroidTest :fixture-app:assembleV1Debug)
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
FIXTURE_APK="$(ls "$ANDROID"/fixture-app/build/outputs/apk/v1/debug/*.apk 2>/dev/null | head -n1)"
[ -n "$FIXTURE_APK" ] || result "NOT MEASURED" "the fixture app did not build"
# Retried: on a freshly wiped emulator the first installs can meet StorageManager before it is up
# ("StorageManager.getVolumes() on a null object reference", measured 2026-09-27), a platform race
# that clears within seconds and says nothing about the product.
fixture_ok=0
for attempt in 1 2 3; do
	if adb install -r -d "$FIXTURE_APK" 2>&1 | tail -n1 | command grep -q '^Success'; then
		fixture_ok=1
		break
	fi
	say "fixture install attempt $attempt failed; retrying"
	sleep 10
done
[ "$fixture_ok" -eq 1 ] || result "NOT MEASURED" "could not install the fixture app"

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
0) result "PASS" "the pause suspended a preinstalled app on the device, left the dialer and the icon-less fixture, and lifting it gave the app back" ;;
1) result "FAIL" "$TEST_NAME failed" ;;
*) result "NOT MEASURED" "the e2e harness could not run (exit $rc) — its own reason is the last NOT MEASURED line above" ;;
esac
