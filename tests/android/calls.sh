#!/usr/bin/env bash
#
# FR-31 on a real device: with the switch on and the call-screening role held, a contact rings; a
# stranger is rejected (no ring, logged only as blocked, no missed-call notification, reported); a
# family number that is not a contact rings; the switch off lets a stranger ring; and the role taken
# away is reported red.
#
#   0  it ran, and Telecom's call list showed RINGING for the contact, the family number and the
#      stranger with the switch off, and the stranger with the switch on left it without ringing,
#      was in the call log only as BLOCKED, added no missed-call notification (while the family
#      number's added one) and was listed on the console; the phone reported the role
#      not held, held and lost, and (API 34+) disallow_config_default_apps followed the role
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# The decision is unit-tested (CallDecisionTest). What only a device can show is that Telecom binds
# the service, passes it only non-contacts, and that a rejected call never rings. Not shown: a real
# SIM on a real network, hidden numbers, SMS and WhatsApp, which are the owner's measurement.
#
# Emulator: launch as pause.sh says. The modem must be up (gsm.sim.operator.iso-country set).
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

case "${1:-}" in
-h | --help)
	sed -n '2,20p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

run_package_test TestOnlyContactsCanCallARealPhone \
	"a contact and a family number rang, a stranger was rejected — blocked in the call log, no missed-call notification — and reported, the switch off let a stranger ring, and the role was reported held and lost"
