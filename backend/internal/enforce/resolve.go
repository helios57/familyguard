// Package enforce turns stored state into the device's desired state.
//
// It is the only place that knows how database rows map onto the enforcement engine's input. The
// engine itself stays pure — no clock, no database — so that the identical computation can run on
// the phone when the network is gone. This package supplies the impure half: which rows, for which
// day, in which time zone.
//
// The day boundary is the subtle part and it is why this package exists at all. A daily quota is a
// wall-clock local notion: the child's day rolls over at local midnight, not at 00:00 UTC. Reading
// usage for the UTC day would hand a Zurich child a second full quota every evening in summer, and
// silently shorten their morning in winter.
package enforce

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/policy"
	"github.com/helios57/familyguard/backend/internal/store"
)

// Source is the slice of the store this package reads. It is an interface so the mapping can be
// tested without a database: the logic worth testing here (the day key, what counts as new since
// the inventory baseline, which apps are excluded) is decided before any SQL runs, and a test that
// needed Postgres to reach it would not be run often enough to matter.
//
// The limit of that trade-off is recorded in TestNoveltyComesFromTheBaselineFlagNotATimestamp: a
// fake Source can be handed rows the real store never writes, and one such row kept a broken FR-5.4
// rule green. Where a field's value is produced by SQL, the end-to-end suite is the authority.
type Source interface {
	GetDevice(ctx context.Context, id uuid.UUID) (*store.Device, error)
	GetPolicy(ctx context.Context, childID uuid.UUID) (*store.Policy, error)
	ListAppRules(ctx context.Context, childID uuid.UUID) ([]store.AppRule, error)
	ListBlockedDomains(ctx context.Context, childID uuid.UUID) ([]string, error)
	ListInstalledApps(ctx context.Context, deviceID uuid.UUID, includeSystem bool) ([]store.InstalledApp, error)
	UsageMinutesForDay(ctx context.Context, deviceID uuid.UUID, day string) (int, error)
	ManagedAppsForChild(ctx context.Context, childID uuid.UUID) ([]store.App, error)
}

// Resolver computes a device's desired state from stored policy and telemetry.
type Resolver struct {
	src Source
	// baseURL is where the phone can reach this control plane, with no trailing slash. It is here
	// rather than in the engine because it is deployment configuration, not policy: the same
	// declared set resolves to different URLs on dev and on the family's own server, and the engine
	// has to stay a pure function that the phone can run offline with the same result.
	baseURL string
}

// New builds a Resolver over any Source. *store.Store satisfies Source.
//
// baseURL is the public origin the device reaches — the same one the provisioning payload and
// /device/apk-info publish, so a phone never has to join a base to a path or guess a scheme.
func New(src Source, baseURL string) *Resolver {
	return &Resolver{src: src, baseURL: strings.TrimSuffix(baseURL, "/")}
}

// ManagedAppDownloadPath is where a device fetches one managed application. Exported because the
// route registration and this URL builder must be the same shape, and a second copy of a path
// string is a 404 that only appears on a phone.
func ManagedAppDownloadPath(packageName string, versionCode int64) string {
	return fmt.Sprintf("/api/v1/device/apps/%s/%d.apk", url.PathEscape(packageName), versionCode)
}

// managedApps turns catalog rows into what the phone needs to fetch and verify each one.
func (r *Resolver) managedApps(rows []store.App) []policy.ManagedApp {
	out := make([]policy.ManagedApp, 0, len(rows))
	for _, a := range rows {
		checksum, err := checksumB64(a.SHA256)
		if err != nil {
			// A row whose stored digest is not a SHA-256 cannot be verified on the phone, and the
			// engine drops an entry with an empty checksum for exactly that reason. Skipped here so
			// the reason is one thing rather than two.
			continue
		}
		out = append(out, policy.ManagedApp{
			PackageName: a.PackageName,
			VersionCode: a.VersionCode,
			VersionName: a.VersionName,
			Checksum:    checksum,
			Size:        a.SizeBytes,
			URL:         r.baseURL + ManagedAppDownloadPath(a.PackageName, a.VersionCode),
		})
	}
	return out
}

// checksumB64 re-encodes the catalog's hex digest as the base64url the device compares against.
//
// Two encodings of one number is a smell, and this is the seam where it is paid for rather than
// spread: the catalog stores hex because that is what every other tool prints (sha256sum,
// apksigner, the console), and the device receives base64url because that is the format
// /device/apk-info already publishes and the DPC already computes. Converting here means the phone
// has one comparison and the operator has one readable digest.
func checksumB64(hexDigest string) (string, error) {
	raw, err := hex.DecodeString(hexDigest)
	if err != nil {
		return "", err
	}
	if len(raw) != sha256.Size {
		return "", fmt.Errorf("digest is %d bytes, not a SHA-256", len(raw))
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Resolve reads every input the engine needs and computes the desired state for one device at one
// instant.
//
// now is passed in rather than read from the clock so that a caller can ask "what would apply at
// 21:00?" — the console's bedtime preview and the end-to-end suite both need that, and a package
// that calls time.Now internally cannot answer it.
func (r *Resolver) Resolve(ctx context.Context, deviceID uuid.UUID, now time.Time) (*policy.DesiredState, *policy.Input, error) {
	dev, err := r.src.GetDevice(ctx, deviceID)
	if err != nil {
		return nil, nil, fmt.Errorf("device: %w", err)
	}
	pol, err := r.src.GetPolicy(ctx, dev.ChildID)
	if err != nil {
		return nil, nil, fmt.Errorf("policy: %w", err)
	}

	// The zone is resolved here, before anything else, because the day key depends on it. An
	// unknown zone is an error rather than a fallback to UTC: a silent fallback would move the
	// quota reset and bedtime by up to a day, and nothing would look wrong.
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: timezone %q: %v", policy.ErrInvalidInput, pol.Timezone, err)
	}
	day := now.In(loc).Format("2006-01-02")

	used, err := r.src.UsageMinutesForDay(ctx, deviceID, day)
	if err != nil {
		return nil, nil, fmt.Errorf("usage: %w", err)
	}
	rules, err := r.src.ListAppRules(ctx, dev.ChildID)
	if err != nil {
		return nil, nil, fmt.Errorf("app rules: %w", err)
	}
	domains, err := r.src.ListBlockedDomains(ctx, dev.ChildID)
	if err != nil {
		return nil, nil, fmt.Errorf("blocked domains: %w", err)
	}
	apps, err := r.src.ListInstalledApps(ctx, deviceID, true)
	if err != nil {
		return nil, nil, fmt.Errorf("installed apps: %w", err)
	}
	managed, err := r.src.ManagedAppsForChild(ctx, dev.ChildID)
	if err != nil {
		return nil, nil, fmt.Errorf("managed apps: %w", err)
	}

	blocked, allowed := splitRules(rules)
	in := policy.Input{
		Settings: policy.Settings{
			TrackingOnly:       pol.TrackingOnly,
			AllowChildInstalls: pol.AllowChildInstalls,
			YouTubeBlocked:     pol.YouTubeBlocked,
			DailyLimitMinutes:  pol.DailyLimitMinutes,
			BedtimeEnabled:     pol.BedtimeEnabled,
			BedtimeStart:       pol.BedtimeStart,
			BedtimeEnd:         pol.BedtimeEnd,
			DNSHost:            pol.DNSHost,
			Timezone:           pol.Timezone,
			Version:            pol.Version,
			BlockedPackages:    blocked,
			AllowedPackages:    allowed,
			BlockedDomains:     domains,
			ManagedApps:        r.managedApps(managed),
		},
		Installed:        installedApps(apps),
		UsedMinutesToday: used,
		ParentLock:       dev.Locked,
		CriticalPackages: dev.CriticalPackages,
		Now:              now.In(loc).Format(time.RFC3339),
	}

	out, err := policy.Compute(in)
	if err != nil {
		return nil, &in, err
	}
	return &out, &in, nil
}

// DayKey is the calendar day a moment falls on in a policy's time zone. Handlers that record usage
// use it so that the day a sample is filed under is the same day the quota is read from — two
// different rules for the same boundary would make a quota that resets at the wrong hour, which is
// invisible until a child is locked out at breakfast.
func DayKey(pol *store.Policy, at time.Time) (string, error) {
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		return "", fmt.Errorf("%w: timezone %q: %v", policy.ErrInvalidInput, pol.Timezone, err)
	}
	return at.In(loc).Format("2006-01-02"), nil
}

func splitRules(rules []store.AppRule) (blocked, allowed []string) {
	blocked, allowed = []string{}, []string{}
	for _, r := range rules {
		switch r.Action {
		case store.ActionBlock:
			blocked = append(blocked, r.PackageName)
		case store.ActionAllow:
			allowed = append(allowed, r.PackageName)
		}
	}
	return blocked, allowed
}

// installedApps maps the inventory the device reported onto the engine's view of it.
//
// Two decisions are encoded here. An app the device has since uninstalled is dropped: leaving it in
// would keep it in the suspend list forever, and the console would show a rule against something
// that is not there. And novelty is read from the baseline flag the store wrote on the device's
// first inventory report — never recomputed from a timestamp here. Comparing first_seen_at against
// devices.enrolled_at looks equivalent and is not: the first report arrives after enrolment, so
// every app on the phone reads as new and turning free installation off sweeps the whole
// preinstalled catalogue into the approval queue (FR-5.4).
func installedApps(apps []store.InstalledApp) []policy.App {
	out := make([]policy.App, 0, len(apps))
	for _, a := range apps {
		if a.RemovedAt != nil {
			continue
		}
		out = append(out, policy.App{
			Package:          a.PackageName,
			System:           a.SystemApp,
			NewSinceBaseline: !a.Baseline,
		})
	}
	return out
}
