#!/usr/bin/env bash
#
# Calibrates every rule TestConsoleRendersOnAPhone enforces.
#
# The rules in mobile_test.go are the only ones in this repo that measure a *rendered* page, and a
# rule that has never been red has not been shown to bind to anything. So each one gets broken here
# on purpose, and the run has to go red **naming that rule** — red for the wrong reason is a rule
# that is still unmeasured. Afterwards the assets are restored and the suite has to be green again;
# without that half, a guard that fails on everything would look perfectly calibrated.
#
# It earns its keep. Rule 5 (the tab bar is pinned to the bottom) passed this script's known-bad
# input the first time it ran: the assertion measured the bar *after* scrolling to the end of the
# page, where a `position: static` bar has flowed to the bottom of the viewport too. A negative
# control that stays green is information, and that one bought a real fix.
#
# Exit: 0 all rules bind · 1 at least one did not · 2 could not measure.
#
# Not part of run_all.sh: it runs the e2e suite ten times and deliberately breaks the console in
# between. Run it after changing app.css, app.js, index.html or the guard itself.
set -u

cd "$(dirname "$0")" || { echo "NOT MEASURED: cannot cd to the script's directory"; exit 2; }
E2E="$PWD"
ASSETS="$(cd ../../backend/internal/console/assets 2>/dev/null && pwd)" || {
  echo "NOT MEASURED: the console assets are not where this script expects them"; exit 2; }

BK="$(mktemp -d)" || { echo "NOT MEASURED: no temp directory"; exit 2; }
cp "$ASSETS/app.css" "$ASSETS/app.js" "$ASSETS/index.html" "$BK/" || {
  echo "NOT MEASURED: could not back the assets up"; exit 2; }
LOGS="$(mktemp -d)"
restore() { cp "$BK/app.css" "$BK/app.js" "$BK/index.html" "$ASSETS/"; }
cleanup() { restore; rm -rf "$BK"; }
trap cleanup EXIT

fails=0
run_case() {
  local name="$1" want="$2"
  local log="$LOGS/$name.log"
  ( cd "$E2E" && ./run.sh -run TestConsoleRendersOnAPhone ) > "$log" 2>&1
  local rc=$?
  restore
  if [ "$rc" -eq 2 ]; then
    echo "NOT MEASURED [$name]: the suite could not run at all — see $log"
    fails=$((fails + 1)); return
  fi
  if [ "$rc" -eq 0 ]; then
    echo "FAILED [$name]: the suite stayed GREEN with the page deliberately broken"
    fails=$((fails + 1)); return
  fi
  if command grep -qF "$want" "$log"; then
    echo "ok  [$name] red, and it named it"
  else
    echo "FAILED [$name]: red, but for the wrong reason — no '$want' in $log"
    fails=$((fails + 1))
  fi
}

# 1. The console's JavaScript must have executed at all. Removing one closing parenthesis is not a
#    contrived break: it is the defect this whole guard was written after, and it shipped.
python3 - "$ASSETS/app.js" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read()
broken = s.replace("          }))))));\n", "          })))));\n", 1)
if broken == s:
    sys.exit("could not find the line to break")
open(p, 'w').write(broken)
PY
[ $? -eq 0 ] || { echo "NOT MEASURED: could not break app.js"; exit 2; }
run_case script-ran "the console's JavaScript did not run"

# 2. Nothing sticks out past the viewport.
printf '\n.card { min-width: 520px; }\n' >> "$ASSETS/app.css"
run_case page-overflow "stick out past the 360 px viewport"

# 3. Only the child switcher scrolls sideways.
printf '\n.list { overflow-x: auto; }\n.list li { min-width: 520px; }\n' >> "$ASSETS/app.css"
run_case sideways-scroller "Only the child switcher is meant to"

# 4. No input below 16px — the size mobile Safari zooms in on and never zooms back out of.
printf '\ninput, select, textarea { font-size: 13px; }\n' >> "$ASSETS/app.css"
run_case small-font "input(s) below 16px"

# 5. The tab bar is pinned to the bottom, at rest and not merely at the end of the page.
printf '\n.tabbar { position: static; }\n' >> "$ASSETS/app.css"
run_case tabbar-pinned "it is not pinned to the bottom of the screen"

# 6. The last card is reachable rather than parked under the bar.
printf '\n.view { padding-bottom: 0 !important; }\n' >> "$ASSETS/app.css"
run_case content-behind-tabbar "behind the navigation"

# 7. The provisioning sheet fits the screen.
printf '\n#sheet { width: 520px !important; max-width: none !important; }\n' >> "$ASSETS/app.css"
run_case sheet-width "the provisioning sheet is"

# 8. The QR is big enough for a second phone's camera.
printf '\n#sheet-body svg { width: 60px !important; height: 60px !important; }\n' >> "$ASSETS/app.css"
run_case qr-size "the provisioning QR renders"

# 9. Tap targets. Already calibrated in anger — `.pill` shipped at 36px and `#sheet-close` at 43x44,
#    and this rule is what found both — but a rule proven by a defect that has since been fixed is a
#    rule with no live evidence, so it is broken here too.
printf '\n.btn { min-height: 30px; }\n.btn-icon { min-width: 30px; }\n' >> "$ASSETS/app.css"
run_case tap-target "tap target(s) below 44 px"

echo
restore
( cd "$E2E" && ./run.sh -run TestConsoleRendersOnAPhone ) > "$LOGS/green.log" 2>&1
if [ $? -eq 0 ]; then
  echo "ok  [restored] green"
else
  echo "FAILED: the suite is not green with the assets restored — see $LOGS/green.log"
  fails=$((fails + 1))
fi

echo
if [ "$fails" -eq 0 ]; then
  echo "CALIBRATION: all 9 rules bind — each observed red for its own reason, and green when restored."
  exit 0
fi
echo "CALIBRATION: $fails problem(s). Logs in $LOGS"
exit 1
