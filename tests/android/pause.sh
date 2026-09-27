#!/usr/bin/env bash
#
# FR-21 on a real device: a pause from the control plane suspends what a child can open, leaves the
# dialer alone, and lifting it gives everything back.
#
#   0  it ran, and the package manager said suspended=true under the pause and false after it
#   1  it ran and it did not
#   2  it could not be run at all — NOT a pass
#
# The pause is proven on the server (tests/e2e/pause_test.go) and in both engines (the shared
# vectors). What only a device can show is the platform's answer: the DPC, as device owner, calling
# setPackagesSuspended — read back from `dumpsys package`, which is what the launcher obeys.
#
# Like remote-adb.sh this layer keeps adb: the test switches Allow debugging on before the phone
# enrols. The app it pauses is a preinstalled one with a launcher entry (DeskClock on the Google
# APIs images); the fixture app, which has no components at all, is the negative control a pause must
# leave alone (FR-3.12).
#
# Emulator: on familyguard37 (API 37) `-gpu swiftshader_indirect` alone aborts SurfaceFlinger in a loop
# ("Assertion failed: !rcEnc->featureInfo()->hasReadColorBufferDma"), which takes device_policy and
# StorageManager down with it and reads like a provisioning failure. Measured 2026-09-27. Launch with
#   emulator -avd familyguard37 -no-window -no-audio -no-boot-anim -no-snapshot -wipe-data \
#            -gpu swiftshader_indirect -feature Minigbm
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

run_package_test TestAPauseSuspendsAppsOnARealPhone \
	"the pause suspended a preinstalled app on the device, left the dialer and the icon-less fixture, and lifting it gave the app back"
