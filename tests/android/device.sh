#!/usr/bin/env bash
#
# The device half of the Android layers, shared by the scripts that need a provisioned phone.
#
# SOURCED, never executed: it defines functions and resolves adb, and calls `result` — which exits
# — when the environment cannot support a measurement at all. Two scripts use it:
#
#   tests/android/instrumented.sh   the instrumented suite, provisioned and after a reboot
#   tests/android/self-update.sh    FR-15: the server replaces the app on a real device
#
# It exists because both need the same twenty lines of device-owner provisioning, and every one of
# the comments below records something that was wrong once. A second copy of that would rot: the
# copy that gets fixed is the one whose failure was seen most recently.
#
# ANDROID_SERIAL is honoured throughout. Without it exactly one attached device is required —
# running against an unspecified one of several is how you provision a phone you did not mean to,
# and on this machine there is routinely more than one emulator up.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ANDROID="$ROOT/android-dpc"

PKG="io.github.helios57.familyguard"
ADMIN="$PKG/.admin.AdminReceiver"

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

# Refuses to go on against an ambiguous device, and says which one it settled on.
require_one_device() {
	local n stalled
	n="$(attached)"
	if [ "$n" -eq 0 ]; then
		# `offline` and `unauthorized` are listed by `adb devices` and counted by nothing, so
		# without this branch a phone that is plainly plugged in reports as "no device attached" —
		# a message that sends the reader to look at a cable.
		#
		# The likeliest cause on this project is not a cable. self-update.sh enrols the phone, the
		# first policy it applies carries `no_debugging_features`, and the platform then switches
		# the debug bridge off and keeps it off across reboots. An emulator in that state stays
		# listed and answers nothing, forever, and the way out is not a retry.
		stalled="$("$ADB" devices | tail -n +2 | command grep -E 'offline|unauthorized' |
			tr '\t' ' ' | tr '\n' ';' | sed 's/;$//')"
		if [ -n "$stalled" ]; then
			result "NOT MEASURED" "adb lists [$stalled] and can talk to none of them. If a previous tests/android/self-update.sh run enrolled this device, its policy applied no_debugging_features and adb is off for good — restart the AVD with '-wipe-data' (a real phone needs a factory reset). Nothing about the product was exercised."
		fi
		result "NOT MEASURED" "no device or emulator is attached"
	fi
	if [ "$n" -gt 1 ] && [ -z "${ANDROID_SERIAL:-}" ]; then
		result "NOT MEASURED" "$n devices are attached and ANDROID_SERIAL is unset; refusing to provision an unspecified one"
	fi

	say "device: $(adb shell getprop ro.product.model 2>/dev/null | tr -d '\r') / API $(adb shell getprop ro.build.version.sdk 2>/dev/null | tr -d '\r')"
}

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
# at `device_provisioned=0` walks into the setup wizard on the next boot — and both callers
# reboot the device.
#
# When provisioning genuinely cannot be done, that is an environment this layer cannot measure — 2,
# never 1. A FAIL here would say the product is broken when nothing about the product was exercised.
# The one place `dumpsys device_policy` is read, because the probe must be shown to have read
# something: an empty dump greps clean, which is byte-identical to "not the device owner" and would
# send us to re-provision a device that already is one.
#
# Retried, because an empty answer is a FAILED READ and not a device that owns nothing. dumpsys
# allows each service about ten seconds, and system_server is starved right after an install on a
# machine that is at that moment finishing two Gradle assembles and running a second emulator.
# Measured 2026-09-05: this returned nothing once, mid-sweep, and the layer reported "device
# ownership could not be determined" forty minutes into a run that was otherwise fine. It does not
# reproduce on an idle machine — ten reads straight after a reinstall, 454 lines every time — which
# is precisely why it has to be retried here rather than diagnosed at the call site.
#
# Prints the dump and returns 0 when one was read; prints nothing and returns 2 when five attempts
# over ~8 s never produced one. The header line is the readability marker because it is the one line
# the service always emits, whoever owns the device.
dpm_dump() {
	local dump attempt=0
	while [ "$attempt" -lt 5 ]; do
		attempt=$((attempt + 1))
		dump="$(adb shell dumpsys device_policy 2>/dev/null | tr -d '\r')"
		if printf '%s' "$dump" | command grep -q 'Current Device Policy Manager state'; then
			printf '%s\n' "$dump"
			return 0
		fi
		sleep 2
	done
	return 2
}

owner_is_us() {
	local dump
	dump="$(dpm_dump)" || return 2
	printf '%s\n' "$dump" | awk '/Device Owner:/{n=6} n && n-- {print}' | command grep -q "$PKG"
}

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
	dump="$(dpm_dump)" || return 2
	printf '%s' "$dump" | command grep -q 'Enabled Device Admins' || return 2
	printf '%s\n' "$dump" |
		awk '/Enabled Device Admins/{n = 1; next} /mPasswordOwner=/{n = 0} n' |
		command grep -q "$PKG"
}

# Makes this app the device owner if it is not already, and refuses — 2, never 1 — when it
# cannot. Every caller's tests assume both halves: the ownership record, and an admin the
# platform will actually accept calls from.
ensure_device_owner() {
	local foreign out attempt accounts
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
		foreign="$(dpm_dump |
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
		# test the caller then runs assumes is exactly this, and `dpm` printing Success is a claim,
		# not the state.
		if ! owner_is_us; then
			result "NOT MEASURED" "dpm reported success but dumpsys does not show $PKG as device owner"
		fi
		say "provisioned, and read back from dumpsys"
		;;
	esac

	admin_is_enabled
	case $? in
	0) say "the admin is enabled, so DevicePolicyManager calls can land" ;;
	2) result "NOT MEASURED" "dumpsys device_policy has no 'Enabled Device Admins' section, so it could not be determined whether the DPC can act" ;;
	*) result "NOT MEASURED" "$PKG is the recorded device owner but is NOT an enabled device admin: every DevicePolicyManager call will fail with 'Admin ... does not exist or is not owned by uid'. The device's policy state is inconsistent, not the product — re-provision it (emulator: restart with -wipe-data)" ;;
	esac
}

# ---------------------------------------------------- local network access ----
#
# Lets the DPC reach a control plane that is on the phone's own link — which on an emulator is the
# only kind there is.
#
# From Android 37 the platform sorts every destination into "global" or "local" and DROPS an app's
# packets to a local one unless the app holds ACCESS_LOCAL_NETWORK. Dropped, not refused: the socket
# waits out the connect timeout, and the app reports "failed to connect ... after 15000ms" — the
# same sentence it would print for a server that is down, for a wrong port, or for a bench that
# never started. Four runs of self-update.sh were spent on that sentence.
#
# The bench control plane is on the host, which the emulator calls 10.0.2.2, and 10.0.2.0/24 is the
# emulator's own subnet: `dumpsys connectivity trafficcontroller` lists it in sLocalNetAccessMap as
# `false`, meaning local. A phone talking to guard.example.com over the internet is not affected at
# all — this is a property of the bench's address, not of the product.
#
# It has to be granted from here because the app cannot grant it to itself. Measured 2026-09-04 on
# an Android 37 emulator: `setPermissionGrantState(..., GRANTED)` returns true, from a clean state
# and from a policy-fixed one, and `checkSelfPermission` stays DENIED. `pm grant` works, and the
# DPC re-applying its whole policy afterwards leaves it granted.
allow_local_network() {
	local sdk granted refusal
	sdk="$(adb shell getprop ro.build.version.sdk 2>/dev/null | tr -d '\r')"
	case "$sdk" in
	'' | *[!0-9]*) result "NOT MEASURED" "could not read ro.build.version.sdk, so it is unknown whether this device blocks local-network access" ;;
	esac
	if [ "$sdk" -lt 37 ]; then
		say "Android $sdk: no local-network restriction to lift"
		return 0
	fi

	say "granting ACCESS_LOCAL_NETWORK (Android $sdk blocks the bench's address without it)"

	# Granted in a retry loop, and read back from the platform every time, because `pm grant` prints
	# nothing on success and its exit status is 0 on at least one failure — so its silence is not
	# evidence, and the cost of believing it is a run that dies 20 minutes later on a connect timeout
	# that names nothing.
	#
	# The retry is not superstition. This step ran green on a freshly wiped device and NOT MEASURED
	# on 2026-09-05 against a device that instrumented.sh had just used, ~3 minutes after two
	# `adb install -r -d`s — and the identical command by hand, on that same device, granted it
	# first try and it stayed granted through six reads over twelve seconds. Whatever the window is,
	# it is short and it is near the reinstall; what is NOT true is that the permission cannot be
	# granted on that device. Retrying is what tells those two apart, and keeping `pm grant`'s own
	# words is what stops the next person guessing at it the way this comment had to.
	local dump mentions attempt=0
	granted=0
	while [ "$attempt" -lt 5 ]; do
		attempt=$((attempt + 1))
		refusal="$(adb shell pm grant "$PKG" android.permission.ACCESS_LOCAL_NETWORK 2>&1 | tr -d '\r')"
		dump="$(adb shell dumpsys package "$PKG" 2>/dev/null | tr -d '\r')"
		granted="$(printf '%s\n' "$dump" | command grep -c 'ACCESS_LOCAL_NETWORK: granted=true')"
		[ "$granted" -ge 1 ] && break
		# A dump that does not mention the permission AT ALL is not a denial, it is a failed read —
		# a `dumpsys` that timed out, or an app that never declared the permission. Reporting that
		# as "still not granted" would send the reader to the wrong half of the problem.
		mentions="$(printf '%s\n' "$dump" | command grep -c 'android.permission.ACCESS_LOCAL_NETWORK')"
		[ "$mentions" -ge 1 ] || say "attempt $attempt: dumpsys said nothing about the permission at all"
		sleep 2
	done
	[ "$granted" -ge 1 ] ||
		result "NOT MEASURED" "ACCESS_LOCAL_NETWORK is still not granted to $PKG after $attempt attempts, so every connection it makes to the bench control plane would be dropped and reported as a timeout. Nothing about the product would have been measured. pm grant's last words: ${refusal:-(it printed nothing)}"
	[ "$attempt" -eq 1 ] || say "ACCESS_LOCAL_NETWORK took $attempt attempts"
	say "ACCESS_LOCAL_NETWORK is held, read back from dumpsys"
}
