package e2e

// FR-24.5 on a real Android device: the Heute screen shows what is on now and tomorrow from an agenda
// kept on a real server — the block sent beside the policy, stored on the phone and read with its
// clock in the profile's timezone. Driven by tests/android/agenda.sh; run on its own it SKIPS.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTheHeuteScreenShowsTheAgendaOnARealPhone(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	// FR-25.4 too: an all-day event from a calendar this test serves, read by the server.
	today := time.Now().In(mustZurich())
	calendar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(icsBody(icsEvent("trip", "Schulreise",
			"DTSTART;VALUE=DATE:"+today.Format("20060102"), "DTEND;VALUE=DATE:"+today.AddDate(0, 0, 1).Format("20060102")))))
	}))
	defer calendar.Close()
	h := newHarness(t, withPublicHost(emulatorHostAlias), withEnv("CALENDAR_ALLOW_LOCAL", "true"))
	parent, child, _, _ := managedOnEmulator(t, h, d)

	zurich, _ := time.LoadLocation("Europe/Zurich")
	now := time.Now().In(zurich)
	if now.Hour() == 23 && now.Minute() >= 50 || now.Hour() == 0 && now.Minute() < 5 {
		t.Skip("too close to midnight in Europe/Zurich for 'now' and 'tomorrow' to be stable; run again later")
	}
	from := now.Add(-time.Hour).Format("15:04")
	if now.Hour() == 0 {
		from = "00:00"
	}
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	h.putAgenda(parent.Token, child.ID, []agendaEntryDTO{
		{Kind: "RECURRING", Title: "Lernzeit", Place: "Stube", Weekdays: 127, StartsAt: from, EndsAt: "23:59"},
		{Kind: "SINGLE", Title: "Zahnarzt", Day: tomorrow, StartsAt: "14:00", EndsAt: "14:30", Optional: true},
	})

	h.call(http.MethodPut, "/children/"+child.ID+"/calendar", parent.Token, map[string]any{"url": calendar.URL + "/cal.ics"}).
		expect(http.StatusOK)

	// The emulator runs in English: "Now: … until 23:59", "Tomorrow: 14:00 Zahnarzt (optional)", and
	// the calendar's all-day event on its own line.
	want := []string{"Now: Lernzeit · Stube until 23:59", "Tomorrow: 14:00 Zahnarzt (optional)", "Today: Schulreise (all day)"}
	deadline := time.Now().Add(60 * time.Second)
	var ui string
	for {
		d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_HOME")
		d.run(30*time.Second, "shell", "am", "start", "-n", "io.github.helios57.familyguard/.recovery.RecoveryActivity")
		time.Sleep(2 * time.Second)
		d.run(30*time.Second, "shell", "uiautomator", "dump", "/sdcard/fg-ui.xml")
		ui, _ = d.run(30*time.Second, "exec-out", "cat", "/sdcard/fg-ui.xml")
		if strings.Contains(ui, want[0]) && strings.Contains(ui, want[1]) && strings.Contains(ui, want[2]) {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	for _, w := range want {
		if !strings.Contains(ui, w) {
			t.Errorf("the Heute screen does not say %q", w)
		}
	}
}

func mustZurich() *time.Location {
	loc, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		panic(err)
	}
	return loc
}
