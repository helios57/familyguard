package e2e

// FR-3.8 … FR-3.11 end to end against the real server: only use counts, extra time for today, each
// app's use against its limit, and why an app cannot be used — the numbers the phone and the
// console are both built from.

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	pkgLauncher = "com.sec.android.app.launcher"
	pkgSystemUI = "com.android.systemui"
	pkgDPC      = "io.github.helios57.familyguard"
)

type screenTimeDTO struct {
	CountedMinutes    int    `json:"counted_minutes"`
	UncountedMinutes  int    `json:"uncounted_minutes"`
	DailyLimitMinutes int    `json:"daily_limit_minutes"`
	BonusMinutes      int    `json:"bonus_minutes"`
	LimitRecorded     bool   `json:"limit_recorded"`
	SuspendReason     string `json:"suspend_reason"`
	IsToday           bool   `json:"is_today"`
}

type appRowDTO struct {
	PackageName  string `json:"package_name"`
	ForegroundMs int64  `json:"foreground_ms"`
	Counted      bool   `json:"counted"`
	LimitMinutes int    `json:"limit_minutes"`
	Rule         string `json:"rule"`
	Blocked      string `json:"blocked"`
}

type dayViewDTO struct {
	Day        string        `json:"day"`
	Apps       []appRowDTO   `json:"apps"`
	ScreenTime screenTimeDTO `json:"screen_time"`
}

func (d dayViewDTO) app(t *testing.T, pkg string) appRowDTO {
	t.Helper()
	for _, a := range d.Apps {
		if a.PackageName == pkg {
			return a
		}
	}
	t.Fatalf("%s is not among the day's apps: %+v", pkg, d.Apps)
	return appRowDTO{}
}

type desiredDTO struct {
	SuspendReason string   `json:"suspend_reason"`
	QuotaMinutes  int      `json:"quota_minutes"`
	UsedMinutes   int      `json:"used_minutes"`
	BonusMinutes  int      `json:"bonus_minutes"`
	Suspended     []string `json:"suspended_packages"`
}

func TestOnlyUseCountsAndExtraTimeLiftsTheLimit(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	zurich, _ := time.LoadLocation("Europe/Zurich")
	today := time.Now().In(zurich).Format("2006-01-02")

	// What is installed: the engine pauses installed apps, and a used app always is one.
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Brawl Stars"},
			{"package_name": pkgLauncher, "label": "One UI Home", "system_app": true},
			// A service with no launcher entry (FR-3.12): nothing a child can open.
			{"package_name": "com.samsung.android.emergency", "system_app": true, "launchable": false},
			{"package_name": pkgSystemUI, "system_app": true, "launchable": false},
		},
	}).expect(http.StatusOK)

	// The phone says which app is its home screen (FR-3.8).
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "home_packages": []string{pkgLauncher},
	}).expect(http.StatusOK)

	report := func(game int64) {
		h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
			"day": today,
			"samples": map[string]int64{
				pkgGame:     game * 60_000,
				pkgLauncher: 57 * 60_000, // on the charger with the screen on, nobody holding it
				pkgSystemUI: 5 * 60_000,
				pkgDPC:      3 * 60_000,
			},
		}).expect(http.StatusOK)
	}
	desired := func() desiredDTO {
		var out struct {
			Desired desiredDTO `json:"desired"`
		}
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/desired-state", f.parent.Token, nil).
			expect(http.StatusOK).decode(&out)
		return out.Desired
	}
	day := func(which string) dayViewDTO {
		var out dayViewDTO
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/usage/timeline?day="+which, f.parent.Token, nil).
			expect(http.StatusOK).decode(&out)
		return out
	}

	// ---- FR-3.8: 50 minutes of a game and 65 of home screen, System UI and FamilyGuard ----
	report(50)
	if d := desired(); d.UsedMinutes != 50 || d.SuspendReason != "" {
		t.Fatalf("with 50 minutes of use and 65 of home screen/system/FamilyGuard the phone is told "+
			"used=%d reason=%q; only the 50 are use", d.UsedMinutes, d.SuspendReason)
	}
	var devicePolicy struct {
		Input struct {
			Uncounted []string `json:"uncounted_packages"`
		} `json:"input"`
	}
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&devicePolicy)
	for _, pkg := range []string{pkgLauncher, pkgSystemUI, pkgDPC} {
		if !slices.Contains(devicePolicy.Input.Uncounted, pkg) {
			t.Errorf("the phone is not told that %s is not counted (%v): its offline count would disagree with the server's",
				pkg, devicePolicy.Input.Uncounted)
		}
	}

	report(60)
	d := desired()
	if d.SuspendReason != "QUOTA" || d.QuotaMinutes != 60 {
		t.Fatalf("60 of 60 counted minutes should pause the phone; got reason=%q quota=%d", d.SuspendReason, d.QuotaMinutes)
	}
	for _, pkg := range []string{"com.samsung.android.emergency", pkgSystemUI, pkgLauncher} {
		if slices.Contains(d.Suspended, pkg) {
			t.Errorf("the daily limit pauses %s, which no child can open or which is not use: %v", pkg, d.Suspended)
		}
	}
	if !slices.Contains(d.Suspended, pkgGame) {
		t.Errorf("the daily limit does not pause the game: %v", d.Suspended)
	}
	v := day(today)
	if v.ScreenTime.CountedMinutes != 60 || v.ScreenTime.UncountedMinutes != 65 {
		t.Errorf("the day reads %d counted and %d uncounted minutes, want 60 and 65", v.ScreenTime.CountedMinutes, v.ScreenTime.UncountedMinutes)
	}
	if v.ScreenTime.SuspendReason != "QUOTA" || !v.ScreenTime.IsToday || v.ScreenTime.DailyLimitMinutes != 60 {
		t.Errorf("today's summary does not say the daily limit pauses the phone: %+v", v.ScreenTime)
	}
	if g := v.app(t, pkgGame); g.Blocked != "QUOTA" || !g.Counted {
		t.Errorf("the game should be counted and paused by the daily limit: %+v", g)
	}
	if l := v.app(t, pkgLauncher); l.Counted || l.Blocked != "" {
		t.Errorf("the home screen must be shown as not counted and never as paused: %+v", l)
	}

	// ---- FR-3.11: extra time for today ----
	var granted struct {
		Day   string `json:"day"`
		Bonus int    `json:"bonus_minutes"`
		Limit int    `json:"limit_today_minutes"`
	}
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": 30}).
		expect(http.StatusOK).decode(&granted)
	if granted.Day != today || granted.Bonus != 30 || granted.Limit != 90 {
		t.Fatalf("a 30-minute grant answered %+v; want day %s, bonus 30, limit today 90", granted, today)
	}
	if d := desired(); d.SuspendReason != "" || d.QuotaMinutes != 90 || d.BonusMinutes != 30 {
		t.Fatalf("after 30 extra minutes the phone is told reason=%q quota=%d bonus=%d; the limit should be 90 and nothing paused",
			d.SuspendReason, d.QuotaMinutes, d.BonusMinutes)
	}
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": 20}).
		expect(http.StatusOK).decode(&granted)
	if granted.Bonus != 50 {
		t.Errorf("two grants on one day add up: got %d, want 50", granted.Bonus)
	}
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": 1391}).
		expectError(http.StatusConflict, "bonus_too_large")
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": 0}).
		expectError(http.StatusBadRequest, "invalid_input")
	if v := day(today); v.ScreenTime.BonusMinutes != 50 || v.app(t, pkgGame).Blocked != "" {
		t.Errorf("the console's day does not show the extra time lifting the pause: %+v", v.ScreenTime)
	}

	// ---- FR-3.10: an app's own limit, spent while the day still has time ----
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token, map[string]any{
		"package_name": pkgGame, "action": "LIMIT", "limit_minutes": 45,
	}).expect(http.StatusOK)
	if g := day(today).app(t, pkgGame); g.Blocked != "APP_LIMIT" || g.LimitMinutes != 45 || g.Rule != "LIMIT" {
		t.Errorf("a game at 60 of its own 45 minutes should be paused by its own limit: %+v", g)
	}

	// ---- FR-3.9: a past day reads the limit that applied THEN, or says it has none ----
	yesterday := time.Now().In(zurich).AddDate(0, 0, -1).Format("2006-01-02")
	h.fixture(fmt.Sprintf(`INSERT INTO day_limits (child_id, day, daily_limit_minutes, bonus_minutes, app_limits)
		VALUES ('%s', '%s', 120, 15, '{"%s": 20}')`, f.child.ID, yesterday, pkgGame))
	past := day(yesterday)
	if !past.ScreenTime.LimitRecorded || past.ScreenTime.DailyLimitMinutes != 120 || past.ScreenTime.BonusMinutes != 15 || past.ScreenTime.IsToday {
		t.Errorf("yesterday must be drawn against yesterday's 120 + 15, not today's 60: %+v", past.ScreenTime)
	}
	older := time.Now().In(zurich).AddDate(0, 0, -5).Format("2006-01-02")
	if o := day(older); o.ScreenTime.LimitRecorded {
		t.Errorf("a day nothing recorded claims a limit: %+v", o.ScreenTime)
	}
}

// With no daily limit there is nothing to add extra time to, and the request says so rather than
// storing minutes that could never do anything.
func TestExtraTimeNeedsADailyLimit(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 0})
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": 30}).
		expectError(http.StatusConflict, "no_daily_limit")
}

// FR-3.9 / FR-3.10 / FR-3.11 in the console, at phone width: one bar per app with its own limit
// marked where it falls, the reason it is paused in words, and "+ time today" that actually grants.
func TestTheConsoleDrawsEachAppAgainstItsLimit(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	zurich, _ := time.LoadLocation("Europe/Zurich")
	today := time.Now().In(zurich).Format("2006-01-02")
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{{"package_name": pkgGame, "label": "Brawl Stars"}},
	}).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token, map[string]any{
		"package_name": pkgGame, "action": "LIMIT", "limit_minutes": 40,
	}).expect(http.StatusOK)
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"day": today, "samples": map[string]int64{pkgGame: 20 * 60_000},
	}).expect(http.StatusOK)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")
	b.switchTab(t, "activity", "#view .app-bars")
	b.measure(t, "activity/app-bars").check(t, "activity/app-bars")

	var drawn struct {
		Rows    []string `json:"rows"`
		Fill    string   `json:"fill"`
		Marker  string   `json:"marker"`
		Summary string   `json:"summary"`
		Buttons []string `json:"buttons"`
	}
	b.eval(`(() => {
	  const li = document.querySelector('#view .app-bars > li');
	  return {
	    rows: Array.from(document.querySelectorAll('#view .app-bars > li')).map((n) => n.textContent),
	    fill: li ? li.querySelector('.ubar-fill').style.width : '',
	    marker: li && li.querySelector('.ubar-limit') ? li.querySelector('.ubar-limit').style.left : '',
	    summary: (document.querySelector('#view .st-summary') || {}).textContent || '',
	    buttons: Array.from(document.querySelectorAll('#view .st-summary button')).map((n) => n.textContent),
	  };
	})()`, &drawn)

	if len(drawn.Rows) != 1 {
		t.Fatalf("the card draws %d app rows, want 1: %q", len(drawn.Rows), drawn.Rows)
	}
	for _, must := range []string{"Brawl Stars", "20 min of 40 min", "Own limit 40 min a day"} {
		if !strings.Contains(drawn.Rows[0], must) {
			t.Errorf("the app row does not say %q: %q", must, drawn.Rows[0])
		}
	}
	// One scale for the table: the largest of use and own limit, here the 40-minute limit. So the
	// bar is half full and the limit sits at the right-hand end.
	// Compared as numbers: the browser normalises "50.0%" to "50%" on the way back out.
	pct := func(v string) float64 {
		n, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
		if err != nil {
			t.Fatalf("an unreadable CSS length %q — was it set as an attribute the CSP dropped?", v)
		}
		return n
	}
	if pct(drawn.Fill) != 50 || pct(drawn.Marker) != 100 {
		t.Errorf("20 of 40 minutes draws a bar %q wide with the limit at %q; want 50%% and 100%%", drawn.Fill, drawn.Marker)
	}
	if !strings.Contains(drawn.Summary, "20 min of 1 h") {
		t.Errorf("the day's summary does not read 20 min of the 1 h limit: %q", drawn.Summary)
	}
	if fmt.Sprint(drawn.Buttons) != "[+15 min +30 min +60 min]" {
		t.Fatalf("the extra-time buttons are %q", drawn.Buttons)
	}

	// Pressing one grants the time on the server, not merely in the page.
	b.eval(`document.querySelector('#view .st-summary button').click()`, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var d struct {
			Desired desiredDTO `json:"desired"`
		}
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/desired-state", f.parent.Token, nil).expect(http.StatusOK).decode(&d)
		if d.Desired.BonusMinutes == 15 && d.Desired.QuotaMinutes == 75 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after pressing +15 min the phone is told bonus=%d quota=%d", d.Desired.BonusMinutes, d.Desired.QuotaMinutes)
		}
		time.Sleep(200 * time.Millisecond)
	}
	b.waitFor("(document.querySelector('#view .st-summary') || {}).textContent.includes('15 min extra')",
		10*time.Second, "the summary to show the extra time")
}
