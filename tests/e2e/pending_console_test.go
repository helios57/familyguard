package e2e

// The pending-apps queue as a parent actually meets it (FR-5.4, FR-5.8), in a real browser.
//
// The owner's report was not that approval was missing — the API had it for weeks. It was: "well if
// they are Pending, i need a list with pending apps in the website to approve them and categorize
// them". The queue existed in the policy the phone received and had no surface anywhere a parent
// looks: the Apps tab was a flat alphabetical list with two buttons per row and nothing marking
// which rows were the ones holding the phone. A queue nobody can see is a queue that does not work,
// so what is measured here is the rendered page and the click, not the endpoint underneath.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pendingCardJS reads the "Waiting for your decision" card out of the rendered Apps tab. It returns
// null rather than throwing when the card is absent, because its absence is a state this test
// asserts twice: before anything is waiting, and after the last app has been answered.
const pendingCardJS = `(() => {
  const cards = Array.from(document.querySelectorAll('#view .card'));
  const card = cards.find((c) => {
    const h = c.querySelector('h2');
    return h && h.textContent.indexOf('Waiting for your decision') >= 0;
  });
  if (!card) return null;
  const badge = card.querySelector('.badge');
  return {
    count: badge ? badge.textContent : '',
    rows: Array.from(card.querySelectorAll('li')).map((li) => ({
      label: (li.querySelector('.label b') || {}).textContent || '',
      package: (li.querySelector('.label small') || {}).textContent || '',
      buttons: Array.from(li.querySelectorAll('.seg button')).map((b) => b.textContent),
      pressed: Array.from(li.querySelectorAll('.seg button'))
        .filter((b) => b.getAttribute('aria-pressed') === 'true').map((b) => b.textContent),
    })),
  };
})()`

type pendingCard struct {
	Count string `json:"count"`
	Rows  []struct {
		Label   string   `json:"label"`
		Package string   `json:"package"`
		Buttons []string `json:"buttons"`
		Pressed []string `json:"pressed"`
	} `json:"rows"`
}

// clickCategory taps one of the category buttons on the row for pkg, wherever that row is drawn —
// the queue card at the top or the full list below it. Selecting by the package id rather than by
// position because the list reorders as answers are given, and a test that clicked "the second
// button of the first row" would be asserting a layout nobody promised.
func clickCategory(pkg, label string) string {
	return fmt.Sprintf(`(() => {
  const rows = Array.from(document.querySelectorAll('#view li'))
    .filter((li) => li.textContent.indexOf(%q) >= 0 && li.querySelector('.seg'));
  if (!rows.length) throw new Error('no row for %s in the rendered page');
  const btn = Array.from(rows[0].querySelectorAll('.seg button'))
    .find((b) => b.textContent === %q);
  if (!btn) throw new Error('row for %s has no %s button; it has: '
    + Array.from(rows[0].querySelectorAll('.seg button')).map((b) => b.textContent).join(' | '));
  btn.click();
  return true;
})()`, pkg, pkg, label, pkg, label)
}

func TestTheConsoleShowsTheApprovalQueueAndCategorisesFromIt(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nils")
	device := h.newDevice(parent.Token, child.ID, "The blue phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Samsung Galaxy S20", "Android 13", nil)

	inventory := func(apps []map[string]any) {
		t.Helper()
		h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken,
			map[string]any{"apps": apps}).expect(http.StatusOK)
	}
	// The first report is the baseline — what the child already had. The second is the install that
	// has to reach a parent.
	inventory([]map[string]any{{"package_name": pkgGame, "label": "Brawl Stars"}})
	h.patchPolicy(parent.Token, child.ID, map[string]any{"allow_child_installs": false})
	inventory([]map[string]any{
		{"package_name": pkgGame, "label": "Brawl Stars"},
		{"package_name": pkgChat, "label": "Sky Chat"},
	})

	// Screen time the phone has actually reported, against a limit, so the home card has both
	// halves of a number to draw.
	h.call(http.MethodPost, "/device/usage", enrolled.DeviceToken, map[string]any{
		"samples": map[string]int64{pkgGame: 42 * 60 * 1000},
	}).expect(http.StatusOK)
	h.patchPolicy(parent.Token, child.ID, map[string]any{"daily_limit_minutes": 60})

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")

	// ---- the home card, which is where a parent looks first ----
	//
	// Both lines below come from `/devices/:id/desired-state`, whose body is `{desired, input}`.
	// The console read that envelope as if it were flat, so every field was undefined: the card
	// said "Screen time today: 0 min (no daily limit)" about a phone that had reported 99 minutes,
	// and the line that says apps are waiting for a decision could never be drawn — which is why
	// the family found out about the queue from somewhere other than the console. Nothing was red,
	// because undefined is not an error and "0 min" is a plausible number.
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")
	var home card
	b.eval(deviceCardJS, &home)
	if !strings.Contains(home.Text, "42 min of 1 h 0 min") {
		t.Errorf("the home card does not report the screen time the phone filed.\ncard: %s", home.Text)
	}
	if !strings.Contains(home.Text, "app(s) are paused waiting for your decision") {
		t.Errorf("the home card does not say an app is waiting for a decision, so a parent has no "+
			"way to learn the queue exists.\ncard: %s", home.Text)
	}

	b.eval("document.querySelector('.tab[data-tab=\"apps\"]').click()", nil)
	b.waitFor("document.querySelectorAll('#view .applist li').length > 0", 15*time.Second,
		"the Apps tab to list the phone's apps")

	var queue *pendingCard
	b.eval(pendingCardJS, &queue)
	if queue == nil {
		var text string
		b.eval("document.getElementById('view').textContent.slice(0, 400)", &text)
		t.Fatalf("the Apps tab draws no queue for an app that is waiting.\nrendered: %s", text)
	}
	if queue.Count != "1" {
		t.Errorf("the queue says %q apps are waiting; one is", queue.Count)
	}
	if len(queue.Rows) != 1 {
		t.Fatalf("the queue lists %d rows, one app is waiting: %+v", len(queue.Rows), queue.Rows)
	}
	row := queue.Rows[0]
	// The name the parent knows, not the package id. This is the same defect as the usage list: a
	// screen that prints `com.example.chat` at somebody is asking a question they cannot answer.
	if row.Label != "Sky Chat" {
		t.Errorf("the waiting row reads %q; the phone reported the label %q", row.Label, "Sky Chat")
	}
	if !strings.Contains(row.Package, pkgChat) {
		t.Errorf("the row does not say which package it is about: %q", row.Package)
	}
	// The four answers the owner asked for, by the words they asked for them in.
	for _, want := range []string{"Always free", "Daily limit", "Own limit", "Always blocked"} {
		if !hasButton(row.Buttons, want) {
			t.Errorf("the queue offers no %q button: %q", want, row.Buttons)
		}
	}
	// Undecided IS the state, so it must not also be a button here — a control that offers the
	// answer already in force is how the old two-button version hid its third state.
	if hasButton(row.Buttons, "Undecided") {
		t.Errorf("an app with no rule offers an 'Undecided' button: %q", row.Buttons)
	}
	if len(row.Pressed) != 0 {
		t.Errorf("a waiting app shows %q as already chosen", row.Pressed)
	}
	// The one app that was never waiting must not be in the queue — an assertion that the queue is
	// a queue rather than a second copy of the list.
	if strings.Contains(fmt.Sprint(queue.Rows), pkgGame) {
		t.Errorf("an app from the baseline inventory is in the approval queue: %+v", queue.Rows)
	}

	// ---- answering from the queue ----
	//
	// Counted, because what one tap COSTS is the difference between a parent answering a queue and
	// a parent meeting "too many requests" half way through it. The console used to re-read
	// everything the tab is built from after every answer — rules, devices, each device's
	// inventory and desired state, the catalog, the declared set, the family blocklist — so one tap
	// was nine requests, and the owner was refused around the fifteenth app on 2026-09-20.
	b.eval(`(() => {
      window.__fetches = 0;
      const real = window.fetch;
      window.fetch = (...args) => { window.__fetches += 1; return real(...args); };
      return true;
    })()`, nil)

	b.eval(clickCategory(pkgChat, "Daily limit"), nil)
	b.waitFor("(() => { const cards = Array.from(document.querySelectorAll('#view .card'));"+
		"return !cards.some((c) => { const h = c.querySelector('h2');"+
		"return h && h.textContent.indexOf('Waiting for your decision') >= 0; }); })()",
		15*time.Second, "the queue to empty once the last app is answered")

	var immediate int
	b.eval("window.__fetches", &immediate)
	if immediate > 2 {
		t.Errorf("one answer cost %d requests before the page even redrew; a parent answering a "+
			"hundred apps would spend %d requests and be refused part way through", immediate, immediate*100)
	}
	// The other half, and without it the assertion above would be satisfied by a console that
	// simply stopped re-reading: the authoritative re-read must still happen once the tapping
	// stops, or the page drifts from the server and nobody finds out.
	b.waitFor("window.__fetches > "+fmt.Sprint(immediate), 15*time.Second,
		"the coalesced re-read that follows a burst of answers")

	rules := listRules(t, h, parent.Token, child.ID)
	if got := rules[pkgChat]; got.Action != "LIMIT" || got.LimitMinutes != 0 {
		t.Fatalf("'Daily limit' stored %+v; it must be a LIMIT with no allowance of its own", got)
	}

	// ---- and the one answer that carries a number ----
	b.eval(clickCategory(pkgChat, "Own limit"), nil)
	b.waitFor("document.querySelectorAll('#view .own-limit input').length > 0", 15*time.Second,
		"the minutes field to appear with the answer it belongs to")
	var minutes string
	b.eval("document.querySelector('#view .own-limit input').value", &minutes)
	if minutes != "60" {
		t.Errorf("the allowance field starts at %q; choosing 'Own limit' must store a real "+
			"allowance rather than a zero under a button that says there is one", minutes)
	}
	rules = listRules(t, h, parent.Token, child.ID)
	if got := rules[pkgChat]; got.Action != "LIMIT" || got.LimitMinutes != 60 {
		t.Fatalf("'Own limit' stored %+v; the console showed 60 minutes", got)
	}

	// Reversibility, from the same control: an answer a parent can give is one they can take back.
	b.eval(clickCategory(pkgChat, "Undecided"), nil)
	b.waitFor("(() => { const cards = Array.from(document.querySelectorAll('#view .card'));"+
		"return cards.some((c) => { const h = c.querySelector('h2');"+
		"return h && h.textContent.indexOf('Waiting for your decision') >= 0; }); })()",
		15*time.Second, "the app to return to the queue")
	if _, still := listRules(t, h, parent.Token, child.ID)[pkgChat]; still {
		t.Error("'Undecided' left the rule in place, so the app is in the queue and approved at once")
	}
}

type ruleDTO struct {
	PackageName  string `json:"package_name"`
	Action       string `json:"action"`
	LimitMinutes int    `json:"limit_minutes"`
}

func listRules(t *testing.T, h *harness, token, childID string) map[string]ruleDTO {
	t.Helper()
	var out struct {
		Rules []ruleDTO `json:"rules"`
	}
	h.call(http.MethodGet, "/children/"+childID+"/app-rules", token, nil).
		expect(http.StatusOK).decode(&out)
	byPkg := map[string]ruleDTO{}
	for _, r := range out.Rules {
		byPkg[r.PackageName] = r
	}
	return byPkg
}
