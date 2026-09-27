package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// AlarmDay is a change to the alarm for one calendar day (FR-23): a time, or nil for no alarm.
type AlarmDay struct {
	Day  string  `json:"day"`
	Time *string `json:"time"`
}

// Alarm is a profile's alarm clock: seven weekdays, Monday first, "" for a day that does not ring,
// and the date changes from a given day on, in date order.
type Alarm struct {
	Weekdays  []string   `json:"weekdays"`
	Overrides []AlarmDay `json:"overrides"`
}

// GetAlarm reads a profile's alarm, with the date changes on or after fromDay.
func (s *Store) GetAlarm(ctx context.Context, childID uuid.UUID, fromDay string) (*Alarm, error) {
	alarm := &Alarm{Weekdays: make([]string, 7), Overrides: []AlarmDay{}}
	rows, err := s.pool.Query(ctx, `SELECT weekday, time FROM alarm_times WHERE child_id = $1`, childID)
	if err != nil {
		return nil, fmt.Errorf("reading alarm times: %w", err)
	}
	for rows.Next() {
		var weekday int
		var at string
		if err := rows.Scan(&weekday, &at); err != nil {
			rows.Close()
			return nil, err
		}
		alarm.Weekdays[weekday-1] = at
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT to_char(day, 'YYYY-MM-DD'), time FROM alarm_overrides
		WHERE child_id = $1 AND day >= $2::date ORDER BY day`, childID, fromDay)
	if err != nil {
		return nil, fmt.Errorf("reading alarm overrides: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d AlarmDay
		if err := rows.Scan(&d.Day, &d.Time); err != nil {
			return nil, err
		}
		alarm.Overrides = append(alarm.Overrides, d)
	}
	return alarm, rows.Err()
}

// SetAlarmWeek replaces the weekly schedule: seven entries, Monday first, "" for no alarm.
func (s *Store) SetAlarmWeek(ctx context.Context, childID uuid.UUID, weekdays []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := tx.Exec(ctx, `DELETE FROM alarm_times WHERE child_id = $1`, childID); err != nil {
		return fmt.Errorf("clearing alarm times: %w", err)
	}
	for i, at := range weekdays {
		if at == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO alarm_times (child_id, weekday, time) VALUES ($1, $2, $3)`,
			childID, i+1, at); err != nil {
			return fmt.Errorf("writing alarm time: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// SetAlarmDay sets the alarm for one day: a time, or nil for none. One change per day.
func (s *Store) SetAlarmDay(ctx context.Context, childID uuid.UUID, day string, at *string, by uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alarm_overrides (child_id, day, time, set_by) VALUES ($1, $2::date, $3, $4)
		ON CONFLICT (child_id, day) DO UPDATE SET time = EXCLUDED.time, set_at = NOW(), set_by = EXCLUDED.set_by`,
		childID, day, at, by)
	if err != nil {
		return fmt.Errorf("writing the alarm for %s: %w", day, err)
	}
	return nil
}

// ClearAlarmDay returns a day to the weekly schedule.
func (s *Store) ClearAlarmDay(ctx context.Context, childID uuid.UUID, day string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM alarm_overrides WHERE child_id = $1 AND day = $2::date`, childID, day); err != nil {
		return fmt.Errorf("clearing the alarm for %s: %w", day, err)
	}
	return nil
}
