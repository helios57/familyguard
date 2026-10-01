#!/usr/bin/env bash
#
# Screenshots of the phone app, in German, for a person to read: the FamilyGuard screen top to bottom,
# the notification shade, a paused app, and the alarm ringing. Asserts nothing about the pictures.
# Needs E2E_TOUR_DIR, and an emulator whose screencap works (the API 33 image; API 37's aborts).
#
#   0  it ran and wrote the pictures
#   1  it ran and failed on the way
#   2  it could not be run at all — NOT a pass
#
# Emulator: launch as pause.sh says, with -avd familyguard33.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/device.sh"

[ -n "${E2E_TOUR_DIR:-}" ] || { echo "NOT MEASURED: set E2E_TOUR_DIR to the directory for the pictures" >&2; exit 2; }
export E2E_TOUR_DIR

run_package_test TestPhoneTour "the phone tour wrote its pictures to $E2E_TOUR_DIR"
