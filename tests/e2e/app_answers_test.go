package e2e

// The four answers a parent can give about one app (FR-5.8), and the apps no answer may silence
// (FR-5.9) — from the PUT the console sends to the packages the phone is told to suspend.
//
// This exists because until 2026-09-20 there were only two answers, and a parent who wanted to say
// "yes, my child may have this, and it still counts like everything else" had no way to say it.
// ALLOW meant exempt from bedtime AND from the daily limit; BLOCK meant gone; no rule at all meant
// the app stayed suspended, waiting, forever. The owner's four categories — always free, daily
// limit, own limit, always blocked — are what this file asserts, in that order, on one device.
//
// The parts only an end-to-end test can answer are the ones where SQL produces the value: whether
// a LIMIT row survives the app_rules CHECK constraint, whether its limit_minutes reaches the
// engine, and whether per-package usage read back out of usage_samples is the number the allowance
// is compared against. A unit test can assert the engine's arithmetic and does (vectors.json); it
// cannot notice that the column was never selected.

import (
	"net/http"
	"testing"
	"time"
)

// The always-usable packages as a real phone carries them (FR-5.9). Written out here rather than
// read from the engine on purpose: this suite is where the family's requirement is recorded, and a
// test that imported the list under test would agree with it by construction — including on the
// day somebody deletes an entry.
const (
	pkgWhatsApp = "com.whatsapp"
	pkgThreema  = "ch.threema.app"
	pkgAudible  = "com.audible.application"
)

// One invented app per answer, so a failure names the answer that broke rather than a package id.
const (
	pkgAlwaysFree = "com.example.alwaysfree"
	pkgDailyLimit = "com.example.dailylimit"
	pkgOwnLimit   = "com.example.ownlimit"
	pkgForbidden  = "com.example.forbidden"
	pkgUndecided  = "com.example.undecided"
)

// baselineThenNew reports two inventories: the first establishes what the child already had, the
// second adds the packages named. Novelty is read from the baseline flag the store writes on the
// FIRST report (see enforce.resolve), so an app can only be "new" if it arrives in a later one —
// posting everything at once would put nothing in front of the parent.
func baselineThenNew(h *harness, f fixture, newPackages ...string) {
	h.t.Helper()
	baseline := []map[string]any{
		{"package_name": pkgGame, "label": "Game"},
		{"package_name": pkgAOSPPhone, "label": "Phone", "system_app": true},
	}
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(),
		map[string]any{"apps": baseline}).expect(http.StatusOK)

	second := append([]map[string]any{}, baseline...)
	for _, p := range newPackages {
		second = append(second, map[string]any{"package_name": p, "label": labelFor(p)})
	}
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(),
		map[string]any{"apps": second}).expect(http.StatusOK)
}

// labelFor gives each invented package a human label, because half of what this file is about is a
// parent being able to read the answer — an app list that prints package ids is the state the
// console was in when the owner said they could not see which app was used how long.
func labelFor(pkg string) string {
	switch pkg {
	case pkgWhatsApp:
		return "WhatsApp"
	case pkgThreema:
		return "Threema"
	case pkgAudible:
		return "Audible"
	case pkgAlwaysFree:
		return "Always free"
	case pkgDailyLimit:
		return "Daily limit"
	case pkgOwnLimit:
		return "Own limit"
	case pkgForbidden:
		return "Forbidden"
	case pkgUndecided:
		return "Undecided"
	}
	return pkg
}

// setRule is the PUT the console's category buttons send. minutes is only meaningful for LIMIT.
func setRule(h *harness, f fixture, pkg, action string, minutes int) {
	h.t.Helper()
	body := map[string]any{"package_name": pkg, "action": action}
	if action == "LIMIT" {
		body["limit_minutes"] = minutes
	}
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token, body).
		expect(http.StatusOK)
}

// bedtimeAroundNow returns a window that certainly contains this instant, for the tests that need
// bedtime and recorded usage at the same time. The `at=` preview cannot serve both: usage is read
// for the day of the instant asked about, so previewing a January evening would look at a day with
// no samples in it and quietly report a quota that was never reached.
func bedtimeAroundNow(t *testing.T, zone string) (string, string) {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatalf("the test's own zone %q does not load: %v", zone, err)
	}
	now := time.Now().In(loc)
	return now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04")
}

// TestAlwaysUsableAppsSurviveEveryPolicyPath — FR-5.9, and it is the owner's requirement stated as
// a test: "threema and whatsapp is not usable, they should ALWAYS be usable", "also audible".
//
// Every path that can suspend an app is switched on at once here — bedtime, an exhausted quota,
// the family blocklist, a per-child BLOCK, and the approval hold that free-installation-off puts
// new apps behind. The three named apps must be in none of the resulting lists, and the ordinary
// apps beside them must be in them: an assertion that something is absent is worth nothing unless
// the same call shows what presence looks like.
func TestAlwaysUsableAppsSurviveEveryPolicyPath(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	baselineThenNew(h, f, pkgWhatsApp, pkgThreema, pkgAudible, pkgUndecided)

	// Path 1: the family blocklist (FR-18) — one entry, every phone in the family.
	h.call(http.MethodPut, "/family/blocked-packages", f.parent.Token,
		map[string]any{"package_name": pkgWhatsApp}).expect(http.StatusOK)
	// Path 2: a per-child BLOCK, which is the strongest thing a parent can say about one app.
	//
	// This is the consequence of reading "ALWAYS" literally, and it is deliberate rather than
	// overlooked: a parent who blocks Threema from the console will find it still working. The
	// alternative — letting a BLOCK win — means one mis-tap can take the messenger off a child's
	// phone, which is the failure this carve-out exists to prevent. Narrowing it is a one-line
	// change to AlwaysUsablePackages if the owner wants the opposite trade.
	setRule(h, f, pkgThreema, "BLOCK", 0)

	// Path 3 and 4: bedtime and an exhausted daily quota, both live at this instant.
	start, end := bedtimeAroundNow(t, "Europe/Zurich")
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{
			pkgGame:    45 * 60 * 1000,
			pkgAudible: 90 * 60 * 1000,
		},
	}).expect(http.StatusOK)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"timezone": "Europe/Zurich", "daily_limit_minutes": 30,
		"bedtime_enabled": true, "bedtime_start": start, "bedtime_end": end,
		// Path 5: the approval hold. pkgUndecided has no rule, so it waits (FR-5.4).
		"allow_child_installs": false,
	})

	state := h.desiredState(f.parent.Token, f.device.ID, "").Desired

	// The calibration, and it comes first: if none of the five paths is actually in effect, every
	// assertion below passes for the wrong reason.
	if state.SuspendReason != "BEDTIME" {
		t.Fatalf("the window %s–%s was supposed to contain this instant; the phone reads %q: %+v",
			start, end, state.SuspendReason, state)
	}
	mustHave(t, state.SuspendedPackages, pkgGame,
		"bedtime must suspend an ordinary app, or the absences below prove nothing")
	mustHave(t, state.PendingApproval, pkgUndecided,
		"an app nobody answered for must be waiting, or the approval hold is not in effect")
	mustHave(t, state.SuspendedPackages, pkgUndecided, "a pending app is suspended while it waits")

	// The requirement itself.
	for _, pkg := range []string{pkgWhatsApp, pkgThreema, pkgAudible} {
		mustNotHave(t, state.SuspendedPackages, pkg,
			"an always-usable app must never be suspended, whatever the policy says (FR-5.9)")
		mustNotHave(t, state.HiddenPackages, pkg, "an always-usable app must never be hidden")
		mustNotHave(t, state.PendingApproval, pkg,
			"an always-usable app must never wait for an approval a parent might not be awake to give")
	}

	// And the same answer with bedtime out of the way, because the owner's report was "after i
	// disabled bedtime the apps are still not usable" — the quota and the approval hold are
	// separate paths and switching one off must not be read as having tested the others.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"bedtime_enabled": false})
	quota := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	if quota.SuspendReason != "QUOTA" {
		t.Fatalf("135 minutes against a 30 minute limit reads as %q: %+v", quota.SuspendReason, quota)
	}
	for _, pkg := range []string{pkgWhatsApp, pkgThreema, pkgAudible} {
		mustNotHave(t, quota.SuspendedPackages, pkg, "an exhausted quota must not reach an always-usable app")
		mustNotHave(t, quota.HiddenPackages, pkg, "an exhausted quota must not hide an always-usable app")
	}

	// The phone recomputes this policy itself when it has no network (FR-9), from the Input it
	// cached — so the carve-out has to be IN that input. If it lived only in the computed answer,
	// or only in the APK, the first tunnel would put the child back where they started.
	input := h.desiredState(f.parent.Token, f.device.ID, "").Input
	for _, pkg := range []string{pkgWhatsApp, pkgThreema, pkgAudible} {
		mustHave(t, input.CriticalPackages, pkg,
			"the phone recomputes offline and must be handed the always-usable set to recompute with")
	}
	mustHave(t, input.CriticalPackages, oemDialer,
		"the device's own critical packages must survive the union, not be replaced by it")
}

// TestAnAppHasFourAnswers — FR-5.8. The owner's words: "i need a list with pending apps in the
// website to approve them and categorize them -> always free, daily limit, always blocked,
// individual limit".
func TestAnAppHasFourAnswers(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	baselineThenNew(h, f, pkgAlwaysFree, pkgDailyLimit, pkgOwnLimit, pkgForbidden, pkgUndecided)

	start, end := bedtimeAroundNow(t, "Europe/Zurich")
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"timezone": "Europe/Zurich", "allow_child_installs": false,
		"bedtime_enabled": false, "daily_limit_minutes": 0,
	})

	// Before any answer, all five wait. This is the state the owner found the phone in.
	waiting := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	for _, pkg := range []string{pkgAlwaysFree, pkgDailyLimit, pkgOwnLimit, pkgForbidden, pkgUndecided} {
		mustHave(t, waiting.PendingApproval, pkg,
			"with free installation off, an app installed since the baseline waits for a parent (FR-5.4)")
	}

	setRule(h, f, pkgAlwaysFree, "ALLOW", 0)
	setRule(h, f, pkgDailyLimit, "LIMIT", 0)
	setRule(h, f, pkgOwnLimit, "LIMIT", 30)
	setRule(h, f, pkgForbidden, "BLOCK", 0)

	answered := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	// All four answers end the wait — including LIMIT, which is the point of the action: a parent
	// saying "yes, and it is governed like everything else" previously had nothing to click.
	for _, pkg := range []string{pkgAlwaysFree, pkgDailyLimit, pkgOwnLimit, pkgForbidden} {
		mustNotHave(t, answered.PendingApproval, pkg, "an answered app must leave the queue")
	}
	// And the one nobody answered stays, which is what makes the four above a result rather than a
	// queue that empties itself.
	mustHave(t, answered.PendingApproval, pkgUndecided, "an unanswered app must still be waiting")

	mustNotHave(t, answered.SuspendedPackages, pkgAlwaysFree, "an approved app is usable")
	mustNotHave(t, answered.SuspendedPackages, pkgDailyLimit, "an approved app is usable")
	mustNotHave(t, answered.SuspendedPackages, pkgOwnLimit, "an approved app is usable")
	mustHave(t, answered.SuspendedPackages, pkgForbidden, "a blocked app is suspended (FR-5.2)")
	mustHave(t, answered.HiddenPackages, pkgForbidden, "a blocked app is hidden as well as suspended")

	// ---- the difference between "always free" and "approved" ----
	//
	// This is the distinction the old two-answer world could not express, and the reason a parent
	// could not approve an app without also exempting it from every schedule they had set.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"bedtime_enabled": true, "bedtime_start": start, "bedtime_end": end,
	})
	night := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	if night.SuspendReason != "BEDTIME" {
		t.Fatalf("the window %s–%s was supposed to contain this instant; the phone reads %q",
			start, end, night.SuspendReason)
	}
	mustNotHave(t, night.SuspendedPackages, pkgAlwaysFree,
		"'always free' means bedtime does not reach it (FR-5.5)")
	mustHave(t, night.SuspendedPackages, pkgDailyLimit,
		"'daily limit' means approved and still governed — bedtime must reach it")
	mustHave(t, night.SuspendedPackages, pkgOwnLimit,
		"an app with its own allowance is still governed by bedtime")

	// ---- an app's own allowance, spent, on a phone with screen time left ----
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"bedtime_enabled": false, "daily_limit_minutes": 240,
	})
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{
			pkgOwnLimit:   20 * 60 * 1000,
			pkgDailyLimit: 10 * 60 * 1000,
		},
	}).expect(http.StatusOK)
	short := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	mustNotHave(t, short.SuspendedPackages, pkgOwnLimit,
		"20 minutes of a 30 minute allowance is not spent")

	// Past the allowance. The shared quota still has 210 minutes in it, so nothing about the phone
	// changes — one app stops, which is exactly what "individual limit" has to mean.
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{
			pkgOwnLimit:   31 * 60 * 1000,
			pkgDailyLimit: 10 * 60 * 1000,
		},
	}).expect(http.StatusOK)
	spent := h.desiredState(f.parent.Token, f.device.ID, "")
	if spent.Desired.SuspendReason != "" {
		t.Fatalf("one app's allowance must not put the whole phone in a suspend state: %q",
			spent.Desired.SuspendReason)
	}
	if spent.Desired.RemainingMinutes <= 0 {
		t.Fatalf("the shared quota was supposed to have room left: %+v", spent.Desired)
	}
	mustHave(t, spent.Desired.SuspendedPackages, pkgOwnLimit,
		"31 minutes against a 30 minute allowance must stop that app (FR-5.8)")
	mustNotHave(t, spent.Desired.SuspendedPackages, pkgDailyLimit,
		"one app's spent allowance must not reach the app beside it")
	mustNotHave(t, spent.Desired.SuspendedPackages, pkgAlwaysFree,
		"one app's spent allowance must not reach an always-free app")

	// The phone has to be able to reach the same answer with no network, so both halves of that
	// arithmetic — the allowance and the minutes spent against it — must be in the Input it caches.
	var minutes int
	for _, l := range spent.Input.Settings.LimitedPackages {
		if l.PackageName == pkgOwnLimit {
			minutes = l.Minutes
		}
	}
	if minutes != 30 {
		t.Fatalf("the phone is handed %d minutes as %s's allowance, the parent set 30: %+v",
			minutes, pkgOwnLimit, spent.Input.Settings.LimitedPackages)
	}
	if got := spent.Input.UsedMinutesByPackage[pkgOwnLimit]; got != 31 {
		t.Fatalf("the phone is handed %d minutes of usage for %s, it filed 31", got, pkgOwnLimit)
	}

	// ---- reversibility: every answer can be taken back ----
	h.call(http.MethodDelete, "/children/"+f.child.ID+"/app-rules?package_name="+pkgOwnLimit,
		f.parent.Token, nil).expect(http.StatusNoContent)
	undone := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	mustHave(t, undone.PendingApproval, pkgOwnLimit,
		"removing the rule must put the app back in front of the parent, not leave it approved")
}

// TestUsageNamesTheAppsAParentRecognises — the other half of the owner's report: "i dont see which
// app was used how long today". The minutes were being recorded correctly the whole time; what the
// console received was a bare package id per row, so the list read as machine output.
func TestUsageNamesTheAppsAParentRecognises(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Brawl Stars"},
			{"package_name": "com.sec.android.app.launcher", "label": "One UI Home", "system_app": true},
		},
	}).expect(http.StatusOK)
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{
			pkgGame:                        42 * 60 * 1000,
			"com.sec.android.app.launcher": 7 * 60 * 1000,
			// A package with no inventory row: uninstalled since, or never reported. It must still
			// appear — dropping it would silently under-report the day — but with nothing invented
			// in place of the label it does not have.
			"com.example.gone": 3 * 60 * 1000,
		},
	}).expect(http.StatusOK)

	var usage struct {
		Minutes  int `json:"minutes"`
		Packages []struct {
			PackageName  string `json:"package_name"`
			ForegroundMs int64  `json:"foreground_ms"`
			Label        string `json:"label"`
			SystemApp    bool   `json:"system_app"`
		} `json:"packages"`
	}
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/usage", f.parent.Token, nil).
		expect(http.StatusOK).decode(&usage)
	if usage.Minutes != 52 {
		t.Fatalf("52 minutes of samples came back as %d", usage.Minutes)
	}
	if len(usage.Packages) != 3 {
		t.Fatalf("three packages were used, the console lists %d: %+v", len(usage.Packages), usage.Packages)
	}
	seen := map[string]struct {
		label  string
		system bool
		ms     int64
	}{}
	for _, p := range usage.Packages {
		seen[p.PackageName] = struct {
			label  string
			system bool
			ms     int64
		}{p.Label, p.SystemApp, p.ForegroundMs}
	}
	if got := seen[pkgGame]; got.label != "Brawl Stars" || got.system || got.ms != 42*60*1000 {
		t.Fatalf("the game reads as %+v; a parent has to see the name they know and the minutes it ran", got)
	}
	if got := seen["com.sec.android.app.launcher"]; got.label != "One UI Home" || !got.system {
		t.Fatalf("the launcher reads as %+v; the console needs the system flag to say so rather "+
			"than presenting it as an app the child chose", got)
	}
	if got := seen["com.example.gone"]; got.label != "" || got.ms != 3*60*1000 {
		t.Fatalf("a package with no inventory row reads as %+v; its minutes must survive and its "+
			"label must stay empty rather than being invented", got)
	}
}
