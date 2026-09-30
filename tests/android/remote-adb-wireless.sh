#!/usr/bin/env bash
#
# FR-19.3 on a device: with the ad filter on and Wireless debugging on, the phone finds its own adbd's
# port with nobody at the screen to read it off — on its own loopback, by the adb protocol's answer.
# Runs on an API 33 and an API 37 emulator alike; both run Wireless debugging over their virtual
# Wi-Fi. Takes about 5 minutes.
#
#   0  it ran, and the phone connected to adbd's Wireless-debugging port, found on its loopback
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass (including a tunnel that never came up for want of
#      internet to fetch the public list)
#
# Emulator: launch as pause.sh says.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

case "${1:-}" in
-h | --help)
	sed -n '2,14p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

run_package_test TestRemoteADBFindsWirelessDebuggingWithTheFilterOn \
	"with the filter on, the phone found adbd's Wireless-debugging port on its own loopback and connected to it"
