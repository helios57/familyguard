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
# It earns its keep. The pinned-navigation rule passed this script's known-bad input the first time
# it ran: back when the navigation was a bottom bar, the assertion measured it *after* scrolling to
# the end of the page, where a `position: static` bar has flowed to the bottom of the viewport too.
# A negative control that stays green is information, and that one bought a real fix. The rule is
# now about a top bar and the same trap exists at the other end of the page, so case 5 below scrolls
# before it measures rather than after.
#
# Exit: 0 all rules bind · 1 at least one did not · 2 could not measure.
#
# Not part of run_all.sh: it runs the e2e suite fifteen times and deliberately breaks the console in
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

# 5. The header stays at the top of the viewport while the page scrolls under it.
#
#    Measured at the END of the page, which is the mirror of the mistake this script found when the
#    navigation was a bottom bar: there "pinned" was checked after scrolling, where even a static
#    bar has reached the bottom edge, and the rule stayed green with the property gone. A static
#    header is at y=0 at the top of the page too, so checking it there would repeat the same error
#    upside down.
printf '\n.topbar { position: static; }\n' >> "$ASSETS/app.css"
run_case topbar-sticky "it scrolled away with the content"

# 6. The first card is below the header rather than behind it. `fixed` instead of `sticky` takes the
#    header out of flow, and the top of every view slides underneath it where nothing can scroll it
#    back out.
printf '\n.topbar { position: fixed; left: 0; right: 0; }\n' >> "$ASSETS/app.css"
run_case content-behind-topbar "underneath the header"

# 7. Permanent chrome stays inside its budget. This is the rule closest to the complaint the rework
#    came from — a header that grows is how "I had to scroll for everything" returns.
printf '\n.topbar-row { min-height: 200px; }\n' >> "$ASSETS/app.css"
run_case topbar-budget "px budget"

# 8. The drawer can be opened at all. Below 900px it holds the whole navigation, so a menu button
#    that is not there is four of the five screens becoming unreachable — with nothing on the page
#    to say so.
printf '\n#menu-open { display: none !important; }\n' >> "$ASSETS/app.css"
run_case drawer-reachable "there is no visible menu button"

# 9. Following a link closes the drawer. A menu left sitting open over the page it just navigated to
#    is the defect every hand-rolled drawer has, and one no check of "did the route change" can see.
#
#    BOTH paths have to go, not one. There are two — the listener on the nav itself, which also
#    covers tapping the link for the screen you are already on (no hashchange, so no route event),
#    and onRoute's own call. Removing either alone leaves the other closing the drawer and the rule
#    stays green while looking calibrated, which is the failure mode this whole script exists for.
python3 - "$ASSETS/app.js" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read()
for needle in (
    "  document.getElementById('mainnav').addEventListener('click', closeDrawer);\n",
    "  closeDrawer();\n  refresh();\n}\n\nconst VIEWS",
):
    if needle not in s:
        sys.exit("could not find %r to remove" % needle[:60])
s = s.replace("  document.getElementById('mainnav').addEventListener('click', closeDrawer);\n", "", 1)
s = s.replace("  closeDrawer();\n  refresh();\n}\n\nconst VIEWS", "  refresh();\n}\n\nconst VIEWS", 1)
open(p, 'w').write(s)
PY
[ $? -eq 0 ] || { echo "NOT MEASURED: could not break app.js"; exit 2; }
run_case drawer-closes-on-nav "the drawer to close behind the link it followed"

# 10. The drawer's destinations are in its LOWER half. This is the rule that pays for moving the
#     navigation off the bottom of the screen: the ☰ is a corner reach, and it is allowed to be one
#     because it happens once. Top-aligning the links inside the drawer makes every navigation a
#     full-screen stretch instead, which is strictly worse than the tab bar this replaced — and
#     nothing else here would notice, because the drawer still opens, still holds five links and
#     still closes behind them.
printf '\n#drawer-nav { margin-top: 0; }\n' >> "$ASSETS/app.css"
run_case drawer-reach "the destinations are not in the drawer's lower half"

# 11. The sign-in button is on screen when the page loads. The layout this replaced centred the card
#     in a 100dvh grid; pushing it down reproduces what that did on a shorter phone.
printf '\n.signin-head { padding-top: 900px; }\n' >> "$ASSETS/app.css"
run_case signin-above-fold "so it is below the fold"

# 12. The provisioning sheet fits the screen.
printf '\n#sheet { width: 520px !important; max-width: none !important; }\n' >> "$ASSETS/app.css"
run_case sheet-width "the provisioning sheet is"

# 13. The QR is big enough for a second phone's camera.
printf '\n#sheet-body svg { width: 60px !important; height: 60px !important; }\n' >> "$ASSETS/app.css"
run_case qr-size "the provisioning QR renders"

# 14. Tap targets. Already calibrated in anger — `.pill` shipped at 36px and `#sheet-close` at 43x44,
#     and this rule is what found both — but a rule proven by a defect that has since been fixed is a
#     rule with no live evidence, so it is broken here too.
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
  echo "CALIBRATION: all 14 rules bind — each observed red for its own reason, and green when restored."
  exit 0
fi
echo "CALIBRATION: $fails problem(s). Logs in $LOGS"
exit 1
