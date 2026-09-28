#!/usr/bin/env bash
#
# FR-26.4 on a real device, read from Android's own record of the tunnel's routes: with the screen on
# the ad filter carries everything; five minutes after the screen goes off it carries only DNS; the
# screen coming on restores the full route at once; and the phone's energy report holds time in both.
# Takes about 15 minutes.
#
#   0  it ran, and all of that held
#   1  it ran and something did not
#   2  it could not be run at all — NOT a pass (including a tunnel that never came up for want of
#      internet to fetch the public list)
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

run_package_test TestTheFilterCarriesOnlyDNSFiveMinutesAfterTheScreenGoesOff \
	"the route narrowed to DNS five minutes after the screen went off and widened again when it came on"
