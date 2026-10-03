package e2e

// FR-5.8 end to end: an app the daily limit never pauses does not spend it either. Measured
// 2026-10-03 on a family phone: a game set to "always free" ran 74 minutes, every one of them was
// counted, and a 30-minute limit paused the apps it governs before the child had opened one.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAnAlwaysFreeAppDoesNotSpendTheDailyLimit(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 30, "timezone": "Europe/Zurich"})
	zurich, _ := time.LoadLocation("Europe/Zurich")
	today := time.Now().In(zurich).Format("2006-01-02")
	const (
		free     = "com.supercell.brawlstars" // the parent's "always free"
		governed = "org.jellyfin.mobile"      // approved, and governed by the daily limit
		chat     = "com.whatsapp"             // always usable (FR-5.9), whatever the rules say
	)
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": free, "label": "Brawl Stars", "launchable": true},
			{"package_name": governed, "label": "Jellyfin", "launchable": true},
			{"package_name": chat, "label": "WhatsApp", "launchable": true},
		},
	}).expect(http.StatusOK)
	rule := func(pkg, action string) {
		h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token, map[string]any{
			"package_name": pkg, "action": action,
		}).expect(http.StatusOK)
	}
	rule(free, "ALLOW")
	rule(governed, "LIMIT")
	report := func(freeMin, governedMin, chatMin int64) {
		h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
			"day":     today,
			"samples": map[string]int64{free: freeMin * 60_000, governed: governedMin * 60_000, chat: chatMin * 60_000},
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
	type row struct {
		PackageName string `json:"package_name"`
		Counted     bool   `json:"counted"`
		Exempt      bool   `json:"exempt"`
	}
	type view struct {
		Apps       []row `json:"apps"`
		ScreenTime struct {
			Counted   int `json:"counted_minutes"`
			Exempt    int `json:"exempt_minutes"`
			Uncounted int `json:"uncounted_minutes"`
		} `json:"screen_time"`
	}
	day := func(which string) view {
		var out view
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/usage/timeline?day="+which, f.parent.Token, nil).
			expect(http.StatusOK).decode(&out)
		return out
	}
	rowOf := func(v view, pkg string) row {
		for _, r := range v.Apps {
			if r.PackageName == pkg {
				return r
			}
		}
		t.Fatalf("%s is not among the day's apps: %+v", pkg, v.Apps)
		return row{}
	}

	// ---- 74 minutes always free, 5 of chat, 20 governed: 20 of 30 spent ----
	report(74, 20, 5)
	if d := desired(); d.UsedMinutes != 20 || d.SuspendReason != "" || slices.Contains(d.Suspended, governed) {
		t.Fatalf("74 minutes of an always-free app and 20 of a governed one: the phone is told used=%d reason=%q "+
			"suspended=%v; only the 20 spend the limit", d.UsedMinutes, d.SuspendReason, d.Suspended)
	}
	// The phone recounts the day itself when offline, from the list it is handed.
	var phone struct {
		Input struct {
			Uncounted []string `json:"uncounted_packages"`
			Used      int      `json:"used_minutes_today"`
		} `json:"input"`
	}
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&phone)
	if !slices.Contains(phone.Input.Uncounted, free) || !slices.Contains(phone.Input.Uncounted, chat) ||
		slices.Contains(phone.Input.Uncounted, governed) || phone.Input.Used != 20 {
		t.Errorf("the phone is handed used=%d and leaves out %v: its own count would disagree with the server's",
			phone.Input.Used, phone.Input.Uncounted)
	}
	v := day(today)
	if v.ScreenTime.Counted != 20 || v.ScreenTime.Exempt != 79 || v.ScreenTime.Uncounted != 0 {
		t.Errorf("the console's day reads counted=%d exempt=%d uncounted=%d, want 20, 79 and 0",
			v.ScreenTime.Counted, v.ScreenTime.Exempt, v.ScreenTime.Uncounted)
	}
	if r := rowOf(v, free); r.Counted || !r.Exempt {
		t.Errorf("the always-free app is shown as %+v, want not counted and exempt", r)
	}
	if r := rowOf(v, governed); !r.Counted || r.Exempt {
		t.Errorf("the governed app is shown as %+v, want counted", r)
	}
	// And Aktivität says it, in a real browser: 20 of 30, the free time on a line of its own, and
	// the game's row saying why it is not counted.
	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="activity"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view [data-screen="exempt"]')`, 15*time.Second, "the always-free line")
	var shown struct {
		Used   string `json:"used"`
		Exempt string `json:"exempt"`
		Rows   string `json:"rows"`
	}
	b.eval(`({
	  used: document.querySelector('#view [data-screen="used"]').textContent,
	  exempt: document.querySelector('#view [data-screen="exempt"]').textContent,
	  rows: [...document.querySelectorAll('#view .app-bar small')].map((e) => e.textContent).join('\n'),
	})`, &shown)
	if shown.Used != "20 min von 30 min" || shown.Exempt != "Immer freie Apps: 1 h 19 min, zählt nicht zum Tageslimit." ||
		!strings.Contains(shown.Rows, free+" · Immer frei, zählt nicht zum Tageslimit") ||
		!strings.Contains(shown.Rows, governed+" · Zählt zum Tageslimit") {
		t.Errorf("Aktivität shows %+v", shown)
	}

	// ---- the governed app spends the rest: the limit is reached, and the free app stays usable ----
	report(74, 30, 5)
	if d := desired(); d.SuspendReason != "QUOTA" || !slices.Contains(d.Suspended, governed) || slices.Contains(d.Suspended, free) {
		t.Fatalf("30 governed minutes of 30 should pause the governed app and only it: reason=%q suspended=%v",
			d.SuspendReason, d.Suspended)
	}

	// ---- the positive control: the same app under LIMIT does spend the limit ----
	rule(free, "LIMIT")
	if d := desired(); d.UsedMinutes != 104 || !slices.Contains(d.Suspended, free) {
		t.Fatalf("under LIMIT the same 74 minutes must count: used=%d suspended=%v", d.UsedMinutes, d.Suspended)
	}
	rule(free, "ALLOW")
	report(74, 30, 5) // records, with the rows, what counts today

	// ---- a past day reads what counted on it, not what counts now ----
	h.fixture(fmt.Sprintf(`UPDATE usage_samples SET day = day - 1 WHERE device_id = '%s'`, f.device.ID))
	yesterday := time.Now().In(zurich).AddDate(0, 0, -1).Format("2006-01-02")
	rule(free, "LIMIT") // a rule changed since must not rewrite yesterday
	if p := day(yesterday); p.ScreenTime.Counted != 30 || p.ScreenTime.Exempt != 79 || !rowOf(p, free).Exempt {
		t.Errorf("yesterday, when the game was always free, reads counted=%d exempt=%d (%+v): want 30 and 79",
			p.ScreenTime.Counted, p.ScreenTime.Exempt, rowOf(p, free))
	}
	// A row from before anything was recorded is read by the rule that applied then: everything counted.
	h.fixture(fmt.Sprintf(`UPDATE usage_samples SET counted = NULL WHERE device_id = '%s'`, f.device.ID))
	if p := day(yesterday); p.ScreenTime.Counted != 109 || p.ScreenTime.Exempt != 0 {
		t.Errorf("a day recorded before 0.6.38 reads counted=%d exempt=%d, want 109 and 0 — how it was enforced",
			p.ScreenTime.Counted, p.ScreenTime.Exempt)
	}
}
