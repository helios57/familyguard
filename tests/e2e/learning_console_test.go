package e2e

// FR-30.6 and FR-30.7 in a real browser: the console says what a learning app is and what was
// learned, lists the prefixes on Familie, and can say "on, 0 minutes" without a minutes-only save
// quietly turning the limit off.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// consoleSignedIn opens the console as the primary admin at the given width.
func consoleSignedIn(t *testing.T, h *harness, width, height int) *browser {
	t.Helper()
	b := startBrowser(t)
	if width <= phoneWidth {
		b.phone(width, height)
	} else {
		b.laptop(width, height)
	}
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	return b
}

func (b *browser) openTab(name string) {
	b.eval(`document.querySelector('.tab[data-tab="`+name+`"]').click()`, nil)
}

func (b *browser) text(selector string) string {
	var s string
	b.eval(`(() => { const e = document.querySelector(`+jsString(selector)+`); return e ? e.textContent : ''; })()`, &s)
	return s
}

// shoot writes a screenshot for a person to look at when E2E_SHOT_DIR is set; it asserts nothing.
func (b *browser) shoot(t *testing.T, name string) {
	if dir := os.Getenv("E2E_SHOT_DIR"); dir != "" {
		b.screenshot(t, filepath.Join(dir, name+".png"))
	}
}

func TestConsoleLearningApps(t *testing.T) {
	h := newHarness(t)
	f := learningFixture(t, h)
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples":  map[string]int64{pkgLetters: 7 * minuteMillis, pkgMovies: 0},
		"learning": map[string]int64{pkgLetters: 7 * minuteMillis},
	}).expect(http.StatusOK)

	for _, size := range []struct{ w, h int }{{phoneWidth, phoneHeight}, {1440, 900}} {
		b := consoleSignedIn(t, h, size.w, size.h)
		tag := fmt.Sprint(size.w)

		// Übersicht: the learned minutes are named inside the gold Bonuszeit.
		b.waitFor(`!!document.querySelector('#view [data-kind="learned"]')`, 15*time.Second, "the learned line on Übersicht")
		if got := b.text(`#view [data-kind="learned"]`); !strings.Contains(got, "7 min") {
			t.Errorf("%d px: Übersicht's learned figure reads %q, want 7 min", size.w, got)
		}
		if got := b.text(`#view .time-kinds`); !strings.Contains(got, "davon erspielt") {
			t.Errorf("%d px: the Bonuszeit block does not say »davon erspielt«: %q", size.w, got)
		}
		b.shoot(t, "learning-overview-"+tag)

		// Aktivität: the learning app is named as one, and the day says what was learned.
		b.openTab("activity")
		b.waitFor(`!!document.querySelector('#view [data-screen="learned"]')`, 15*time.Second, "the learned line on Aktivität")
		if got := b.text(`#view [data-screen="learned"]`); !strings.Contains(got, "davon erspielt 7 min") {
			t.Errorf("%d px: Aktivität's learned line reads %q", size.w, got)
		}
		var rowText string
		b.eval(`Array.from(document.querySelectorAll('#view .usage-list li, #view li')).map((li) => li.textContent).find((s) => s.includes('`+pkgLetters+`')) || ''`, &rowText)
		if !strings.Contains(rowText, "Lern-App · verdient Zeit") {
			t.Errorf("%d px: the learning app's activity line reads %q", size.w, rowText)
		}
		b.shoot(t, "learning-activity-"+tag)

		// Familie: each prefix with the number of installed apps it matches.
		b.openTab("family")
		b.waitFor(`!!document.querySelector('#view [data-learning-prefix]')`, 15*time.Second, "the Lern-Apps card")
		if got := b.text(`#view [data-learning-prefix="` + learnPrefix + `"]`); !strings.Contains(got, "2 Apps") {
			t.Errorf("%d px: the prefix line reads %q, want it to say 2 Apps", size.w, got)
		}
		b.shoot(t, "learning-family-"+tag)

		// Apps: the label replaces the rule chip, and Sperren is on the row.
		b.openTab("apps")
		b.waitFor(`!!document.querySelector('#view li[data-package="`+pkgLetters+`"]')`, 15*time.Second, "the learning app on Apps")
		row := `#view li[data-package="` + pkgLetters + `"]`
		if got := b.text(row); !strings.Contains(got, "Lern-App · verdient Zeit") {
			t.Errorf("%d px: the learning app's row reads %q", size.w, got)
		}
		var chips int
		b.eval(`document.querySelectorAll('`+row+` [data-rule-chip]').length`, &chips)
		if chips != 0 {
			t.Errorf("%d px: a learning app still offers the rule chip, whose answers would all be overridden", size.w)
		}
		if got := b.text(row + ` [data-rule="BLOCK"]`); got != "Sperren" {
			t.Errorf("%d px: the learning app's block button reads %q", size.w, got)
		}
		var overflow bool
		b.eval(`document.documentElement.scrollWidth > window.innerWidth`, &overflow)
		if overflow {
			t.Errorf("%d px: the Apps page scrolls sideways", size.w)
		}
		b.shoot(t, "learning-apps-"+tag)
	}

	// Editing on Familie reaches the server; Sperren on Apps does too, and the row then says so.
	b := consoleSignedIn(t, h, phoneWidth, phoneHeight)
	b.openTab("family")
	b.waitFor(`!!document.querySelector('#view [data-learning="input"]')`, 15*time.Second, "the prefix field")
	b.eval(`(() => { const i = document.querySelector('#view [data-learning="input"]');
	  i.value = '`+learnPrefix+`\norg.example.more.'; i.dispatchEvent(new Event('input'));
	  document.querySelector('#view [data-learning="save"]').click(); })()`, nil)
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Equal(h.learningPrefixes(f.parent.Token).Prefixes, []string{learnPrefix, "org.example.more."}) {
		if time.Now().After(deadline) {
			t.Fatalf("saving the card left the server at %v", h.learningPrefixes(f.parent.Token).Prefixes)
		}
		time.Sleep(100 * time.Millisecond)
	}

	b.openTab("apps")
	row := `#view li[data-package="` + pkgLetters + `"]`
	b.waitFor(`!!document.querySelector('`+row+` [data-rule="BLOCK"]')`, 15*time.Second, "Sperren on the learning app")
	b.eval(`document.querySelector('`+row+` [data-rule="BLOCK"]').click()`, nil)
	b.waitFor(`!!document.querySelector('`+row+` [data-rule="none"]')`, 15*time.Second, "the blocked learning app's Entsperren")
	var rules struct {
		Rules []struct {
			Package string `json:"package_name"`
			Action  string `json:"action"`
		} `json:"rules"`
	}
	h.call(http.MethodGet, "/children/"+f.child.ID+"/app-rules", f.parent.Token, nil).expect(http.StatusOK).decode(&rules)
	if !slices.ContainsFunc(rules.Rules, func(r struct {
		Package string `json:"package_name"`
		Action  string `json:"action"`
	}) bool {
		return r.Package == pkgLetters && r.Action == "BLOCK"
	}) {
		t.Errorf("Sperren did not block the learning app: %+v", rules.Rules)
	}
	if got := b.text(row); !strings.Contains(got, "gesperrt") {
		t.Errorf("the blocked learning app's row does not say it is blocked: %q", got)
	}
}

func TestConsoleDailyLimitZero(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 0, "timezone": "Europe/Zurich"})
	policyNow := func() (bool, int) {
		t.Helper()
		var p struct {
			Enabled bool `json:"daily_limit_enabled"`
			Minutes int  `json:"daily_limit_minutes"`
		}
		h.call(http.MethodGet, "/children/"+f.child.ID+"/policy", f.parent.Token, nil).expect(http.StatusOK).decode(&p)
		return p.Enabled, p.Minutes
	}
	waitPolicy := func(enabled bool, minutes int, what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			e, m := policyNow()
			if e == enabled && m == minutes {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the server holds enabled=%v minutes=%d, want %v %d", what, e, m, enabled, minutes)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	b := consoleSignedIn(t, h, phoneWidth, phoneHeight)
	b.openTab("rules")
	sw := `#view input[data-policy="daily_limit_enabled"]`
	b.waitFor(`!!document.querySelector('`+sw+`')`, 15*time.Second, "the daily-limit switch")
	var checked, quotaShown bool
	b.eval(`document.querySelector('`+sw+`').checked`, &checked)
	b.eval(`!document.getElementById('quota').closest('[data-quota]').hidden`, &quotaShown)
	if checked || quotaShown {
		t.Errorf("a policy with no limit shows the switch checked=%v and the minutes field shown=%v", checked, quotaShown)
	}
	var label string
	b.eval(`document.querySelector('`+sw+`').closest('.switch').textContent`, &label)
	if got := label; !strings.Contains(got, "Kein Tageslimit") {
		t.Errorf("the switch, off, does not say »Kein Tageslimit«: %q", got)
	}
	b.shoot(t, "limit-off-360")

	b.eval(`document.querySelector('`+sw+`').click()`, nil)
	waitPolicy(true, 0, "switching the limit on")
	b.waitFor(`!document.getElementById('quota').closest('[data-quota]').hidden`, 10*time.Second, "the minutes field")

	// The trap: a save of the minutes alone, at 0, is "no limit" to the API (an old client's
	// meaning). The console must say the switch every time.
	setQuota := func(n string) {
		b.eval(`(() => { const q = document.getElementById('quota'); q.value = '`+n+`'; q.dispatchEvent(new Event('change')); })()`, nil)
	}
	setQuota("45")
	waitPolicy(true, 45, "45 minutes")
	setQuota("0")
	waitPolicy(true, 0, "back to 0 minutes with the switch on")
	b.shoot(t, "limit-on-zero-360")

	// Übersicht treats on-at-0 as a limit: nothing left, and Extrazeit offered.
	b.openTab("overview")
	b.waitFor(`!!document.querySelector('#view .time-left')`, 15*time.Second, "the time line")
	if got := b.text(`#view .time-left`); !strings.Contains(got, "Keine Zeit mehr") {
		t.Errorf("on at 0, Übersicht reads %q, want »Keine Zeit mehr«", got)
	}
	var extra int
	b.eval(`document.querySelectorAll('#view .btn-extra[data-minutes]').length`, &extra)
	if extra == 0 {
		t.Error("on at 0, Übersicht offers no Extrazeit")
	}
	b.shoot(t, "limit-on-zero-overview-360")

	b.openTab("rules")
	b.waitFor(`!!document.querySelector('`+sw+`')`, 15*time.Second, "the daily-limit switch again")
	b.eval(`document.querySelector('`+sw+`').click()`, nil)
	waitPolicy(false, 0, "switching the limit off")
}
