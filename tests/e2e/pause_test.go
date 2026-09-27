package e2e

// FR-21.1 and FR-21.2 against the real server: a pause, and today's time going down as well as up. Every
// assertion about what the phone must do reads the policy with the DEVICE's own credential — the
// answer the phone acts on — rather than the console's preview of it.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// pkgCamera and pkgWhatsApp are declared beside the tests that introduced them.
const (
	pkgAllowed = "com.example.alwaysfree"
	pkgService = "com.samsung.android.service.hidden"
)

// pauseFixture is an enrolled phone with a game, an app the parent made always free, the
// preinstalled camera, WhatsApp, the phone's own dialer and a service with no icon.
func pauseFixture(t *testing.T, h *harness) fixture {
	t.Helper()
	f := enrolledFixture(t, h)
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Brawl Stars", "launchable": true},
			{"package_name": pkgAllowed, "label": "Always free", "launchable": true},
			{"package_name": pkgCamera, "label": "Camera", "system_app": true, "launchable": true},
			{"package_name": pkgWhatsApp, "label": "WhatsApp", "launchable": true},
			{"package_name": oemDialer, "label": "Phone", "system_app": true, "launchable": true},
			{"package_name": pkgService, "system_app": true, "launchable": false},
		},
	}).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token, map[string]any{
		"package_name": pkgAllowed, "action": "ALLOW",
	}).expect(http.StatusOK)
	return f
}

type pausedDTO struct {
	Paused   bool    `json:"paused"`
	PausedAt *string `json:"paused_at"`
}

func (h *harness) pause(token, childID string, paused bool) apiResponse {
	return h.call(http.MethodPost, "/children/"+childID+"/pause", token, map[string]any{"paused": paused})
}

// phoneSees is what the phone is told, including the input it recomputes from while offline.
func (h *harness) phoneSees(deviceToken string) (desiredStateDTO, map[string]json.RawMessage) {
	h.t.Helper()
	var out struct {
		Desired desiredStateDTO `json:"desired"`
		Input   struct {
			Settings map[string]json.RawMessage `json:"settings"`
		} `json:"input"`
	}
	h.call(http.MethodGet, "/device/policy", deviceToken, nil).expect(http.StatusOK).decode(&out)
	return out.Desired, out.Input.Settings
}

func TestAPauseTakesEverythingButCallsAndMessages(t *testing.T) {
	h := newHarness(t)
	f := pauseFixture(t, h)
	before, _ := h.phoneSees(f.deviceToken())

	var answer pausedDTO
	h.pause(f.parent.Token, f.child.ID, true).expect(http.StatusOK).decode(&answer)
	if !answer.Paused || answer.PausedAt == nil {
		t.Fatalf("the pause answered %+v; want paused with a time", answer)
	}

	check := func(when string) {
		t.Helper()
		d, settings := h.phoneSees(f.deviceToken())
		if d.SuspendReason != "PAUSED" {
			t.Errorf("%s: the phone is told reason %q, want PAUSED", when, d.SuspendReason)
		}
		for _, p := range []string{pkgGame, pkgAllowed, pkgCamera} {
			if !slices.Contains(d.SuspendedPackages, p) {
				t.Errorf("%s: %s is not paused (the pause takes always-free and preinstalled apps too): %v",
					when, p, d.SuspendedPackages)
			}
		}
		for _, p := range []string{pkgWhatsApp, oemDialer, pkgService} {
			if slices.Contains(d.SuspendedPackages, p) {
				t.Errorf("%s: %s is paused; calls, messages and services must keep working", when, p)
			}
		}
		// Review focus 1: the phone recomputes offline from this input, so the pause must be in it.
		if string(settings["paused"]) != "true" {
			t.Errorf("%s: the input the phone caches says paused=%s, so an offline phone would drop the pause",
				when, settings["paused"])
		}
		if d.PolicyVersion <= before.PolicyVersion {
			t.Errorf("%s: the policy version did not move (%d -> %d), so a phone has no reason to fetch",
				when, before.PolicyVersion, d.PolicyVersion)
		}
	}
	check("paused")

	// A state, not a command: a server restart does not lose it.
	h.restart()
	check("after a restart")

	h.pause(f.parent.Token, f.child.ID, false).expect(http.StatusOK).decode(&answer)
	if answer.Paused {
		t.Fatalf("unpausing answered %+v", answer)
	}
	d, settings := h.phoneSees(f.deviceToken())
	if d.SuspendReason != "" {
		t.Errorf("after unpausing the phone is told reason %q, want none", d.SuspendReason)
	}
	for _, p := range []string{pkgGame, pkgAllowed, pkgCamera} {
		if slices.Contains(d.SuspendedPackages, p) {
			t.Errorf("after unpausing %s is still paused: %v", p, d.SuspendedPackages)
		}
	}
	if string(settings["paused"]) != "false" {
		t.Errorf("after unpausing the cached input says paused=%s", settings["paused"])
	}
}

// Review focus 4: unpausing during bedtime lands in bedtime, not in "free".
func TestUnpausingDuringBedtimeLeavesBedtime(t *testing.T) {
	h := newHarness(t)
	f := pauseFixture(t, h)
	// A window that contains every minute but one, so "now" is inside it whatever the clock says.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"bedtime_enabled": true, "bedtime_start": "00:00", "bedtime_end": "23:59", "timezone": "Europe/Zurich",
	})
	h.pause(f.parent.Token, f.child.ID, true).expect(http.StatusOK)
	if d, _ := h.phoneSees(f.deviceToken()); d.SuspendReason != "PAUSED" {
		t.Fatalf("paused in bedtime: reason %q, want PAUSED", d.SuspendReason)
	}
	h.pause(f.parent.Token, f.child.ID, false).expect(http.StatusOK)
	d, _ := h.phoneSees(f.deviceToken())
	if d.SuspendReason != "BEDTIME" || !slices.Contains(d.SuspendedPackages, pkgGame) {
		t.Errorf("unpaused in bedtime: reason %q, game paused %v; want BEDTIME with the game paused",
			d.SuspendReason, slices.Contains(d.SuspendedPackages, pkgGame))
	}
	if slices.Contains(d.SuspendedPackages, pkgAllowed) {
		t.Error("unpaused in bedtime: the always-free app is still paused, so the pause was not fully lifted")
	}
}

func TestAGuardianCanPauseAndBadRequestsSayWhy(t *testing.T) {
	h := newHarness(t)
	f := pauseFixture(t, h)
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	h.pause(guardian.Token, f.child.ID, true).expect(http.StatusOK)
	if d, _ := h.phoneSees(f.deviceToken()); d.SuspendReason != "PAUSED" {
		t.Errorf("a guardian's pause did not reach the phone: reason %q", d.SuspendReason)
	}
	h.mustRefuse(t, []refusal{
		{what: "a body without paused", method: http.MethodPost, path: "/children/" + f.child.ID + "/pause",
			token: f.parent.Token, body: map[string]any{},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"paused"}},
		{what: "a profile that does not exist", method: http.MethodPost,
			path: "/children/11111111-2222-3333-4444-555555555555/pause", token: f.parent.Token,
			body: map[string]any{"paused": true}, status: http.StatusNotFound, code: "not_found"},
	})
}

type adjustDTO struct {
	Bonus      int `json:"bonus_minutes"`
	Limit      int `json:"daily_limit_minutes"`
	LimitToday int `json:"limit_today_minutes"`
}

func TestTodaysTimeGoesDownAsWellAsUp(t *testing.T) {
	h := newHarness(t)
	f := pauseFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	adjust := func(minutes int) adjustDTO {
		t.Helper()
		var out adjustDTO
		h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": minutes}).
			expect(http.StatusOK).decode(&out)
		return out
	}

	if a := adjust(-15); a.Bonus != -15 || a.LimitToday != 45 {
		t.Fatalf("−15 answered %+v; want adjustment −15, 45 minutes today", a)
	}
	d, _ := h.phoneSees(f.deviceToken())
	if d.QuotaMinutes != 45 || d.DailyLimitMinutes != 60 || d.BonusMinutes != -15 || d.SuspendReason != "" {
		t.Errorf("after −15 the phone is told quota %d, limit %d, adjustment %d, reason %q; want 45, 60, −15, none",
			d.QuotaMinutes, d.DailyLimitMinutes, d.BonusMinutes, d.SuspendReason)
	}

	// Taking more than the day has leaves the day at zero — a limit reached, not "no limit".
	if a := adjust(-120); a.Bonus != -60 || a.LimitToday != 0 {
		t.Fatalf("−120 on a 45-minute day answered %+v; want the adjustment floored at −60, 0 minutes today", a)
	}
	d, _ = h.phoneSees(f.deviceToken())
	if d.QuotaMinutes != 0 || d.DailyLimitMinutes != 60 || d.SuspendReason != "QUOTA" ||
		!slices.Contains(d.SuspendedPackages, pkgGame) {
		t.Errorf("a day taken to zero: quota %d, limit %d, reason %q, game paused %v; want 0, 60, QUOTA, paused",
			d.QuotaMinutes, d.DailyLimitMinutes, d.SuspendReason, slices.Contains(d.SuspendedPackages, pkgGame))
	}

	// +15 after that gives exactly 15 minutes back, rather than vanishing into a deeper negative.
	if a := adjust(15); a.Bonus != -45 || a.LimitToday != 15 {
		t.Errorf("+15 after the floor answered %+v; want −45, 15 minutes today", a)
	}

	h.mustRefuse(t, []refusal{
		{what: "zero minutes", method: http.MethodPost, path: "/children/" + f.child.ID + "/bonus",
			token: f.parent.Token, body: map[string]any{"minutes": 0},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"-1440", "1440"}},
		{what: "more than a day taken", method: http.MethodPost, path: "/children/" + f.child.ID + "/bonus",
			token: f.parent.Token, body: map[string]any{"minutes": -1441},
			status: http.StatusBadRequest, code: "invalid_input"},
	})
}

// Review focus 5.
func TestAReductionWithoutALimitSaysSo(t *testing.T) {
	h := newHarness(t)
	f := pauseFixture(t, h)
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": -15}).
		expectError(http.StatusConflict, "no_daily_limit")
}
