package e2e

// FR-24 against the real server: a profile's agenda as a document, the family's holidays, the week
// the server expands from them, what the phone is sent, and the alarm's "not during holidays".

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

type agendaEntryDTO struct {
	ID       string `json:"id,omitempty"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Place    string `json:"place"`
	Optional bool   `json:"optional"`
	Weekdays int    `json:"weekdays,omitempty"`
	Day      string `json:"day,omitempty"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

type agendaItemDTO struct {
	EntryID  string `json:"entry_id"`
	Title    string `json:"title"`
	Place    string `json:"place"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
	Optional bool   `json:"optional"`
}

type agendaDayDTO struct {
	Day     string          `json:"day"`
	Holiday string          `json:"holiday"`
	Items   []agendaItemDTO `json:"items"`
}

type holidayDTO struct {
	ID       string `json:"id,omitempty"`
	Title    string `json:"title"`
	StartsOn string `json:"starts_on"`
	EndsOn   string `json:"ends_on"`
}

func (h *harness) putAgenda(token, childID string, entries []agendaEntryDTO) []agendaEntryDTO {
	h.t.Helper()
	var out struct {
		Entries []agendaEntryDTO `json:"entries"`
	}
	h.call(http.MethodPut, "/children/"+childID+"/agenda", token, map[string]any{"entries": entries}).
		expect(http.StatusOK).decode(&out)
	return out.Entries
}

func (h *harness) agendaDays(token, childID, from string, days int) []agendaDayDTO {
	h.t.Helper()
	var out struct {
		Days []agendaDayDTO `json:"days"`
	}
	h.call(http.MethodGet, "/children/"+childID+"/agenda/days?from="+from+"&days="+itoa(days), token, nil).
		expect(http.StatusOK).decode(&out)
	return out.Days
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func titles(items []agendaItemDTO) []string {
	out := []string{}
	for _, i := range items {
		out = append(out, i.Title)
	}
	return out
}

func TestTheAgendaIsAWeekOfEntriesAndHolidaysSuspendTheRepeatingOnes(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"timezone": "Europe/Zurich"})
	c := "/children/" + f.child.ID

	school := agendaEntryDTO{Kind: "RECURRING", Title: "Schule", Place: "Schulhaus", Weekdays: 31, StartsAt: "08:00", EndsAt: "12:00"}
	training := agendaEntryDTO{Kind: "RECURRING", Title: "Training", Weekdays: 4, StartsAt: "17:00", EndsAt: "18:30", Optional: true}
	dentist := agendaEntryDTO{Kind: "SINGLE", Title: "Zahnarzt", Day: "2026-10-07", StartsAt: "14:00", EndsAt: "14:30"}
	saved := h.putAgenda(f.parent.Token, f.child.ID, []agendaEntryDTO{school, training, dentist})
	if len(saved) != 3 || saved[0].ID == "" || saved[1].Optional != true || saved[2].Day != "2026-10-07" {
		t.Fatalf("the saved agenda is %+v", saved)
	}
	// Edited with its id: the same entry.
	saved[0].Title = "Primarschule"
	again := h.putAgenda(f.parent.Token, f.child.ID, saved)
	if again[0].ID != saved[0].ID || again[0].Title != "Primarschule" {
		t.Errorf("an edited entry sent with its id came back as %+v (id was %s)", again[0], saved[0].ID)
	}
	for name, bad := range map[string]agendaEntryDTO{
		"unknown kind":          {Kind: "WEEKLY", Title: "x", Weekdays: 1, StartsAt: "08:00", EndsAt: "09:00"},
		"repeating on no day":   {Kind: "RECURRING", Title: "x", StartsAt: "08:00", EndsAt: "09:00"},
		"single with no date":   {Kind: "SINGLE", Title: "x", StartsAt: "08:00", EndsAt: "09:00"},
		"single on no real day": {Kind: "SINGLE", Title: "x", Day: "2026-02-30", StartsAt: "08:00", EndsAt: "09:00"},
		"ends before it starts": {Kind: "RECURRING", Title: "x", Weekdays: 1, StartsAt: "09:00", EndsAt: "08:00"},
		"no title":              {Kind: "RECURRING", Title: " ", Weekdays: 1, StartsAt: "08:00", EndsAt: "09:00"},
	} {
		if r := h.call(http.MethodPut, c+"/agenda", f.parent.Token, map[string]any{"entries": []agendaEntryDTO{bad}}); r.Status != http.StatusBadRequest {
			t.Errorf("%s: PUT agenda answered %d, want 400", name, r.Status)
		}
	}

	// ---- the week, expanded: 2026-10-05 is a Monday ----
	week := h.agendaDays(f.parent.Token, f.child.ID, "2026-10-05", 7)
	if len(week) != 7 || week[0].Day != "2026-10-05" || week[6].Day != "2026-10-11" {
		t.Fatalf("the week is %+v", week)
	}
	if got := titles(week[0].Items); !slices.Equal(got, []string{"Primarschule"}) {
		t.Errorf("Monday holds %v", got)
	}
	// Wednesday: in time order, the single entry between the two repeating ones.
	if got := titles(week[2].Items); !slices.Equal(got, []string{"Primarschule", "Zahnarzt", "Training"}) {
		t.Errorf("Wednesday holds %v, want school, the dentist and training in time order", got)
	}
	if !week[2].Items[2].Optional || week[2].Items[0].Place != "Schulhaus" {
		t.Errorf("Wednesday's items lost their place or optional flag: %+v", week[2].Items)
	}
	if len(week[5].Items) != 0 || len(week[6].Items) != 0 {
		t.Errorf("the weekend holds %v and %v", titles(week[5].Items), titles(week[6].Items))
	}

	// ---- holidays: the repeating entries stop, the single one stays ----
	var hol struct {
		Holidays []holidayDTO `json:"holidays"`
	}
	h.call(http.MethodPut, "/family/holidays", f.parent.Token, map[string]any{"holidays": []holidayDTO{
		{Title: "Herbstferien", StartsOn: "2026-10-06", EndsOn: "2026-10-08"},
	}}).expect(http.StatusOK).decode(&hol)
	if len(hol.Holidays) != 1 || hol.Holidays[0].ID == "" {
		t.Fatalf("the saved holidays are %+v", hol.Holidays)
	}
	week = h.agendaDays(f.parent.Token, f.child.ID, "2026-10-05", 7)
	if week[1].Holiday != "Herbstferien" || len(week[1].Items) != 0 {
		t.Errorf("Tuesday in the holiday reads holiday=%q items=%v", week[1].Holiday, titles(week[1].Items))
	}
	if got := titles(week[2].Items); !slices.Equal(got, []string{"Zahnarzt"}) {
		t.Errorf("Wednesday in the holiday holds %v; the dentist is a single entry and stays", got)
	}
	if week[0].Holiday != "" || len(week[0].Items) != 1 || week[4].Holiday != "" || len(week[4].Items) != 1 {
		t.Errorf("the days around the holiday changed: %+v / %+v", week[0], week[4])
	}
	for name, bad := range map[string]holidayDTO{
		"ends before it starts": {Title: "x", StartsOn: "2026-10-08", EndsOn: "2026-10-06"},
		"no title":              {Title: "", StartsOn: "2026-10-06", EndsOn: "2026-10-06"},
		"longer than 120 days":  {Title: "x", StartsOn: "2026-01-01", EndsOn: "2026-12-31"},
	} {
		if r := h.call(http.MethodPut, "/family/holidays", f.parent.Token, map[string]any{"holidays": []holidayDTO{bad}}); r.Status != http.StatusBadRequest {
			t.Errorf("%s: PUT holidays answered %d, want 400", name, r.Status)
		}
	}
	if r := h.call(http.MethodGet, c+"/agenda/days?from=2026-10-05&days=32", f.parent.Token, nil); r.Status != http.StatusBadRequest {
		t.Errorf("32 days answered %d, want 400 (at most 31)", r.Status)
	}

	// ---- the alarm's "not during holidays" ----
	h.call(http.MethodPut, c+"/alarm", f.parent.Token, map[string]any{
		"weekdays": []string{"06:30", "06:30", "06:30", "06:30", "06:30", "", ""}, "skip_holidays": true,
	}).expect(http.StatusOK)
	var alarm struct {
		SkipHolidays bool `json:"skip_holidays"`
	}
	h.call(http.MethodGet, c+"/alarm", f.parent.Token, nil).expect(http.StatusOK).decode(&alarm)
	if !alarm.SkipHolidays {
		t.Error("skip_holidays was not stored")
	}

	// ---- what the phone is sent ----
	var raw map[string]json.RawMessage
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&raw)
	var block struct {
		Days []agendaDayDTO `json:"days"`
	}
	if err := json.Unmarshal(raw["agenda"], &block); err != nil || len(block.Days) != 2 ||
		block.Days[0].Day != zurichDay(0) || block.Days[1].Day != zurichDay(1) {
		t.Errorf("the phone is sent agenda %s (err %v); want today and tomorrow in the profile's calendar", raw["agenda"], err)
	}
	var phoneAlarm struct {
		SkipHolidays bool         `json:"skip_holidays"`
		Holidays     []holidayDTO `json:"holidays"`
	}
	if err := json.Unmarshal(raw["alarm"], &phoneAlarm); err != nil || !phoneAlarm.SkipHolidays ||
		len(phoneAlarm.Holidays) != 1 || phoneAlarm.Holidays[0].StartsOn != "2026-10-06" {
		t.Errorf("the phone's alarm block is %s; want skip_holidays and the coming holiday", raw["alarm"])
	}

	var actions []string
	for _, e := range h.readAudit(f.parent.Token) {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{"AGENDA_UPDATED", "HOLIDAYS_UPDATED"} {
		if !slices.Contains(actions, want) {
			t.Errorf("the audit log has no %s", want)
		}
	}
}

func TestAGuardianCannotReadOrChangeTheAgenda(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)
	c := "/children/" + child.ID
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, c + "/agenda", nil},
		{http.MethodPut, c + "/agenda", map[string]any{"entries": []any{}}},
		{http.MethodGet, c + "/agenda/days", nil},
		{http.MethodGet, "/family/holidays", nil},
		{http.MethodPut, "/family/holidays", map[string]any{"holidays": []any{}}},
		{http.MethodGet, c + "/calendar", nil},
		{http.MethodPut, c + "/calendar", map[string]any{"url": "https://example.com/cal.ics"}},
		{http.MethodDelete, c + "/calendar", nil},
	} {
		if resp := h.call(r.method, r.path, guardian.Token, r.body); resp.Status != http.StatusForbidden {
			t.Errorf("guardian %s %s: got %d, want 403", r.method, r.path, resp.Status)
		}
	}
	h.call(http.MethodGet, c+"/agenda", primary.Token, nil).expect(http.StatusOK)
	h.call(http.MethodGet, "/family/holidays", primary.Token, nil).expect(http.StatusOK)
}
