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
	rows, err := s.pool.Query(ctx,
		`SELECT device_id, day::text, package_name, foreground_ms
		   FROM usage_samples WHERE device_id = $1 AND day = $2::date
		  ORDER BY foreground_ms DESC`, deviceID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageSample{}
	for rows.Next() {
		var u UsageSample
		if err := rows.Scan(&u.DeviceID, &u.Day, &u.PackageName, &u.ForegroundMs); err != nil {
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
				`INSERT INTO installed_apps (device_id, package_name, label, system_app, baseline, hidden, suspended)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)
				 ON CONFLICT (device_id, package_name) DO UPDATE
				   SET label        = COALESCE(NULLIF(EXCLUDED.label, ''), installed_apps.label),
				       system_app   = EXCLUDED.system_app,
				       hidden       = EXCLUDED.hidden,
				       suspended    = EXCLUDED.suspended,
				       last_seen_at = NOW(),
				       removed_at   = NULL`,
				deviceID, a.PackageName, a.Label, a.SystemApp, baseline, a.Hidden, a.Suspended); err != nil {
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
		`SELECT device_id, package_name, label, system_app, baseline, hidden, suspended,
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
			&a.Hidden, &a.Suspended, &a.FirstSeenAt, &a.LastSeenAt, &a.RemovedAt); err != nil {
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
