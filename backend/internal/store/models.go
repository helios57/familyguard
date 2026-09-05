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
const (
	ActionAllow = "ALLOW"
	ActionBlock = "BLOCK"
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

	// AppVersionName and AppVersionCode are the DPC build actually running on the phone, as the
	// phone reports it. They exist because the APK this server hosts is installed out of band —
	// it is a file on the node, not part of the image — so before this, nothing anywhere could
	// answer "is the phone running the build the server is serving?". An empty name and a zero
	// code mean a device that has not reported one yet, which is not the same as a device on
	// version zero and is rendered as "not reported".
	AppVersionName string `json:"app_version_name"`
	AppVersionCode int64  `json:"app_version_code"`
}

// Policy is a child's governance settings. DailyLimitMinutes of 0 means "no quota".
type Policy struct {
	ChildID            uuid.UUID `json:"child_id"`
	TrackingOnly       bool      `json:"tracking_only"`
	AllowChildInstalls bool      `json:"allow_child_installs"`
	YouTubeBlocked     bool      `json:"youtube_blocked"`
	DailyLimitMinutes  int       `json:"daily_limit_minutes"`
	BedtimeEnabled     bool      `json:"bedtime_enabled"`
	BedtimeStart       string    `json:"bedtime_start"`
	BedtimeEnd         string    `json:"bedtime_end"`
	DNSHost            string    `json:"dns_host"`
	Timezone           string    `json:"timezone"`
	Version            int64     `json:"version"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type AppRule struct {
	ChildID     uuid.UUID `json:"child_id"`
	PackageName string    `json:"package_name"`
	Action      string    `json:"action"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type InstalledApp struct {
	DeviceID    uuid.UUID  `json:"device_id"`
	PackageName string     `json:"package_name"`
	Label       string     `json:"label"`
	SystemApp   bool       `json:"system_app"`
	Baseline    bool       `json:"baseline"`
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
