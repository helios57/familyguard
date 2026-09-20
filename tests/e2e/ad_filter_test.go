package e2e

// The ad filter end to end (FR-6.6 … FR-6.10): a parent's PATCH, what the phone is handed, and
// what the phone reports back — against a real server and a real PostgreSQL.
//
// It exists because the interesting behaviour is in SQL and nowhere else. `internal/store` has no
// unit tests at all and cannot usefully have them: the thing being asserted is the three-valued
// `COALESCE` upsert, which is a property of PostgreSQL's `ON CONFLICT DO UPDATE`, not of any Go
// code a fake could stand in for. A mock store here would assert that the mock does what the test
// author thought the SQL does, which is exactly the shape that has to be avoided.
//
// The three device-reported values are `true`, `false` and **absent**, and the one that matters is
// the middle one: a filter a parent switched on whose tunnel never came up. A console that reads
// absent as "off" hides a phone that has not been updated; one that reads false as "nothing
// reported" hides the failure this whole feature would otherwise have no symptom for.

import (
	"net/http"
	"testing"
	"time"
)

// A real list, because the URL is the one value with a rule attached to it. Never fetched here —
// nothing in this suite reaches the internet.
const adFilterList = "https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt"

// FR-6.10.
func TestAParentTurnsTheAdFilterOnAndThePhoneIsToldToRunIt(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	// The calibration for everything below: a new child has no filter and no list. If this were
	// already on, the "switching it on changed something" half would prove nothing.
	pol := h.policy(f.parent.Token, f.child.ID)
	if pol.AdFilter || pol.AdFilterListURL != "" {
		t.Fatalf("a new child starts with the filter off and no list; got ad_filter=%v url=%q",
			pol.AdFilter, pol.AdFilterListURL)
	}
	if before := h.desiredState(f.parent.Token, f.device.ID, "").Desired; before.AdFilter {
		t.Fatal("a child with the switch off must be handed ad_filter=false")
	}

	// The switch alone, with nowhere to fetch a list from. This is deliberately NOT enough: a
	// tunnel with an empty index puts every packet on the phone through this code and blocks
	// nothing. The switch is recorded — a parent's decision is not silently discarded — and the
	// device is still told false, which is what lets the console say "on, and not filtering".
	onlyTheSwitch := h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"ad_filter": true})
	if !onlyTheSwitch.AdFilter {
		t.Fatal("the PATCH did not stick: the policy still reports ad_filter=false")
	}
	half := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	if half.AdFilter {
		t.Fatal("a filter switched on with no list url must be handed to the phone as OFF — a " +
			"tunnel with nothing to block is every packet through our code for no benefit")
	}

	// And with a list, both halves arrive.
	withList := h.patchPolicy(f.parent.Token, f.child.ID,
		map[string]any{"ad_filter_list_url": adFilterList})
	if withList.AdFilterListURL != adFilterList {
		t.Fatalf("the list url did not stick: %q", withList.AdFilterListURL)
	}
	if withList.Version <= onlyTheSwitch.Version {
		t.Fatalf("the policy version did not move (%d → %d), so no phone would notice",
			onlyTheSwitch.Version, withList.Version)
	}
	full := h.desiredState(f.parent.Token, f.device.ID, "").Desired
	if !full.AdFilter || full.AdFilterListURL != adFilterList {
		t.Fatalf("the phone was handed ad_filter=%v url=%q", full.AdFilter, full.AdFilterListURL)
	}

	// The device asks the same question with its own credential and must get the same answer.
	// Asserted because these are two different code paths onto one resolver, and a console that
	// disagrees with the phone is worse than one that shows nothing.
	asDevice := h.devicePolicy(f.enroll.DeviceToken).Desired
	if !asDevice.AdFilter || asDevice.AdFilterListURL != adFilterList {
		t.Fatalf("the device sees ad_filter=%v url=%q while the console sees %v %q",
			asDevice.AdFilter, asDevice.AdFilterListURL, full.AdFilter, full.AdFilterListURL)
	}

	// Off again, in the same test and against the same device, so "the filter is gone" cannot pass
	// because it was never there.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"ad_filter": false})
	if off := h.desiredState(f.parent.Token, f.device.ID, "").Desired; off.AdFilter {
		t.Fatal("switching the filter off left the phone being told to run it")
	}
}

// FR-6.10: whatever can rewrite a plain-HTTP list decides what this phone refuses to connect to.
func TestAFilterListMustBeHTTPS(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	for _, refused := range []string{
		"http://example.com/list.txt",
		"HTTP://example.com/list.txt",
		"ftp://example.com/list.txt",
		"file:///etc/passwd",
		"//example.com/list.txt",
		"example.com/list.txt",
	} {
		h.call(http.MethodPatch, "/children/"+f.child.ID+"/policy", f.parent.Token,
			map[string]any{"ad_filter_list_url": refused}).
			expectError(http.StatusBadRequest, "invalid_input")
		if pol := h.policy(f.parent.Token, f.child.ID); pol.AdFilterListURL != "" {
			t.Fatalf("%q was refused and stored anyway: %q", refused, pol.AdFilterListURL)
		}
	}

	// The positive control. Without it, every refusal above would also pass on a server that
	// refuses every url, which is the failure that looks like a working validator.
	accepted := h.patchPolicy(f.parent.Token, f.child.ID,
		map[string]any{"ad_filter_list_url": adFilterList})
	if accepted.AdFilterListURL != adFilterList {
		t.Fatalf("an https url was refused: %q", accepted.AdFilterListURL)
	}

	// Empty clears it, because a parent has to be able to undo a decision (FR-6.4's rule, applied
	// to the field next door).
	cleared := h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"ad_filter_list_url": ""})
	if cleared.AdFilterListURL != "" {
		t.Fatalf("the list url could not be cleared: %q", cleared.AdFilterListURL)
	}
}

// FR-6.10, and the reason this file talks to a real database: the upsert is what keeps the three
// states apart, and it is written in SQL.
func TestWhatThePhoneMeasuredAboutItsFilterSurvivesAnOlderBuildsHeartbeat(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	state := func() deviceStateDTO {
		t.Helper()
		var view deviceViewDTO
		h.call(http.MethodGet, "/devices/"+f.device.ID, f.parent.Token, nil).
			expect(http.StatusOK).decode(&view)
		if view.State == nil {
			t.Fatal("the device has no state row")
		}
		return *view.State
	}
	// The one good stamp, kept so every later assertion can say "still this one" rather than
	// "still something". See the last block of this test for why "something" is not enough.
	fetched := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	beat := func(body map[string]any) {
		t.Helper()
		h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken, body).
			expect(http.StatusOK)
	}

	// Every heartbeat the Play build sends, and every heartbeat any build older than this feature
	// sends. Nothing may be invented from silence: doing so would put "ad filter not running" on
	// every phone in the fleet the day this ships.
	beat(map[string]any{"connectivity": "wifi"})
	if st := state(); st.AdFilterRules != nil || st.AdFilterFetchedAt != nil || st.AdFilterRunning != nil {
		t.Fatalf("a heartbeat that said nothing was recorded as rules=%v fetched=%v running=%v",
			st.AdFilterRules, st.AdFilterFetchedAt, st.AdFilterRunning)
	}

	// The state this feature exists to make visible: a list compiled, and a tunnel that is NOT up.
	// `false` here has to reach the console as false, not as "nothing reported" — they render
	// differently and only one of them tells a parent to go and look at the phone.
	beat(map[string]any{
		"connectivity":         "wifi",
		"ad_filter_rules":      181117,
		"ad_filter_fetched_at": fetched.Format(time.RFC3339),
		"ad_filter_running":    false,
	})
	st := state()
	if st.AdFilterRules == nil || *st.AdFilterRules != 181117 {
		t.Fatalf("the phone reported 181117 rules and the server holds %v", st.AdFilterRules)
	}
	if st.AdFilterFetchedAt == nil || !st.AdFilterFetchedAt.Equal(fetched) {
		t.Fatalf("the phone reported %v as the fetch time and the server holds %v",
			fetched, st.AdFilterFetchedAt)
	}
	if st.AdFilterRunning == nil || *st.AdFilterRunning {
		t.Fatalf("the phone reported the tunnel is down and the server holds %v", st.AdFilterRunning)
	}

	// An older build heartbeating in between must not erase any of it. The fleet this exists to
	// diagnose is exactly the fleet that will also be running builds too old to report.
	beat(map[string]any{"connectivity": "wifi"})
	st = state()
	if st.AdFilterRules == nil || *st.AdFilterRules != 181117 {
		t.Fatalf("a heartbeat that omitted the rule count cleared it: %v", st.AdFilterRules)
	}
	if st.AdFilterFetchedAt == nil || !st.AdFilterFetchedAt.Equal(fetched) {
		t.Fatalf("a heartbeat that omitted the fetch time changed it to %v", st.AdFilterFetchedAt)
	}
	if st.AdFilterRunning == nil || *st.AdFilterRunning {
		t.Fatalf("a heartbeat that omitted the running flag changed it: %v", st.AdFilterRunning)
	}

	// The tunnel comes up. Both directions, because a field that can only ever go one way is a
	// field a parent learns to ignore.
	beat(map[string]any{"connectivity": "wifi", "ad_filter_running": true})
	if got := state().AdFilterRunning; got == nil || !*got {
		t.Fatalf("the phone reported the tunnel is up and the server holds %v", got)
	}

	// A measured zero is a finding, not a silence: the filter is on, the list fetched, and it
	// compiled to nothing. It must be storable and it must survive, which is the one case a
	// `COALESCE` over a column would get wrong if the value were carried as "unset means zero".
	beat(map[string]any{"connectivity": "wifi", "ad_filter_rules": 0})
	if got := state().AdFilterRules; got == nil || *got != 0 {
		t.Fatalf("a measured zero rule count was not stored as zero: %v", got)
	}
	beat(map[string]any{"connectivity": "wifi"})
	if got := state().AdFilterRules; got == nil || *got != 0 {
		t.Fatalf("a measured zero was erased by a heartbeat that said nothing: %v", got)
	}

	// Nonsense is dropped rather than stored. A negative rule count would render as a number, and
	// an unparseable stamp stored as the zero time renders as "1 January year one" — which reads
	// as a bug in the console rather than as a phone that sent something unusable.
	//
	// **This asserts the stamp is still the GOOD one, and `IsZero()` would not do.** Written that
	// way first, the assertion passed with the production code deliberately broken to store the
	// zero time. PostgreSQL gave it back as `0001-01-01 01:05:21 +0105` — the column is
	// `timestamptz`, and year 1 predates standard time, so the session zone applies Zurich's local
	// mean time and the instant that comes back is 1h5m21s away from the one Go calls zero.
	// `IsZero()` is therefore false for exactly the value it was written to catch, while the
	// console still renders "1 January year one". Comparing against the value the phone actually
	// sent has no such hole.
	beat(map[string]any{
		"connectivity":         "wifi",
		"ad_filter_rules":      -1,
		"ad_filter_fetched_at": "yesterday afternoon",
	})
	st = state()
	if st.AdFilterRules == nil || *st.AdFilterRules != 0 {
		t.Fatalf("a negative rule count reached the column: %v", st.AdFilterRules)
	}
	if st.AdFilterFetchedAt == nil || !st.AdFilterFetchedAt.Equal(fetched) {
		t.Fatalf("an unreadable stamp replaced the good one: %v", st.AdFilterFetchedAt)
	}
}
