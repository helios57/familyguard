package e2e

// FR-24 in a real browser: an admin keeps a profile's agenda on the Rules tab and sees the week, sets
// the family's holidays on the Family tab, and turns on "not during holidays" for the alarm. Every
// effect is read back from the server.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTheAdminKeepsTheAgendaAndHolidaysInTheConsole(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	h.patchPolicy(primary.Token, child.ID, map[string]any{"timezone": "Europe/Zurich"})
	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="rules"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .agenda-card button[data-agenda="add"]')`, 15*time.Second, "the agenda card")

	// A repeating entry: school, Monday to Friday (the default), 08:00–12:00.
	b.eval(`document.querySelector('#view .agenda-card button[data-agenda="add"]').click()`, nil)
	b.waitFor(`document.querySelectorAll('#view .agenda-entry').length === 1`, 5*time.Second, "a new entry")
	b.measure(t, "rules/agenda").check(t, "rules/agenda")
	fill := `(i, values) => {
	  const row = document.querySelectorAll('#view .agenda-entry')[i];
	  for (const [field, v] of Object.entries(values)) {
	    const input = row.querySelector('[data-field="' + field + '"]');
	    if (input.type === 'checkbox') { input.checked = v; input.dispatchEvent(new Event('change', { bubbles: true })); }
	    else { input.value = v; input.dispatchEvent(new Event(input.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true })); }
	  }
	}`
	b.eval(`(`+fill+`)(0, { title: 'Schule', place: 'Schulhaus', starts_at: '08:00', ends_at: '12:00' })`, nil)
	// A single one: the dentist on one date, optional.
	b.eval(`document.querySelector('#view .agenda-card button[data-agenda="add"]').click()`, nil)
	b.waitFor(`document.querySelectorAll('#view .agenda-entry').length === 2`, 5*time.Second, "a second entry")
	b.eval(`(`+fill+`)(1, { kind: 'SINGLE' })`, nil)
	b.waitFor(`!!document.querySelectorAll('#view .agenda-entry')[1].querySelector('[data-field="day"]')`, 5*time.Second, "the date field of a one-date entry")
	b.eval(`(`+fill+`)(1, { title: 'Zahnarzt', day: '2026-10-07', starts_at: '14:00', ends_at: '14:30', optional: true })`, nil)
	b.eval(`document.querySelector('#view .agenda-card button[data-agenda="save"]').click()`, nil)

	var saved []agendaEntryDTO
	deadline := time.Now().Add(10 * time.Second)
	for {
		var out struct {
			Entries []agendaEntryDTO `json:"entries"`
		}
		h.call(http.MethodGet, "/children/"+child.ID+"/agenda", primary.Token, nil).expect(http.StatusOK).decode(&out)
		if len(out.Entries) == 2 {
			saved = out.Entries
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Save agenda did not reach the server: %+v\n%s", out.Entries, b.pageErrorReport())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if s := saved[0]; s.Kind != "RECURRING" || s.Weekdays != 31 || s.Title != "Schule" || s.Place != "Schulhaus" || s.StartsAt != "08:00" {
		t.Errorf("the repeating entry was saved as %+v", s)
	}
	if z := saved[1]; z.Kind != "SINGLE" || z.Day != "2026-10-07" || !z.Optional || z.EndsAt != "14:30" {
		t.Errorf("the one-date entry was saved as %+v", z)
	}
	// The week below the editor carries school (Monday to Friday always falls within seven days).
	b.waitFor(`(document.querySelector('#view .agenda-week') || {}).textContent?.includes('Schule')`, 10*time.Second, "the week to show school")
	// Each item's place under its title, not run into it ("SchuleSchulhaus", seen at 360 px).
	var stacked bool
	b.eval(`(() => {
	  const li = Array.from(document.querySelectorAll('#view .agenda-week li')).find((n) => n.textContent.includes('Schulhaus'));
	  const title = li.querySelector('.label > span'), place = li.querySelector('.label > small');
	  return place.getBoundingClientRect().top >= title.getBoundingClientRect().bottom - 1;
	})()`, &stacked)
	if !stacked {
		t.Error("in the week an item's place is drawn on the same line as its title")
	}

	// ---- the alarm's "not during holidays" ----
	b.eval(`(() => {
	  const box = document.querySelector('#view .alarm-card input[data-alarm="skip-holidays"]');
	  box.checked = true; box.dispatchEvent(new Event('change', { bubbles: true }));
	  document.querySelector('#view .alarm-card button[data-alarm="save"]').click();
	})()`, nil)
	deadline = time.Now().Add(10 * time.Second)
	for {
		var a struct {
			SkipHolidays bool `json:"skip_holidays"`
		}
		h.call(http.MethodGet, "/children/"+child.ID+"/alarm", primary.Token, nil).expect(http.StatusOK).decode(&a)
		if a.SkipHolidays {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("'Not during holidays' did not reach the server")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// ---- the family's holidays ----
	b.eval(`document.querySelector('.tab[data-tab="family"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .holidays-card button[data-holiday="add"]')`, 15*time.Second, "the holidays card")
	b.eval(`document.querySelector('#view .holidays-card button[data-holiday="add"]').click()`, nil)
	b.waitFor(`document.querySelectorAll('#view .holiday').length === 1`, 5*time.Second, "a new holiday")
	b.measure(t, "family/holidays").check(t, "family/holidays")
	b.eval(`(() => {
	  const row = document.querySelector('#view .holiday');
	  const set = (f, v) => { const i = row.querySelector('[data-field="' + f + '"]'); i.value = v; i.dispatchEvent(new Event('input', { bubbles: true })); };
	  set('title', 'Herbstferien'); set('starts_on', '2026-10-05'); set('ends_on', '2026-10-16');
	  document.querySelector('#view .holidays-card button[data-holiday="save"]').click();
	})()`, nil)
	deadline = time.Now().Add(10 * time.Second)
	for {
		var out struct {
			Holidays []holidayDTO `json:"holidays"`
		}
		h.call(http.MethodGet, "/family/holidays", primary.Token, nil).expect(http.StatusOK).decode(&out)
		if len(out.Holidays) == 1 {
			if hol := out.Holidays[0]; hol.Title != "Herbstferien" || hol.StartsOn != "2026-10-05" || hol.EndsOn != "2026-10-16" {
				t.Errorf("the holiday was saved as %+v", hol)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Save holidays did not reach the server\n%s", b.pageErrorReport())
		}
		time.Sleep(200 * time.Millisecond)
	}
	var listed string
	b.eval(`document.querySelector('#view .holidays-card').textContent`, &listed)
	if !strings.Contains(listed, "12 days") {
		t.Errorf("the holiday does not say how long it is: %q", listed)
	}
	if len(b.pageErrors) != 0 {
		t.Errorf("the agenda and holidays made the page complain: %s", b.pageErrorReport())
	}
}

// A save that answers after the parent has moved to another tab must not redraw that tab from the
// data of the one they left. Found when this file's own test switched to Family right after "Save
// alarm": the answer arrived, the handler redrew the current view — Family — from the Rules data,
// and the page threw. Family's load is delayed here so the order is certain rather than lucky.
func TestASaveThatAnswersAfterATabSwitchLeavesTheNewTabIntact(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="rules"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .alarm-card button[data-alarm="save"]')`, 15*time.Second, "the alarm card")
	b.eval(`(() => {
	  // The Family tab's own load is held back, so the save answers while Rules' data is still the
	  // data in hand and Family is already the view — the order that threw.
	  const real = window.fetch;
	  window.fetch = (url, opts) => String(url).endsWith('/parents')
	    ? new Promise((r) => setTimeout(r, 1500)).then(() => real(url, opts))
	    : real(url, opts);
	  document.querySelector('#view .alarm-card button[data-alarm="save"]').click();
	  document.querySelector('.tab[data-tab="family"]').click();
	})()`, nil)
	b.waitFor(`!!document.querySelector('#view .holidays-card')`, 15*time.Second, "the Family tab")
	time.Sleep(3 * time.Second) // the delayed save answers inside this
	var stillThere bool
	b.eval(`!!document.querySelector('#view .holidays-card') && !document.querySelector('#view .alarm-card')`, &stillThere)
	if !stillThere {
		t.Error("after the late answer the Family tab is no longer what is on screen")
	}
	if len(b.pageErrors) != 0 {
		t.Errorf("the late answer made the page throw: %s", b.pageErrorReport())
	}
	if a := h.getAlarm(primary.Token, child.ID); len(a.Weekdays) != 7 {
		t.Errorf("the delayed save did not land: %+v", a)
	}
}
