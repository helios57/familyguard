#!/usr/bin/env bash
#
# FR-24.5 on a real device: the Heute screen shows what is on now and tomorrow, from an agenda kept
# on a real server and sent to the phone beside its policy.
#
#   0  it ran, and the screen said both lines (read through uiautomator)
#   1  it ran and it did not
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

run_package_test TestTheHeuteScreenShowsTheAgendaOnARealPhone \
	"the Heute screen showed now and tomorrow from the agenda kept on the server"
