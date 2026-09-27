package e2e

// FR-20 as people meet it in a real browser: the primary admin picks and changes roles, and a
// guardian who signs in sees their window, not an admin console full of refusals.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestThePrimaryAdminPicksAndChangesRolesInTheConsole(t *testing.T) {
	h := newHarness(t)
	h.signIn(primaryParent)
	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.eval("document.querySelector('.tab[data-tab=\"family\"]').click()", nil)
	b.waitFor("!!document.querySelector('#view form.add-person')", 15*time.Second, "the People & rights card")

	b.eval(`(() => { const f = document.querySelector('#view form.add-person');
	  f.querySelector('input[type=email]').value = 'guardian@family.test';
	  f.querySelector('select').value = 'GUARDIAN';
	  f.requestSubmit(); })()`, nil)
	b.waitFor(`Array.from(document.querySelectorAll('#view li')).some((li) => li.textContent.includes('guardian@family.test'))`,
		15*time.Second, "the new person in the list")

	primary := h.signIn(primaryParent)
	roleOf := func(email string) string {
		var list struct {
			Parents []parentDTO `json:"parents"`
		}
		h.call(http.MethodGet, "/parents", primary.Token, nil).expect(http.StatusOK).decode(&list)
		for _, p := range list.Parents {
			if p.Email == email {
				return p.Role
			}
		}
		return ""
	}
	if got := roleOf("guardian@family.test"); got != "GUARDIAN" {
		t.Fatalf("the console added the person as %q, not GUARDIAN", got)
	}

	// Change the role with the row's select. confirm() is answered by the page override below.
	b.eval(`window.confirm = () => true;
	  (() => { const li = Array.from(document.querySelectorAll('#view li')).find((l) => l.textContent.includes('guardian@family.test'));
	    const s = li.querySelector('select.role-select'); s.value = 'ADMIN'; s.dispatchEvent(new Event('change')); })()`, nil)
	deadline := time.Now().Add(10 * time.Second)
	for roleOf("guardian@family.test") != "ADMIN" {
		if time.Now().After(deadline) {
			t.Fatalf("the role select did not change the role; it is %q", roleOf("guardian@family.test"))
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Measured on the first screenshot: with the select and Remove beside it, a person's address
	// was left about 40 px at phone width and broke one letter per line. Every row with controls
	// must leave its name and address most of the width.
	var narrowest float64
	b.eval(`Math.min(...Array.from(document.querySelectorAll('#view li.person')).filter((l) => l.querySelector('select'))
	  .map((l) => l.querySelector('.label').getBoundingClientRect().width))`, &narrowest)
	if narrowest < 200 {
		t.Errorf("a person's name and address get %.0f px at phone width; the controls crowd them out", narrowest)
	}
	// The primary admin's own row offers no select: the server would refuse it.
	var ownSelect bool
	b.eval(`!!Array.from(document.querySelectorAll('#view li')).find((l) => l.textContent.includes('primary@family.test')).querySelector('select')`, &ownSelect)
	if ownSelect {
		t.Error("the primary admin's own row offers a role select")
	}
}

func TestAGuardianSeesTheGuardianViewAndCanGiveTime(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	withPhone := h.newChild(primary.Token, "Mira")
	h.newChild(primary.Token, "Nils")
	// Review focus 5: a phone, but no daily limit, so +time would be refused (409 no_daily_limit).
	// Without this profile the only one with no limit was also the one with no phone, which returns
	// before the buttons are drawn at all, so the limit check was never exercised (probe 13).
	noLimit := h.newChild(primary.Token, "Lea")
	leaPhone := h.newDevice(primary.Token, noLimit.ID, "Leas Handy")
	_, leaToken := h.provision(primary.Token, leaPhone.ID)
	h.enrollDevice(leaToken, "Pixel 7a", "Android 15", nil)
	device := h.newDevice(primary.Token, withPhone.ID, "Mira's phone")
	_, enrollToken := h.provision(primary.Token, device.ID)
	h.enrollDevice(enrollToken, "Pixel 8", "Android 16", nil)
	h.call(http.MethodPatch, "/children/"+withPhone.ID+"/policy", primary.Token,
		map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"}).expect(http.StatusOK)
	h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	// Review focus 1: arrive on an old bookmark to an admin page.
	b.navigate(h.base + "/#/rules")
	h.issuer.setNextLogin(guardianIdentity)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .guardian-card').length === 3", 15*time.Second,
		"one guardian card per profile")

	var page struct {
		NavHidden bool     `json:"navHidden"`
		Cards     []string `json:"cards"`
		Buttons   []string `json:"buttons"`
	}
	b.eval(`({
	  navHidden: document.getElementById('mainnav').hidden,
	  cards: Array.from(document.querySelectorAll('#view .guardian-card')).map((c) => c.textContent),
	  buttons: Array.from(document.querySelectorAll('#view .guardian-card button[data-minutes]')).map((x) => x.dataset.minutes),
	})`, &page)
	if !page.NavHidden {
		t.Error("a guardian sees the admin tab bar")
	}
	var crumb string
	b.eval(`document.getElementById('crumb').textContent`, &crumb)
	if crumb != "" {
		t.Errorf("the header names one profile (%q) above a page that shows all of them", crumb)
	}
	var mira, nils, lea string
	for _, c := range page.Cards {
		if strings.Contains(c, "Lea") {
			lea = c
		}
		if strings.Contains(c, "Mira") {
			mira = c
		}
		if strings.Contains(c, "Nils") {
			nils = c
		}
	}
	// The status line itself, not the card: the card also holds a "+60 min" button, and a check on
	// the card's text passed on that button alone while the status read something else.
	var status string
	b.eval(`Array.from(document.querySelectorAll('#view .guardian-card')).find((c) => c.textContent.includes('Mira'))
	  .querySelector('h2 + p').textContent`, &status)
	if !strings.Contains(status, "Heute 0 min von 1 h 0 min") {
		t.Errorf("Mira's status line does not show today's time against her 60-minute limit: %q (card %q)", status, mira)
	}
	// Review focus 4.
	if !strings.Contains(nils, "Noch kein Handy eingerichtet") {
		t.Errorf("a profile with no phone does not say so: %q", nils)
	}
	if !strings.Contains(lea, "kein Tageslimit") {
		t.Errorf("a phone with no daily limit does not say so: %q", lea)
	}
	// Review focus 5: one set of buttons, for the profile that has a limit.
	if fmt.Sprint(page.Buttons) != "[15 30 60]" {
		t.Errorf("time buttons %v, want exactly [15 30 60] (Mira only)", page.Buttons)
	}
	// Chrome logs a refused request (403) as a page error, so this also catches a guardian view that
	// still asks for something only an admin may read.
	if len(b.pageErrors) != 0 {
		t.Errorf("the guardian view made the page complain: %s", b.pageErrorReport())
	}

	b.eval(`document.querySelector('#view .guardian-card button[data-minutes="15"]').click()`, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ds struct {
			Desired struct {
				BonusMinutes int `json:"bonus_minutes"`
			} `json:"desired"`
		}
		h.call(http.MethodGet, "/devices/"+device.ID+"/desired-state", primary.Token, nil).
			expect(http.StatusOK).decode(&ds)
		if ds.Desired.BonusMinutes == 15 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("+15 in the guardian view did not reach the server (bonus %d)", ds.Desired.BonusMinutes)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
