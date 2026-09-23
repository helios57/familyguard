package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaxBonusMinutesPerDay bounds a day's extra time: a day has 1440 minutes, and the column's CHECK
// holds the same number, so a grant past it is refused here in a sentence rather than by Postgres.
const MaxBonusMinutesPerDay = 1440

// ErrBonusTooLarge is a grant that would take the day's bonus past MaxBonusMinutesPerDay.
var ErrBonusTooLarge = errors.New("the bonus for one day cannot exceed 1440 minutes")

// UsageMinutesCountedForDay is UsageMinutesForDay without the packages whose foreground time is not
// use (FR-3.8). Floored the same way, so the phone — which leaves out the same packages from its own
// count — reaches the same number.
func (s *Store) UsageMinutesCountedForDay(ctx context.Context, deviceID uuid.UUID, day string, uncounted []string) (int, error) {
	if uncounted == nil {
		uncounted = []string{}
	}
	var ms int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(foreground_ms), 0) FROM usage_samples
		  WHERE device_id = $1 AND day = $2::date AND NOT (package_name = ANY($3))`,
		deviceID, day, uncounted).Scan(&ms)
	if err != nil {
		return 0, err
	}
	return int(ms / 60000), nil
}

// GrantBonus adds minutes to a child's bonus for one local day and returns the day's new total.
func (s *Store) GrantBonus(ctx context.Context, childID uuid.UUID, day string, minutes int) (int, error) {
	var total int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO bonus_minutes (child_id, day, minutes) VALUES ($1, $2::date, $3)
		 ON CONFLICT (child_id, day) DO UPDATE
		   SET minutes = bonus_minutes.minutes + EXCLUDED.minutes, updated_at = NOW()
		   WHERE bonus_minutes.minutes + EXCLUDED.minutes <= $4
		 RETURNING minutes`,
		childID, day, minutes, MaxBonusMinutesPerDay).Scan(&total)
	if errors.Is(err, pgx.ErrNoRows) {
		// The conditional update matched nothing: the sum would pass the ceiling.
		return 0, ErrBonusTooLarge
	}
	if err != nil {
		return 0, err
	}
	return total, nil
}

// BonusMinutes is a child's bonus for one local day, or 0.
func (s *Store) BonusMinutes(ctx context.Context, childID uuid.UUID, day string) (int, error) {
	var minutes int
	err := s.pool.QueryRow(ctx,
		`SELECT minutes FROM bonus_minutes WHERE child_id = $1 AND day = $2::date`, childID, day).Scan(&minutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return minutes, err
}

// SetHomePackages records what a phone reports as its home screen. nil means "not reported" and
// leaves the stored value alone, so a DPC that predates the field cannot clear it.
func (s *Store) SetHomePackages(ctx context.Context, deviceID uuid.UUID, packages []string) error {
	if packages == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE device_state SET home_packages = $2 WHERE device_id = $1`, deviceID, packages)
	return err
}

// HomePackages is what a phone last reported as its home screen, never nil.
func (s *Store) HomePackages(ctx context.Context, deviceID uuid.UUID) ([]string, error) {
	var out []string
	err := s.pool.QueryRow(ctx,
		`SELECT home_packages FROM device_state WHERE device_id = $1`, deviceID).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return []string{}, nil
	}
	if out == nil {
		out = []string{}
	}
	return out, err
}

// DayLimits is the limit that applied to a child on one day (FR-3.9).
type DayLimits struct {
	Day               string         `json:"day"`
	DailyLimitMinutes int            `json:"daily_limit_minutes"`
	BonusMinutes      int            `json:"bonus_minutes"`
	AppLimits         map[string]int `json:"app_limits"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// RecordDayLimits writes the limits in force on a day. Called while the day is current; the last
// write of a day is the value the day ends with.
func (s *Store) RecordDayLimits(ctx context.Context, childID uuid.UUID, day string, dailyLimit, bonus int, appLimits map[string]int) error {
	if appLimits == nil {
		appLimits = map[string]int{}
	}
	encoded, err := json.Marshal(appLimits)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO day_limits (child_id, day, daily_limit_minutes, bonus_minutes, app_limits)
		 VALUES ($1, $2::date, $3, $4, $5)
		 ON CONFLICT (child_id, day) DO UPDATE
		   SET daily_limit_minutes = EXCLUDED.daily_limit_minutes, bonus_minutes = EXCLUDED.bonus_minutes,
		       app_limits = EXCLUDED.app_limits, updated_at = NOW()`,
		childID, day, dailyLimit, bonus, encoded)
	return err
}

// GetDayLimits is what was recorded for a day, or ErrNotFound for a day before anything recorded
// it — which the console must say as "not recorded", never fill in with today's limit.
func (s *Store) GetDayLimits(ctx context.Context, childID uuid.UUID, day string) (*DayLimits, error) {
	var d DayLimits
	var raw []byte
	var date time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT day, daily_limit_minutes, bonus_minutes, app_limits, updated_at FROM day_limits
		  WHERE child_id = $1 AND day = $2::date`, childID, day).
		Scan(&date, &d.DailyLimitMinutes, &d.BonusMinutes, &raw, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Day = date.Format("2006-01-02")
	d.AppLimits = map[string]int{}
	if err := json.Unmarshal(raw, &d.AppLimits); err != nil {
		return nil, err
	}
	return &d, nil
}

// SortedUnique returns the non-empty values of a, deduplicated and sorted, never nil.
func SortedUnique(a ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range a {
		for _, v := range list {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}
