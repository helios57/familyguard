package policy

import (
	"slices"
	"testing"
)

// TestOnlyWhatTheLimitPausesCountsAgainstIt: a minute counts toward the daily limit only on an app
// the limit would pause. Until 0.6.38 every app's minutes counted, so on 2026-10-03 a child's
// 74 minutes of an "always free" game spent her whole 30-minute limit before she opened a single
// app the limit governs — and an app the parent had exempted on purpose locked every other one.
func TestOnlyWhatTheLimitPausesCountsAgainstIt(t *testing.T) {
	yes := true
	no := false
	s := baseSettings()
	s.BlockedPackages = []string{"com.example.blocked"}
	s.AllowedPackages = []string{"com.supercell.brawlstars", "com.example.allowed.notinstalled"}
	s.LimitedPackages = []AppLimit{{PackageName: "org.jellyfin.mobile"}, {PackageName: "com.example.own", Minutes: 20}}
	s.BonusPackages = []string{"com.example.bonus"}
	s.CountedSystemPackages = []string{"com.android.chrome"}
	in := Input{
		Settings: s,
		Installed: append(baseInstalled(),
			App{Package: "com.supercell.brawlstars", NewSinceBaseline: true},
			App{Package: "org.jellyfin.mobile", NewSinceBaseline: true},
			App{Package: "com.example.own", NewSinceBaseline: true},
			App{Package: "com.example.bonus", NewSinceBaseline: true},
			App{Package: "com.example.blocked"},
			App{Package: "com.example.pending", NewSinceBaseline: true},
			App{Package: "com.android.chrome", System: true, Launchable: &yes},
			App{Package: "com.sec.android.gallery3d", System: true, Launchable: &yes},
			App{Package: "com.example.service", System: true, Launchable: &no},
		),
		CriticalPackages: []string{"com.oem.dialer"},
		Now:              "2026-08-17T15:00:00+02:00",
	}
	got, err := LimitExemptPackages(in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(got) {
		t.Errorf("not sorted: %v", got)
	}
	for _, p := range append([]string{
		"com.supercell.brawlstars",         // the parent's ALLOW: "always free"
		"com.example.allowed.notinstalled", // a rule is a rule before the app arrives
		"com.sec.android.gallery3d",        // FR-5.10: preinstalled and free by default
		"com.example.service",              // nothing the child can open, nothing the limit pauses
		"com.oem.dialer",                   // the phone's own critical packages
	}, append(DefaultCriticalPackages, AlwaysUsablePackages...)...) {
		if !slices.Contains(got, p) {
			t.Errorf("%s is never paused by the daily limit, so its minutes must not count against it; exempt = %v", p, got)
		}
	}
	for _, p := range []string{
		"org.jellyfin.mobile",   // LIMIT: approved, and governed by the daily limit
		"com.example.own",       // LIMIT with an allowance of its own: still governed by the daily one
		"com.example.preloaded", // no rule, baseline: governed
		"com.example.bonus",     // runs on earned time; its minutes are paid from that
		"com.example.blocked",   // blocked: paused whatever
		"com.example.pending",   // waiting for approval
		"com.android.chrome",    // a preinstalled app that IS screen time
		"com.google.android.youtube",
	} {
		if slices.Contains(got, p) {
			t.Errorf("%s is paused by the daily limit, so its minutes count against it — yet it is exempt", p)
		}
	}
}

// TestWhatCountsDoesNotDependOnTheMoment: whether an app's minute counts is a property of the rules,
// not of the hour or of how much is left. A definition read off the current state would make the
// same minute count at 15:00 and not at 22:00, or flip the moment the limit is reached.
func TestWhatCountsDoesNotDependOnTheMoment(t *testing.T) {
	for _, v := range loadVectors(t) {
		want, err := LimitExemptPackages(v.Input)
		if err != nil {
			continue // a vector that is invalid input on purpose
		}
		for _, mutate := range []func(*Input){
			func(in *Input) { in.UsedMinutesToday = 0 },
			func(in *Input) { in.UsedMinutesToday = 100000 },
			func(in *Input) { in.Settings.Paused = !in.Settings.Paused },
			func(in *Input) { in.Settings.BedtimeEnabled = !in.Settings.BedtimeEnabled },
			func(in *Input) { in.Settings.EarnedAvailableMinutes = 120 },
			func(in *Input) { in.Settings.DailyLimitMinutes = 0 },
			func(in *Input) { in.Settings.TrackingOnly = !in.Settings.TrackingOnly },
			func(in *Input) { in.ParentLock = !in.ParentLock },
		} {
			in := v.Input
			in.Settings.AllowedPackages = slices.Clone(in.Settings.AllowedPackages)
			mutate(&in)
			got, err := LimitExemptPackages(in)
			if err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: what counts changed with the moment:\n  was %v\n  now %v", v.Name, want, got)
			}
		}
	}
}
