// Package policy is the enforcement engine: given a child's policy, the device's app inventory and
// today's usage, it computes the exact state the device must be in.
//
// It is deliberately pure. No clock, no database, no network — every input is a parameter, so the
// same computation runs on the server (to show a parent what the device is doing) and on the device
// (to actually do it). The two implementations are held together by a shared vector file,
// vectors.json in this directory, which both test suites replay — the Kotlin engine reads it from
// the repository rather than carrying a copy. Two enforcement engines that drift
// apart is the failure mode this package exists to prevent; a console that shows "bedtime is over"
// while the phone is still locked is worse than no console.
//
// Three invariants hold for every possible input, and each has a test that fails without it:
//
//   - Nothing on the critical whitelist is ever suspended or hidden (FR-5.5). Emergency calling
//     works in every policy state.
//   - Every state is reversible (FR-4.2, FR-7.4). Removing the cause restores the previous output
//     exactly — no output is one-way.
//   - Tracking-only suspends nothing and locks nothing, while leaving filtering and hardening in
//     place (FR-8).
package policy

import (
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	// The zone database is embedded rather than read from the filesystem: the server image is
	// built FROM scratch, where /usr/share/zoneinfo does not exist. Without this import
	// time.LoadLocation("Europe/Zurich") fails in production and succeeds on every developer
	// machine — a difference that no local test can see.
	_ "time/tzdata"
)

// Vectors is the shared enforcement test-vector file, replayed by this package's tests and by the
// Android DPC's Kotlin engine (Gradle copies it into the module's test resources, so there is one
// file and no second copy to drift).
//
// It is embedded rather than read from disk by the test for a reason that was measured, not
// assumed: while it lived outside this module, `go test` served a *cached pass* across an edit to
// it, so adding a vector that the code fails could still print "ok". Embedding makes the file a
// build input, and any change to it now invalidates the cache.
//
//go:embed vectors.json
var Vectors []byte

// ErrInvalidInput is returned for input the engine cannot evaluate: an unknown time zone, a
// malformed clock time, an unparseable instant. The engine never guesses. A caller that receives
// this must keep the last known-good state rather than fall back to an unenforced one — guessing
// UTC would move bedtime by an hour twice a year without anyone noticing.
var ErrInvalidInput = errors.New("policy: invalid input")

// Suspension reasons.
const (
	ReasonNone    = ""
	ReasonBedtime = "BEDTIME"
	ReasonQuota   = "QUOTA"
)

// DefaultCriticalPackages can never be suspended or hidden, whatever the policy says (FR-5.5).
// The list covers the AOSP dialer, messaging, contacts, emergency information, settings and
// package installer, plus the common OEM variants, plus this DPC itself — a suspended DPC could
// not lift its own suspension.
//
// Callers may add to this set through Input.CriticalPackages but can never subtract from it: the
// union is taken, so a device reporting an empty list still keeps emergency calling.
var DefaultCriticalPackages = []string{
	"io.github.helios57.familyguard",    // this DPC — suspending it would be unrecoverable
	"com.android.cellbroadcastreceiver", // emergency alerts
	"com.android.contacts",
	"com.android.dialer",
	"com.android.emergency", // emergency information
	"com.android.mms",
	"com.android.packageinstaller",
	"com.android.phone",
	"com.android.providers.contacts",
	"com.android.providers.telephony",
	"com.android.server.telecom",
	"com.android.settings",
	"com.google.android.apps.messaging",
	"com.google.android.contacts",
	"com.google.android.dialer",
	"com.google.android.packageinstaller",
	"com.google.android.permissioncontroller",
	"com.samsung.android.app.contacts",
	"com.samsung.android.dialer",
	"com.samsung.android.messaging",
}

// YouTubePackages is the app family the killswitch suspends and hides (FR-7.1).
var YouTubePackages = []string{
	"app.revanced.android.youtube",
	"com.google.android.apps.youtube.kids",
	"com.google.android.apps.youtube.music",
	"com.google.android.youtube",
	"com.vanced.android.youtube",
	"org.schabi.newpipe",
}

// YouTubeDomains is blocked at the DNS layer and in the managed browser when the killswitch is on
// (FR-7.2, FR-7.3). googlevideo.com carries the media itself; blocking only youtube.com leaves
// playback working through an embed.
var YouTubeDomains = []string{
	"googlevideo.com",
	"youtu.be",
	"youtube-nocookie.com",
	"youtube.com",
	"youtubei.googleapis.com",
	"youtubekids.com",
	"yt3.ggpht.com",
	"ytimg.com",
}

// Device Owner user restrictions. Only restrictions that defend a stated requirement are listed:
// each one is a thing a child could otherwise do to escape a control we promised the parent.
//
// These are platform keys, copied by hand from android.os.UserManager, and a wrong one is silent:
// DevicePolicyManager.addUserRestriction accepts an unknown key, logs, and applies nothing. That is
// not a theory — "no_config_private_dns" below used to be spelled that way, following the `no_`
// convention every other key here uses, and the emulator reported the device hardened while Private
// DNS stayed changeable. DISALLOW_CONFIG_PRIVATE_DNS is the one constant in this set whose value
// does not start with `no_` (read back from android-34/android.jar with javap -constants).
//
// Nothing on this side can check that. The chain that does: the Kotlin engine's constants are
// asserted equal to android.os.UserManager's in RestrictionKeysMatchThePlatformTest, and the Kotlin
// engine is asserted equal to this one by vectors.json.
const (
	RestrictionSafeBoot       = "no_safe_boot"                // safe boot starts without the DPC
	RestrictionDebugging      = "no_debugging_features"       // adb can un-suspend anything
	RestrictionPrivateDNS     = "disallow_config_private_dns" // FR-6.1: filtering not changeable on the device
	RestrictionDateTime       = "no_config_date_time"         // FR-3.4/FR-4.1 are wall-clock
	RestrictionAddUser        = "no_add_user"                 // a second user is an unmanaged device
	RestrictionUnknownSources = "no_install_unknown_sources"  // sideloading bypasses app governance
	RestrictionInstallApps    = "no_install_apps"             // FR-5.3 free-installation mode, off
	RestrictionUninstallApps  = "no_uninstall_apps"           // uninstalling is how a suspension is escaped
	RestrictionFactoryReset   = "no_factory_reset"            // never set; named so the test can assert its absence
	RestrictionOutgoingCalls  = "no_outgoing_calls"           // never set
	RestrictionCreateWindows  = "no_create_windows"           // never set
	RestrictionSMS            = "no_sms"                      // never set
)

// forbiddenRestrictions can never appear in the output. They would each break a promise in NFR-6:
// a device that cannot call, cannot be reached, or cannot show a recovery dialog.
//
// no_factory_reset is here for a different reason than the other three, and it is the one most
// likely to be added back by someone who thinks it sounds strict. A fully managed device that
// forbids factory reset can be recovered from a bad policy, a wrong DNS host or a control plane that
// will not answer *only* through the control plane and the on-device recovery code. Wiping the phone
// from the recovery menu is the last escape hatch that depends on nothing this project ships, and
// while this project is young that hatch stays open. Turning it on is a decision to make once the
// deployment has run in a family's hands for a while — and it belongs in policy where a parent can
// see it, never as a constant compiled into the DPC.
//
// None of the four is ever added to the set in the first place, so this map removes nothing today —
// it is a net under a future edit, not a live filter. That is worth stating because it defeats the
// obvious calibration: deleting the strip changes no output, and a test that went green either way
// would look like proof. TestFactoryResetIsNeverBlocked was calibrated by simulating the mistake
// instead — adding RestrictionFactoryReset to the hardening set below. With this map intact the
// test stays green (the net catches it); with the map entry also removed it goes red.
var forbiddenRestrictions = map[string]bool{
	RestrictionFactoryReset:  true,
	RestrictionOutgoingCalls: true,
	RestrictionCreateWindows: true,
	RestrictionSMS:           true,
}

// App is one entry of the device's inventory.
type App struct {
	Package string `json:"package"`
	// System marks a preloaded system app. System apps are still governable — a parent may block
	// the stock browser — but they are never auto-suspended by the free-installation rule.
	System bool `json:"system"`
	// NewSinceBaseline is true for an app that was not in the device's FIRST inventory report. It
	// is the input to FR-5.4: with free-installation off, such an app is suspended until a parent
	// allows it. The apps of that first report are the baseline and are never swept up by the rule,
	// because suspending a device's entire preinstalled inventory the moment a parent flips one
	// toggle is indistinguishable from bricking it.
	//
	// The field is named for the baseline and not for enrolment on purpose. The first inventory
	// necessarily arrives AFTER enrolment, so any implementation — this one or the device's —
	// that derives this from "installed after enrolled_at" marks every app on the phone as new.
	// Whoever supplies this value must carry the flag; it cannot be recomputed from a timestamp.
	NewSinceBaseline bool `json:"new_since_baseline"`
}

// Settings mirrors the child's policy row, flattened with the rule tables it is always read with.
type Settings struct {
	TrackingOnly       bool   `json:"tracking_only"`
	AllowChildInstalls bool   `json:"allow_child_installs"`
	YouTubeBlocked     bool   `json:"youtube_blocked"`
	DailyLimitMinutes  int    `json:"daily_limit_minutes"`
	BedtimeEnabled     bool   `json:"bedtime_enabled"`
	BedtimeStart       string `json:"bedtime_start"`
	BedtimeEnd         string `json:"bedtime_end"`
	DNSHost            string `json:"dns_host"`
	Timezone           string `json:"timezone"`
	Version            int64  `json:"version"`

	BlockedPackages []string `json:"blocked_packages"`
	AllowedPackages []string `json:"allowed_packages"`
	BlockedDomains  []string `json:"blocked_domains"`

	// FamilyBlockedPackages is the blocklist that applies to every child in the family (FR-18) —
	// vendor preinstalls a parent decided nobody should have, most of which arrived on the phone
	// without anyone choosing them.
	//
	// It is a separate field from BlockedPackages, rather than merged into it by the caller, so
	// that the precedence below can be written and tested. Merging upstream would make the two
	// indistinguishable here and the ALLOW carve-out impossible to express.
	FamilyBlockedPackages []string `json:"family_blocked_packages"`

	// ManagedApps is the set of applications a parent has declared this child's phone should have
	// (FR-16). It is a declared SET, not a queue of install commands: the device converges on it at
	// every sync, so an install that failed retries by itself and an app a child managed to remove
	// comes back, without a parent noticing anything went wrong.
	ManagedApps []ManagedApp `json:"managed_apps"`
}

// ManagedApp is one entry of that set: which application, which exact build, and everything the
// phone needs to fetch and verify it without asking a second question.
//
// The version is pinned rather than left as "latest" on purpose. The phone verifies the checksum it
// was given here against the bytes it downloads, so a URL that resolved to a newer build between
// the sync and the download would fail that check — and the failure would read as a corrupted
// download rather than as a race. Naming the version makes the two requests describe one artifact.
type ManagedApp struct {
	PackageName string `json:"package_name"`
	VersionCode int64  `json:"version_code"`
	VersionName string `json:"version_name"`
	// Checksum is base64url without padding, of the SHA-256 of the file — the same encoding
	// /device/apk-info publishes for the DPC, so the phone has one checksum format and not two.
	Checksum string `json:"checksum"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

// Input is everything the engine needs. Nothing else is consulted.
type Input struct {
	Settings  Settings `json:"settings"`
	Installed []App    `json:"installed"`
	// UsedMinutesToday is the child's total foreground minutes for the local day that contains Now.
	UsedMinutesToday int `json:"used_minutes_today"`
	// ParentLock is true while a LOCK_NOW command is in effect and no UNLOCK_DEVICE has followed.
	ParentLock bool `json:"parent_lock"`
	// CriticalPackages widens the whitelist with what the device resolved at runtime (its actual
	// dialer, SMS and emergency-info packages, which vary by OEM). It can only add.
	CriticalPackages []string `json:"critical_packages"`
	// Now is the instant to evaluate, RFC 3339 with an offset.
	Now string `json:"now"`
}

// DesiredState is what the device must make true. Every slice is sorted and never nil, so two
// engines that agree produce byte-identical JSON and the shared vectors can be compared directly.
type DesiredState struct {
	// Locked is the keyguard, and only a parent's LOCK_NOW sets it. Bedtime and quota suspend apps;
	// they do not lock the screen, because a locked keyguard makes the phone less able to place an
	// emergency call than a phone whose apps are merely suspended.
	Locked bool `json:"locked"`

	// SuspendReason is why apps are suspended: "", "BEDTIME" or "QUOTA".
	SuspendReason     string   `json:"suspend_reason"`
	SuspendedPackages []string `json:"suspended_packages"`
	HiddenPackages    []string `json:"hidden_packages"`
	// PendingApproval lists apps suspended solely because free-installation mode is off and a
	// parent has not allowed them yet (FR-5.4). The console notifies on this.
	PendingApproval []string `json:"pending_approval"`

	PrivateDNSHost        string   `json:"private_dns_host"`
	BlockedDomains        []string `json:"blocked_domains"`
	SafeSearch            bool     `json:"safe_search"`
	YouTubeRestrictedMode bool     `json:"youtube_restricted_mode"`

	AllowInstalls    bool     `json:"allow_installs"`
	UserRestrictions []string `json:"user_restrictions"`

	QuotaMinutes     int `json:"quota_minutes"`
	UsedMinutes      int `json:"used_minutes"`
	RemainingMinutes int `json:"remaining_minutes"`

	// ManagedApps is passed through from the settings, sorted by package name and never nil. The
	// engine does not decide anything about it — which applications a child has is a parent's
	// declaration, not a computed consequence of bedtime — but it travels in the desired state
	// rather than beside it so that the device has exactly one description of what it must make
	// true, and so that the shared vectors cover it.
	//
	// A managed app is not exempt from anything. It is suspended at bedtime, hidden by a block
	// rule and counted against the quota like any other app: installing an application and
	// governing it are separate decisions, and a parent who declares one has not thereby allowed
	// it at midnight.
	ManagedApps []ManagedApp `json:"managed_apps"`

	// NextChangeAt is the next instant at which this output would differ, RFC 3339 in the policy's
	// zone, or "" if nothing is scheduled. The device sets one exact alarm for it instead of
	// polling, which is what keeps NFR-10 (idle while the screen is off) achievable.
	NextChangeAt string `json:"next_change_at"`

	PolicyVersion int64 `json:"policy_version"`
}

// Compute evaluates the policy. It is a pure function of its input.
func Compute(in Input) (DesiredState, error) {
	var out DesiredState

	loc, err := loadLocation(in.Settings.Timezone)
	if err != nil {
		return out, err
	}
	instant, err := time.Parse(time.RFC3339, in.Now)
	if err != nil {
		return out, fmt.Errorf("%w: now %q is not RFC 3339: %v", ErrInvalidInput, in.Now, err)
	}
	local := instant.In(loc)

	critical := newSet(DefaultCriticalPackages)
	critical.addAll(in.CriticalPackages)

	// ---- content filtering and hardening: in effect in every mode, including tracking-only ----

	out.PrivateDNSHost = strings.TrimSpace(in.Settings.DNSHost)
	out.SafeSearch = true
	out.YouTubeRestrictedMode = true
	out.PolicyVersion = in.Settings.Version

	domains := newSet(nil)
	for _, d := range in.Settings.BlockedDomains {
		if n := normalizeDomain(d); n != "" {
			domains.add(n)
		}
	}
	if in.Settings.YouTubeBlocked {
		domains.addAll(YouTubeDomains)
	}
	out.BlockedDomains = domains.sorted()

	out.ManagedApps = normalizeManagedApps(in.Settings.ManagedApps)

	out.AllowInstalls = in.Settings.AllowChildInstalls
	restrictions := newSet([]string{
		RestrictionSafeBoot,
		RestrictionDebugging,
		RestrictionPrivateDNS,
		RestrictionDateTime,
		RestrictionAddUser,
		RestrictionUnknownSources,
		RestrictionUninstallApps,
	})
	if !in.Settings.AllowChildInstalls {
		restrictions.add(RestrictionInstallApps)
	}
	for r := range forbiddenRestrictions {
		restrictions.remove(r)
	}
	out.UserRestrictions = restrictions.sorted()

	// A parent's explicit LOCK_NOW is an instant command, not a policy schedule, so it is honoured
	// in every mode including tracking-only. A parent who presses lock while their child is with a
	// stranger does not care what the enforcement mode is set to.
	out.Locked = in.ParentLock

	out.QuotaMinutes = in.Settings.DailyLimitMinutes
	out.UsedMinutes = in.UsedMinutesToday
	if in.Settings.DailyLimitMinutes > 0 {
		out.RemainingMinutes = max(0, in.Settings.DailyLimitMinutes-in.UsedMinutesToday)
	}

	// ---- tracking-only: measure, do not restrain (FR-8) ----
	//
	// The requirement is exact about which half is disabled: "no quota, bedtime, or app suspension
	// is enforced. Content filtering and hardening remain in effect." So the YouTube killswitch
	// still blocks YouTube's domains and the browser, but does not suspend the app: suspension is
	// suspension whatever caused it.
	if in.Settings.TrackingOnly {
		out.SuspendReason = ReasonNone
		out.SuspendedPackages = []string{}
		out.HiddenPackages = []string{}
		out.PendingApproval = []string{}
		out.NextChangeAt = ""
		return out, nil
	}

	// ---- enforcement ----

	inBedtime := false
	if in.Settings.BedtimeEnabled {
		start, err := parseClock(in.Settings.BedtimeStart)
		if err != nil {
			return DesiredState{}, fmt.Errorf("%w: bedtime_start: %v", ErrInvalidInput, err)
		}
		end, err := parseClock(in.Settings.BedtimeEnd)
		if err != nil {
			return DesiredState{}, fmt.Errorf("%w: bedtime_end: %v", ErrInvalidInput, err)
		}
		// start == end is treated as disabled rather than as a 24-hour window. A window that can
		// never be left is a permanent lockdown reachable by a single typo in the console.
		if start != end {
			inBedtime = withinWindow(minutesOfDay(local), start, end)
		}
	}

	quotaReached := in.Settings.DailyLimitMinutes > 0 && in.UsedMinutesToday >= in.Settings.DailyLimitMinutes

	switch {
	case inBedtime:
		out.SuspendReason = ReasonBedtime
	case quotaReached:
		out.SuspendReason = ReasonQuota
	default:
		out.SuspendReason = ReasonNone
	}

	suspended := newSet(nil)
	hidden := newSet(nil)
	pending := newSet(nil)

	allowed := newSet(in.Settings.AllowedPackages)

	blocked := newSet(in.Settings.BlockedPackages)
	if in.Settings.YouTubeBlocked {
		blocked.addAll(YouTubePackages)
	}
	// The family list is a default for every child, and a child-level ALLOW is the exemption from
	// it (FR-18). One child needing an app the family generally does not want is a real situation,
	// and the alternative — deleting the family entry — would unblock it for everybody.
	//
	// The carve-out is deliberately one-directional: an ALLOW does not lift a BLOCK from this
	// child's own app_rules, and that asymmetry is the point. A family entry is a default somebody
	// set for the household; a child's own BLOCK is a decision somebody made about this child, and
	// a default must never be able to overrule it.
	for _, p := range in.Settings.FamilyBlockedPackages {
		if !allowed.has(p) {
			blocked.add(p)
		}
	}

	// A blocked app is suspended and hidden (FR-5.2), whether or not it is currently installed:
	// the DPC applies the list, and an app that is installed later must already be covered.
	for _, p := range blocked.sorted() {
		suspended.add(p)
		hidden.add(p)
	}

	for _, app := range in.Installed {
		if app.Package == "" {
			continue
		}
		// FR-5.4: with free-installation off, an app the child added after enrollment waits for a
		// parent's decision.
		if !in.Settings.AllowChildInstalls && app.NewSinceBaseline && !app.System &&
			!allowed.has(app.Package) && !blocked.has(app.Package) {
			suspended.add(app.Package)
			pending.add(app.Package)
		}
		// Bedtime or an exhausted quota suspends everything non-exempt (FR-3.4, FR-4.2). An
		// explicit ALLOW rule is the exemption a parent can grant.
		if out.SuspendReason != ReasonNone && !allowed.has(app.Package) {
			suspended.add(app.Package)
		}
	}

	// The whitelist is applied last and unconditionally, so no branch above can outlive it.
	for _, p := range critical.sorted() {
		suspended.remove(p)
		hidden.remove(p)
		pending.remove(p)
	}

	out.SuspendedPackages = suspended.sorted()
	out.HiddenPackages = hidden.sorted()
	out.PendingApproval = pending.sorted()
	out.NextChangeAt = nextChangeAt(in.Settings, local, loc)
	return out, nil
}

// nextChangeAt is the earliest future instant at which Compute would return something different:
// the bedtime edge, or local midnight when a quota is set and the counter resets.
func nextChangeAt(s Settings, local time.Time, loc *time.Location) string {
	var candidates []time.Time

	if s.BedtimeEnabled {
		start, errS := parseClock(s.BedtimeStart)
		end, errE := parseClock(s.BedtimeEnd)
		if errS == nil && errE == nil && start != end {
			if withinWindow(minutesOfDay(local), start, end) {
				candidates = append(candidates, nextOccurrence(local, end, loc))
			} else {
				candidates = append(candidates, nextOccurrence(local, start, loc))
			}
		}
	}
	if s.DailyLimitMinutes > 0 {
		candidates = append(candidates, nextMidnight(local, loc))
	}
	if len(candidates) == 0 {
		return ""
	}
	next := candidates[0]
	for _, c := range candidates[1:] {
		if c.Before(next) {
			next = c
		}
	}
	return next.Format(time.RFC3339)
}

func loadLocation(name string) (*time.Location, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return nil, fmt.Errorf("%w: timezone must not be empty", ErrInvalidInput)
	}
	loc, err := time.LoadLocation(n)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown timezone %q: %v", ErrInvalidInput, name, err)
	}
	return loc, nil
}

// parseClock reads "HH:MM" into minutes since local midnight.
func parseClock(v string) (int, error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("%q is not HH:MM", v)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", v)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", v)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%q is out of range", v)
	}
	return h*60 + m, nil
}

func minutesOfDay(t time.Time) int { return t.Hour()*60 + t.Minute() }

// withinWindow reports whether m falls in [start, end), handling a window that crosses midnight.
func withinWindow(m, start, end int) bool {
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// nextOccurrence is the next local wall-clock time strictly after local. time.Date is used rather
// than adding a duration so that a DST transition inside the interval does not shift the result by
// an hour.
func nextOccurrence(local time.Time, minute int, loc *time.Location) time.Time {
	y, mo, d := local.Date()
	t := time.Date(y, mo, d, minute/60, minute%60, 0, 0, loc)
	if !t.After(local) {
		t = time.Date(y, mo, d+1, minute/60, minute%60, 0, 0, loc)
	}
	return t
}

func nextMidnight(local time.Time, loc *time.Location) time.Time {
	y, mo, d := local.Date()
	return time.Date(y, mo, d+1, 0, 0, 0, 0, loc)
}

// normalizeDomain matches store.NormalizeDomain. It is duplicated rather than imported because the
// engine must not depend on the database layer; the two are held together by a test.
func normalizeDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "https://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	return strings.TrimSuffix(d, ".")
}

// ---- a small ordered set, so every output is deterministic ----

type set map[string]struct{}

func newSet(items []string) set {
	s := make(set, len(items))
	s.addAll(items)
	return s
}

func (s set) add(v string) {
	if v != "" {
		s[v] = struct{}{}
	}
}

func (s set) addAll(items []string) {
	for _, v := range items {
		s.add(v)
	}
}

func (s set) remove(v string)   { delete(s, v) }
func (s set) has(v string) bool { _, ok := s[v]; return ok }

func (s set) sorted() []string {
	out := make([]string, 0, len(s))
	for v := range s {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// normalizeManagedApps sorts the declared set and drops what a device could not act on.
//
// Sorted and never nil for the same reason every other slice here is: the Kotlin engine has to
// produce byte-identical JSON for the shared vectors, and "nil vs empty" is a difference that only
// shows up in the comparison and never in a test that reads the field.
//
// An entry missing its package name, its URL or its checksum is DROPPED rather than passed through.
// The phone cannot install it — there is nothing to fetch, or nothing to verify against — so
// carrying it would produce a device that reports a failure every sync, forever, about a row nobody
// can see is malformed. Dropping it makes the app simply absent, which is the state the console
// already renders as "not available".
//
// Duplicates by package name collapse to the highest version code. Two rows for one package is not
// reachable through the API (a child's set is keyed by package), but the engine is also fed by
// vectors and by whatever a future caller builds, and "install both versions of one package" is not
// a thing a phone can do.
func normalizeManagedApps(apps []ManagedApp) []ManagedApp {
	best := make(map[string]ManagedApp, len(apps))
	for _, a := range apps {
		a.PackageName = strings.TrimSpace(a.PackageName)
		a.URL = strings.TrimSpace(a.URL)
		a.Checksum = strings.TrimSpace(a.Checksum)
		if a.PackageName == "" || a.URL == "" || a.Checksum == "" {
			continue
		}
		if existing, ok := best[a.PackageName]; ok && existing.VersionCode >= a.VersionCode {
			continue
		}
		best[a.PackageName] = a
	}
	out := make([]ManagedApp, 0, len(best))
	for _, a := range best {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PackageName < out[j].PackageName })
	return out
}
