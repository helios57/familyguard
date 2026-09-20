#!/usr/bin/env bash
# The real-TUN end-to-end tests for the ad filter: the DNS half, then the SNI half.
#
# Gradle runs OUTSIDE the namespace on purpose: running it as root would leave root-owned files
# across the build directory. It only prints the classpath; the namespace gets a plain `java`.
#
# Each arm gets its own `unshare -n`, so one test's routing cannot become another's input and
# nothing it does outlives it. Exit 0 all green, 1 measured wrong, 2 could not measure.
set -uo pipefail
cd "$(dirname "$0")/../.."

./gradlew :app:printUnitTestClasspath -q || exit 2
CP="$PWD/app/build/unit-test-classpath.txt"
[ -s "$CP" ] || { echo "NOT MEASURED: the classpath file is empty" >&2; exit 2; }

only="${1:-all}"
worst=0
for test in dns sni; do
  case "$only" in all|"$test") ;; *) continue ;; esac
  case "$test" in
    dns) script=tools/realtun/realtun_test.py ;;
    sni) script=tools/realtun/realtun_sni_test.py ;;
  esac
  echo "=== $test ==="
  sudo unshare -n python3 "$script" --classpath "$CP"
  rc=$?
  # 2 (not measured) outranks 1 (measured wrong): a run that could not measure must never be
  # summarised by a failure it did not observe.
  if [ "$rc" -gt "$worst" ]; then worst=$rc; fi
done
exit "$worst"
