package e2e

// FR-25 against the real server: a profile's calendar address is read into the week beside the
// agenda's own entries, follows the calendar when it changes, keeps the last good copy when a read
// fails, and is fenced off from local addresses. The calendars are served by this test.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type calendarDTO struct {
	URL       string     `json:"url"`
	FetchedAt *time.Time `json:"fetched_at"`
	Error     string     `json:"error"`
	Events    int        `json:"events"`
}

func icsBody(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//e2e//EN\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func icsEvent(uid, summary, start, end string) string {
	return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260901T000000Z\r\nSUMMARY:" + summary + "\r\n" + start + "\r\n" + end + "\r\nEND:VEVENT\r\n"
}

func TestACalendarIsReadIntoTheWeek(t *testing.T) {
	var body atomic.Value
	var failing atomic.Bool
	today := strings.ReplaceAll(zurichDay(0), "-", "")
	body.Store(icsBody(
		icsEvent("a", "Elterngespräch", "DTSTART;TZID=Europe/Zurich:20261007T140000", "DTEND;TZID=Europe/Zurich:20261007T143000"),
		icsEvent("b", "Schulreise", "DTSTART;VALUE=DATE:20261008", "DTEND;VALUE=DATE:20261009"),
		icsEvent("c", "Heute-Termin", "DTSTART;TZID=Europe/Zurich:"+today+"T120000", "DTEND;TZID=Europe/Zurich:"+today+"T123000"),
	))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bad.ics":
			_, _ = w.Write([]byte("<html>this is a web page</html>"))
		case failing.Load():
			http.Error(w, "down", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "text/calendar")
			_, _ = w.Write([]byte(body.Load().(string)))
		}
	}))
	defer srv.Close()

	h := newHarness(t, withEnv("CALENDAR_ALLOW_LOCAL", "true"), withEnv("CALENDAR_MAX_AGE", "1s"))
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"timezone": "Europe/Zurich"})
	c := "/children/" + f.child.ID
	h.putAgenda(f.parent.Token, f.child.ID, []agendaEntryDTO{{Kind: "RECURRING", Title: "Schule", Weekdays: 31, StartsAt: "08:00", EndsAt: "12:00"}})

	var none calendarDTO
	h.call(http.MethodGet, c+"/calendar", f.parent.Token, nil).expect(http.StatusOK).decode(&none)
	if none.URL != "" || none.FetchedAt != nil {
		t.Fatalf("a profile with no calendar reads %+v", none)
	}

	var set calendarDTO
	h.call(http.MethodPut, c+"/calendar", f.parent.Token, map[string]any{"url": srv.URL + "/cal.ics"}).expect(http.StatusOK).decode(&set)
	if set.FetchedAt == nil || set.Error != "" || set.Events != 3 {
		t.Fatalf("setting the calendar answered %+v; want it read at once, with three events", set)
	}

	week := h.agendaDays(f.parent.Token, f.child.ID, "2026-10-05", 7)
	if got := titles(week[2].Items); !slices.Equal(got, []string{"Schule", "Elterngespräch"}) {
		t.Errorf("Wednesday holds %v; want school and the calendar's 14:00 event", got)
	}
	var raw struct {
		Days []struct {
			Items []struct {
				Title  string `json:"title"`
				Source string `json:"source"`
				AllDay bool   `json:"all_day"`
			} `json:"items"`
		} `json:"days"`
	}
	h.call(http.MethodGet, c+"/agenda/days?from=2026-10-05&days=7", f.parent.Token, nil).expect(http.StatusOK).decode(&raw)
	thursday := raw.Days[3].Items
	if len(thursday) != 2 || thursday[0].Title != "Schulreise" || !thursday[0].AllDay || thursday[0].Source != "calendar" || thursday[1].Source != "agenda" {
		t.Errorf("Thursday holds %+v; want the all-day calendar event first, then school", thursday)
	}

	// The phone's block carries today's calendar event.
	var policy map[string]json.RawMessage
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&policy)
	if !strings.Contains(string(policy["agenda"]), "Heute-Termin") {
		t.Errorf("the phone's agenda block does not carry today's calendar event: %s", policy["agenda"])
	}

	// The calendar changes; once the kept copy is stale a read fetches it again.
	body.Store(icsBody(icsEvent("a", "Elternabend", "DTSTART;TZID=Europe/Zurich:20261007T190000", "DTEND;TZID=Europe/Zurich:20261007T200000")))
	time.Sleep(1100 * time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for {
		week = h.agendaDays(f.parent.Token, f.child.ID, "2026-10-05", 7)
		if slices.Equal(titles(week[2].Items), []string{"Schule", "Elternabend"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the week did not follow the changed calendar: Wednesday %v", titles(week[2].Items))
		}
		time.Sleep(300 * time.Millisecond)
	}

	// A failing read keeps the last good copy and says why.
	failing.Store(true)
	time.Sleep(1100 * time.Millisecond)
	deadline = time.Now().Add(10 * time.Second)
	for {
		h.agendaDays(f.parent.Token, f.child.ID, "2026-10-05", 7)
		var cal calendarDTO
		h.call(http.MethodGet, c+"/calendar", f.parent.Token, nil).expect(http.StatusOK).decode(&cal)
		if cal.Error != "" {
			if !strings.Contains(cal.Error, "500") {
				t.Errorf("the recorded error does not say what the calendar answered: %q", cal.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a calendar answering 500 recorded no error")
		}
		time.Sleep(300 * time.Millisecond)
	}
	if got := titles(h.agendaDays(f.parent.Token, f.child.ID, "2026-10-05", 7)[2].Items); !slices.Equal(got, []string{"Schule", "Elternabend"}) {
		t.Errorf("a failed read lost the last good copy: Wednesday %v", got)
	}
	failing.Store(false)

	// Refusals: not a calendar, not a web address.
	if r := h.call(http.MethodPut, c+"/calendar", f.parent.Token, map[string]any{"url": srv.URL + "/bad.ics"}); r.Status != http.StatusBadRequest ||
		!strings.Contains(string(r.Body), "iCalendar") {
		t.Errorf("a web page as a calendar answered %d %s", r.Status, r.Body)
	}
	if r := h.call(http.MethodPut, c+"/calendar", f.parent.Token, map[string]any{"url": "ftp://example.com/cal.ics"}); r.Status != http.StatusBadRequest {
		t.Errorf("an ftp address answered %d", r.Status)
	}

	// Removed: the week is the agenda alone again.
	h.call(http.MethodDelete, c+"/calendar", f.parent.Token, nil).expect(http.StatusNoContent)
	if got := titles(h.agendaDays(f.parent.Token, f.child.ID, "2026-10-05", 7)[2].Items); !slices.Equal(got, []string{"Schule"}) {
		t.Errorf("after removing the calendar Wednesday holds %v", got)
	}

	// Audited, and never with the address itself: a calendar's secret address is a credential.
	var calendarRows int
	for _, e := range h.readAudit(f.parent.Token) {
		if e.Action == "CALENDAR_SET" || e.Action == "CALENDAR_REMOVED" {
			calendarRows++
			if detail, _ := json.Marshal(e.Detail); strings.Contains(string(detail), "cal.ics") {
				t.Errorf("the audit row %s carries the calendar's address: %s", e.Action, detail)
			}
		}
	}
	if calendarRows < 2 {
		t.Errorf("%d CALENDAR_SET/CALENDAR_REMOVED rows, want both", calendarRows)
	}
}

func TestTheCalendarFenceRefusesLocalAddresses(t *testing.T) {
	h := newHarness(t) // no CALENDAR_ALLOW_LOCAL: the production fence
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	for _, url := range []string{"http://example.com/cal.ics", "https://127.0.0.1:9/cal.ics", "webcal://localhost:9/cal.ics", "https://169.254.169.254/latest"} {
		r := h.call(http.MethodPut, "/children/"+child.ID+"/calendar", primary.Token, map[string]any{"url": url})
		if r.Status != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: %s", url, r.Status, r.Body)
		}
	}
	// Control: the refusal is the fence, not a broken route — the same body is accepted by shape.
	r := h.call(http.MethodPut, "/children/"+child.ID+"/calendar", primary.Token, map[string]any{"url": "https://127.0.0.1:9/cal.ics"})
	if !strings.Contains(string(r.Body), "private") && !strings.Contains(string(r.Body), "local") {
		t.Errorf("the refusal of a loopback address does not say why: %s", r.Body)
	}
}

func TestTheAdminAddsACalendarInTheConsole(t *testing.T) {
	tomorrow := strings.ReplaceAll(zurichDay(1), "-", "")
	after := strings.ReplaceAll(zurichDay(2), "-", "")
	afterEnd := strings.ReplaceAll(zurichDay(3), "-", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(icsBody(
			icsEvent("a", "Elterngespräch", "DTSTART;TZID=Europe/Zurich:"+tomorrow+"T140000", "DTEND;TZID=Europe/Zurich:"+tomorrow+"T143000"),
			icsEvent("b", "Schulreise", "DTSTART;VALUE=DATE:"+after, "DTEND;VALUE=DATE:"+afterEnd),
		)))
	}))
	defer srv.Close()
	h := newHarness(t, withEnv("CALENDAR_ALLOW_LOCAL", "true"))
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	h.patchPolicy(primary.Token, child.ID, map[string]any{"timezone": "Europe/Zurich"})
	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="rules"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .agenda-card input[data-calendar="url"]')`, 15*time.Second, "the calendar field")
	b.eval(`(() => {
	  const i = document.querySelector('#view .agenda-card input[data-calendar="url"]');
	  i.value = '`+srv.URL+`/cal.ics'; i.dispatchEvent(new Event('input', { bubbles: true }));
	  document.querySelector('#view .agenda-card button[data-calendar="save"]').click();
	})()`, nil)
	b.waitFor(`(document.querySelector('#view .calendar-status') || {}).textContent?.includes('2 events')`, 15*time.Second, "the calendar's status to say what it read")
	var cal calendarDTO
	h.call(http.MethodGet, "/children/"+child.ID+"/calendar", primary.Token, nil).expect(http.StatusOK).decode(&cal)
	if cal.URL != srv.URL+"/cal.ics" {
		t.Fatalf("the server holds %+v", cal)
	}
	b.measure(t, "rules/calendar").check(t, "rules/calendar")
	var week string
	b.eval(`document.querySelector('#view .agenda-week').textContent`, &week)
	for _, want := range []string{"14:00–14:30 Elterngespräch", "calendar", "Schulreise", "all day"} {
		if !strings.Contains(week, want) {
			t.Errorf("the week does not say %q: %q", want, week)
		}
	}
	b.eval(`document.querySelector('#view .agenda-card button[data-calendar="remove"]').click()`, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.call(http.MethodGet, "/children/"+child.ID+"/calendar", primary.Token, nil).expect(http.StatusOK).decode(&cal)
		if cal.URL == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Remove did not reach the server")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(b.pageErrors) != 0 {
		t.Errorf("the calendar made the page complain: %s", b.pageErrorReport())
	}
}

func TestFgctlSetsTheCalendar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(icsBody(icsEvent("a", "Termin", "DTSTART:20261007T120000Z", "DTEND:20261007T123000Z"))))
	}))
	defer srv.Close()
	h := newHarness(t, withEnv("CALENDAR_ALLOW_LOCAL", "true"))
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-calendar")}
	calendar := func() calendarDTO {
		var c calendarDTO
		h.call(http.MethodGet, "/children/"+child.ID+"/calendar", primary.Token, nil).expect(http.StatusOK).decode(&c)
		return c
	}

	if r := fgctlRun(t, home, env, "calendar", child.ID, "--set", srv.URL+"/cal.ics"); r.code != 0 {
		t.Fatalf("fgctl calendar --set exited %d: %q", r.code, r.stderr)
	}
	if calendar().URL != srv.URL+"/cal.ics" {
		t.Fatal("fgctl calendar --set answered 0 and the server holds no address")
	}
	if r := fgctlRun(t, home, env, "calendar", child.ID); !strings.Contains(r.stdout, "read") || !strings.Contains(r.stdout, srv.URL) {
		t.Errorf("fgctl calendar does not show the address and when it was read: %q", r.stdout)
	}
	if r := fgctlRun(t, home, env, "calendar", child.ID, "--set", "ftp://x/cal.ics"); r.code == 0 || !strings.Contains(r.stderr, "https") {
		t.Errorf("an ftp address exited %d with %q", r.code, r.stderr)
	}
	if r := fgctlRun(t, home, env, "calendar", child.ID, "--remove"); r.code != 0 || calendar().URL != "" {
		t.Errorf("fgctl calendar --remove exited %d and the server holds %+v", r.code, calendar())
	}

	session := startMCP(t, t.TempDir(), env)
	defer session.close()
	session.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "1"},
	})
	session.notify(t, "notifications/initialized", map[string]any{})
	out := session.call(t, "tools/call", map[string]any{"name": "set_calendar", "arguments": map[string]any{"child_id": child.ID, "url": srv.URL + "/cal.ics"}})
	if isErr, _ := out["isError"].(bool); isErr || calendar().URL == "" {
		t.Fatalf("set_calendar: %s", mcpText(t, out))
	}
	got := session.call(t, "tools/call", map[string]any{"name": "get_calendar", "arguments": map[string]any{"child_id": child.ID}})
	if text := mcpText(t, got); !strings.Contains(text, `"events"`) {
		t.Errorf("get_calendar: %s", text)
	}
	cleared := session.call(t, "tools/call", map[string]any{"name": "set_calendar", "arguments": map[string]any{"child_id": child.ID, "url": ""}})
	if isErr, _ := cleared["isError"].(bool); isErr || calendar().URL != "" {
		t.Errorf("set_calendar with an empty address should remove it: %s", mcpText(t, cleared))
	}
}
