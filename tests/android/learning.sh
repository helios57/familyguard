#!/usr/bin/env bash
#
# FR-30 on a real device: with the daily limit on at 0 a counted app is paused, two and a half
# minutes in a learning app OPEN it with Wi-Fi and data off, and using up what was learned pauses it
# again.
#
#   0  it ran, and the package manager said suspended=true / false / true at those three points
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# The rules are proven on the server and in both engines (the shared vectors). What only a device can
# show is that UsageStats measures the learning app, the phone credits it and re-evaluates from its
# own cache with no server, and the platform suspension follows — read back from `dumpsys package`.
# The learning app is DeskClock, named by its full package as the prefix; the counted app is Camera2.
# Takes about 15 minutes: the phone re-evaluates offline on its 5-minute poll.
#
# Emulator: launch as pause.sh says.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

case "${1:-}" in
-h | --help)
	sed -n '2,17p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

run_package_test TestLearningTimeOpensACountedAppOfflineOnARealPhone \
	"on at 0 the counted app was paused, learning time opened it offline, and spending it paused it again"
