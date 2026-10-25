#!/usr/bin/env bash
#
# FR-9.4 on a real device: a ringing siren keeps ringing untouched, and through a display timeout with
# another screen over it, and stops within 5 s of a volume key, of the side key, and (from API 36) of a
# volume key while the notification shade holds the focus.
#
#   0  it ran, and dumpsys audio showed the alarm player started for 10 s untouched, then not
#      started within 5 s of KEYCODE_VOLUME_DOWN; rung again, of KEYCODE_POWER; rung again and
#      covered by Settings, still started 3 s after the display timed out, then not within 5 s of
#      KEYCODE_POWER; and rung again
#      with the shade pulled down over the Stopp screen, of KEYCODE_VOLUME_UP — that last case only
#      from API 36, and the result line says when it was not measured
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# The press rules are unit-tested (SirenKeysTest). What only a device can show is that the Stopp
# screen comes up over everything, an injected key reaches it, the side key arrives as the display
# going off, a volume key the screen never sees still reaches the siren, and the tone really ends.
# Not shown: a hardware key on a real handset, which is the owner's measurement on the family phone.
#
# Emulator: launch as pause.sh says.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

case "${1:-}" in
-h | --help)
	sed -n '2,21p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

# Below API 36 the platform does not hand a volume key to a remote-volume session (the test's comment
# cites where), so there the shade case is not measured and the pass line must not claim it.
sdk="$(adb shell getprop ro.build.version.sdk 2>/dev/null | tr -d '\r')"
if [ -n "$sdk" ] && [ "$sdk" -ge 36 ] 2>/dev/null; then
	pass="the siren rang on untouched and through a covered display timeout, and a volume key, the side key and a volume key under the shade each silenced it within 5 s"
else
	pass="the siren rang on untouched and through a covered display timeout, and a volume key and the side key each silenced it within 5 s; a volume key under the shade NOT MEASURED (API ${sdk:-unknown} < 36)"
fi
run_package_test TestAnyButtonStopsTheSirenOnARealPhone "$pass"
