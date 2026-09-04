#!/usr/bin/env bash
#
# Calibrates every check AppUpdaterTest enforces (FR-15.3).
#
# The updater is the one piece of this app that replaces the app, so each of its refusals is the
# last thing standing between a phone and an APK it should not install. A refusal that has never
# been observed to fire is a refusal nobody has shown to work — so each one is deliberately removed
# here, and the suite has to go red **naming that check**. Red for another reason is a check that is
# still unmeasured, so every case names the test it expects to see fail.
#
# The last case is the odd one and the most important: it does not break a refusal at all. It makes
# the updater commit the install itself, which is what every reasonable implementation of this
# function would do — and which would mean the command's acknowledgement is written by a process the
# platform has already killed. The console would show a phone that took the update and never
# answered.
#
# Afterwards the source is restored and the suite has to be green again. Without that half a suite
# that failed on everything would look perfectly calibrated.
#
# Exit: 0 every check binds · 1 at least one did not · 2 could not measure.
#
# Not part of run_all.sh: it edits the source and runs Gradle eight times.
set -u

cd "$(dirname "$0")/.." 2>/dev/null || { echo "NOT MEASURED: cannot find the tests directory"; exit 2; }
ROOT="$(cd .. && pwd)"
SRC="$ROOT/android-dpc/app/src/main/kotlin/io/github/helios57/familyguard/update/AppUpdater.kt"
[ -f "$SRC" ] || { echo "NOT MEASURED: $SRC is missing"; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "NOT MEASURED: python3 is needed to apply the breaks"; exit 2; }

BK="$(mktemp -d)" || { echo "NOT MEASURED: no temp directory"; exit 2; }
LOGS="$(mktemp -d)"
cp "$SRC" "$BK/AppUpdater.kt" || { echo "NOT MEASURED: could not back the source up"; exit 2; }
restore() { cp "$BK/AppUpdater.kt" "$SRC"; }
cleanup() { restore; rm -rf "$BK"; }
trap cleanup EXIT

fails=0

# break <python-expression-file-content> — applies an exact-string edit, refusing if it does not match.
break_with() {
  python3 - "$SRC" <<PY
import sys, pathlib
p = pathlib.Path(sys.argv[1]); s = p.read_text()
old = '''$1'''
new = '''$2'''
if old not in s:
    sys.exit("the break no longer matches the source; this calibration is measuring nothing")
p.write_text(s.replace(old, new, 1))
PY
}

run_case() { # run_case <name> <expected test name fragment>
  # Three separate `local` statements: bash expands every word of one statement before it assigns
  # any of them, so `local a="$1" b="$LOGS/$a"` reads an unset `a` — under `set -u` that is this
  # script exiting before it has measured anything.
  local name="$1"
  local want="$2"
  local log="$LOGS/$name.log"
  ( cd "$ROOT/android-dpc" && ./gradlew :app:testDebugUnitTest --tests '*AppUpdaterTest*' --console=plain ) \
    > "$log" 2>&1
  local rc=$?
  restore
  if [ "$rc" -eq 0 ]; then
    echo "FAILED [$name]: the suite stayed GREEN with the check removed — it binds to nothing"
    fails=$((fails + 1)); return
  fi
  if command grep -qF "$want" "$log"; then
    echo "ok     [$name]: red, naming \"$want\""
  else
    echo "FAILED [$name]: red for the WRONG reason — expected a failure naming \"$want\"; see $log"
    fails=$((fails + 1))
  fi
}

# 1. The size the server declared. Without it a cut-off download is judged only by its hash, and the
#    parent is told the file was wrong rather than that the transfer was interrupted.
break_with 'if (want.size > 0 && downloaded.bytes != want.size) {' \
           'if (false) {' || exit 2
run_case size 'refuses a truncated download and says it was truncated'

# 2. The checksum the server published. This is the check that makes the download the artifact
#    rather than merely something that arrived over the right URL.
break_with 'if (downloaded.checksum != want.packageChecksum) {' \
           'if (false) {' || exit 2
run_case checksum 'refuses a download that does not match the checksum the server published'

# 3. It parses as an APK. Broken by treating an unparseable file as "the same as what is installed",
#    which is what a null-safe shortcut looks like when it is written without thinking.
break_with 'val archive = identify(file)
                ?: return UpdateOutcome.Refused("the server is hosting a file that is not a readable APK")
            val current = installed()' \
           'val current = installed()
            val archive = identify(file) ?: current' || exit 2
run_case unreadable 'refuses when the server hosts something that is not a readable APK'

# 4. The signing certificate. The platform refuses this too — later, quietly, and on the phone.
break_with 'if (!archive.signerSha256.equals(current.signerSha256, ignoreCase = true)) {' \
           'if (false) {' || exit 2
run_case signer 'refuses an APK signed by a different certificate'

# 5. Equal version codes are "already current", not an install. Broken so they fall through.
break_with 'if (archive.versionCode == current.versionCode) {' \
           'if (false) {' || exit 2
run_case already-current 'answers already-current when the phone runs the build the server hosts'

# 6. A downgrade is refused rather than staged. Android refuses it too; the cost of not checking is
#    a console that reports an install that will never happen.
break_with 'if (archive.versionCode < current.versionCode) {' \
           'if (false) {' || exit 2
run_case downgrade 'refuses a downgrade rather than reporting an install that can never happen'

# 7. The one that is not a refusal. Committing here is the obvious implementation and it is wrong:
#    the install kills this process, so the acknowledgement is never sent.
break_with 'return UpdateOutcome.Staged(archive, current.versionCode) { install(file) }' \
           'install(file)
            return UpdateOutcome.Staged(archive, current.versionCode) { install(file) }' || exit 2
run_case commit-timing 'stages a newer build and hands back a commit that has not run yet'

# The other half. A guard that fails on everything looks calibrated until you check this.
restore
( cd "$ROOT/android-dpc" && ./gradlew :app:testDebugUnitTest --tests '*AppUpdaterTest*' --console=plain ) \
  > "$LOGS/restored.log" 2>&1
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "FAILED [restored]: the suite is RED with the source put back — see $LOGS/restored.log"
  fails=$((fails + 1))
else
  echo "ok     [restored]: green again with the source restored"
fi

echo
if [ "$fails" -eq 0 ]; then
  echo "all 7 checks bind, and the restored source is green"
  exit 0
fi
echo "$fails case(s) did not behave — logs in $LOGS"
exit 1
