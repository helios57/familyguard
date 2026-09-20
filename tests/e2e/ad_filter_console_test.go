package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A phone whose ad filter is switched on and whose tunnel is not running has to say WHY, in the
// console, in its own words (FR-6.11).
//
// This is the state the family was in on 2026-09-20 and the console could not explain: the switch
// was on, 180423 rules were compiled and loaded, the heartbeat was thirty seconds old, and
// `ad_filter_running` was false for hours. The line the console drew guessed at the remedy — "the
// list may not have downloaded, check that the phone is online" — and was wrong in both halves.
// The phone knew the real reason the whole time and printed it in its own notification shade,
// where nothing but a person holding the phone can read it.
//
// Driven through a real browser because that is the only place the last defect of this shape was
// visible: every API-level assertion about the approval queue was green while the console rendered
// nothing at all, and the fault was in how the console read the answer rather than in the answer.
func TestTheConsoleSaysWhyTheAdFilterIsNotRunning(t *testing.T) {
	h := newHarness(t)

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nils")
	device := h.newDevice(parent.Token, child.ID, "The blue phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Samsung Galaxy S20", "Android 13", nil)

	// The parent's switch. Without it the console says nothing about the filter at all, which is
	// correct: "not running" on a child whose filter is off is not news.
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", parent.Token, map[string]any{
		"ad_filter":          true,
		"ad_filter_list_url": "https://example.invalid/list.txt",
	}).expect(http.StatusOK)

	const why = "the network offers no resolver to forward queries to"
	beat := func(body map[string]any) {
		t.Helper()
		body["connectivity"] = "wifi"
		h.call(http.MethodPost, "/device/heartbeat", enrolled.DeviceToken, body).expect(http.StatusOK)
	}
	// The measurement, exactly as the family's phone filed it: a list that downloaded and compiled,
	// and no tunnel. The rule count is here so the assertion below cannot be satisfied by a console
	// that is merely pessimistic about a phone it knows nothing about.
	beat(map[string]any{
		"ad_filter_rules":      180423,
		"ad_filter_fetched_at": time.Now().UTC().Format(time.RFC3339),
		"ad_filter_running":    false,
		"ad_filter_reason":     why,
	})

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")

	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")

	var home card
	b.eval(deviceCardJS, &home)
	warnings := strings.Join(home.Warnings, "\n")

	if !strings.Contains(warnings, "The ad filter is not running on this phone") {
		t.Errorf("the card does not say the filter is off on a phone that reported exactly that.\n"+
			"warnings: %q\ncard: %s", home.Warnings, home.Text)
	}
	if !strings.Contains(warnings, why) {
		t.Errorf("the card does not carry the phone's own reason, so a parent cannot tell which of "+
			"three unrelated faults is holding the tunnel.\nwarnings: %q", home.Warnings)
	}
	// The guess must be GONE, not merely accompanied. Shown alongside a real reason it sends a
	// parent to check a list that downloaded and a connection that is up — which is how an hour
	// went into the wrong diagnosis.
	if strings.Contains(warnings, "may not have downloaded") {
		t.Errorf("the card still guesses at the remedy next to the phone's own answer: %q", home.Warnings)
	}

	// And it goes away when the phone says there is nothing to explain. A warning that outlives
	// the fault is the defect this console was carrying about updates until 0.6.10, one subsystem
	// over: the state where a parent learns to skip a whole class of line.
	beat(map[string]any{"ad_filter_running": true, "ad_filter_reason": "", "ad_filter_rules": 180423})
	// Awaited, not fired: `refresh` is async, and reading the card out of the same turn would be
	// asserting on the view the previous heartbeat drew.
	b.eval("(async () => { await refresh(); return true; })()", nil)
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view again")

	var fixed card
	b.eval(deviceCardJS, &fixed)
	quiet := strings.Join(fixed.Warnings, "\n")
	if strings.Contains(quiet, why) || strings.Contains(quiet, "ad filter is not running") {
		t.Errorf("the card still explains a tunnel that is up.\nwarnings: %q", fixed.Warnings)
	}
	if !strings.Contains(fixed.Text, "Ad filter: running") {
		t.Errorf("the card does not report the tunnel the phone says is up.\ncard: %s", fixed.Text)
	}
}
