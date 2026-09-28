#!/usr/bin/env bash
#
# FR-26.4 / FR-26.5 on a real device: the ad-free apps bypass the ad filter (read from Android's own
# VPN record), and the phone reports what FamilyGuard spends (read from the server).
#
#   0  it ran, and both held
#   1  it ran and one did not
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

run_package_test TestAdFreeAppsBypassTheFilterAndThePhoneReportsItsEnergy \
	"the ad-free apps bypassed the filter and the phone reported its energy"
