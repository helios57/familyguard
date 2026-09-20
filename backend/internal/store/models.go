package store

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Parent roles. Only PRIMARY_ADMIN may add or remove parents; the roles are otherwise equal,
// because a guardian who cannot act in an emergency is worse than no guardian (REQUIREMENTS §2).
const (
	RolePrimaryAdmin = "PRIMARY_ADMIN"
	RoleAdmin        = "ADMIN"
	RoleGuardian     = "GUARDIAN"
)

// Command lifecycle states. A command is created QUEUED and only the hub advances it, so no
// handler can report a delivery that did not happen (NFR-3).
const (
	CmdQueued    = "QUEUED"
	CmdDelivered = "DELIVERED"
	CmdAcked     = "ACKED"
	CmdFailed    = "FAILED"
	CmdExpired   = "EXPIRED"
)

// Instant command types (FR-9).
const (
	CmdTypeLockNow        = "LOCK_NOW"
	CmdTypeUnlockDevice   = "UNLOCK_DEVICE"
	CmdTypeTriggerAlarm   = "TRIGGER_ALARM"
	CmdTypeStopAlarm      = "STOP_ALARM"
	CmdTypeLocateNow      = "LOCATE_NOW"
	CmdTypeBlockYouTube   = "BLOCK_YOUTUBE_ALL"
	CmdTypeUnblockYouTube = "UNBLOCK_YOUTUBE_ALL"
	CmdTypeSyncPolicy     = "SYNC_POLICY"
	// CmdTypeUpdateApp tells the phone to fetch the DPC this server is hosting and install it over
	// itself. It is the only command whose success the acknowledgement cannot report: applying it
	// kills the process that would send the acknowledgement, so the device answers "downloaded and
	// verified, installing" and the *next heartbeat* — carrying app_version_code — is what says
	// whether it worked.
	CmdTypeUpdateApp = "UPDATE_APP"
)

// ValidCommandTypes is the closed set the API accepts. An unknown type is a 400, not a queued row
// that the device will silently ignore forever.
var ValidCommandTypes = map[string]bool{
	CmdTypeLockNow:        true,
	CmdTypeUnlockDevice:   true,
	CmdTypeTriggerAlarm:   true,
	CmdTypeStopAlarm:      true,
	CmdTypeLocateNow:      true,
	CmdTypeBlockYouTube:   true,
	CmdTypeUnblockYouTube: true,
	CmdTypeSyncPolicy:     true,
	CmdTypeUpdateApp:      true,
}

// App rule actions.
//
// The three are not a scale from permissive to strict; they answer different questions.
// ActionAllow is the whitelist — an allowed app is exempt from bedtime and from the daily limit.
// ActionLimit is approval WITHOUT that exemption, which is the ordinary case and which had no way
// to be expressed until migration 0013: before it, approving an app and exempting it from every
// schedule were the same keystroke. ActionBlock suspends and hides.
//
// No rule at all is a fourth state and is not one of these: with free installation off it is what
// keeps a newly installed app in pending_approval.
const (
	ActionAllow = "ALLOW"
	ActionBlock = "BLOCK"
	ActionLimit = "LIMIT"
)

type Family struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Parent struct {
	ID          uuid.UUID  `json:"id"`
	FamilyID    uuid.UUID  `json:"family_id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	GoogleSub   string     `json:"-"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type Child struct {
	ID        uuid.UUID `json:"id"`
	FamilyID  uuid.UUID `json:"family_id"`
	Name      string    `json:"name"`
	BirthYear *int      `json:"birth_year,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Device struct {
	ID        uuid.UUID `json:"id"`
	ChildID   uuid.UUID `json:"child_id"`
	Name      string    `json:"name"`
	Model     string    `json:"model"`
	OSVersion string    `json:"os_version"`
	// Locked is the parent's explicit lock (FR-9.1), held as state so it survives a reboot and a
	// command expiry. Bedtime and quota do not set it: those suspend apps instead, which keeps the
	// dialer reachable (NFR-6).
	Locked bool `json:"locked"`
	// CriticalPackages is what the device reported as unsuspendable on its own hardware. It only
	// ever widens the built-in list.
	CriticalPackages []string   `json:"critical_packages,omitempty"`
	EnrolledAt       *time.Time `json:"enrolled_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// DeviceState is the last telemetry the device reported. Online is derived from LastSeenAt against
// the configured threshold rather than stored, so a server restart cannot leave a dead device
// marked online forever.
type DeviceState struct {
	DeviceID      uuid.UUID  `json:"device_id"`
	BatteryLevel  *int       `json:"battery_level,omitempty"`
	Charging      *bool      `json:"charging,omitempty"`
	ScreenOn      *bool      `json:"screen_on,omitempty"`
	Connectivity  string     `json:"connectivity"`
	PolicyVersion int64      `json:"policy_version"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	Online        bool       `json:"online"`

	// UsageAccess is whether the phone may read usage stats (FR-3.6). Nil means it has not said —
	// an older DPC does not send it — and nil, false and true are three different things here:
	// only false is "this phone is reporting zero minutes because it can see nothing".
	UsageAccess *bool `json:"usage_access,omitempty"`

	// PowerExempt and ExactAlarms are whether Android is letting this phone's DPC keep its own
	// schedule. Nil in both means the phone has not said; only a measured false is a finding.
	//
	// They exist because a deferred alarm has no error. Measured on the pilot phone 2026-09-07: the
	// DPC's 15-minute update check fired 6m51s, 21m44s and 8m20s late, and the event stream's
	// one-second reconnect took 83 s to 495 s over five sleeping cycles against 1.5 s while it was
	// awake — so a parent pressing Ring waited minutes, and every log on both sides was clean.
	// From the server a battery-restricted phone and an offline one are the same shape, and these
	// two fields are the only thing that tells them apart.
	//
	// Separate rather than one "restricted" flag because the remedies differ: PowerExempt is the
	// ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS dialog (API 23+, so it holds at the API 29 floor),
	// while ExactAlarms is SCHEDULE_EXACT_ALARM and only exists from API 31. Reporting one number
	// would tell a parent something is wrong without telling them which switch to find.
	PowerExempt *bool `json:"power_exempt,omitempty"`
	ExactAlarms *bool `json:"exact_alarms,omitempty"`

	// AdFilterRules, AdFilterFetchedAt and AdFilterRunning are what the PHONE says about its ad
	// filter (FR-6.6), which is a different question from Policy.AdFilter — that one says a parent
	// turned the switch on.
	//
	// The switch being on does not mean the phone has a list, that the list compiled, or that the
	// tunnel came up, and every one of those fails quietly: a captive portal serves a login page
	// with a perfectly good 200, the watchdog stands down a tunnel that carried nothing, a platform
	// can decline always-on. Without these the console would show "Ad filter: on" over a phone
	// filtering nothing.
	//
	// Nil is "this phone has not said" — an older DPC does not send them — and is a third state,
	// distinct from zero rules. Only a measured zero is a finding.
	AdFilterRules     *int       `json:"ad_filter_rules,omitempty"`
	AdFilterFetchedAt *time.Time `json:"ad_filter_fetched_at,omitempty"`
	AdFilterRunning   *bool      `json:"ad_filter_running,omitempty"`

	// AdFilterReason is why no tunnel is running, in the phone's own words (FR-6.11).
	//
	// AdFilterRunning says whether one is up; this says which of several unrelated faults is
	// holding it, and they have different remedies — no list fetched yet, the phone has not allowed
	// the connection, a watchdog stood down a tunnel that carried nothing, the network named no
	// resolver to forward to. Empty is nothing to say: a tunnel that is up, or a DPC that predates
	// the field. Never a reason of its own.
	AdFilterReason string `json:"ad_filter_reason,omitempty"`

	// AppVersionName and AppVersionCode are the DPC build actually running on the phone, as the
	// phone reports it. They exist because the APK this server hosts is installed out of band —
	// it is a file on the node, not part of the image — so before this, nothing anywhere could
	// answer "is the phone running the build the server is serving?". An empty name and a zero
	// code mean a device that has not reported one yet, which is not the same as a device on
	// version zero and is rendered as "not reported".
	AppVersionName string `json:"app_version_name"`
	AppVersionCode int64  `json:"app_version_code"`

	// UpdateError is why the phone's last self-update did not end with a new build running, in the
	// platform's own words, and "" when there is nothing to report (FR-15.7). It exists because the
	// UPDATE_APP acknowledgement is sent before the install and therefore proves nothing about it:
	// the failure it now carries was, before this field, visible only as a version that never
	// changed on a phone whose every other signal was green.
	UpdateError   string     `json:"update_error"`
	UpdateErrorAt *time.Time `json:"update_error_at,omitempty"`

	// ReportedUpdateError is the WRITE side of the field above, and it is separate because the two
	// have different shapes for a reason. Reading, the column is NOT NULL and "" means "nothing to
	// report". Writing, there is a third state: a DPC old enough not to know about the field sends
	// no key at all, and that must leave the stored value alone rather than clear it. A single
	// string could not tell those apart, and the one it would get wrong is the one that matters —
	// an old build's heartbeat silently erasing a real failure a newer build reported.
	//
	// Never serialised: it is an input to TouchDevice and nothing reads it back.
	ReportedUpdateError *string `json:"-"`

	// ReportedAdFilterReason is the write side of AdFilterReason, three-valued for exactly the
	// reason above: nil is a DPC that does not know the field and must not clear what a newer one
	// reported, "" is a phone with nothing to say, and text replaces it.
	ReportedAdFilterReason *string `json:"-"`
}

// Policy is a child's governance settings. DailyLimitMinutes of 0 means "no quota".
type Policy struct {
	ChildID            uuid.UUID `json:"child_id"`
	TrackingOnly       bool      `json:"tracking_only"`
	AllowChildInstalls bool      `json:"allow_child_installs"`
	// AllowDebugging leaves developer options and adb switched on (FR-5.6). False everywhere it is
	// not deliberately turned on, because the restriction it withholds is the one that cannot be
	// undone from outside the phone.
	AllowDebugging bool `json:"allow_debugging"`
	// AllowUninstall withholds no_uninstall_apps, so apps can be removed over adb or from Settings
	// (FR-5.7). False everywhere it is not deliberately turned on: uninstalling is how a child
	// escapes a suspension.
	AllowUninstall    bool   `json:"allow_uninstall"`
	YouTubeBlocked    bool   `json:"youtube_blocked"`
	DailyLimitMinutes int    `json:"daily_limit_minutes"`
	BedtimeEnabled    bool   `json:"bedtime_enabled"`
	BedtimeStart      string `json:"bedtime_start"`
	BedtimeEnd        string `json:"bedtime_end"`
	DNSHost           string `json:"dns_host"`
	// AdFilter runs the on-device advertising and tracker filter (FR-6.6 to FR-6.9): a local
	// VpnService that reads the server name out of a TLS ClientHello and resets what a list names.
	// It is the only layer in this product that reaches an advertisement inside a game, because an
	// ad SDK that ships its server's address never asks a resolver for a name.
	AdFilter bool `json:"ad_filter"`
	// AdFilterListURL is where the phone fetches that list. A url and nothing else — this project
	// ships the fetcher and never the data. Empty means the filter cannot run whatever AdFilter
	// says, which is a state the engine computes rather than one the console has to remember.
	AdFilterListURL string    `json:"ad_filter_list_url"`
	Timezone        string    `json:"timezone"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AppRule struct {
	ChildID     uuid.UUID `json:"child_id"`
	PackageName string    `json:"package_name"`
	Action      string    `json:"action"`
	// LimitMinutes is this app's own daily allowance, and is meaningful only with ActionLimit.
	// Zero means the app is governed by the family's shared daily limit and nothing more; a
	// positive value is spent independently of that shared one, so an app can run out while the
	// child still has screen time left.
	LimitMinutes int       `json:"limit_minutes"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// FamilyBlockedPackage is one entry on the family-wide blocklist (FR-18): a package no child in
// this family may use, on any phone, now or after the next child is added.
//
// It carries no child id on purpose. The whole point of the table is that the decision outlives the
// set of children it was made for — an entry that had to name them would be `app_rules` again.
type FamilyBlockedPackage struct {
	PackageName string    `json:"package_name"`
	Label       string    `json:"label"`
	Reason      string    `json:"reason"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
}

// Provenance of a blocklist entry. Both block identically; the distinction is whether a person
// chose it or the curated set in migration 0005 seeded it.
const (
	BlocklistSourceBuiltin = "BUILTIN"
	BlocklistSourceParent  = "PARENT"
)

type InstalledApp struct {
	DeviceID    uuid.UUID `json:"device_id"`
	PackageName string    `json:"package_name"`
	Label       string    `json:"label"`
	SystemApp   bool      `json:"system_app"`
	Baseline    bool      `json:"baseline"`
	// Hidden and Suspended are what the DEVICE reports it is doing, not what the policy asked for
	// (FR-18.6). They are the only evidence the console has that a block took effect.
	Hidden      bool       `json:"hidden"`
	Suspended   bool       `json:"suspended"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	RemovedAt   *time.Time `json:"removed_at,omitempty"`
}

// App is one registered APK in the catalog (FR-16.1).
//
// Every field but Label and Source is read out of the file by internal/apk, never supplied by a
// caller. FileName is relative to the configured APK directory.
type App struct {
	ID           uuid.UUID `json:"id"`
	PackageName  string    `json:"package_name"`
	VersionCode  int64     `json:"version_code"`
	VersionName  string    `json:"version_name"`
	Label        string    `json:"label"`
	SHA256       string    `json:"sha256"`
	SignerSHA256 string    `json:"signer_sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	MinSDK       int       `json:"min_sdk"`
	FileName     string    `json:"file_name"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
}

// Where a catalog entry came from.
const (
	AppSourceNode   = "NODE"
	AppSourceUpload = "UPLOAD"
)

// ErrSignerChanged is returned when a package is registered under a different key from the one
// pinned at its first registration (FR-16.4). It is deliberately distinct from ErrConflict: a
// duplicate version is an operator repeating themselves, and a signer change is either a rebuild
// with a new key or a substituted file, which are the two things a person has to be told apart.
var ErrSignerChanged = errors.New("this package is pinned to a different signing key")

// APIKey is a non-interactive credential that acts as a parent (FR-17).
//
// Token is set only by CreateAPIKey, and only on the response that creates it: the plaintext is
// never stored, so no read can return it.
type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	ParentID   uuid.UUID  `json:"parent_id"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	Token      string     `json:"token,omitempty"`
}

type UsageSample struct {
	DeviceID     uuid.UUID `json:"device_id"`
	Day          string    `json:"day"`
	PackageName  string    `json:"package_name"`
	ForegroundMs int64     `json:"foreground_ms"`
	// Label and SystemApp come from the device's inventory, not from the usage row itself, and are
	// empty/false for a package that has since been uninstalled. They are joined on rather than
	// stored per sample because the label is a property of the install, not of a day's usage — and
	// because without them the console can only print "com.sec.android.app.launcher" at a parent,
	// which is the state this field was added to end.
	Label     string `json:"label"`
	SystemApp bool   `json:"system_app"`
}

type Command struct {
	ID          uuid.UUID      `json:"id"`
	DeviceID    uuid.UUID      `json:"device_id"`
	Type        string         `json:"type"`
	Params      map[string]any `json:"params"`
	State       string         `json:"state"`
	IssuedBy    *uuid.UUID     `json:"issued_by,omitempty"`
	Result      map[string]any `json:"result,omitempty"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	DeliveredAt *time.Time     `json:"delivered_at,omitempty"`
	AckedAt     *time.Time     `json:"acked_at,omitempty"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

type Location struct {
	ID         uuid.UUID `json:"id"`
	DeviceID   uuid.UUID `json:"device_id"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	AccuracyM  *float64  `json:"accuracy_m,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
}

type RecoveryEvent struct {
	ID         uuid.UUID `json:"id"`
	DeviceID   uuid.UUID `json:"device_id"`
	Succeeded  bool      `json:"succeeded"`
	OccurredAt time.Time `json:"occurred_at"`
	ReportedAt time.Time `json:"reported_at"`
}

type AuditEntry struct {
	ID         int64          `json:"id"`
	ActorType  string         `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Detail     map[string]any `json:"detail"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// RecoverySecret is the per-device offline recovery material (FR-12.3). The plaintext Code is
// shown to the parent in the console; only the derived hash reaches the device.
type RecoverySecret struct {
	Code       string
	Salt       []byte
	Iterations int
	Hash       []byte
}

// UsageSession is one stretch of one package being in the foreground, as the platform timestamped
// it on the phone (FR-3.7).
//
// Milliseconds are deliberately not carried to the console: the platform's own event timestamps are
// not that precise, and a timeline that printed 09:12:04.318 would be claiming a precision nobody
// measured. Seconds is what the phone sends and what a parent reads.
type UsageSession struct {
	PackageName string    `json:"package_name"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	Seconds     int       `json:"seconds"`
	// Joined from the device's inventory, exactly as UsageSample does it, and empty for a package
	// that has since been uninstalled.
	Label     string `json:"label"`
	SystemApp bool   `json:"system_app"`
}
