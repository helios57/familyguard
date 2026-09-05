#!/usr/bin/env bash
#
# FR-15 on a real device: the control plane replaces the DPC on a phone that is already enrolled.
#
#   0  it ran and the phone updated itself
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# This is the only layer that can see the feature work. The JVM tests prove the updater's five
# checks against fakes, the e2e tests prove the endpoints against a real server, and neither of them
# can install anything: a `PackageInstaller` session committed by a device owner, a process killed by
# its own install, and a foreground service that has to come back on `MY_PACKAGE_REPLACED` only
# exist on a device.
#
# What it does, in order:
#
#   1. Builds the DPC twice from the same tree — once as it ships, once with `-PbuildOffset=1`, which
#      adds one to the build number and changes nothing else. The second is what the server hosts.
#      Two builds rather than two tags: the updater installs on a strictly greater versionCode, so
#      the difference between the two APKs has to be exactly that number and nothing else.
#   2. Installs the shipping one on the phone, with the instrumentation APK beside it, and makes it
#      the device owner (device.sh, the same recipe instrumented.sh uses).
#   3. Hands the rest to `tests/e2e/run.sh`, which starts a real database and the real server, and to
#      TestTheServerReplacesTheDPCOnARealDevice, which enrols the phone against it and watches the
#      rest happen from the control plane.
#
# The APKs are DEBUG builds, and that is not a shortcut: the debug variant's network security config
# is the one that permits a cleartext control plane on 10.0.2.2, the emulator's name for this
# machine. A release build would refuse the connection, correctly, and there is no release keystore
# on a bench. Both APKs are signed with the same debug key, so the updater's signer check — the one
# that would refuse an APK from anywhere but this project — is exercised rather than skipped.
#
# 10.0.2.2 is also on the emulator's own subnet, and from Android 37 an app's packets to its own
# subnet are dropped unless it holds ACCESS_LOCAL_NETWORK — which is why allow_local_network runs
# below, and why it runs before anything tries to connect. See device.sh for the measurement; a
# phone reaching a control plane over the internet is not affected.
#
# **THE PHONE LOSES ADB PARTWAY THROUGH, AND THAT IS THE PRODUCT WORKING.** The first policy the DPC
# applies contains `no_debugging_features`; the platform switches the debug bridge off and the
# setting survives a reboot. So this layer is a ONE-SHOT on any given device: everything that needs
# adb (install, device owner, the enrollment instrumentation, the reboot) happens before the first
# sync, and everything after it is read from the server. To run it again, the device has to be put
# back — on an emulator that is a restart with `-wipe-data`, and there is no other way, because the
# restriction is exactly the one that stops you clearing it from a shell.
#
# Usage:
#   tests/android/self-update.sh
#
# ANDROID_SERIAL is honoured, and on a machine with more than one emulator running it is required.

set -uo pipefail

# shellcheck source=tests/android/device.sh
. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

E2E="$ROOT/tests/e2e/run.sh"
TEST_NAME="TestTheServerReplacesTheDPCOnARealDevice"
WORK=""

cleanup() {
	[ -n "$WORK" ] && rm -rf "$WORK"
	return 0
}
trap cleanup EXIT

case "${1:-}" in
-h | --help)
	sed -n '2,36p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

[ -x "$E2E" ] || result "NOT MEASURED" "no e2e runner at $E2E"

# aapt2, to read the build number out of each APK. Read from the FILE and never from Gradle's
# output-metadata.json, which is the build system's claim about what it produced: the whole test
# turns on the two APKs differing by exactly one in that number, and a claim is not the number.
AAPT=""
for sdk in "${ANDROID_HOME:-}" "${ANDROID_SDK_ROOT:-}" "$HOME/Android/Sdk"; do
	[ -n "$sdk" ] || continue
	# Newest build-tools first. `sort -V` so 37.0.0 beats 9.0.0, which a lexical sort does not.
	for candidate in $(ls -1 "$sdk/build-tools" 2>/dev/null | sort -Vr); do
		if [ -x "$sdk/build-tools/$candidate/aapt2" ]; then
			AAPT="$sdk/build-tools/$candidate/aapt2"
			break 2
		fi
	done
done
[ -n "$AAPT" ] || result "NOT MEASURED" "no aapt2 in any build-tools; the APKs' build numbers cannot be read"

require_one_device

WORK="$(mktemp -d)" || result "NOT MEASURED" "could not create a work directory"

DEBUG_APK="$ANDROID/app/build/outputs/apk/debug/app-debug.apk"
TEST_APK="$ANDROID/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk"

# build_number <apk> — the versionCode inside the file, or empty.
build_number() {
	"$AAPT" dump badging "$1" 2>/dev/null |
		sed -n "s/^package:.*versionCode='\([0-9]\+\)'.*/\1/p" | head -n1
}

# ---------------------------------------------------------------- builds ----
#
# The offset build runs FIRST and is copied out immediately, because both builds write to the same
# path. Leaving the tree holding the shipping APK at the end is deliberate: an interrupted run must
# not leave a build with a bumped version number sitting where the next `installDebug` finds it.
say "building the DPC the server will host (-PbuildOffset=1)"
(cd "$ANDROID" && ./gradlew --console=plain :app:assembleDebug -PbuildOffset=1)
[ $? -eq 0 ] || result "NOT MEASURED" "the DPC did not build with -PbuildOffset=1"
cp "$DEBUG_APK" "$WORK/next.apk" ||
	result "NOT MEASURED" "the offset build produced no APK at $DEBUG_APK"

say "building the DPC as it ships, and the instrumentation"
(cd "$ANDROID" && ./gradlew --console=plain :app:assembleDebug :app:assembleDebugAndroidTest)
[ $? -eq 0 ] || result "NOT MEASURED" "the DPC did not build"
cp "$DEBUG_APK" "$WORK/current.apk" || result "NOT MEASURED" "no APK at $DEBUG_APK"
cp "$TEST_APK" "$WORK/test.apk" || result "NOT MEASURED" "no instrumentation APK at $TEST_APK"

CURRENT_BUILD="$(build_number "$WORK/current.apk")"
NEXT_BUILD="$(build_number "$WORK/next.apk")"
[ -n "$CURRENT_BUILD" ] && [ -n "$NEXT_BUILD" ] ||
	result "NOT MEASURED" "aapt2 read no versionCode out of the APKs (current='$CURRENT_BUILD' next='$NEXT_BUILD')"

# The check that keeps this whole layer from being vacuous. `-PbuildOffset` is one line of Kotlin in
# build.gradle.kts, and a version of it that silently did nothing would produce two identical APKs:
# the updater would then refuse to install one over the other for exactly the right reason, and a
# run that only looked for "something failed" could not tell that apart from a broken product.
if [ "$NEXT_BUILD" -le "$CURRENT_BUILD" ]; then
	result "NOT MEASURED" "the two builds are $CURRENT_BUILD and $NEXT_BUILD; -PbuildOffset did not take effect, so there is nothing to update to"
fi
say "builds: the phone gets $CURRENT_BUILD, the server hosts $NEXT_BUILD"

# --------------------------------------------------------------- install ----
#
# `-d` allows the downgrade. A previous run of this script leaves the phone on the offset build, and
# without it the install fails with INSTALL_FAILED_VERSION_DOWNGRADE — which reads exactly like the
# product refusing something, on the one step that is pure setup.
say "installing build $CURRENT_BUILD and the instrumentation"
adb install -r -d "$WORK/current.apk" | tail -n1
[ "${PIPESTATUS[0]}" -eq 0 ] || result "NOT MEASURED" "could not install the DPC on the device"
adb install -r -d "$WORK/test.apk" | tail -n1
[ "${PIPESTATUS[0]}" -eq 0 ] || result "NOT MEASURED" "could not install the instrumentation APK"

ensure_device_owner
allow_local_network

# The enrollment instrumentation opens the encrypted credential store on its first line, so a locked
# user fails it with a message about SharedPreferences and the layer reports "the device did not
# enrol". Measured 2026-09-05: that is exactly how this layer failed, on a device that was fine
# twenty seconds later. See device.sh for why every other readiness signal is satisfied first.
wait_for_unlocked_user "before the enrollment instrumentation"

# ------------------------------------------------------------------- run ----
#
# run.sh owns the database, the server binary and the browser precondition, and answers 2 for every
# one of them it cannot satisfy. Its exit status is passed through unchanged except in one case: a
# green that did not actually run this test.

LOG="$WORK/e2e.log"
say "running $TEST_NAME"
E2E_ANDROID=1 \
	E2E_ANDROID_ADB="$ADB" \
	E2E_ANDROID_SERIAL="${ANDROID_SERIAL:-}" \
	E2E_ANDROID_CURRENT_BUILD="$CURRENT_BUILD" \
	E2E_ANDROID_NEXT_BUILD="$NEXT_BUILD" \
	E2E_ANDROID_NEXT_APK="$WORK/next.apk" \
	"$E2E" -run "^${TEST_NAME}\$" -v 2>&1 | tee "$LOG"
rc="${PIPESTATUS[0]}"

# The test SKIPS when it is not given a device, so a run that failed to hand it one would otherwise
# be reported as a pass by every layer above. `-run` matching nothing is the same shape: go test
# exits 0 having opened no test at all.
if ! command grep -q -- "--- PASS: $TEST_NAME" "$LOG"; then
	if command grep -q -- "--- SKIP: $TEST_NAME" "$LOG"; then
		result "NOT MEASURED" "$TEST_NAME skipped itself; it was not given a device (see $LOG, which this script is about to delete — rerun with the harness held open if you need it)"
	fi
	if [ "$rc" -eq 0 ]; then
		result "NOT MEASURED" "the suite exited 0 without running $TEST_NAME; the filter matched nothing"
	fi
fi

case "$rc" in
0) result "PASS" "the server replaced build $CURRENT_BUILD with build $NEXT_BUILD on the device, and the DPC reported the new build back" ;;
1) result "FAIL" "$TEST_NAME failed; the phone did not end up running build $NEXT_BUILD" ;;
*) result "NOT MEASURED" "the e2e harness could not run (exit $rc) — its own reason is the last NOT MEASURED line above" ;;
esac
