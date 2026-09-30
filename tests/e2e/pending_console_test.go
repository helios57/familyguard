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

// pendingCardJS reads the "Wartet auf deine Entscheidung" card out of the rendered Apps page. It
// returns null rather than throwing when the card is absent, because its absence is a state this
// test asserts twice: before anything is waiting, and after the last app has been answered.
const pendingCardJS = `(() => {
  const card = document.querySelector('#view .pending-card');
  if (!card) return null;
  const badge = card.querySelector('.badge');
  return {
    count: badge ? badge.textContent : '',
    rows: Array.from(card.querySelectorAll('li')).map((li) => ({
      label: (li.querySelector('.label b') || {}).textContent || '',
      package: (li.querySelector('.label small') || {}).textContent || '',
      buttons: Array.from(li.querySelectorAll('button')).map((b) => b.textContent.trim()),
      rules: Array.from(li.querySelectorAll('button[data-rule]')).map((b) => b.dataset.rule),
    })),
  };
})()`

type pendingCard struct {
	Count string `json:"count"`
	Rows  []struct {
		Label   string   `json:"label"`
		Package string   `json:"package"`
		Buttons []string `json:"buttons"`
		Rules   []string `json:"rules"`
	} `json:"rows"`
}

// ruleSheetJS reads the rule sheet: which answers it offers, by the server-side key each writes,
// and which one is marked as in force.
const ruleSheetJS = `(() => {
  const options = Array.from(document.querySelectorAll('#sheet .rule-option'));
  return {
    offered: options.map((o) => o.dataset.rule),
    pressed: options.filter((o) => o.getAttribute('aria-pressed') === 'true').map((o) => o.dataset.rule),
  };
})()`

type ruleSheet struct {
	Offered []string `json:"offered"`
	Pressed []string `json:"pressed"`
}

// openRuleSheet opens the sheet with every answer for pkg: from the row's rule chip in the list, or
// from "Andere Regel …" on its row in the queue. Selected by the package id, never by position — the
// list reorders as answers are given.
func openRuleSheet(pkg string) string {
	return fmt.Sprintf(`(() => {
  const rows = Array.from(document.querySelectorAll('#view li[data-package=%q]'));
  const opener = rows.map((li) => li.querySelector('[data-rule-chip], [data-action="more-rules"]')).find(Boolean);
  if (!opener) throw new Error('no row for %s in the rendered page');
  opener.click();
  return true;
})()`, pkg, pkg)
}

// clickCategory gives pkg the answer `rule` — ALLOW, LIMIT, OWN, BLOCK, BONUS, or "none" for no
// rule — through the rule sheet, the way a parent gives any answer that is not on the row itself.
func clickCategory(b *browser, pkg, rule string) {
	b.eval(openRuleSheet(pkg), nil)
	b.waitFor(`document.getElementById('sheet').open && !!document.querySelector('#sheet .rule-option')`, 10*time.Second, "the rule sheet for "+pkg)
	b.eval(fmt.Sprintf(`(() => {
  const opt = document.querySelector('#sheet .rule-option[data-rule=%q]');
  if (!opt) throw new Error('the rule sheet offers no %s; it offers: '
    + Array.from(document.querySelectorAll('#sheet .rule-option')).map((o) => o.dataset.rule).join(' | '));
  opt.click();
})()`, rule, rule), nil)
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
	b.waitFor("document.querySelectorAll('#view .child-card').length > 0", 15*time.Second, "Übersicht")
	var home struct {
		Today   string `json:"today"`
		Pending string `json:"pending"`
	}
	b.eval(`({
	  today: (document.querySelector('#view .child-card .today') || {}).textContent || '',
	  pending: (document.querySelector('#view .child-card [data-pending]') || {}).textContent || '',
	})`, &home)
	// "1 h", not "1 h 0 min": a round hour says no minutes.
	if !strings.Contains(home.Today, "42 min von 1 h") || strings.Contains(home.Today, "1 h 0 min") {
		t.Errorf("the child's card does not report the screen time the phone filed: %q", home.Today)
	}
	if !strings.Contains(home.Pending, "1 App wartet auf deine Entscheidung") {
		t.Errorf("the child's card does not say an app is waiting for a decision, so a parent has no "+
			"way to learn the queue exists: %q", home.Pending)
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
	// The two answers almost everyone gives, on the row itself: the ordinary yes (it counts like
	// every other app) and no.
	if fmt.Sprint(row.Rules) != "[LIMIT BLOCK]" || !hasButton(row.Buttons, "Erlauben") || !hasButton(row.Buttons, "Sperren") {
		t.Errorf("the queue row offers %q (rules %v); want Erlauben (LIMIT) and Sperren (BLOCK)", row.Buttons, row.Rules)
	}
	// And every answer the owner asked for behind "Andere Regel …".
	b.eval(openRuleSheet(pkgChat), nil)
	b.waitFor(`document.getElementById('sheet').open && !!document.querySelector('#sheet .rule-option')`, 10*time.Second, "the rule sheet")
	var sheet ruleSheet
	b.eval(ruleSheetJS, &sheet)
	if fmt.Sprint(sheet.Offered) != "[ALLOW LIMIT OWN BLOCK BONUS]" {
		t.Errorf("the rule sheet offers %v; want every answer and — for an app with no rule — no "+
			"'Keine Regel', because a control that offers the answer already in force is how the "+
			"old two-button version hid its third state", sheet.Offered)
	}
	if len(sheet.Pressed) != 0 {
		t.Errorf("a waiting app shows %v as already chosen", sheet.Pressed)
	}
	b.eval(`document.getElementById('sheet-close').click()`, nil)
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

	b.eval(`document.querySelector('#view .pending-card li[data-package="`+pkgChat+`"] button[data-rule="LIMIT"]').click()`, nil)
	b.waitFor("document.querySelector('#view .pending-card') === null",
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
		t.Fatalf("'Erlauben' stored %+v; it must be a LIMIT with no allowance of its own", got)
	}

	// ---- and the one answer that carries a number ----
	clickCategory(b, pkgChat, "OWN")
	b.waitFor("document.querySelectorAll('#sheet .own-limit input').length > 0", 15*time.Second,
		"the minutes field to appear with the answer it belongs to")
	var minutes string
	b.eval("document.querySelector('#sheet .own-limit input').value", &minutes)
	if minutes != "60" {
		t.Errorf("the allowance field starts at %q; choosing 'Own limit' must store a real "+
			"allowance rather than a zero under a button that says there is one", minutes)
	}
	rules = listRules(t, h, parent.Token, child.ID)
	if got := rules[pkgChat]; got.Action != "LIMIT" || got.LimitMinutes != 60 {
		t.Fatalf("'Eigenes Limit' stored %+v; the console showed 60 minutes", got)
	}
	b.eval(`document.getElementById('sheet-close').click()`, nil)

	// Reversibility, from the same control: an answer a parent can give is one they can take back.
	clickCategory(b, pkgChat, "none")
	b.waitFor("document.querySelector('#view .pending-card') !== null",
		15*time.Second, "the app to return to the queue")
	if _, still := listRules(t, h, parent.Token, child.ID)[pkgChat]; still {
		t.Error("'Keine Regel' left the rule in place, so the app is in the queue and approved at once")
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
