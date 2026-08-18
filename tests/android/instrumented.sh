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

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ANDROID="$ROOT/android-dpc"
RESULTS="$ANDROID/app/build/outputs/androidTest-results/connected"

PKG="io.github.helios57.familyguard"
ADMIN="$PKG/.admin.AdminReceiver"
AS_FOUND="io.github.helios57.familyguard.WipeableAsFoundTest"

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

# The final line this script prints, and the only one tests/run_all.sh reads.
result() { # result <PASS|FAIL|NOT MEASURED> <reason>
	printf '\nRESULT\t%s\t%s\n' "$1" "${2:-}"
	case "$1" in
	PASS) exit 0 ;;
	FAIL) exit 1 ;;
	*) exit 2 ;;
	esac
}

say() { printf '\n---- %s\n' "$*"; }

# ------------------------------------------------------------------- adb ----
#
# Android Studio installs adb under platform-tools and does not put it on PATH, so "adb not found"
# on a machine that plainly has the SDK sends the reader hunting for a missing install instead of an
# unplugged phone.
ADB=""
for sdk in "${ANDROID_HOME:-}" "${ANDROID_SDK_ROOT:-}" "$HOME/Android/Sdk"; do
	if [ -n "$sdk" ] && [ -x "$sdk/platform-tools/adb" ]; then
		ADB="$sdk/platform-tools/adb"
		break
	fi
done
if [ -z "$ADB" ] && command -v adb >/dev/null 2>&1; then
	ADB="$(command -v adb)"
fi
[ -n "$ADB" ] || result "NOT MEASURED" "no adb on PATH, in ANDROID_HOME/ANDROID_SDK_ROOT or in ~/Android/Sdk"
[ -x "$ANDROID/gradlew" ] || result "NOT MEASURED" "no Gradle wrapper at $ANDROID/gradlew"

adb() { "$ADB" ${ANDROID_SERIAL:+-s "$ANDROID_SERIAL"} "$@"; }

# `adb devices` prints a header and exits 0 with nothing attached, so the header is stripped and the
# rest counted. Its exit status says only that adb ran.
attached() { "$ADB" devices | tail -n +2 | command grep -c 'device$'; }

# Whether the phone is still *there*, as opposed to still listed.
#
# `adb devices` is a cache of transports, not a probe. Measured: the emulator was shut down while
# instrumentation was running, and it went on being reported as `device` for several seconds after
# it was gone — long enough that a snapshot taken the moment Gradle failed saw a healthy phone and
# this layer called the product broken. That is the exact false red the offline branch exists to
# prevent, and counting was never going to catch it. `adb shell true` round-trips to the device.
#
# The retry is what makes the answer safe in BOTH directions, and only one of them is obvious. A
# device that blips resolves to FAIL — a red we may well have earned, and the worse mistake would be
# to file a genuine product failure as "not measured", because that is a red nobody ever looks at
# again. Only a phone still unreachable at the end of the window is reported as absent.
reachable() {
	local _
	for _ in $(seq 1 15); do
		timeout 10 "$ADB" ${ANDROID_SERIAL:+-s "$ANDROID_SERIAL"} shell true >/dev/null 2>&1 && return 0
		sleep 1
	done
	return 1
}

n="$(attached)"
if [ "$n" -eq 0 ]; then
	result "NOT MEASURED" "no device or emulator is attached"
fi
if [ "$n" -gt 1 ] && [ -z "${ANDROID_SERIAL:-}" ]; then
	result "NOT MEASURED" "$n devices are attached and ANDROID_SERIAL is unset; refusing to provision an unspecified one"
fi

say "device: $(adb shell getprop ro.product.model 2>/dev/null | tr -d '\r') / API $(adb shell getprop ro.build.version.sdk 2>/dev/null | tr -d '\r')"

# --------------------------------------------------------- device owner ----
#
# A device owner can only be set on a device that has not finished setup — that is true of a real
# phone (NFC, QR or afw#setup, all of them during the setup wizard) and it is true here. A booted
# emulator has `device_provisioned=1`, so `dpm set-device-owner` refuses.
#
# It refuses with the WRONG REASON: "Not allowed to set the device owner because there are already
# some accounts on the device", on a device where `dumpsys account` reports `Accounts: 0`. Measured
# on this AVD. Following that message leads to hunting an account that does not exist; the two
# settings below are what actually gate it. The flags are put back afterwards, because a device left
# at `device_provisioned=0` walks into the setup wizard on the next boot — and the next boot is the
# reboot this script performs.
#
# When provisioning genuinely cannot be done, that is an environment this layer cannot measure — 2,
# never 1. A FAIL here would say the product is broken when nothing about the product was exercised.
owner_is_us() {
	local dump
	dump="$(adb shell dumpsys device_policy 2>/dev/null | tr -d '\r')"
	# The probe must be shown to have read something. An empty dump greps clean, which is
	# byte-identical to "not the device owner" and would send us to re-provision a device that
	# already is one.
	if ! printf '%s' "$dump" | command grep -q 'Device Owner\|Current Device Policy Manager state'; then
		return 2
	fi
	printf '%s\n' "$dump" | awk '/Device Owner:/{n=6} n && n-- {print}' | command grep -q "$PKG"
}

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

owner_is_us
case $? in
0) say "already the device owner" ;;
2) result "NOT MEASURED" "dumpsys device_policy returned nothing readable, so device ownership could not be determined" ;;
*)
	# Before retrying anything: is there already a device owner, and is it somebody else? A device
	# owner can only be replaced by a factory reset, so retrying five times against a foreign one
	# burns 25 s to arrive at a truncated message about accounts that do not exist. This branch
	# exists because it happened: after the package rename this AVD still carried the OLD package
	# as device owner, and `dpm` reported the account fiction rather than the collision. The reason
	# is one line and it names the way out.
	foreign="$(adb shell dumpsys device_policy 2>/dev/null | tr -d '\r' |
		awk '/Device Owner:/{n=6} n && n-- {print}' |
		command grep -o 'ComponentInfo{[^}]*}' | head -1)"
	case "$foreign" in
	"") ;;
	*"$PKG"*) ;;
	*) result "NOT MEASURED" "this device already has a device owner and it is not us: $foreign. Only a factory reset removes one — restart the AVD with '-wipe-data', or factory-reset the phone. Nothing about the product was exercised." ;;
	esac

	say "provisioning: dpm set-device-owner $ADMIN"
	# Retried, because the first attempt on a device that has just been wiped fails and the same
	# command succeeds a few seconds later — measured, by hand, with the settings at the same values.
	# The platform is still settling after first boot and says so in the least helpful way available.
	one_line() { printf '%s' "$1" | command grep -v '^[[:space:]]*at ' | tr '\n' ' ' | cut -c1-200; }
	out=""
	for attempt in 1 2 3 4 5; do
		adb shell settings put global device_provisioned 0
		adb shell settings put secure user_setup_complete 0
		out="$(adb shell dpm set-device-owner "$ADMIN" 2>&1 | tr -d '\r')"
		adb shell settings put global device_provisioned 1
		adb shell settings put secure user_setup_complete 1
		printf '%s' "$out" | command grep -q 'Success' && break
		printf 'attempt %s: %s\n' "$attempt" "$(one_line "$out")"
		sleep 5
	done
	printf '%s\n' "$out"
	if ! printf '%s' "$out" | command grep -q 'Success'; then
		# The reason travels up into run_all.sh's summary table as one line, because a 40-line
		# reason there is a reason nobody reads — and it carries the disproof of dpm's own
		# explanation with it. "There are already some accounts on the device" is what this
		# command says on a device where `dumpsys account` reports Accounts: 0, and a reader who
		# believes it spends the next hour looking for an account that does not exist.
		accounts="$(adb shell dumpsys account 2>/dev/null | tr -d '\r' | command grep -m1 -E '^ *Accounts:' | tr -d ' \t')"
		result "NOT MEASURED" "could not make this app the device owner in 5 attempts; dumpsys reports ${accounts:-Accounts:unreadable} whatever dpm says below: $(one_line "$out")"
	fi
	# Read it back from the platform rather than trusting the word "Success" — the one thing every
	# test below assumes is exactly this, and `dpm` printing Success is a claim, not the state.
	if ! owner_is_us; then
		result "NOT MEASURED" "dpm reported success but dumpsys does not show $PKG as device owner"
	fi
	say "provisioned, and read back from dumpsys"
	;;
esac

# Ownership is a record. This is whether the DPC can act, and the two can disagree.
#
# `dumpsys device_policy` reports the device OWNER and the list of enabled device ADMINS in separate
# sections. Measured: after the emulator was shut down abruptly mid-test, the owner record survived
# and the admin list came back EMPTY — so every DevicePolicyManager call failed with *"Admin … does
# not exist or is not owned by uid 10192"* while this script, having asked only about ownership,
# announced "already the device owner" and ran the suite. Three tests went red naming restrictions
# that were never the problem, and the layer that exists to be the one honest word about the device
# spent its credibility on an environment fault.
#
# Nothing about the product is exercised in that state, so it is a 2. The direction matters: this
# must not become a way for a genuine red to be filed as "not measured", and it cannot, because a
# DPC that failed to declare its admin never becomes device owner in the first place — the branch
# above catches that, and ManifestAndPlatformCallsTest catches the declaration itself.
admin_is_enabled() {
	local dump
	dump="$(adb shell dumpsys device_policy 2>/dev/null | tr -d '\r')"
	printf '%s' "$dump" | command grep -q 'Enabled Device Admins' || return 2
	printf '%s\n' "$dump" |
		awk '/Enabled Device Admins/{n = 1; next} /mPasswordOwner=/{n = 0} n' |
		command grep -q "$PKG"
}

admin_is_enabled
case $? in
0) say "the admin is enabled, so DevicePolicyManager calls can land" ;;
2) result "NOT MEASURED" "dumpsys device_policy has no 'Enabled Device Admins' section, so it could not be determined whether the DPC can act" ;;
*) result "NOT MEASURED" "$PKG is the recorded device owner but is NOT an enabled device admin: every DevicePolicyManager call will fail with 'Admin ... does not exist or is not owned by uid'. The device's policy state is inconsistent, not the product — re-provision it (emulator: restart with -wipe-data)" ;;
esac

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
	"-Pandroid.testInstrumentationRunnerArguments.notClass=$AS_FOUND"
case $? in
0) ;;
2) result "NOT MEASURED" "pass 1 reported success without running its tests" ;;
3) result "NOT MEASURED" "the device went offline during pass 1, so nothing was measured (adb disconnected)" ;;
*) result "FAIL" "pass 1 (provisioned state) failed — see the report under $RESULTS" ;;
esac

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
