#!/usr/bin/env bash
#
# FR-22.6 on a real device: a bonus app is suspended while there is no earned time, opens once a
# parent's confirmation earns some, and is suspended again when the confirmation is undone.
#
#   0  it ran, and the package manager said suspended=true / false / true at those three points
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# The balance and the engine rule are proven on the server (tests/e2e/plan_test.go) and in both
# engines (the shared vectors). What only a device can show is that the phone, told a new balance by
# the server, changes the platform's suspension with nobody touching it — read back from
# `dumpsys package`. The app it uses is a preinstalled one with a launcher entry, marked a bonus app.
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

run_package_test TestABonusAppOpensOnlyWithEarnedTimeOnARealPhone \
	"with no earned time the bonus app was suspended, a parent's confirmation opened it, and undoing it suspended it again"
