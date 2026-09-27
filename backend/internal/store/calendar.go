package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CalendarSource is a profile's calendar (FR-25): its address, the last copy read, and how the last
// attempt went.
type CalendarSource struct {
	URL           string
	Body          []byte
	FetchedAt     *time.Time
	LastAttemptAt *time.Time
	LastError     string
}

// GetCalendarSource is the profile's calendar, or nil when it has none.
func (s *Store) GetCalendarSource(ctx context.Context, childID uuid.UUID) (*CalendarSource, error) {
	var c CalendarSource
	err := s.pool.QueryRow(ctx, `SELECT url, body, fetched_at, last_attempt_at, last_error
		FROM calendar_sources WHERE child_id = $1`, childID).Scan(&c.URL, &c.Body, &c.FetchedAt, &c.LastAttemptAt, &c.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SetCalendarSource stores a new address with the copy just read from it.
func (s *Store) SetCalendarSource(ctx context.Context, childID uuid.UUID, url string, body []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO calendar_sources (child_id, url, body, fetched_at, last_attempt_at, last_error)
		VALUES ($1, $2, $3, NOW(), NOW(), '')
		ON CONFLICT (child_id) DO UPDATE SET url = EXCLUDED.url, body = EXCLUDED.body,
		    fetched_at = NOW(), last_attempt_at = NOW(), last_error = ''`, childID, url, body)
	return err
}

// RecordCalendarRead stores a refresh: a new body on success, only the error on failure — the last
// good copy stays. url guards against a refresh that raced a change of address.
func (s *Store) RecordCalendarRead(ctx context.Context, childID uuid.UUID, url string, body []byte, readErr string) error {
	if readErr != "" {
		_, err := s.pool.Exec(ctx, `UPDATE calendar_sources SET last_attempt_at = NOW(), last_error = $3
			WHERE child_id = $1 AND url = $2`, childID, url, readErr)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE calendar_sources SET body = $3, fetched_at = NOW(), last_attempt_at = NOW(), last_error = ''
		WHERE child_id = $1 AND url = $2`, childID, url, body)
	return err
}

// DeleteCalendarSource removes the profile's calendar.
func (s *Store) DeleteCalendarSource(ctx context.Context, childID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM calendar_sources WHERE child_id = $1`, childID)
	return err
}
