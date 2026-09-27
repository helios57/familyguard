#!/usr/bin/env bash
#
# FR-23 on a real device: the alarm clock rings at its minute with Wi-Fi and data off, the screen off
# and the device forced into Doze, and Stop silences it and books the next ring.
#
#   0  it ran, and the platform delivered the ring on time: the ring service ran and an
#      alarm-usage player was started (dumpsys audio), then both ended after Stop
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# What this does not show: a phone that has lain unused for an hour (forced Doze is the platform's
# deepest state, reached at once), and the lock screen of a real handset with a PIN. Those are the
# owner's measurement on the family phone, recorded in IMPLEMENTATION_PLAN.md Phase 37.3.
#
# Emulator: launch as pause.sh says.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

case "${1:-}" in
-h | --help)
	sed -n '2,15p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

run_package_test TestTheAlarmRingsOnARealPhoneOfflineAndInDoze \
	"the alarm rang at its minute offline and in forced Doze, on the alarm stream, and Stop silenced it and booked the next one"
