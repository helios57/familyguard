package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/helios57/familyguard/backend/internal/store"
)

type vector struct {
	Name         string                     `json:"name"`
	Requirements []string                   `json:"requirements"`
	Input        Input                      `json:"input"`
	Expect       map[string]json.RawMessage `json:"expect"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	// Vectors is embedded, so it cannot be missing at run time and a change to it invalidates the
	// test cache. Reading it from disk here is what let a stale pass survive an edit.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(Vectors, &top); err != nil {
		t.Fatalf("shared vector file is not valid JSON: %v", err)
	}
	list, ok := top["vectors"]
	if !ok {
		t.Fatal(`shared vector file has no "vectors" key`)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(list, &items); err != nil {
		t.Fatalf(`"vectors" is not an array: %v`, err)
	}
	if len(items) == 0 {
		t.Fatal("the shared vector file contains no vectors")
	}
	out := make([]vector, 0, len(items))
	for i, item := range items {
		dec := json.NewDecoder(bytes.NewReader(item))
		// A misspelled input key would otherwise be dropped in silence, and the vector would then
		// test the default value instead of the one that was written down.
		dec.DisallowUnknownFields()
		var v vector
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("vector %d is malformed: %v", i, err)
		}
		if v.Name == "" {
			t.Fatalf("vector %d has no name", i)
		}
		out = append(out, v)
	}
	return out
}

// TestSharedVectors is the anti-drift control: the same file is replayed by the Kotlin engine, so a
// change that makes one side pass and the other fail is a red build rather than a phone that
// disagrees with the console.
func TestSharedVectors(t *testing.T) {
	vectors := loadVectors(t)
	seen := map[string]bool{}

	for _, v := range vectors {
		if seen[v.Name] {
			t.Fatalf("duplicate vector name %q — a rename would silently drop a case", v.Name)
		}
		seen[v.Name] = true

		t.Run(v.Name, func(t *testing.T) {
			got, err := Compute(v.Input)
			if err != nil {
				t.Fatalf("Compute failed: %v", err)
			}
			actual, err := toFieldMap(got)
			if err != nil {
				t.Fatalf("cannot serialise the result: %v", err)
			}
			if len(v.Expect) == 0 {
				t.Fatal("vector asserts nothing")
			}
			for key, want := range v.Expect {
				have, ok := actual[key]
				if !ok {
					t.Fatalf("expectation names %q, which is not a field of DesiredState", key)
				}
				if !jsonEqual(t, want, have) {
					t.Fatalf("%s:\n  want %s\n  got  %s", key, want, have)
				}
			}
		})
	}
}

// TestVectorsCoverTheEnforcementRequirements is a coverage guard rather than a count pin: a count
// stays green when a case is replaced by an unrelated one, whereas this fails by name.
func TestVectorsCoverTheEnforcementRequirements(t *testing.T) {
	must := []string{
		"FR-3.4",  // daily limit
		"FR-4.1",  // bedtime window, may cross midnight
		"FR-4.2",  // leaving the window un-suspends
		"FR-5.2",  // blocked app suspended and hidden
		"FR-5.3",  // free-installation mode
		"FR-5.4",  // new app waits for approval
		"FR-5.5",  // critical whitelist
		"FR-6.2",  // per-child filtering endpoint
		"FR-6.4",  // domain removal restores access
		"FR-7.1",  // youtube apps
		"FR-7.2",  // youtube dns
		"FR-7.3",  // youtube browser blocklist
		"FR-7.4",  // the inverse lifts all of it
		"FR-8",    // tracking-only
		"FR-16.3", // the declared set of managed apps reaches the device
		"FR-18.1", // the family blocklist reaches every child
		"FR-18.2", // it covers a package that is not installed yet
		"FR-18.3", // a child ALLOW is the exemption; a child BLOCK is not overruled
		"FR-18.4", // the critical whitelist outranks it
		"NFR-6",   // unbrickable
	}
	covered := map[string]bool{}
	for _, v := range loadVectors(t) {
		for _, r := range v.Requirements {
			covered[r] = true
		}
	}
	var missing []string
	for _, r := range must {
		if !covered[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("no shared vector covers %v", missing)
	}
}

// ---- properties that must hold for every input, not just the vectors --------

// baseSettings is a policy with every control switched on, which is the state most likely to
// produce a brick.
func baseSettings() Settings {
	return Settings{
		TrackingOnly:       false,
		AllowChildInstalls: false,
		YouTubeBlocked:     true,
		DailyLimitMinutes:  30,
		BedtimeEnabled:     true,
		BedtimeStart:       "21:00",
		BedtimeEnd:         "07:00",
		DNSHost:            "family.adguard-dns.com",
		Timezone:           "Europe/Zurich",
		Version:            3,
		BlockedPackages:    append([]string{"com.example.game"}, DefaultCriticalPackages...),
		BlockedDomains:     []string{"bad.example.com"},
	}
}

func baseInstalled() []App {
	apps := []App{
		{Package: "com.example.game", NewSinceBaseline: true},
		{Package: "com.example.preloaded"},
		{Package: "com.google.android.youtube"},
	}
	for _, p := range DefaultCriticalPackages {
		apps = append(apps, App{Package: p, System: true})
	}
	return apps
}

// TestCriticalPackagesAreNeverSuspended sweeps every combination of the toggles, at four times of
// day, and asserts FR-5.5 for all of them. The whitelist being applied inside one branch instead of
// after all of them is the defect this catches.
func TestCriticalPackagesAreNeverSuspended(t *testing.T) {
	times := []string{
		"2026-08-17T03:00:00+02:00", // deep in the bedtime window
		"2026-08-17T08:00:00+02:00", // outside it
		"2026-08-17T21:00:00+02:00", // the first minute of it
		"2026-08-17T20:59:00+02:00", // the last minute before it
	}
	combos := 0
	for _, now := range times {
		for _, tracking := range []bool{false, true} {
			for _, lock := range []bool{false, true} {
				for _, used := range []int{0, 500} {
					s := baseSettings()
					s.TrackingOnly = tracking
					in := Input{
						Settings:         s,
						Installed:        baseInstalled(),
						UsedMinutesToday: used,
						ParentLock:       lock,
						CriticalPackages: []string{"com.oem.dialer"},
						Now:              now,
					}
					got, err := Compute(in)
					if err != nil {
						t.Fatalf("Compute failed: %v", err)
					}
					combos++
					for _, p := range append(append([]string{}, DefaultCriticalPackages...), "com.oem.dialer") {
						if contains(got.SuspendedPackages, p) {
							t.Fatalf("%s suspended %s (tracking=%v lock=%v used=%d)", now, p, tracking, lock, used)
						}
						if contains(got.HiddenPackages, p) {
							t.Fatalf("%s hid %s (tracking=%v lock=%v used=%d)", now, p, tracking, lock, used)
						}
						if contains(got.PendingApproval, p) {
							t.Fatalf("%s queued %s for approval (tracking=%v)", now, p, tracking)
						}
					}
				}
			}
		}
	}
	if combos != len(times)*2*2*2 {
		t.Fatalf("the sweep ran %d combinations, not %d — it is not covering what it claims", combos, len(times)*8)
	}
}

// TestEveryStateIsReversible: FR-4.2 and FR-7.4 both say the same thing in different words — no
// output may be one-way. Turning a control off must return the state to exactly what it was before
// it went on, byte for byte.
func TestEveryStateIsReversible(t *testing.T) {
	const now = "2026-08-17T15:00:00+02:00"
	off := Settings{
		AllowChildInstalls: true,
		BedtimeStart:       "21:00",
		BedtimeEnd:         "07:00",
		DNSHost:            "family.adguard-dns.com",
		Timezone:           "Europe/Zurich",
		Version:            1,
	}
	installed := []App{
		{Package: "com.example.game", NewSinceBaseline: true},
		{Package: "com.google.android.youtube"},
		{Package: "com.android.dialer", System: true},
	}
	mk := func(s Settings, used int, lock bool) DesiredState {
		t.Helper()
		got, err := Compute(Input{Settings: s, Installed: installed, UsedMinutesToday: used, ParentLock: lock, Now: now})
		if err != nil {
			t.Fatalf("Compute failed: %v", err)
		}
		return got
	}

	baseline := mk(off, 0, false)

	toggles := []struct {
		name string
		on   func(*Settings)
		used int
		lock bool
	}{
		{"youtube killswitch", func(s *Settings) { s.YouTubeBlocked = true }, 0, false},
		{"app block rule", func(s *Settings) { s.BlockedPackages = []string{"com.example.game"} }, 0, false},
		{"custom blocked domain", func(s *Settings) { s.BlockedDomains = []string{"bad.example.com"} }, 0, false},
		{"free-installation off", func(s *Settings) { s.AllowChildInstalls = false }, 0, false},
		{"daily quota reached", func(s *Settings) { s.DailyLimitMinutes = 30 }, 45, false},
		{"parent lock", func(s *Settings) {}, 0, true},
	}

	for _, tc := range toggles {
		t.Run(tc.name, func(t *testing.T) {
			on := off
			tc.on(&on)
			enabled := mk(on, tc.used, tc.lock)
			if reflect.DeepEqual(enabled, baseline) {
				t.Fatal("turning the control on changed nothing — this case would prove nothing about turning it off")
			}
			restored := mk(off, 0, false)
			if !reflect.DeepEqual(restored, baseline) {
				t.Fatalf("state is one-way:\n  before %+v\n  after  %+v", baseline, restored)
			}
		})
	}

	// Bedtime is reversible in time rather than in configuration, so it is checked by moving the
	// clock rather than by flipping a flag.
	t.Run("bedtime window", func(t *testing.T) {
		s := off
		s.BedtimeEnabled = true
		before, err := Compute(Input{Settings: s, Installed: installed, Now: "2026-08-17T20:00:00+02:00"})
		if err != nil {
			t.Fatal(err)
		}
		during, err := Compute(Input{Settings: s, Installed: installed, Now: "2026-08-17T22:00:00+02:00"})
		if err != nil {
			t.Fatal(err)
		}
		after, err := Compute(Input{Settings: s, Installed: installed, Now: "2026-08-18T08:00:00+02:00"})
		if err != nil {
			t.Fatal(err)
		}
		if during.SuspendReason != ReasonBedtime || len(during.SuspendedPackages) == 0 {
			t.Fatalf("bedtime did not suspend anything: %+v", during)
		}
		if after.SuspendReason != ReasonNone || len(after.SuspendedPackages) != 0 {
			t.Fatalf("leaving the window did not un-suspend: %+v", after)
		}
		before.NextChangeAt, after.NextChangeAt = "", "" // the alarm legitimately differs by a day
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("the state after the window differs from the state before it:\n  %+v\n  %+v", before, after)
		}
	})
}

// TestHiddenPackagesAreAlsoSuspended: FR-5.2 says blocked apps are suspended *and* hidden. A hidden
// app that is not suspended is still runnable from a launcher shortcut.
func TestHiddenPackagesAreAlsoSuspended(t *testing.T) {
	for _, now := range []string{"2026-08-17T03:00:00+02:00", "2026-08-17T15:00:00+02:00"} {
		got, err := Compute(Input{
			Settings:  baseSettings(),
			Installed: baseInstalled(),
			Now:       now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.HiddenPackages) == 0 {
			t.Fatal("nothing was hidden, so this assertion would hold vacuously")
		}
		for _, p := range got.HiddenPackages {
			if !contains(got.SuspendedPackages, p) {
				t.Fatalf("%s is hidden but not suspended", p)
			}
		}
	}
}

// TestNoRestrictionCanBlockCallingOrRecovery guards NFR-6 against a future edit that adds a
// restriction because it sounds strict.
func TestNoRestrictionCanBlockCallingOrRecovery(t *testing.T) {
	for _, tracking := range []bool{false, true} {
		for _, installs := range []bool{false, true} {
			s := baseSettings()
			s.TrackingOnly = tracking
			s.AllowChildInstalls = installs
			got, err := Compute(Input{Settings: s, Installed: baseInstalled(), UsedMinutesToday: 999, ParentLock: true, Now: "2026-08-17T03:00:00+02:00"})
			if err != nil {
				t.Fatal(err)
			}
			// Calibration: the loop below proves nothing if there is nothing to inspect. An engine
			// that emitted no restrictions at all would satisfy every assertion in this test.
			if len(got.UserRestrictions) == 0 {
				t.Fatalf("no restrictions were emitted at all (tracking=%v installs=%v), so this test is vacuous",
					tracking, installs)
			}
			for _, r := range got.UserRestrictions {
				if forbiddenRestrictions[r] {
					t.Fatalf("restriction %q would break emergency use", r)
				}
			}
			// Named separately from the map membership above: the requirement is that a parent can
			// always wipe the phone from the recovery menu, and a requirement deserves an assertion
			// that says its own name.
			if contains(got.UserRestrictions, RestrictionFactoryReset) {
				t.Fatalf("factory reset is blocked (tracking=%v installs=%v); the phone would then be "+
					"recoverable only through this control plane", tracking, installs)
			}
			if !sort.StringsAreSorted(got.UserRestrictions) {
				t.Fatalf("restrictions are not sorted, so two engines cannot compare: %v", got.UserRestrictions)
			}
		}
	}
}

// TestTrackingOnlyKeepsFilteringAndHardening is FR-8's exact wording, split into the half that must
// stop and the half that must continue.
func TestTrackingOnlyKeepsFilteringAndHardening(t *testing.T) {
	s := baseSettings()
	s.TrackingOnly = true
	got, err := Compute(Input{Settings: s, Installed: baseInstalled(), UsedMinutesToday: 999, Now: "2026-08-17T03:00:00+02:00"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SuspendReason != ReasonNone || len(got.SuspendedPackages) != 0 || len(got.HiddenPackages) != 0 {
		t.Fatalf("tracking-only enforced something: %+v", got)
	}
	if got.PrivateDNSHost == "" || !got.SafeSearch || !got.YouTubeRestrictedMode {
		t.Fatalf("tracking-only switched filtering off, which FR-8 says stays on: %+v", got)
	}
	if !contains(got.BlockedDomains, "youtube.com") {
		t.Fatal("tracking-only dropped the youtube domain block, which is filtering, not enforcement")
	}
	if len(got.UserRestrictions) == 0 {
		t.Fatal("tracking-only switched hardening off, which FR-8 says stays on")
	}
	if got.UsedMinutes != 999 {
		t.Fatalf("tracking-only stopped measuring: %d", got.UsedMinutes)
	}
	// Negative control: the same input without tracking-only must enforce, otherwise the assertions
	// above would hold for a reason unrelated to the flag.
	s.TrackingOnly = false
	enforced, err := Compute(Input{Settings: s, Installed: baseInstalled(), UsedMinutesToday: 999, Now: "2026-08-17T03:00:00+02:00"})
	if err != nil {
		t.Fatal(err)
	}
	if enforced.SuspendReason == ReasonNone || len(enforced.SuspendedPackages) == 0 {
		t.Fatalf("the same input without tracking-only enforced nothing either: %+v", enforced)
	}
}

func TestComputeRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		mutow func(*Input)
	}{
		{"unknown timezone", func(in *Input) { in.Settings.Timezone = "Mars/Olympus" }},
		{"empty timezone", func(in *Input) { in.Settings.Timezone = "" }},
		{"now is not a timestamp", func(in *Input) { in.Now = "yesterday" }},
		{"now is empty", func(in *Input) { in.Now = "" }},
		{"bedtime start is nonsense", func(in *Input) { in.Settings.BedtimeEnabled = true; in.Settings.BedtimeStart = "9pm" }},
		{"bedtime end is out of range", func(in *Input) { in.Settings.BedtimeEnabled = true; in.Settings.BedtimeEnd = "25:00" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Settings: baseSettings(), Installed: baseInstalled(), Now: "2026-08-17T15:00:00+02:00"}
			tc.mutow(&in)
			got, err := Compute(in)
			if err == nil {
				t.Fatalf("accepted invalid input and produced %+v", got)
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error is not ErrInvalidInput, so a caller cannot distinguish it: %v", err)
			}
			if !reflect.DeepEqual(got, DesiredState{}) {
				t.Fatalf("a partial state was returned alongside the error: %+v", got)
			}
		})
	}
}

// TestNextChangeAtIsInTheFuture: the device sets an exact alarm for this instant. A value in the
// past is an alarm that fires immediately and forever.
func TestNextChangeAtIsInTheFuture(t *testing.T) {
	for _, now := range []string{
		"2026-08-17T06:59:00+02:00",
		"2026-08-17T07:00:00+02:00",
		"2026-08-17T20:59:59+02:00",
		"2026-08-17T21:00:00+02:00",
		"2026-08-17T23:59:59+02:00",
		"2026-08-18T00:00:00+02:00",
	} {
		in := Input{Settings: baseSettings(), Installed: baseInstalled(), Now: now}
		got, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got.NextChangeAt == "" {
			t.Fatalf("%s: no next change scheduled although bedtime and a quota are set", now)
		}
		next, err := time.Parse(time.RFC3339, got.NextChangeAt)
		if err != nil {
			t.Fatalf("%s: next_change_at is not RFC 3339: %q", now, got.NextChangeAt)
		}
		cur, _ := time.Parse(time.RFC3339, now)
		if !next.After(cur) {
			t.Fatalf("%s: next change %s is not in the future", now, got.NextChangeAt)
		}
		if next.Sub(cur) > 24*time.Hour {
			t.Fatalf("%s: next change %s is more than a day away", now, got.NextChangeAt)
		}
	}
}

// TestNormalizeDomainMatchesTheStore guards the one duplicated function in this package. If the two
// diverge, a parent removes a domain in the console and the device keeps blocking it.
func TestNormalizeDomainMatchesTheStore(t *testing.T) {
	for _, in := range []string{
		"Example.COM", "example.com.", "https://Example.com/path?q=1", "http://a.b.c",
		"  spaced.example  ", "already.normal", "UPPER.EXAMPLE.CH.",
	} {
		if got, want := normalizeDomain(in), store.NormalizeDomain(in); got != want {
			t.Fatalf("normalizeDomain(%q) = %q, store returns %q", in, got, want)
		}
	}
}

// TestOutputSlicesAreNeverNil: a nil slice marshals to null, and the Kotlin side compares against
// an empty list. Two engines that agree must serialise identically.
func TestOutputSlicesAreNeverNil(t *testing.T) {
	for _, tracking := range []bool{false, true} {
		s := Settings{AllowChildInstalls: true, DNSHost: "d", Timezone: "UTC", TrackingOnly: tracking}
		got, err := Compute(Input{Settings: s, Now: "2026-08-17T15:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"suspended_packages", "hidden_packages", "pending_approval", "blocked_domains", "user_restrictions"} {
			if string(m[k]) == "null" {
				t.Fatalf("tracking=%v: %s serialised as null rather than []", tracking, k)
			}
		}
	}
}

// ---- helpers ---------------------------------------------------------------

func toFieldMap(s DesiredState) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("expectation is not valid JSON: %s", a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("result is not valid JSON: %s", b)
	}
	return reflect.DeepEqual(x, y)
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
