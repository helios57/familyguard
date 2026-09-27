package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AgendaEntry is one entry of a profile's agenda (FR-24.1): repeating on weekdays, or on one date.
type AgendaEntry struct {
	ID       uuid.UUID `json:"id"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Place    string    `json:"place"`
	Optional bool      `json:"optional"`
	Weekdays int       `json:"weekdays,omitempty"`
	Day      string    `json:"day,omitempty"`
	StartsAt string    `json:"starts_at"`
	EndsAt   string    `json:"ends_at"`
}

// Holiday is a family-wide range of dates (FR-24.2), first and last day included.
type Holiday struct {
	ID       uuid.UUID `json:"id"`
	Title    string    `json:"title"`
	StartsOn string    `json:"starts_on"`
	EndsOn   string    `json:"ends_on"`
}

// GetAgenda lists a profile's active entries in their saved order.
func (s *Store) GetAgenda(ctx context.Context, childID uuid.UUID) ([]AgendaEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, title, place, optional, COALESCE(weekdays, 0), COALESCE(to_char(day, 'YYYY-MM-DD'), ''),
		       starts_at, ends_at
		  FROM agenda_entries WHERE child_id = $1 AND retired_at IS NULL ORDER BY position, created_at`, childID)
	if err != nil {
		return nil, fmt.Errorf("reading the agenda: %w", err)
	}
	defer rows.Close()
	out := []AgendaEntry{}
	for rows.Next() {
		var e AgendaEntry
		if err := rows.Scan(&e.ID, &e.Kind, &e.Title, &e.Place, &e.Optional, &e.Weekdays, &e.Day, &e.StartsAt, &e.EndsAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReplaceAgenda replaces a profile's agenda as one document, like ReplacePlan: an entry with the id of
// an active one of this profile is updated in place, one without is created, the others are retired,
// and an id that is not this profile's is refused.
func (s *Store) ReplaceAgenda(ctx context.Context, childID uuid.UUID, entries []AgendaEntry) ([]AgendaEntry, error) {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		existing, err := activeIDs(ctx, tx,
			`SELECT id FROM agenda_entries WHERE child_id = $1 AND retired_at IS NULL FOR UPDATE`, childID)
		if err != nil {
			return err
		}
		keep := []uuid.UUID{}
		for i, e := range entries {
			var weekdays, day any
			if e.Kind == "RECURRING" {
				weekdays = e.Weekdays
			} else {
				day = e.Day
			}
			id := e.ID
			if id == uuid.Nil {
				id = uuid.New()
				if _, err := tx.Exec(ctx, `
					INSERT INTO agenda_entries (id, child_id, kind, title, place, optional, weekdays, day, starts_at, ends_at, position)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date, $9, $10, $11)`,
					id, childID, e.Kind, e.Title, e.Place, e.Optional, weekdays, day, e.StartsAt, e.EndsAt, i); err != nil {
					return err
				}
			} else {
				if !existing[id] {
					return fmt.Errorf("%w: entry %s is not in this profile's agenda", ErrNotFound, id)
				}
				if _, err := tx.Exec(ctx, `
					UPDATE agenda_entries SET kind = $2, title = $3, place = $4, optional = $5, weekdays = $6,
					       day = $7::date, starts_at = $8, ends_at = $9, position = $10 WHERE id = $1`,
					id, e.Kind, e.Title, e.Place, e.Optional, weekdays, day, e.StartsAt, e.EndsAt, i); err != nil {
					return err
				}
			}
			keep = append(keep, id)
		}
		_, err = tx.Exec(ctx, `UPDATE agenda_entries SET retired_at = NOW()
			WHERE child_id = $1 AND retired_at IS NULL AND NOT (id = ANY($2))`, childID, keep)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetAgenda(ctx, childID)
}

// ListHolidays lists the family's active holidays, earliest first.
func (s *Store) ListHolidays(ctx context.Context) ([]Holiday, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, title, to_char(starts_on, 'YYYY-MM-DD'), to_char(ends_on, 'YYYY-MM-DD')
		  FROM holidays WHERE retired_at IS NULL ORDER BY starts_on, title`)
	if err != nil {
		return nil, fmt.Errorf("reading the holidays: %w", err)
	}
	defer rows.Close()
	out := []Holiday{}
	for rows.Next() {
		var h Holiday
		if err := rows.Scan(&h.ID, &h.Title, &h.StartsOn, &h.EndsOn); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ReplaceHolidays replaces the family's holidays as one document, keeping ids like ReplaceAgenda.
func (s *Store) ReplaceHolidays(ctx context.Context, familyID uuid.UUID, holidays []Holiday) ([]Holiday, error) {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		existing, err := activeIDs(ctx, tx,
			`SELECT id FROM holidays WHERE family_id = $1 AND retired_at IS NULL FOR UPDATE`, familyID)
		if err != nil {
			return err
		}
		keep := []uuid.UUID{}
		for _, h := range holidays {
			id := h.ID
			if id == uuid.Nil {
				id = uuid.New()
				if _, err := tx.Exec(ctx, `INSERT INTO holidays (id, family_id, title, starts_on, ends_on)
					VALUES ($1, $2, $3, $4::date, $5::date)`, id, familyID, h.Title, h.StartsOn, h.EndsOn); err != nil {
					return err
				}
			} else {
				if !existing[id] {
					return fmt.Errorf("%w: holiday %s is not one of this family's", ErrNotFound, id)
				}
				if _, err := tx.Exec(ctx, `UPDATE holidays SET title = $2, starts_on = $3::date, ends_on = $4::date WHERE id = $1`,
					id, h.Title, h.StartsOn, h.EndsOn); err != nil {
					return err
				}
			}
			keep = append(keep, id)
		}
		_, err = tx.Exec(ctx, `UPDATE holidays SET retired_at = NOW()
			WHERE family_id = $1 AND retired_at IS NULL AND NOT (id = ANY($2))`, familyID, keep)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.ListHolidays(ctx)
}

// AlarmSkipsHolidays is the profile's "not during holidays" (FR-24.4); false when never set.
func (s *Store) AlarmSkipsHolidays(ctx context.Context, childID uuid.UUID) (bool, error) {
	var skip bool
	err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT skip_holidays FROM alarm_settings WHERE child_id = $1), FALSE)`, childID).Scan(&skip)
	return skip, err
}

// SetAlarmSkipsHolidays stores the profile's "not during holidays".
func (s *Store) SetAlarmSkipsHolidays(ctx context.Context, childID uuid.UUID, skip bool) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO alarm_settings (child_id, skip_holidays) VALUES ($1, $2)
		ON CONFLICT (child_id) DO UPDATE SET skip_holidays = EXCLUDED.skip_holidays`, childID, skip)
	return err
}

func activeIDs(ctx context.Context, tx pgx.Tx, query string, arg any) (map[uuid.UUID]bool, error) {
	rows, err := tx.Query(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
