#!/usr/bin/env bash
#
# FR-26.3 on a real device against the real FCM, read from authorities: the phone registers for push
# with the Firebase project the server names (the server's device view), lets go of its stream with
# the screen off (the server's stream count), and a command queued for it in Doze is acknowledged
# within about a minute (the command queue) — where the 5-minute poll alone takes minutes. Takes about
# 5 minutes.
#
# Needs, in the environment, the four values the control plane takes for push (DEPLOYMENT.md):
# FCM_CREDENTIALS (a service-account key, JSON or base64 of it), FCM_APPLICATION_ID, FCM_API_KEY and
# FCM_SENDER_ID of a Firebase project whose Android app is this package. The emulator image must carry
# Google Play services (a google_apis image does).
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
	sed -n '2,20p' "${BASH_SOURCE[0]}"
	exit 0
	;;
"") ;;
*)
	printf 'unknown argument %q\n' "$1" >&2
	exit 2
	;;
esac

for v in FCM_CREDENTIALS FCM_APPLICATION_ID FCM_API_KEY FCM_SENDER_ID; do
	[ -n "${!v:-}" ] || result "NOT MEASURED" "$v is not set; a real push needs a Firebase project"
done
export FCM_CREDENTIALS FCM_APPLICATION_ID FCM_API_KEY FCM_SENDER_ID

run_package_test TestARestingPhoneIsWokenByARealPush \
	"the phone registered, rested in Doze, and a push brought a command within about a minute"
