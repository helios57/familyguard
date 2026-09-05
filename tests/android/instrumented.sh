#!/usr/bin/env bash
#
# The instrumented layer: the tests that need a real Android device, run in the state they are
# actually about.
#
#   0  the tests ran and passed, on both passes
#   1  the tests ran and failed
#   2  they could not be run at all — NOT a pass
#
# Why this is a script and not just `gradlew connectedDebugAndroidTest`:
#
#   1. The tests measure a DEVICE-OWNED phone. `WipeabilityTest` deliberately fails rather than
#      skips when the app is not device owner, because a skip would be a green in the one layer that
#      can see the device. That makes establishing ownership somebody's job, and it has to be this
#      script's — otherwise every developer emulator reports FAIL, which is a false red, and a false
#      red is as expensive as a false green: it trains the reader to ignore the layer.
#
#   2. "The device is still wipeable after a reboot" (FR-2.3) cannot be tested by a process running
#      inside the device's own uptime. It needs a real `adb reboot` between two runs, and the second
#      run has to READ the state the boot left rather than re-apply it — which is why
#      WipeableAsFoundTest exists as its own class and why pass 1 excludes it.
#
#   3. Every filtered run can pass by matching no tests. Both passes therefore count the testcases
#      that actually executed and refuse a green that came from an empty run.
#
# Usage:
#   tests/android/instrumented.sh            # both passes
#   tests/android/instrumented.sh --no-reboot   # pass 1 only; pass 2 reports NOT MEASURED
#
# ANDROID_SERIAL is honoured. Without it exactly one attached device is required — running against
# an unspecified one of several is how you provision a phone you did not mean to.

set -uo pipefail

# Everything about talking to the device — resolving adb, refusing an ambiguous one, and the
# device-owner provisioning with its five retries and its three fail-closed read-backs — lives in
# device.sh, because self-update.sh needs the identical recipe and a second copy of it would drift.
# shellcheck source=tests/android/device.sh
. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

RESULTS="$ANDROID/app/build/outputs/androidTest-results/connected"

AS_FOUND="io.github.helios57.familyguard.WipeableAsFoundTest"

# The classes that only mean something in a position this script puts them in: AS_FOUND, which reads
# the state a reboot left, and ServerDrivenEnrollmentTest, the device half of the FR-15 self-update
# proof, which needs a live control plane and a single-use token only tests/android/self-update.sh
# can hand it. Both fail rather than skip out of position, so pass 1 has to exclude them.
#
# Excluded by ANNOTATION, and it has to be, because AGP splits its instrumentation arguments on
# commas. Measured 2026-09-05: `-P…notClass=$AS_FOUND,$ENROLL` reached the device as
# `am instrument … -e notClass io.github.helios57.familyguard.WipeableAsFoundTest` — AGP's own
# --info line — with the second class silently dropped as a malformed argument. The runner itself is
# innocent: `am instrument -e notClass A,B` by hand excludes both. So every sweep since the FR-15
# work landed ran ServerDrivenEnrollmentTest and reported FAIL on a filter that reads correctly.
# One annotation is one value with no comma in it.
SEQUENCED="io.github.helios57.familyguard.SequencedByAScript"

REBOOT=yes
case "${1:-}" in
--no-reboot) REBOOT=no ;;
-h | --help)
	sed -n '2,30p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

require_one_device

# A guard on the instrument, run before anything touches the device.
#
# `no_debugging_features` switches adb off, and the switch outlives a reboot: applying it here would
# sever the connection this whole layer runs over and leave the AVD needing a wipe. It is out of the
# pre-sync baseline for a product reason, and EnforcementEngineHardeningTest is where that is
# asserted — so the cheapest way to refuse to run against a baseline that would destroy the device is
# to run that assertion first. A filter that matches nothing makes Gradle fail, which lands here as
# NOT MEASURED rather than as permission to proceed.
say "pre-flight: the baseline must leave adb reachable"
(cd "$ANDROID" && ./gradlew --console=plain :app:testDebugUnitTest --tests '*EnforcementEngineHardeningTest*')
[ $? -eq 0 ] || result "NOT MEASURED" "the pre-sync baseline does not pass its own hardening test; refusing to apply it to a device"

say "installing the debug build"
(cd "$ANDROID" && ./gradlew --console=plain :app:installDebug)
[ $? -eq 0 ] || result "NOT MEASURED" "the debug APK would not install; nothing was measured on the device"

ensure_device_owner

# ------------------------------------------------------------------ runs ----
#
# A filtered instrumentation run that matches nothing exits 0 having opened no test. Both passes
# below therefore count what executed, and a pass that ran fewer tests than it was supposed to is a
# NOT MEASURED rather than a green.
# Three things here were each wrong once, and each failure counted zero tests — which this script
# reads as NOT MEASURED. A guard that cannot count is a guard that reports "nothing ran" after a
# perfectly good run, and a false NOT MEASURED trains the reader to ignore the layer exactly as fast
# as a false green does.
#
#   - `xargs -r command grep …` cannot work: `command` is a shell BUILTIN, so xargs exits 127 with no
#     output, which reaches `wc -l` as 0. Measured. `command grep` is required in this workspace
#     because bare `grep` is a ugrep wrapper with an unreliable exit status, so the fix is to keep it
#     in the shell rather than hand it to xargs.
#   - AGP names the report after the device: `TEST-familyguard34(AVD) - 14-_app-.xml`. Spaces and
#     parentheses, so the file list has to travel as arguments (`-exec cat {} +`), never through a
#     pipe that word-splits.
#   - `grep -c` counts LINES, not occurrences. `tr '<' '\n'` puts every element on its own line
#     first, so the count is exact however AGP decides to wrap the XML.
ran_count() {
	find "$RESULTS" -name 'TEST-*.xml' -newer "$1" -exec cat {} + 2>/dev/null |
		tr '<' '\n' | command grep -c '^testcase '
}

# The same count, restricted to one class.
#
# `ran_count`'s floor catches a filter that matched nothing. It cannot catch a filter that matched
# everything EXCEPT the classes this layer exists for — which is not hypothetical: the sweep that
# found the AGP comma defect above ran 17 testcases, comfortably over its floor of 3, while silently
# running the wrong set. A count of the FR-2.3 classes by name is the only assertion that would have
# been red for the right reason.
ran_class_count() { # ran_class_count <mark> <fully-qualified-class>
	find "$RESULTS" -name 'TEST-*.xml' -newer "$1" -exec cat {} + 2>/dev/null |
		tr '<' '\n' | command grep -c "^testcase .*classname=\"$2\""
}

MARK="$(mktemp)"
trap 'rm -f "$MARK"' EXIT

pass() { # pass <label> <expected-minimum> <gradle-arg...>
	local label="$1" min="$2"
	shift 2
	say "$label"
	: >"$MARK"
	(cd "$ANDROID" && ./gradlew --console=plain :app:connectedDebugAndroidTest "$@")
	local rc=$?
	local ran
	ran="$(ran_count "$MARK")"
	printf '%s: gradle rc=%s, testcases executed=%s (expected at least %s)\n' "$label" "$rc" "$ran" "$min"
	if [ "$rc" -ne 0 ]; then
		# A run whose device disappeared measured nothing, and reporting it as FAIL would say the
		# product is broken on the strength of a severed connection. The known way to cause this is
		# applying `no_debugging_features`, which switches adb off; the pre-flight above is what
		# stops the baseline doing it, and this is the net for everything else.
		if ! reachable; then
			return 3
		fi
		return 1
	fi
	if [ "$ran" -lt "$min" ]; then
		printf '%s passed having run %s tests; the filter matched less than it should have\n' "$label" "$ran"
		return 2
	fi
	return 0
}

# Pass 1 applies the baseline and asserts against it. WipeableAsFoundTest is excluded because it
# reads the state it finds — before pass 1 there is no state to find, and a class that has to run
# second is a flake unless something sequences it. This script is that something.
pass "pass 1 — provisioned state" 3 \
	"-Pandroid.testInstrumentationRunnerArguments.notAnnotation=$SEQUENCED"
case $? in
0) ;;
2) result "NOT MEASURED" "pass 1 reported success without running its tests" ;;
3) result "NOT MEASURED" "the device went offline during pass 1, so nothing was measured (adb disconnected)" ;;
*) result "FAIL" "pass 1 (provisioned state) failed — see the report under $RESULTS" ;;
esac

# The FR-2.3 evidence, named. Both classes are about the same guarantee from opposite sides:
# WipeabilityTest asserts `no_factory_reset` is absent on a provisioned device, and
# FactoryResetRecoveryTest sets it on purpose to show the platform would REPORT it if it were there
# — the positive control the absence assertions cannot carry themselves — and that the DPC takes it
# back off. A green pass 1 that ran neither is not evidence about a phone that can be reset.
for required in \
	"io.github.helios57.familyguard.WipeabilityTest" \
	"io.github.helios57.familyguard.FactoryResetRecoveryTest"; do
	n="$(ran_class_count "$MARK" "$required")"
	printf 'pass 1: %s ran %s testcases\n' "$required" "$n"
	[ "$n" -gt 0 ] ||
		result "NOT MEASURED" "pass 1 passed without running $required, which is the FR-2.3 evidence this layer exists to produce"
done

if [ "$REBOOT" = no ]; then
	result "NOT MEASURED" "pass 1 passed, but --no-reboot skipped the after-reboot pass, which is the FR-2.3 evidence"
fi

# ---------------------------------------------------------------- reboot ----
#
# `adb wait-for-device` waits for a device to APPEAR and carries no deadline of its own, so the
# `|| result` that used to follow it was unreachable: a device that never comes back does not fail
# here, it blocks here. Measured — the emulator was shut down while this script was running and the
# run had to be killed by hand, having printed "rebooting the device" and nothing ever after. The
# whole point of this layer is to end in one of three states, and hanging is not one of them.
# `adb reboot` is the opposite and needs no clock: on an absent device it exits 1 in milliseconds.
#
# `timeout` execs what it is given, and `adb` here is a shell FUNCTION, so it has to be handed the
# binary: `timeout 180 adb wait-for-device` exits 127 — a wait that never happened, which reads
# exactly like a device that never came back.
say "rebooting the device"
adb reboot || result "NOT MEASURED" "adb reboot failed; the after-reboot pass did not happen"
timeout 180 "$ADB" ${ANDROID_SERIAL:+-s "$ANDROID_SERIAL"} wait-for-device ||
	result "NOT MEASURED" "the device did not come back within 180s of the reboot"

booted=no
for _ in $(seq 1 120); do
	if [ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then
		booted=yes
		break
	fi
	sleep 2
done
[ "$booted" = yes ] || result "NOT MEASURED" "sys.boot_completed never became 1 within 240s"
# The boot receiver runs on ACTION_BOOT_COMPLETED, which the platform sends after sys.boot_completed
# is already set. Reading the restrictions the instant the property flips would race it.
sleep 10
adb shell input keyevent 82 >/dev/null 2>&1 # dismiss a swipe lock; harmless if there is none

pass "pass 2 — as the boot left it" 1 \
	"-Pandroid.testInstrumentationRunnerArguments.class=$AS_FOUND"
case $? in
0) ;;
2) result "NOT MEASURED" "pass 2 reported success without running WipeableAsFoundTest" ;;
3) result "NOT MEASURED" "the device went offline during pass 2, so nothing was measured (adb disconnected)" ;;
*) result "FAIL" "pass 2 (after a real reboot) failed — see the report under $RESULTS" ;;
esac

result "PASS" "provisioned and after a real reboot"
