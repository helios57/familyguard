package e2e

// A picture of every console view, at a phone's width and a laptop's, for a person to look at.
//
// Not a check: nothing here can judge whether a screen is clear, and a test that claimed to would be
// the kind of green this suite exists to avoid. What it gives is the one thing the layout guards
// cannot — the rendered page — so a design review starts from what a parent sees rather than from
// the stylesheet. Run it with E2E_TOUR_DIR set to a directory; without it, it skips.
//
//	E2E_TOUR_DIR=/tmp/tour ./run.sh -run '^TestConsoleTour$'

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// screenshot writes the whole page — not just the viewport — as a PNG.
func (b *browser) screenshot(t *testing.T, path string) {
	t.Helper()
	var metrics struct {
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := json.Unmarshal(b.call("Page.getLayoutMetrics", nil), &metrics); err != nil {
		t.Fatalf("layout metrics: %v", err)
	}
	raw := b.call("Page.captureScreenshot", map[string]any{
		"format":                "png",
		"captureBeyondViewport": true,
		"clip": map[string]any{
			"x": 0, "y": 0, "scale": 1,
			"width":  metrics.CSSContentSize.Width,
			"height": metrics.CSSContentSize.Height,
		},
	})
	var shot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &shot); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil || len(png) == 0 {
		t.Fatalf("screenshot of %s came back empty: %v", path, err)
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleTour(t *testing.T) {
	dir := os.Getenv("E2E_TOUR_DIR")
	if dir == "" {
		t.Skip("E2E_TOUR_DIR is not set; the tour writes screenshots for a person and asserts nothing")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h, _ := catalogHarness(t)
	fam := seedAFamilyWorthLookingAt(t, h)
	enrichTheTourFamily(t, h, fam)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	var lang string
	b.eval("navigator.language", &lang)
	t.Logf("the browser's language: %s", lang)
	b.screenshot(t, filepath.Join(dir, "phone-00-signin.png"))
	b.laptop(1440, 900)
	b.screenshot(t, filepath.Join(dir, "laptop-00-signin.png"))

	b.phone(phoneWidth, phoneHeight)
	h.issuer.setNextLogin(primaryParent)
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#child-switcher .pill').length >= 2", 15*time.Second, "the children")

	screens := []struct{ tab, ready string }{
		{"overview", "#view .child-card"},
		{"rules", "#view .card"},
		{"rules/protection", "#view .switch"},
		{"rules/agenda", "#view .agenda-card"},
		{"apps", "#view .list li"},
		{"activity", "#view .card"},
		{"family", "#view .list li"},
	}
	for i, size := range []struct {
		name string
		set  func()
	}{
		{"phone", func() { b.phone(phoneWidth, phoneHeight); b.colorScheme("light") }},
		{"laptop", func() { b.laptop(1440, 900); b.colorScheme("light") }},
		{"phone-dark", func() { b.phone(phoneWidth, phoneHeight); b.colorScheme("dark") }},
		{"laptop-dark", func() { b.laptop(1440, 900); b.colorScheme("dark") }},
	} {
		size.set()
		for n, s := range screens {
			b.eval(fmt.Sprintf("location.hash = '#/%s'", s.tab), nil)
			b.waitFor(fmt.Sprintf("location.hash === '#/%s' && document.querySelector(%q) !== null", s.tab, s.ready),
				20*time.Second, "the "+s.tab+" view")
			// Let late fetches (usage, energy) land before the picture is taken.
			time.Sleep(1500 * time.Millisecond)
			b.eval("window.scrollTo(0, 0)", nil)
			b.screenshot(t, filepath.Join(dir, fmt.Sprintf("%s-%d%d-%s.png", size.name, i, n+1, strings.ReplaceAll(s.tab, "/", "-"))))
		}
		// The phone sheet, from the first phone row that opens one.
		b.eval("location.hash = '#/overview'", nil)
		b.waitFor("document.querySelector('#view button.device-row') !== null", 20*time.Second, "a phone row")
		b.eval("document.querySelector('#view button.device-row').click()", nil)
		b.waitFor("document.getElementById('sheet').open", 10*time.Second, "the phone sheet")
		time.Sleep(500 * time.Millisecond)
		b.screenshot(t, filepath.Join(dir, fmt.Sprintf("%s-%d9-phone-sheet.png", size.name, i)))
		b.eval("document.getElementById('sheet-close').click()", nil)
	}
}

// enrichTheTourFamily gives the seeded child a whole day, so every part of the console has something
// to draw: a plan with one group earned and one task reported (the "Wartet auf dich" card and the
// gold), an agenda and an alarm week, sittings for the hour chart, an energy report, Live running.
func enrichTheTourFamily(t *testing.T, h *harness, fam seededFamily) {
	t.Helper()
	zurich := mustZurich()
	now := time.Now().In(zurich)
	plan := h.putPlan(fam.parentToken, fam.childID, []planGroupDTO{
		{Title: "Morgen", Weekdays: 127, StartsAt: "00:00", EndsAt: "23:59", EarnedMinutes: 15,
			Tasks: []planTaskDTO{{Title: "Zähne putzen"}, {Title: "Bett machen"}}},
		{Title: "Nach der Schule", Weekdays: 127, StartsAt: "00:00", EndsAt: "23:59", EarnedMinutes: 30,
			Tasks: []planTaskDTO{{Title: "Hausaufgaben", Note: "Mathe Seite 42"}, {Title: "Zimmer aufräumen"}}},
	})
	for _, task := range plan[0].Tasks {
		h.call(http.MethodPost, "/children/"+fam.childID+"/tasks/"+task.ID+"/decision", fam.parentToken,
			map[string]any{"decision": "confirm"}).expect(http.StatusOK)
	}
	h.call(http.MethodPost, "/device/tasks/"+plan[1].Tasks[0].ID+"/report", fam.deviceToken, nil).expect(http.StatusOK)
	// FR-28: a request waiting for an answer.
	h.call(http.MethodPost, "/device/time-requests", fam.deviceToken,
		map[string]any{"minutes": 30, "note": "Film fertig schauen"}).expect(http.StatusOK)
	h.putAgenda(fam.parentToken, fam.childID, []agendaEntryDTO{
		{Kind: "RECURRING", Title: "Schule", Place: "Schulhaus", Weekdays: 31, StartsAt: "08:00", EndsAt: "12:00"},
		{Kind: "SINGLE", Title: "Fussball", Place: "Sportplatz", Day: now.AddDate(0, 0, 1).Format("2006-01-02"), StartsAt: "17:00", EndsAt: "18:30"},
	})
	h.call(http.MethodPut, "/children/"+fam.childID+"/alarm", fam.parentToken,
		map[string]any{"weekdays": []string{"06:45", "06:45", "06:45", "06:45", "06:45", "", ""}}).expect(http.StatusOK)

	// Sittings earlier today, in the child's zone, so the hour chart has a shape.
	at := func(back time.Duration, minutes int) map[string]any {
		from := now.Add(-back)
		return map[string]any{"package_name": pkgGame, "started_at": from.Format(time.RFC3339),
			"ended_at": from.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339)}
	}
	sessions := []map[string]any{}
	for _, s := range []struct {
		back time.Duration
		min  int
	}{{5 * time.Hour, 40}, {3 * time.Hour, 25}, {90 * time.Minute, 30}} {
		if now.Add(-s.back).Format("2006-01-02") == now.Format("2006-01-02") {
			sessions = append(sessions, at(s.back, s.min))
		}
	}
	h.call(http.MethodPost, "/device/usage", fam.deviceToken, map[string]any{
		"day": now.Format("2006-01-02"), "samples": map[string]int64{pkgGame: 97 * 60 * 1000, pkgChat: 23 * 60 * 1000, pkgYouTube: 41 * 60 * 1000},
		"sessions": sessions,
	}).expect(http.StatusOK)

	// Two heartbeats an hour apart in what they report, so the energy card has a rate.
	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for i, cpu := range []int64{1000, 4600} {
		if i > 0 {
			time.Sleep(1100 * time.Millisecond)
		}
		h.call(http.MethodPost, "/device/heartbeat", fam.deviceToken, map[string]any{
			"connectivity": "wifi", "battery_level": 41 - i, "charging": false, "screen_on": true,
			"energy": map[string]any{"since": since, "cpu_ms": cpu, "rx_bytes": 0, "tx_bytes": 0,
				"stream_opens": 1 + 2*i, "events": 0, "polls": 0, "pushes": 0, "other_syncs": 0,
				"active_ms": 1000 * i, "passive_ms": 3000 * i, "route_full_ms": 2000 * i, "route_dns_ms": 2000 * i},
		}).expect(http.StatusOK)
	}
	h.call(http.MethodPost, "/devices/"+fam.deviceID+"/live", fam.parentToken, map[string]any{"minutes": 30}).expect(http.StatusOK)
	h.call(http.MethodPost, "/device/location", fam.deviceToken, map[string]any{
		"latitude": 47.3769, "longitude": 8.5417, "accuracy_m": 12,
	}).expect(http.StatusOK)
}

// colorScheme makes the page see prefers-color-scheme as "light" or "dark".
func (b *browser) colorScheme(scheme string) {
	b.call("Emulation.setEmulatedMedia", map[string]any{
		"features": []map[string]any{{"name": "prefers-color-scheme", "value": scheme}},
	})
}
