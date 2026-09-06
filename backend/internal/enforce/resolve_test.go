package enforce

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/policy"
	"github.com/helios57/familyguard/backend/internal/store"
)

// fakeSource is an in-memory Source. Every method can be made to fail independently, because a
// resolver that swallowed one read would compute a desired state from partial data — a child whose
// usage query failed would silently get their quota back.
type fakeSource struct {
	device    store.Device
	policy    store.Policy
	rules     []store.AppRule
	domains   []string
	apps      []store.InstalledApp
	usage     map[string]int
	managed   []store.App
	blocklist []string
	fail      map[string]error
	usageDay  string // the day key the resolver actually asked for
}

func (f *fakeSource) GetDevice(context.Context, uuid.UUID) (*store.Device, error) {
	if err := f.fail["device"]; err != nil {
		return nil, err
	}
	d := f.device
	return &d, nil
}

func (f *fakeSource) GetPolicy(context.Context, uuid.UUID) (*store.Policy, error) {
	if err := f.fail["policy"]; err != nil {
		return nil, err
	}
	p := f.policy
	return &p, nil
}

func (f *fakeSource) ListAppRules(context.Context, uuid.UUID) ([]store.AppRule, error) {
	if err := f.fail["rules"]; err != nil {
		return nil, err
	}
	return f.rules, nil
}

func (f *fakeSource) ListBlockedDomains(context.Context, uuid.UUID) ([]string, error) {
	if err := f.fail["domains"]; err != nil {
		return nil, err
	}
	return f.domains, nil
}

func (f *fakeSource) ListInstalledApps(context.Context, uuid.UUID, bool) ([]store.InstalledApp, error) {
	if err := f.fail["apps"]; err != nil {
		return nil, err
	}
	return f.apps, nil
}

func (f *fakeSource) ManagedAppsForChild(context.Context, uuid.UUID) ([]store.App, error) {
	if err := f.fail["managed"]; err != nil {
		return nil, err
	}
	return f.managed, nil
}

func (f *fakeSource) FamilyBlockedPackageNames(context.Context) ([]string, error) {
	if err := f.fail["blocklist"]; err != nil {
		return nil, err
	}
	return f.blocklist, nil
}

func (f *fakeSource) UsageMinutesForDay(_ context.Context, _ uuid.UUID, day string) (int, error) {
	f.usageDay = day
	if err := f.fail["usage"]; err != nil {
		return 0, err
	}
	return f.usage[day], nil
}

// testBaseURL stands for the deployment's public origin. Written with a trailing slash on purpose:
// the resolver has to trim it, and a URL with a double slash in the middle is the kind of thing
// that works in a browser and fails in an HTTP client on a phone.
const testBaseURL = "https://guard.example.com/"

func baseSource() *fakeSource {
	enrolled := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	return &fakeSource{
		device: store.Device{
			ID: uuid.New(), ChildID: uuid.New(), Name: "Pixel",
			EnrolledAt: &enrolled,
		},
		policy: store.Policy{
			AllowChildInstalls: true,
			BedtimeStart:       "21:00",
			BedtimeEnd:         "07:00",
			DNSHost:            "dns.example-family.net",
			Timezone:           "Europe/Zurich",
			Version:            7,
		},
		usage: map[string]int{},
		fail:  map[string]error{},
	}
}

func resolve(t *testing.T, f *fakeSource, at time.Time) (*policy.DesiredState, *policy.Input) {
	t.Helper()
	got, in, err := New(f, testBaseURL).Resolve(context.Background(), f.device.ID, at)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return got, in
}

// TestQuotaIsReadForTheLOCALDay is the reason this package exists. 22:30 UTC on a July evening is
// already the next day in Zurich; reading the UTC day would hand the child a second full quota at
// midnight local, every night of summer time, and nothing would look broken.
func TestQuotaIsReadForTheLocalDay(t *testing.T) {
	f := baseSource()
	f.policy.DailyLimitMinutes = 60
	f.usage["2026-07-15"] = 55 // the UTC day
	f.usage["2026-07-16"] = 5  // the local day, which is the one that counts

	at := time.Date(2026, 7, 15, 22, 30, 0, 0, time.UTC)
	got, _ := resolve(t, f, at)

	if f.usageDay != "2026-07-16" {
		t.Fatalf("read usage for day %q, want the local day 2026-07-16", f.usageDay)
	}
	if got.UsedMinutes != 5 {
		t.Fatalf("used %d minutes, want 5 — the resolver read the wrong day", got.UsedMinutes)
	}
	// Negative control: the two numbers must actually differ, or this test would pass on a
	// resolver that used either day.
	if f.usage["2026-07-15"] == f.usage["2026-07-16"] {
		t.Fatal("fixture cannot discriminate: both days hold the same usage")
	}
}

// TestDayKeyMatchesTheDayTheResolverReads pins the write path to the read path. Usage is filed
// under DayKey and read back by Resolve; if they ever disagreed the quota would reset at the wrong
// hour, and the only symptom would be a child locked out at breakfast.
func TestDayKeyMatchesTheDayTheResolverReads(t *testing.T) {
	f := baseSource()
	for _, at := range []time.Time{
		time.Date(2026, 7, 15, 22, 30, 0, 0, time.UTC),  // summer: local is already tomorrow
		time.Date(2026, 1, 15, 22, 30, 0, 0, time.UTC),  // winter: one hour offset, same effect
		time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC),   // the spring-forward morning
		time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC),  // the autumn fall-back morning
		time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),   // midday, no boundary anywhere near
		time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC), // a year boundary
	} {
		f.usageDay = ""
		resolve(t, f, at)
		want, err := DayKey(&f.policy, at)
		if err != nil {
			t.Fatalf("DayKey(%s): %v", at, err)
		}
		if f.usageDay != want {
			t.Errorf("at %s the resolver read day %q but DayKey files under %q", at, f.usageDay, want)
		}
	}
}

// TestUnknownTimezoneIsAnErrorNotAFallback: falling back to UTC would move bedtime by an hour and
// the day boundary by up to a day, with nothing in the response to say so.
//
// The assertion that no usage read happened is the load-bearing one, and it was added after
// calibration: with only the error checks, replacing this package's zone rejection with a silent
// UTC fallback left the test GREEN, because policy.Compute rejects the same bad zone one layer
// down. The error surfaced either way, so the test was measuring the engine, not the resolver.
// Refusing before any dated work is a property only this package can have.
func TestUnknownTimezoneIsAnErrorNotAFallback(t *testing.T) {
	f := baseSource()
	f.policy.Timezone = "Mars/Olympus_Mons"

	got, _, err := New(f, testBaseURL).Resolve(context.Background(), f.device.ID, time.Now())
	if err == nil {
		t.Fatal("an unknown time zone resolved successfully")
	}
	if f.usageDay != "" {
		t.Errorf("usage was read for day %q under an unresolvable zone", f.usageDay)
	}
	if !errors.Is(err, policy.ErrInvalidInput) {
		t.Errorf("error %v does not wrap ErrInvalidInput, so a handler cannot map it to 400", err)
	}
	if got != nil {
		t.Error("a desired state came back alongside the error; a caller could apply it")
	}
	if _, err := DayKey(&f.policy, time.Now()); !errors.Is(err, policy.ErrInvalidInput) {
		t.Errorf("DayKey accepted the same bad zone: %v", err)
	}
}

// TestUninstalledAppsAreNotSuspended: an app the child removed must drop out of the enforcement
// input, or bedtime would keep listing it forever and the console would show a rule against
// something that is not on the phone.
func TestUninstalledAppsAreNotSuspended(t *testing.T) {
	removed := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := baseSource()
	f.policy.BedtimeEnabled = true
	f.apps = []store.InstalledApp{
		{PackageName: "com.example.present", FirstSeenAt: removed},
		{PackageName: "com.example.gone", FirstSeenAt: removed, RemovedAt: &removed},
	}

	at := time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC) // 22:00 local, inside bedtime
	got, _ := resolve(t, f, at)

	if got.SuspendReason != policy.ReasonBedtime {
		t.Fatalf("suspend reason %q, want BEDTIME — the fixture is not exercising suspension", got.SuspendReason)
	}
	if !slices.Contains(got.SuspendedPackages, "com.example.present") {
		t.Error("the installed app was not suspended, so this test cannot detect the bug it is for")
	}
	if slices.Contains(got.SuspendedPackages, "com.example.gone") {
		t.Error("an uninstalled app was suspended")
	}
}

// TestNoveltyComesFromTheBaselineFlagNotATimestamp guards FR-5.4's failure mode: measuring novelty
// from the wrong instant sweeps the entire preinstalled inventory into the approval queue the
// moment a parent switches free installation off.
//
// This test is written against the baseline flag because the timestamp version of it was WRONG AND
// GREEN. It read `first_seen_at.After(enrolled_at)` and was calibrated with rows whose first_seen_at
// sat an hour BEFORE enrolment — a state the store cannot produce, because a device's first
// inventory report necessarily arrives after it enrols. Every app on every real phone was therefore
// "new", and it took the end-to-end suite (which drives an actual enrolment and an actual inventory
// POST) to show it. So the rows below carry the flag the store writes, and the assertion that
// matters is the one on apps that were in the first report.
func TestNoveltyComesFromTheBaselineFlagNotATimestamp(t *testing.T) {
	// Deliberately AFTER every timestamp below: nothing in this test may depend on the enrolment
	// instant, and a resolver that started comparing against it again would read all four as new.
	enrolled := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	first := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	f := baseSource()
	f.device.EnrolledAt = &enrolled
	f.policy.AllowChildInstalls = false
	f.apps = []store.InstalledApp{
		{PackageName: "com.example.preinstalled", Baseline: true, FirstSeenAt: first},
		{PackageName: "com.example.alsopreinstalled", Baseline: true, FirstSeenAt: first},
		{PackageName: "com.example.addedlater", FirstSeenAt: first.Add(48 * time.Hour)},
		{PackageName: "com.example.systemota", SystemApp: true, FirstSeenAt: first.Add(72 * time.Hour)},
	}

	got, _ := resolve(t, f, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))

	// systemota is not here even though it is off-baseline: a system app arriving with an OTA is
	// not a child installing something (FR-5.4).
	want := []string{"com.example.addedlater"}
	if !slices.Equal(got.PendingApproval, want) {
		t.Errorf("pending approval = %v, want %v", got.PendingApproval, want)
	}
	for _, pkg := range []string{"com.example.preinstalled", "com.example.alsopreinstalled"} {
		if slices.Contains(got.SuspendedPackages, pkg) {
			t.Errorf("%s was in the device's first inventory and was suspended anyway", pkg)
		}
	}

	// Calibration: the assertion above is only worth anything if dropping the flag changes it.
	f.apps[0].Baseline = false
	got, _ = resolve(t, f, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))
	if !slices.Contains(got.PendingApproval, "com.example.preinstalled") {
		t.Error("clearing the baseline flag did not make the app pending; this test binds to nothing")
	}
}

// TestParentLockIsStateNotACommand: the lock has to survive a reboot and a command expiry, so it
// comes from the device row rather than from an unexpired LOCK_NOW.
func TestParentLockIsStateNotACommand(t *testing.T) {
	f := baseSource()
	if got, _ := resolve(t, f, time.Now()); got.Locked {
		t.Fatal("an untouched device came back locked")
	}
	f.device.Locked = true
	if got, _ := resolve(t, f, time.Now()); !got.Locked {
		t.Error("devices.locked did not reach the desired state")
	}
}

// TestDeviceCriticalPackagesWidenAndNeverNarrow. The device knows its own dialer and launcher; the
// server knows the built-in floor. Union, never replace — a device that reports nothing must still
// be able to place a call.
func TestDeviceCriticalPackagesWidenAndNeverNarrow(t *testing.T) {
	const ownLauncher = "com.oem.launcher3"
	f := baseSource()
	f.policy.BedtimeEnabled = true
	f.device.CriticalPackages = []string{ownLauncher}
	f.apps = []store.InstalledApp{
		{PackageName: ownLauncher},
		{PackageName: "com.android.dialer"},
		{PackageName: "com.example.game"},
	}
	at := time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC) // inside bedtime

	got, _ := resolve(t, f, at)
	if !slices.Contains(got.SuspendedPackages, "com.example.game") {
		t.Fatal("bedtime suspended nothing; the fixture proves nothing")
	}
	for _, p := range []string{ownLauncher, "com.android.dialer"} {
		if slices.Contains(got.SuspendedPackages, p) {
			t.Errorf("%s was suspended", p)
		}
	}

	// Reporting nothing must not narrow the floor.
	f.device.CriticalPackages = nil
	got, _ = resolve(t, f, at)
	if slices.Contains(got.SuspendedPackages, "com.android.dialer") {
		t.Error("a device that reported no critical packages lost the built-in dialer exemption")
	}
	if !slices.Contains(got.SuspendedPackages, ownLauncher) {
		t.Error("fixture check: without the device's report its launcher should now be suspendable")
	}
}

// TestAppRulesSplitByAction: an ALLOW rule is a bedtime exemption and a BLOCK rule is a
// suspension. Mapping both onto the same slice would turn every exemption into a block.
func TestAppRulesSplitByAction(t *testing.T) {
	f := baseSource()
	f.policy.BedtimeEnabled = true
	f.rules = []store.AppRule{
		{PackageName: "com.example.homework", Action: store.ActionAllow},
		{PackageName: "com.example.casino", Action: store.ActionBlock},
	}
	f.apps = []store.InstalledApp{{PackageName: "com.example.homework"}, {PackageName: "com.example.other"}}

	got, in := resolve(t, f, time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC))

	if !slices.Equal(in.Settings.AllowedPackages, []string{"com.example.homework"}) {
		t.Errorf("allowed = %v", in.Settings.AllowedPackages)
	}
	if !slices.Equal(in.Settings.BlockedPackages, []string{"com.example.casino"}) {
		t.Errorf("blocked = %v", in.Settings.BlockedPackages)
	}
	if slices.Contains(got.SuspendedPackages, "com.example.homework") {
		t.Error("an ALLOW-listed app was suspended by bedtime")
	}
	if !slices.Contains(got.SuspendedPackages, "com.example.casino") {
		t.Error("a BLOCK-listed app was not suspended")
	}
}

// TestEveryReadFailureIsReported. A resolver that carried on after a failed read would enforce a
// policy computed from whatever it did manage to fetch: the most dangerous shape of a false green,
// because the response looks entirely normal.
func TestEveryReadFailureIsReported(t *testing.T) {
	boom := errors.New("database is down")
	for _, key := range []string{"device", "policy", "usage", "rules", "domains", "apps", "managed"} {
		t.Run(key, func(t *testing.T) {
			f := baseSource()
			f.fail[key] = boom
			got, _, err := New(f, testBaseURL).Resolve(context.Background(), f.device.ID, time.Now())
			if err == nil {
				t.Fatalf("a failing %s read produced a desired state", key)
			}
			if !errors.Is(err, boom) {
				t.Errorf("error %v does not wrap the cause", err)
			}
			if got != nil {
				t.Error("a desired state came back alongside the error")
			}
		})
	}
}

// TestManagedAppsReachTheDeviceAsSomethingItCanFetch covers the seam where the catalog's row
// becomes an instruction to a phone: the URL it downloads from and the checksum it compares.
//
// The digest is asserted against a value computed here from known bytes rather than copied out of
// the resolver's answer. An assertion that re-encodes the input the same way the code does passes
// whichever encoding the code picked, which is precisely the mistake available at this seam — the
// store speaks hex and the device speaks base64url, and only one of them is right on the wire.
func TestManagedAppsReachTheDeviceAsSomethingItCanFetch(t *testing.T) {
	raw := sha256.Sum256([]byte("the muplay apk bytes"))
	f := baseSource()
	f.managed = []store.App{{
		PackageName: "net.muplay.player",
		VersionCode: 7,
		VersionName: "1.4.0",
		SHA256:      hex.EncodeToString(raw[:]),
		SizeBytes:   4200000,
	}}

	_, in, err := New(f, testBaseURL).Resolve(context.Background(), f.device.ID, time.Now())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(in.Settings.ManagedApps) != 1 {
		t.Fatalf("managed apps: %#v", in.Settings.ManagedApps)
	}
	got := in.Settings.ManagedApps[0]

	if want := base64.RawURLEncoding.EncodeToString(raw[:]); got.Checksum != want {
		t.Errorf("checksum is %q, want the base64url the phone compares: %q", got.Checksum, want)
	}
	if strings.ContainsAny(got.Checksum, "+/=") {
		t.Errorf("checksum %q is not URL-safe base64", got.Checksum)
	}
	// Absolute, no double slash, and carrying the exact version — the three things that make the
	// phone's second request describe the same artifact as its first.
	want := "https://guard.example.com/api/v1/device/apps/net.muplay.player/7.apk"
	if got.URL != want {
		t.Errorf("url is %q, want %q", got.URL, want)
	}
	if got.Size != 4200000 || got.VersionCode != 7 || got.VersionName != "1.4.0" {
		t.Errorf("the build was not carried through: %#v", got)
	}
}

// TestAManagedAppWithAnUnusableDigestIsNotSentToThePhone. The engine drops an entry with no
// checksum; this is the other half — a row whose digest is not a SHA-256 at all must arrive there
// as no checksum rather than as a string the phone will compare and always fail.
func TestAManagedAppWithAnUnusableDigestIsNotSentToThePhone(t *testing.T) {
	for name, digest := range map[string]string{
		"not hex":   "zzzz",
		"too short": hex.EncodeToString([]byte("sixteen bytes!!!")),
		"empty":     "",
	} {
		t.Run(name, func(t *testing.T) {
			f := baseSource()
			f.managed = []store.App{{PackageName: "net.muplay.player", VersionCode: 7, SHA256: digest}}
			_, in, err := New(f, testBaseURL).Resolve(context.Background(), f.device.ID, time.Now())
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if len(in.Settings.ManagedApps) != 0 {
				t.Fatalf("an unverifiable app was sent to the phone: %#v", in.Settings.ManagedApps)
			}
		})
	}

	// The positive control. Without it every case above would also pass against a resolver that
	// dropped every managed app, whatever its digest.
	f := baseSource()
	raw := sha256.Sum256([]byte("good"))
	f.managed = []store.App{{PackageName: "net.muplay.player", VersionCode: 7, SHA256: hex.EncodeToString(raw[:])}}
	_, in, err := New(f, testBaseURL).Resolve(context.Background(), f.device.ID, time.Now())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(in.Settings.ManagedApps) != 1 {
		t.Fatal("a valid digest was dropped too; the cases above measured nothing")
	}
}

// TestTheDownloadPathEscapesWhatItInterpolates. A package name is not arbitrary, but this function
// builds a URL by interpolation and the store's column is free text — the guard belongs where the
// string is built, not only where it is validated.
func TestTheDownloadPathEscapesWhatItInterpolates(t *testing.T) {
	got := ManagedAppDownloadPath("com.example/../../etc", 1)
	if strings.Contains(got, "..") && !strings.Contains(got, "%2F") {
		t.Fatalf("path traversal survived interpolation: %q", got)
	}
	if want := "/api/v1/device/apps/com.example%2F..%2F..%2Fetc/1.apk"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
