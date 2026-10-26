package e2e

// FR-31 in a real browser: the switch on Regeln with the list of what it cannot check, the family
// numbers on Familie, the rejected calls on Aktivität, and the phone row on Übersicht telling
// "waiting for the phone" (amber) from "was on and was taken away" (red).

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestConsoleContactsOnlyCalls(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	beat := func(body map[string]any) {
		t.Helper()
		body["connectivity"] = "wifi"
		h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), body).expect(http.StatusOK)
	}
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"calls_contacts_only": true})
	beat(map[string]any{"call_screening": false})
	at := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	h.call(http.MethodPost, "/device/rejected-calls", f.deviceToken(), map[string]any{
		"calls": []map[string]any{{"id": "c-1", "number": "+15550199", "at": at}},
	}).expect(http.StatusOK)

	row := `#view [data-device="` + f.device.ID + `"]`
	for _, size := range []struct{ w, h int }{{phoneWidth, phoneHeight}, {1440, 900}} {
		b := consoleSignedIn(t, h, size.w, size.h)
		tag := fmt.Sprint(size.w)

		// Übersicht: on, never held — amber, and in the words the phone's own notification uses.
		b.waitFor(`!!document.querySelector('`+row+` small.warn')`, 15*time.Second, "the phone row's warning")
		if got := b.text(row + ` small.warn`); !strings.Contains(got, "Anrufschutz: wartet auf Freigabe am Handy") {
			t.Errorf("%d px: the phone row reads %q, want the amber waiting line", size.w, got)
		}
		b.shoot(t, "calls-overview-"+tag)

		// Regeln: the switch is on, and the list of what it does not cover is there with it.
		b.openRulesProtection()
		b.waitFor(`!!document.querySelector('#view [data-calls] input[type=checkbox]')`, 15*time.Second, "the calls switch")
		var checked bool
		b.eval(`document.querySelector('#view [data-calls] input[type=checkbox]').checked`, &checked)
		if !checked {
			t.Errorf("%d px: the switch reads off while the policy is on", size.w)
		}
		if got := b.text(`#view [data-calls-checklist]`); !strings.Contains(got, "nicht prüfbar") || !strings.Contains(got, "WhatsApp") || !strings.Contains(got, "Unterdrückte Nummern") {
			t.Errorf("%d px: the checklist reads %q", size.w, got)
		}
		b.shoot(t, "calls-rules-"+tag)

		// Aktivität: the rejected call, by number.
		b.openTab("activity")
		b.waitFor(`!!document.querySelector('#view [data-rejected-call="+15550199"]')`, 15*time.Second, "the rejected call on Aktivität")
		if got := b.text(`#view .rejected-calls-card`); !strings.Contains(got, "Abgewiesene Anrufe") || !strings.Contains(got, "1 Anruf") {
			t.Errorf("%d px: the rejected-calls card reads %q", size.w, got)
		}
		b.shoot(t, "calls-activity-"+tag)

		// Familie: the card, empty for now, and nothing on the page scrolls sideways.
		b.openTab("family")
		b.waitFor(`!!document.querySelector('#view [data-family-numbers="input"]')`, 15*time.Second, "the Familien-Nummern card")
		var overflow bool
		b.eval(`document.documentElement.scrollWidth > window.innerWidth`, &overflow)
		if overflow {
			t.Errorf("%d px: the Familie page scrolls sideways", size.w)
		}
		b.shoot(t, "calls-family-"+tag)
	}

	b := consoleSignedIn(t, h, phoneWidth, phoneHeight)

	// Typed as a parent types them; stored as the phone compares them.
	b.openTab("family")
	b.waitFor(`!!document.querySelector('#view [data-family-numbers="input"]')`, 15*time.Second, "the number field")
	b.eval(`(() => { const i = document.querySelector('#view [data-family-numbers="input"]');
	  i.value = '079 123 45 67\n+1 555 0101'; i.dispatchEvent(new Event('input'));
	  document.querySelector('#view [data-family-numbers="save"]').click(); })()`, nil)
	want := []string{"+41791234567", "+15550101"}
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Equal(h.familyNumbers(f.parent.Token).Numbers, want) {
		if time.Now().After(deadline) {
			t.Fatalf("saving the card left the server at %v, want %v", h.familyNumbers(f.parent.Token).Numbers, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.waitFor(`!!document.querySelector('#view [data-family-number="+41791234567"]')`, 10*time.Second, "the saved number in the list")

	// The switch, from the page, both ways.
	policyOn := func() bool {
		var pol struct {
			On bool `json:"calls_contacts_only"`
		}
		h.call(http.MethodGet, "/children/"+f.child.ID+"/policy", f.parent.Token, nil).expect(http.StatusOK).decode(&pol)
		return pol.On
	}
	b.openRulesProtection()
	for _, wantOn := range []bool{false, true} {
		b.waitFor(`!!document.querySelector('#view [data-calls] input[type=checkbox]')`, 15*time.Second, "the calls switch")
		b.eval(`document.querySelector('#view [data-calls] input[type=checkbox]').click()`, nil)
		deadline := time.Now().Add(10 * time.Second)
		for policyOn() != wantOn {
			if time.Now().After(deadline) {
				t.Fatalf("clicking the switch left calls_contacts_only at %v, want %v", !wantOn, wantOn)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Held once, then taken away: red, and first on the row.
	beat(map[string]any{"call_screening": true})
	beat(map[string]any{"call_screening": false})
	b.openTab("overview")
	b.reload()
	b.waitFor(`!!document.querySelector('`+row+` small.danger')`, 15*time.Second, "the red line on the phone row")
	if got := b.text(row + ` small.danger`); !strings.HasPrefix(got, "Anrufschutz aus") {
		t.Errorf("taken away, the phone row reads %q, want it to start with Anrufschutz aus", got)
	}
	b.shoot(t, "calls-overview-red")
}

// openRulesProtection opens Regeln › Schutz, where the switches live.
func (b *browser) openRulesProtection() {
	b.openTab("rules")
	b.waitFor(`!!document.querySelector('#view [data-sub="protection"]')`, 15*time.Second, "Regeln's Schutz tab")
	b.eval(`document.querySelector('#view [data-sub="protection"]').click()`, nil)
}
