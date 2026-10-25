package e2e

// FR-30 against the real server: the family's learning prefixes, a phone's learning minutes turning
// into Bonuszeit, the balance a phone is sent leaving its own today out (the phone adds its own live
// count), the daily-limit switch that makes 0 a real limit, and the migration that gave every
// existing policy its old meaning. The prefix is never the family's real one: tests use
// org.example.learn.

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

const (
	learnPrefix  = "org.example.learn."
	pkgLetters   = "org.example.learn.letters"
	pkgNumbers   = "org.example.learn.numbers"
	minuteMillis = 60000
)

type learningPrefixesDTO struct {
	Prefixes []string       `json:"prefixes"`
	Matches  map[string]int `json:"matches"`
}

func (h *harness) learningPrefixes(token string) learningPrefixesDTO {
	h.t.Helper()
	var out learningPrefixesDTO
	h.call(http.MethodGet, "/family/learning-prefixes", token, nil).expect(http.StatusOK).decode(&out)
	return out
}

func (h *harness) putLearningPrefixes(token string, prefixes ...string) apiResponse {
	h.t.Helper()
	return h.call(http.MethodPut, "/family/learning-prefixes", token, map[string]any{"prefixes": prefixes})
}

// learningFixture is a phone with two learning apps and a film app, and the family's prefix set.
func learningFixture(t *testing.T, h *harness) fixture {
	t.Helper()
	f := enrolledFixture(t, h)
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{"apps": []map[string]any{
		{"package_name": pkgLetters, "label": "Buchstaben", "launchable": true},
		{"package_name": pkgNumbers, "label": "Zahlen", "launchable": true},
		{"package_name": pkgMovies, "label": "Filme", "launchable": true},
	}}).expect(http.StatusOK)
	h.putLearningPrefixes(f.parent.Token, learnPrefix).expect(http.StatusOK)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{
		"daily_limit_enabled": true, "daily_limit_minutes": 0, "timezone": "Europe/Zurich",
	})
	return f
}

type learnedTodayDTO struct {
	todayDTO
	LearnedToday int `json:"learned_today_minutes"`
}

func (h *harness) learnedToday(token, childID string) learnedTodayDTO {
	h.t.Helper()
	var out learnedTodayDTO
	h.call(http.MethodGet, "/children/"+childID+"/today", token, nil).expect(http.StatusOK).decode(&out)
	return out
}

func TestLearningPrefixesAdminOnlyAndValidated(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	// Empty until a parent says otherwise: no family has learning apps it did not choose.
	if got := h.learningPrefixes(f.parent.Token); len(got.Prefixes) != 0 || got.Prefixes == nil {
		t.Fatalf("a new family's learning prefixes are %#v, want an empty list", got.Prefixes)
	}

	for _, bad := range [][]string{
		{"1example."},
		{"org..example."},
		{"org.example.*"},
		{"org.example learn."},
		{""},
		{"o" + strings.Repeat("x", 100)},
		{"a.", "b.", "c.", "d.", "e.", "f.", "g.", "h.", "i.", "j.", "k."},
	} {
		h.putLearningPrefixes(f.parent.Token, bad...).expectError(http.StatusBadRequest, "invalid_input")
	}

	var put learningPrefixesDTO
	h.putLearningPrefixes(f.parent.Token, learnPrefix, "  "+learnPrefix+" ", "org.example.other").
		expect(http.StatusOK).decode(&put)
	if !slices.Equal(put.Prefixes, []string{learnPrefix, "org.example.other"}) {
		t.Fatalf("stored %v, want the two distinct prefixes, trimmed, in order", put.Prefixes)
	}

	// The count beside each prefix is what makes a typo visible: it reads 0.
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{"apps": []map[string]any{
		{"package_name": pkgLetters, "label": "Buchstaben", "launchable": true},
		{"package_name": pkgNumbers, "label": "Zahlen", "launchable": true},
		{"package_name": pkgMovies, "label": "Filme", "launchable": true},
	}}).expect(http.StatusOK)
	got := h.learningPrefixes(f.parent.Token)
	if got.Matches[learnPrefix] != 2 || got.Matches["org.example.other"] != 0 {
		t.Errorf("matches %v, want 2 for %s and 0 for the prefix nothing has", got.Matches, learnPrefix)
	}

	// The change reaches the phone with its next policy.
	_, settings := h.phoneSees(f.deviceToken())
	if !strings.Contains(string(settings["learning_prefixes"]), learnPrefix) {
		t.Errorf("the phone's settings carry learning_prefixes=%s", settings["learning_prefixes"])
	}

	// A guardian neither reads nor changes it: the guardian window lists no apps (FR-20.1).
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)
	h.call(http.MethodGet, "/family/learning-prefixes", guardian.Token, nil).expectError(http.StatusForbidden, "forbidden")
	h.putLearningPrefixes(guardian.Token).expectError(http.StatusForbidden, "forbidden")

	// Emptying it is a change like any other.
	h.putLearningPrefixes(f.parent.Token).expect(http.StatusOK)
	if got := h.learningPrefixes(f.parent.Token); len(got.Prefixes) != 0 {
		t.Errorf("after emptying, the prefixes are %v", got.Prefixes)
	}

	audited := 0
	for _, e := range h.readAudit(f.parent.Token) {
		if e.Action == "LEARNING_APPS_UPDATED" {
			audited++
		}
	}
	if audited != 2 {
		t.Errorf("%d LEARNING_APPS_UPDATED rows for two changes", audited)
	}
}

func TestLearningUsageEarnsBonuszeit(t *testing.T) {
	h := newHarness(t)
	f := learningFixture(t, h)

	report := func(samples, learning map[string]int64) {
		t.Helper()
		h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
			"samples": samples, "learning": learning,
		}).expect(http.StatusOK)
	}
	// 7.5 minutes over two apps: the day is floored, not each app.
	report(map[string]int64{pkgLetters: 4*minuteMillis + 15000, pkgNumbers: 3*minuteMillis + 15000},
		map[string]int64{pkgLetters: 4*minuteMillis + 15000, pkgNumbers: 3*minuteMillis + 15000})
	d := h.learnedToday(f.parent.Token, f.child.ID)
	if d.LearnedToday != 7 || d.Earned.Available != 7 || d.Earned.Left != 7 {
		t.Fatalf("7.5 learning minutes read as learned %d, available %d, left %d; want 7 each",
			d.LearnedToday, d.Earned.Available, d.Earned.Left)
	}

	// The same report again, and a smaller one, change nothing: the figure is cumulative.
	report(map[string]int64{pkgLetters: 4*minuteMillis + 15000, pkgNumbers: 3*minuteMillis + 15000},
		map[string]int64{pkgLetters: 4*minuteMillis + 15000, pkgNumbers: 3*minuteMillis + 15000})
	report(map[string]int64{pkgLetters: 1 * minuteMillis}, map[string]int64{pkgLetters: 1 * minuteMillis})
	if d := h.learnedToday(f.parent.Token, f.child.ID); d.LearnedToday != 7 || d.Earned.Available != 7 {
		t.Errorf("after re-sent and smaller reports: learned %d, available %d; want 7, 7", d.LearnedToday, d.Earned.Available)
	}

	// Learning can never be more than the time the app was in front: a phone claiming an hour of
	// learning on a minute of use earns the minute.
	report(map[string]int64{pkgMovies: 1 * minuteMillis}, map[string]int64{pkgMovies: 60 * minuteMillis})
	if d := h.learnedToday(f.parent.Token, f.child.ID); d.LearnedToday != 8 {
		t.Errorf("a minute of film reported as an hour of learning gives learned %d; want 8 (7 + the one minute)", d.LearnedToday)
	}
}

func TestDesiredStateLeavesOutTheDevicesOwnTodayCredit(t *testing.T) {
	h := newHarness(t)
	f := learningFixture(t, h)

	// Before any learning, with the limit on at 0, the film is paused and the learning apps are not.
	phone, _ := h.phoneSees(f.deviceToken())
	if !slices.Contains(phone.SuspendedPackages, pkgMovies) || slices.Contains(phone.SuspendedPackages, pkgLetters) {
		t.Fatalf("at a limit of 0 with nothing learned: suspended %v; want the film and not the learning app",
			phone.SuspendedPackages)
	}

	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{pkgLetters: 10 * minuteMillis}, "learning": map[string]int64{pkgLetters: 10 * minuteMillis},
	}).expect(http.StatusOK)

	var out struct {
		Desired struct {
			desiredStateDTO
			LearningPackages []string `json:"learning_packages"`
		} `json:"desired"`
		Input struct {
			Settings struct {
				EarnedAvailable int      `json:"earned_available_minutes"`
				LearningPrefix  []string `json:"learning_prefixes"`
			} `json:"settings"`
			LearningMinutesToday int `json:"learning_minutes_today"`
		} `json:"input"`
	}
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&out)
	// The phone adds its own live count; the server's balance must not already hold it, or the
	// phone would count those ten minutes twice.
	if out.Input.Settings.EarnedAvailable != 0 || out.Input.LearningMinutesToday != 10 {
		t.Errorf("the phone is sent a balance of %d and %d learned today; want 0 and 10",
			out.Input.Settings.EarnedAvailable, out.Input.LearningMinutesToday)
	}
	if slices.Contains(out.Desired.SuspendedPackages, pkgMovies) {
		t.Error("ten learned minutes leave the film paused")
	}
	if !slices.Equal(out.Desired.LearningPackages, []string{pkgLetters, pkgNumbers}) {
		t.Errorf("learning_packages %v", out.Desired.LearningPackages)
	}

	// A second phone of the same child sees the first one's minutes as balance, and none of its own.
	second := h.newDevice(f.parent.Token, f.child.ID, "Mira's tablet")
	_, token := h.provision(f.parent.Token, second.ID)
	tablet := h.enrollDevice(token, "Tab", "Android 14", []string{oemDialer})
	settings := struct {
		Input struct {
			Settings struct {
				EarnedAvailable int `json:"earned_available_minutes"`
			} `json:"settings"`
			LearningMinutesToday int `json:"learning_minutes_today"`
		} `json:"input"`
	}{}
	h.call(http.MethodGet, "/device/policy", tablet.DeviceToken, nil).expect(http.StatusOK).decode(&settings)
	if settings.Input.Settings.EarnedAvailable != 10 || settings.Input.LearningMinutesToday != 0 {
		t.Errorf("the second phone is sent a balance of %d and %d learned; want 10 and 0",
			settings.Input.Settings.EarnedAvailable, settings.Input.LearningMinutesToday)
	}
	// The console's view excludes nothing.
	if d := h.learnedToday(f.parent.Token, f.child.ID); d.Earned.Available != 10 {
		t.Errorf("the console's balance is %d, want 10", d.Earned.Available)
	}
}

func TestDailyLimitSwitch(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	type switchDTO struct {
		Minutes int  `json:"daily_limit_minutes"`
		Enabled bool `json:"daily_limit_enabled"`
	}
	patch := func(body map[string]any) switchDTO {
		t.Helper()
		var out switchDTO
		h.call(http.MethodPatch, "/children/"+f.child.ID+"/policy", f.parent.Token, body).expect(http.StatusOK).decode(&out)
		return out
	}

	// An older console sends minutes only, and keeps the meaning it always had.
	if got := patch(map[string]any{"daily_limit_minutes": 30}); !got.Enabled || got.Minutes != 30 {
		t.Errorf("minutes 30 alone: %+v, want on at 30", got)
	}
	if got := patch(map[string]any{"daily_limit_minutes": 0}); got.Enabled {
		t.Errorf("minutes 0 alone: %+v, want off — an old console must not lock a phone", got)
	}
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": 15}).
		expectError(http.StatusConflict, "no_daily_limit")

	// Only the switch says "on, 0 minutes".
	if got := patch(map[string]any{"daily_limit_enabled": true, "daily_limit_minutes": 0}); !got.Enabled || got.Minutes != 0 {
		t.Fatalf("switch on at 0: %+v", got)
	}
	phone, _ := h.phoneSees(f.deviceToken())
	if phone.SuspendReason != "QUOTA" {
		t.Errorf("on at 0 with nothing earned: reason %q, want QUOTA", phone.SuspendReason)
	}
	var var1 struct {
		LimitToday int `json:"limit_today_minutes"`
	}
	h.call(http.MethodPost, "/children/"+f.child.ID+"/bonus", f.parent.Token, map[string]any{"minutes": 15}).
		expect(http.StatusOK).decode(&var1)
	if var1.LimitToday != 15 {
		t.Errorf("Extrazeit at a limit of 0 gives limit_today %d, want 15", var1.LimitToday)
	}
	if phone, _ := h.phoneSees(f.deviceToken()); phone.SuspendReason != "" {
		t.Errorf("with 15 minutes of Extrazeit the reason is still %q", phone.SuspendReason)
	}
	// The child may ask for more time whenever the switch is on (FR-28.1).
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 15}).expect(http.StatusOK)

	// The switch alone keeps the minutes; switching it off is "Kein Tageslimit".
	if got := patch(map[string]any{"daily_limit_enabled": false}); got.Enabled || got.Minutes != 0 {
		t.Errorf("switch off: %+v", got)
	}
	if got := patch(map[string]any{"daily_limit_minutes": 45, "daily_limit_enabled": false}); got.Enabled || got.Minutes != 45 {
		t.Errorf("minutes 45 with the switch off: %+v, want off at 45 — the switch wins", got)
	}
	if phone, _ := h.phoneSees(f.deviceToken()); phone.SuspendReason != "" {
		t.Errorf("with the switch off the reason is %q", phone.SuspendReason)
	}
}

// The migration is the one place every existing family's limit was reinterpreted. Re-run on rows
// written the old way, it must give a limit exactly to the policies whose minutes were above 0.
func TestMigrationKeepsZeroAsNoLimit(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	none := h.newChild(parent.Token, "Ohne")
	some := h.newChild(parent.Token, "Mit")
	h.patchPolicy(parent.Token, some.ID, map[string]any{"daily_limit_minutes": 45})
	h.patchPolicy(parent.Token, none.ID, map[string]any{"daily_limit_enabled": true, "daily_limit_minutes": 0})

	// Back to the schema before 0030, as a database that never had it.
	h.stop()
	h.fixture(`ALTER TABLE families DROP COLUMN learning_prefixes;
	           ALTER TABLE policies DROP COLUMN daily_limit_enabled;
	           ALTER TABLE day_limits DROP COLUMN limit_enabled;
	           ALTER TABLE usage_samples DROP COLUMN learning_ms;
	           DELETE FROM schema_migrations WHERE name LIKE '0030_%';`)
	h.start()

	var got struct {
		Enabled bool `json:"daily_limit_enabled"`
	}
	h.call(http.MethodGet, "/children/"+some.ID+"/policy", parent.Token, nil).expect(http.StatusOK).decode(&got)
	if !got.Enabled {
		t.Error("a policy with 45 minutes came out of the migration with no limit")
	}
	h.call(http.MethodGet, "/children/"+none.ID+"/policy", parent.Token, nil).expect(http.StatusOK).decode(&got)
	if got.Enabled {
		t.Error("a policy with 0 minutes came out of the migration with a limit of 0: every phone of it would lock")
	}
}

// FR-30.6: "on, 0 minutes" is a limit only to a phone from 0.6.45 (build 54) on. An older phone reads
// daily_limit_minutes 0 as no limit at all, so setting it while one of the child's phones still runs
// such a build would give that child unlimited time while the console says 0 minutes. The server
// refuses that, names the phone, and lets every other limit through.
func TestOnAtZeroIsRefusedWhileAPhoneRunsABuildThatReadsZeroAsNoLimit(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	beat := func(code int) {
		t.Helper()
		h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
			"connectivity": "wifi", "app_version_name": "0.6.44", "app_version_code": code,
		}).expect(http.StatusOK)
	}
	onAtZero := map[string]any{"daily_limit_enabled": true, "daily_limit_minutes": 0}

	beat(53)
	before := h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60})
	r := h.call(http.MethodPatch, "/children/"+f.child.ID+"/policy", f.parent.Token, onAtZero).
		expectError(http.StatusConflict, "devices_outdated")
	if !strings.Contains(string(r.Body), f.device.Name) || !strings.Contains(string(r.Body), "0.6.45") {
		t.Errorf("the refusal does not name the phone %q and the build it needs: %s", f.device.Name, r.Body)
	}
	var after policyDTO
	h.call(http.MethodGet, "/children/"+f.child.ID+"/policy", f.parent.Token, nil).expect(http.StatusOK).decode(&after)
	if after.DailyLimitMinutes != 60 || after.Version != before.Version {
		t.Errorf("a refused change was stored: minutes %d version %d, was 60 version %d",
			after.DailyLimitMinutes, after.Version, before.Version)
	}

	// Everything an old phone reads the same way still goes through: a positive limit, and 0 with
	// the switch off, which is "no limit" on every build.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 30})
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_enabled": false, "daily_limit_minutes": 0})

	beat(54)
	h.patchPolicy(f.parent.Token, f.child.ID, onAtZero)
}
