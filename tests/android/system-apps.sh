#!/usr/bin/env bash
#
# FR-1.9 on a device: a system app setup removed comes back, and a blocked one stays away.
#
#   0  measured and passed     1  measured and failed     2  NOT MEASURED
#
# A Samsung on Android 16 left its Clock uninstalled for the child's user despite
# LEAVE_ALL_SYSTEM_APPS_ENABLED, so the child had no alarm and no timer. Nothing in the product can
# remove a system app for a user, so this script makes that state with root — `pm uninstall --user 0`
# leaves the same platform record setup leaves (on the image, installed=false for user 0) — then
# runs SystemAppRestoreTest and reads the result back from the package manager itself, not from the
# app. Root needs a Google APIs image (familyguard37); a Play Store image refuses `adb root`, which
# is NOT MEASURED, never a pass.
#
# Emulator: see pause.sh for the flags familyguard37 needs to keep SurfaceFlinger alive.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

CLOCK=com.google.android.deskclock
CALENDAR=com.google.android.calendar
CLASS=io.github.helios57.familyguard.SystemAppRestoreTest
RESULTS="$ANDROID/app/build/outputs/androidTest-results/connected"

installed_for_user0() { # installed_for_user0 <pkg> -> prints true|false|unknown
	local v
	v="$(adb shell dumpsys package "$1" | tr -d '\r' | command grep -m1 -oE 'User 0:.* installed=(true|false)' | command grep -oE 'installed=(true|false)$')"
	if [ -n "$v" ]; then printf '%s\n' "${v#installed=}"; else printf 'unknown\n'; fi
}

require_one_device

say "root, for the one step the product cannot do"
adb root >/dev/null 2>&1
adb wait-for-device
[ "$(adb shell id -u | tr -d '\r')" = 0 ] ||
	result "NOT MEASURED" "adb root was refused (a Play Store image?); the removed-for-user state cannot be made"
wait_for_unlocked_user "after adb root"

say "building and installing the DPC and its instrumentation"
(cd "$ANDROID" && ./gradlew --console=plain :app:installDebug :app:assembleDebugAndroidTest)
[ $? -eq 0 ] || result "NOT MEASURED" "the DPC did not build or install"
ensure_device_owner

say "removing $CLOCK and $CALENDAR for user 0, as setup did"
for p in "$CLOCK" "$CALENDAR"; do
	out="$(adb shell pm uninstall --user 0 "$p" 2>&1 | tr -d '\r')"
	state="$(installed_for_user0 "$p")"
	[ "$state" = false ] || result "NOT MEASURED" "$p is installed=$state for user 0 after pm uninstall ($out); the precondition is not there"
done

MARK="$(mktemp)"
LOG="$(mktemp)"
trap 'rm -f "$MARK" "$LOG"; adb shell pm install-existing --user 0 "$CALENDAR" >/dev/null 2>&1; adb shell pm install-existing --user 0 "$CLOCK" >/dev/null 2>&1; adb unroot >/dev/null 2>&1' EXIT

say "running $CLASS"
(cd "$ANDROID" && ./gradlew --console=plain :app:connectedDebugAndroidTest \
	"-Pandroid.testInstrumentationRunnerArguments.class=$CLASS") 2>&1 | tee "$LOG"
rc="${PIPESTATUS[0]}"
ran="$(find "$RESULTS" -name 'TEST-*.xml' -newer "$MARK" -exec cat {} + 2>/dev/null | tr '<' '\n' | command grep -c "^testcase .*classname=\"$CLASS\"")"
printf 'gradle rc=%s, testcases of %s executed=%s\n' "$rc" "$CLASS" "$ran"
[ "$ran" -ge 1 ] || result "NOT MEASURED" "$CLASS did not run (the filter matched nothing, or the device went away)"

# The authority is the package manager, read from outside the app.
clock_now="$(installed_for_user0 "$CLOCK")"
calendar_now="$(installed_for_user0 "$CALENDAR")"
printf 'read back from dumpsys: %s installed=%s, %s installed=%s\n' "$CLOCK" "$clock_now" "$CALENDAR" "$calendar_now"

[ "$rc" -eq 0 ] || result "FAIL" "$CLASS failed; see its assertion above"
[ "$clock_now" = true ] || result "FAIL" "the test passed and dumpsys says $CLOCK is installed=$clock_now for user 0"
[ "$calendar_now" = false ] || result "FAIL" "$CALENDAR was blocked and dumpsys says installed=$calendar_now"
result "PASS" "a system app removed for the user was restored under no_install_apps, and a blocked one stayed removed"
