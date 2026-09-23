package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecordUsage merges a device's per-day, per-package foreground totals.
//
// The merge takes the greater of the stored and reported value. Android's UsageStats counters are
// cumulative for the day, so a reboot or a counter reset must never be able to lower a total the
// server already saw — that would hand the child a way to earn screen time back by rebooting
// (FR-3.2).
func (s *Store) RecordUsage(ctx context.Context, deviceID uuid.UUID, day string, samples map[string]int64) error {
	if len(samples) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		for pkg, ms := range samples {
			if ms < 0 {
				continue
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO usage_samples (device_id, day, package_name, foreground_ms)
				 VALUES ($1, $2::date, $3, $4)
				 ON CONFLICT (device_id, day, package_name) DO UPDATE
				   SET foreground_ms = GREATEST(usage_samples.foreground_ms, EXCLUDED.foreground_ms),
				       updated_at    = NOW()`,
				deviceID, day, pkg, ms); err != nil {
				return err
			}
		}
		return nil
	})
}

// UsageForDay returns the per-package totals a device reported for one day.
func (s *Store) UsageForDay(ctx context.Context, deviceID uuid.UUID, day string) ([]UsageSample, error) {
	// LEFT JOIN, never an inner one: a package the child has since uninstalled still has the
	// minutes it burned today, and dropping those rows would quietly shrink the day's total below
	// the number the same table reports as screen time.
	rows, err := s.pool.Query(ctx,
		`SELECT u.device_id, u.day::text, u.package_name, u.foreground_ms,
		        COALESCE(i.label, ''), COALESCE(i.system_app, false)
		   FROM usage_samples u
		   LEFT JOIN installed_apps i
		          ON i.device_id = u.device_id AND i.package_name = u.package_name
		  WHERE u.device_id = $1 AND u.day = $2::date
		  ORDER BY u.foreground_ms DESC`, deviceID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageSample{}
	for rows.Next() {
		var u UsageSample
		if err := rows.Scan(&u.DeviceID, &u.Day, &u.PackageName, &u.ForegroundMs, &u.Label, &u.SystemApp); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageMinutesForDay is the total screen time a device recorded on one day, in whole minutes.
// A day with no samples is 0 used, not "unknown" — the device reports every heartbeat, so an
// absent row means no foreground activity was seen.
func (s *Store) UsageMinutesForDay(ctx context.Context, deviceID uuid.UUID, day string) (int, error) {
	var ms int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(foreground_ms), 0) FROM usage_samples WHERE device_id = $1 AND day = $2::date`,
		deviceID, day).Scan(&ms)
	if err != nil {
		return 0, err
	}
	return int(ms / 60000), nil
}

// UsageMinutesByPackageForDay is the per-package foreground time a device recorded on one day, in
// whole minutes.
//
// Floor division in SQL, matching UsageMinutesForDay above and the device's own conversion, so a
// per-app allowance is spent at the same instant whichever of the two engines is asked.
//
// The whole day is returned rather than only the packages that have an allowance: filtering here
// would make this result depend on policy, and the engine is the one place that should decide
// anything.
func (s *Store) UsageMinutesByPackageForDay(ctx context.Context, deviceID uuid.UUID, day string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT package_name, foreground_ms / 60000 FROM usage_samples
		  WHERE device_id = $1 AND day = $2::date`, deviceID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var pkg string
		var minutes int64
		if err := rows.Scan(&pkg, &minutes); err != nil {
			return nil, err
		}
		out[pkg] = int(minutes)
	}
	return out, rows.Err()
}

// UsageHistory returns daily totals in minutes for the last n days, oldest first.
type UsageDay struct {
	Day     string `json:"day"`
	Minutes int    `json:"minutes"`
}

// UsageHistory returns one row per day that has data, for the console's chart.
func (s *Store) UsageHistory(ctx context.Context, deviceID uuid.UUID, days int) ([]UsageDay, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT day::text, COALESCE(SUM(foreground_ms), 0) / 60000
		   FROM usage_samples
		  WHERE device_id = $1 AND day >= (CURRENT_DATE - ($2::int - 1))
		  GROUP BY day ORDER BY day`, deviceID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageDay{}
	for rows.Next() {
		var d UsageDay
		var m int64
		if err := rows.Scan(&d.Day, &m); err != nil {
			return nil, err
		}
		d.Minutes = int(m)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReplaceInstalledApps records the device's current inventory. Packages that were present before
// and are absent now are stamped removed_at rather than deleted, so an app that was uninstalled to
// dodge a block stays visible in the console.
//
// The FIRST report a device sends establishes the baseline: those rows are the apps the child
// already had, and no later policy change may put them in front of a parent for approval (FR-5.4).
// The flag is written once, inside this transaction, and never recomputed — deriving it from
// first_seen_at against enrolled_at reads every app of that first report as new, because the report
// necessarily arrives after enrolment.
func (s *Store) ReplaceInstalledApps(ctx context.Context, deviceID uuid.UUID, apps []InstalledApp) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		var known bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM installed_apps WHERE device_id = $1)`,
			deviceID).Scan(&known); err != nil {
			return err
		}
		baseline := !known

		seen := make([]string, 0, len(apps))
		for _, a := range apps {
			if a.PackageName == "" {
				continue
			}
			seen = append(seen, a.PackageName)
			if _, err := tx.Exec(ctx,
				`INSERT INTO installed_apps (device_id, package_name, label, system_app, baseline, hidden, suspended, launchable)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				 ON CONFLICT (device_id, package_name) DO UPDATE
				   SET label        = COALESCE(NULLIF(EXCLUDED.label, ''), installed_apps.label),
				       system_app   = EXCLUDED.system_app,
				       launchable   = COALESCE(EXCLUDED.launchable, installed_apps.launchable),
				       hidden       = EXCLUDED.hidden,
				       suspended    = EXCLUDED.suspended,
				       last_seen_at = NOW(),
				       removed_at   = NULL`,
				deviceID, a.PackageName, a.Label, a.SystemApp, baseline, a.Hidden, a.Suspended, a.Launchable); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx,
			`UPDATE installed_apps SET removed_at = NOW()
			  WHERE device_id = $1 AND removed_at IS NULL AND NOT (package_name = ANY($2::text[]))`,
			deviceID, seen)
		return err
	})
}

// ListInstalledApps returns the inventory for a device.
func (s *Store) ListInstalledApps(ctx context.Context, deviceID uuid.UUID, includeSystem bool) ([]InstalledApp, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT device_id, package_name, label, system_app, baseline, hidden, suspended, launchable,
		        first_seen_at, last_seen_at, removed_at
		   FROM installed_apps
		  WHERE device_id = $1 AND ($2::bool OR NOT system_app)
		  ORDER BY system_app, lower(COALESCE(NULLIF(label, ''), package_name))`,
		deviceID, includeSystem)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledApp{}
	for rows.Next() {
		var a InstalledApp
		if err := rows.Scan(&a.DeviceID, &a.PackageName, &a.Label, &a.SystemApp, &a.Baseline,
			&a.Hidden, &a.Suspended, &a.Launchable, &a.FirstSeenAt, &a.LastSeenAt, &a.RemovedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddLocation stores one position report.
func (s *Store) AddLocation(ctx context.Context, deviceID uuid.UUID, lat, lon float64, accuracy *float64, capturedAt time.Time) (*Location, error) {
	l := Location{ID: uuid.New(), DeviceID: deviceID, Latitude: lat, Longitude: lon, AccuracyM: accuracy, CapturedAt: capturedAt}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO locations (id, device_id, latitude, longitude, accuracy_m, captured_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		l.ID, l.DeviceID, l.Latitude, l.Longitude, l.AccuracyM, l.CapturedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// ListLocations returns recent positions, newest first.
func (s *Store) ListLocations(ctx context.Context, deviceID uuid.UUID, limit int) ([]Location, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, device_id, latitude, longitude, accuracy_m, captured_at
		   FROM locations WHERE device_id = $1 ORDER BY captured_at DESC LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.DeviceID, &l.Latitude, &l.Longitude, &l.AccuracyM, &l.CapturedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PruneLocations deletes position history older than the retention window (NFR-8).
func (s *Store) PruneLocations(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM locations WHERE captured_at < NOW() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RecordUsageSessions stores what ran when (FR-3.7).
//
// The merge keeps the LATER end for a session the server already has. A phone re-sends what it
// could not deliver, and a session is identified by the instant it began, so a re-send is the same
// sitting — possibly observed for longer the second time, never for less. Taking the reported value
// unconditionally would let a truncated retry shorten a sitting the server had already seen whole.
//
// A session whose end is not after its start is dropped here rather than rejected: the device
// filters them, the table refuses them, and a request is not worth failing over a transition the
// platform happened to report twice.
func (s *Store) RecordUsageSessions(ctx context.Context, deviceID uuid.UUID, sessions []UsageSession) error {
	if len(sessions) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		for _, sess := range sessions {
			if sess.PackageName == "" || !sess.EndedAt.After(sess.StartedAt) {
				continue
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO usage_sessions (device_id, package_name, started_at, ended_at)
				 VALUES ($1, $2, $3, $4)
				 ON CONFLICT (device_id, package_name, started_at) DO UPDATE
				   SET ended_at    = GREATEST(usage_sessions.ended_at, EXCLUDED.ended_at),
				       reported_at = NOW()`,
				deviceID, sess.PackageName, sess.StartedAt.UTC(), sess.EndedAt.UTC()); err != nil {
				return err
			}
		}
		return nil
	})
}

// UsageSessionsBetween returns every session that OVERLAPS the window, oldest first.
//
// Overlap rather than containment, because a sitting that began at 23:50 belongs to both days it
// touches and a parent looking at either one should see it. The console clamps what it draws to the
// day it is showing; the times returned here are the real ones, so the card can say "started
// 23:50 yesterday" rather than inventing a start at midnight.
func (s *Store) UsageSessionsBetween(ctx context.Context, deviceID uuid.UUID, from, to time.Time) ([]UsageSession, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT u.package_name, u.started_at, u.ended_at,
		        COALESCE(i.label, ''), COALESCE(i.system_app, false)
		   FROM usage_sessions u
		   LEFT JOIN installed_apps i
		          ON i.device_id = u.device_id AND i.package_name = u.package_name
		  WHERE u.device_id = $1 AND u.ended_at > $2 AND u.started_at < $3
		  ORDER BY u.started_at`, deviceID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageSession{}
	for rows.Next() {
		var u UsageSession
		if err := rows.Scan(&u.PackageName, &u.StartedAt, &u.EndedAt, &u.Label, &u.SystemApp); err != nil {
			return nil, err
		}
		u.Seconds = int(u.EndedAt.Sub(u.StartedAt).Seconds())
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageSessionsEverReported says whether this device has EVER filed a session.
//
// The console needs it to tell two states apart that look identical in an empty list: a child who
// did not pick up the phone, and a phone running a DPC too old to report sessions at all. Drawing
// "nothing ran today" over the second is the same defect as a console that guesses — see FR-6.11.
func (s *Store) UsageSessionsEverReported(ctx context.Context, deviceID uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM usage_sessions WHERE device_id = $1)`, deviceID).Scan(&exists)
	return exists, err
}
