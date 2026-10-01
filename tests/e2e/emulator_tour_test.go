package e2e

// A picture of every screen the phone app shows, in German, on a real Android, for a person to read.
//
// Not a check, like the console's tour (tour_test.go): nothing here can judge whether a screen is
// clear. It enrolls the device against a server holding a believable day — a limit, a bedtime, a
// daily plan with one group already earned, an agenda, an alarm week — and photographs the
// FamilyGuard screen top to bottom, the notification shade, the dialog a paused app shows, and the
// alarm ringing. Driven by tests/android/tour.sh with E2E_TOUR_DIR set; on its own it SKIPS.
//
// Screenshots come from `screencap`, which works on the API 33 image; the API 37 image's renderer
// aborts on it ("hasReadColorBufferDma", measured 2026-10-01).

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (d *androidDevice) shot(t *testing.T, dir, name string) {
	t.Helper()
	out, err := d.run(30*time.Second, "exec-out", "screencap", "-p")
	if err != nil || !strings.HasPrefix(out, "\x89PNG") {
		t.Fatalf("screencap for %s failed (%v): %.80q", name, err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (d *androidDevice) uiText(t *testing.T) string {
	t.Helper()
	d.run(30*time.Second, "shell", "uiautomator", "dump", "/sdcard/fg-ui.xml")
	ui, _ := d.run(30*time.Second, "exec-out", "cat", "/sdcard/fg-ui.xml")
	return ui
}

func TestPhoneTour(t *testing.T) {
	dir := os.Getenv("E2E_TOUR_DIR")
	if dir == "" {
		t.Skip("E2E_TOUR_DIR is not set; the tour writes screenshots for a person and asserts nothing")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, child, device, target := managedOnEmulator(t, h, d)

	zurich := mustZurich()
	now := time.Now().In(zurich)
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	h.patchPolicy(parent.Token, child.ID, map[string]any{
		"daily_limit_minutes": 90, "bedtime_enabled": true, "bedtime_start": "21:00", "bedtime_end": "06:30",
	})
	plan := h.putPlan(parent.Token, child.ID, []planGroupDTO{
		{Title: "Morgen", Weekdays: 127, StartsAt: "00:00", EndsAt: "23:59", EarnedMinutes: 15,
			Tasks: []planTaskDTO{{Title: "Zähne putzen"}, {Title: "Bett machen"}}},
		{Title: "Nach der Schule", Weekdays: 127, StartsAt: "00:00", EndsAt: "23:59", EarnedMinutes: 30,
			Tasks: []planTaskDTO{{Title: "Hausaufgaben", Note: "Mathe Seite 42"}, {Title: "Zimmer aufräumen"}}},
	})
	// The first group earned: both tasks confirmed by a parent.
	for _, task := range plan[0].Tasks {
		h.call(http.MethodPost, "/children/"+child.ID+"/tasks/"+task.ID+"/decision", parent.Token,
			map[string]any{"decision": "confirm"}).expect(http.StatusOK)
	}
	h.putAgenda(parent.Token, child.ID, []agendaEntryDTO{
		{Kind: "RECURRING", Title: "Schule", Place: "Schulhaus", Weekdays: 127, StartsAt: "00:00", EndsAt: "23:59"},
		{Kind: "SINGLE", Title: "Fussball", Place: "Sportplatz", Day: tomorrow, StartsAt: "17:00", EndsAt: "18:30"},
	})
	h.call(http.MethodPut, "/children/"+child.ID+"/alarm", parent.Token,
		map[string]any{"weekdays": []string{"06:45", "06:45", "06:45", "06:45", "06:45", "", ""}}).expect(http.StatusOK)
	h.call(http.MethodPost, "/children/"+child.ID+"/bonus", parent.Token, map[string]any{"minutes": 15}).expect(http.StatusOK)
	h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)

	// German, as the family's phones are.
	d.mustRun(30*time.Second, "shell", "cmd", "locale", "set-app-locales", "io.github.helios57.familyguard", "--locales", "de-CH")

	open := func() {
		d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_HOME")
		d.run(30*time.Second, "shell", "am", "start", "-n", "io.github.helios57.familyguard/.recovery.RecoveryActivity")
		time.Sleep(3 * time.Second)
	}
	// Wait until the screen shows the plan, the agenda and the alarm, i.e. the phone has applied the
	// day this test wrote. Not "Schule": the group "Nach der Schule" says it too, and a tour taken on
	// that photographed the phone mid-sync (2026-10-01).
	deadline := time.Now().Add(3 * time.Minute)
	for {
		open()
		if ui := d.uiText(t); strings.Contains(ui, "Hausaufgaben") && strings.Contains(ui, "Schulhaus") && strings.Contains(ui, "Wecker:") {
			break
		}
		if time.Now().After(deadline) {
			t.Log("the plan and agenda never showed; photographing what is there")
			break
		}
		h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
		time.Sleep(5 * time.Second)
	}

	// The screen, top to bottom: one picture per swipe until the view stops changing.
	prev := ""
	for i := 1; i <= 8; i++ {
		d.shot(t, dir, fmt.Sprintf("app-%02d", i))
		ui := d.uiText(t)
		if ui == prev {
			break
		}
		prev = ui
		d.run(30*time.Second, "shell", "input", "swipe", "540", "1900", "540", "700", "400")
		time.Sleep(1200 * time.Millisecond)
	}

	// The notification shade.
	d.run(30*time.Second, "shell", "cmd", "statusbar", "expand-notifications")
	time.Sleep(2 * time.Second)
	d.shot(t, dir, "shade")
	d.run(30*time.Second, "shell", "cmd", "statusbar", "collapse")

	// FR-28: "Mehr Zeit erbitten", from the button, as a child taps it.
	// Reopened, the screen keeps where it was scrolled to — the bottom, after the pictures above —
	// so it is scrolled back up until the button is on screen.
	open()
	for i := 0; i < 6 && !d.tapText(t, "Mehr Zeit erbitten"); i++ {
		d.run(30*time.Second, "shell", "input", "swipe", "540", "700", "540", "1900", "300")
		time.Sleep(time.Second)
	}
	time.Sleep(2 * time.Second)
	d.shot(t, dir, "ask-dialog")
	if d.tapText(t, "Senden") {
		time.Sleep(3 * time.Second)
		d.shot(t, dir, "ask-waiting")
		var today struct {
			TimeRequests []struct {
				ID string `json:"id"`
			} `json:"time_requests"`
		}
		h.call(http.MethodGet, "/children/"+child.ID+"/today", parent.Token, nil).expect(http.StatusOK).decode(&today)
		if len(today.TimeRequests) == 1 {
			h.call(http.MethodPost, "/children/"+child.ID+"/time-requests/"+today.TimeRequests[0].ID+"/decision",
				parent.Token, map[string]any{"decision": "grant"}).expect(http.StatusOK)
			time.Sleep(8 * time.Second)
			d.run(30*time.Second, "shell", "cmd", "statusbar", "expand-notifications")
			time.Sleep(2 * time.Second)
			d.shot(t, dir, "ask-answer-shade")
			d.run(30*time.Second, "shell", "cmd", "statusbar", "collapse")
			open()
			d.shot(t, dir, "ask-granted")
		} else {
			t.Logf("the request did not reach the server: %+v", today)
		}
	} else {
		t.Log("no Senden button: the ask dialog did not open")
	}

	// What a child meets opening an app while paused.
	h.call(http.MethodPost, "/children/"+child.ID+"/pause", parent.Token, map[string]any{"paused": true}).expect(http.StatusOK)
	h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
	d.awaitSuspended(t, target, "true", 2*time.Minute)
	d.run(30*time.Second, "shell", "monkey", "-p", target, "-c", "android.intent.category.LAUNCHER", "1")
	time.Sleep(3 * time.Second)
	d.shot(t, dir, "paused-app")
	open()
	d.shot(t, dir, "app-paused")
	h.call(http.MethodPost, "/children/"+child.ID+"/pause", parent.Token, map[string]any{"paused": false}).expect(http.StatusOK)

	// The alarm, ringing: today's ring moved to the next whole minute that is at least 70 s away.
	ring := time.Now().In(zurich).Add(70 * time.Second).Truncate(time.Minute).Add(time.Minute)
	if ring.Format("2006-01-02") == now.Format("2006-01-02") {
		h.call(http.MethodPut, "/children/"+child.ID+"/alarm/days/"+ring.Format("2006-01-02"), parent.Token,
			map[string]any{"time": ring.Format("15:04")}).expect(http.StatusOK)
		h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
		d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_SLEEP")
		time.Sleep(time.Until(ring) + 8*time.Second)
		d.shot(t, dir, "alarm")
		d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")
		time.Sleep(2 * time.Second)
		d.shot(t, dir, "alarm-awake")
	} else {
		t.Log("too close to midnight for a same-day ring; no alarm picture")
	}
}
