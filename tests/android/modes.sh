#!/usr/bin/env bash
#
# FR-26.1, FR-26.2 and FR-27 on a real device, read from authorities: the phone lets go of the
# server a minute after the screen goes off (the socket table), a command queued for it in Doze
# arrives by the 5-minute poll (the command queue), and Live wakes it and streams a position about
# every 10 s (the server), then lets go again when stopped. Takes about 15 minutes.
#
#   0  it ran, and all of that held
#   1  it ran and something did not
#   2  it could not be run at all — NOT a pass
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

run_package_test TestAPhoneWithItsScreenOffLetsGoOfTheServerAndStillHearsOfChanges \
	"the stream closed with the screen off, a command arrived by the poll, and Live streamed positions"
