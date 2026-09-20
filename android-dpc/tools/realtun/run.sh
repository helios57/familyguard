#!/usr/bin/env bash
# Real-TUN end-to-end test for the ad filter.
#
# Gradle runs OUTSIDE the namespace on purpose: running it as root would leave root-owned files
# across the build directory. It only prints the classpath; the namespace gets a plain `java`.
set -euo pipefail
cd "$(dirname "$0")/../.."

./gradlew :app:printUnitTestClasspath -q
CP="$PWD/app/build/unit-test-classpath.txt"
[ -s "$CP" ] || { echo "NOT MEASURED: the classpath file is empty" >&2; exit 2; }

exec sudo unshare -n python3 tools/realtun/realtun_test.py --classpath "$CP" "$@"
