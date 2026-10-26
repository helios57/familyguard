package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RejectedCall is a call the phone rejected because the caller was neither a contact nor a family
// number (FR-31.5). ClientID is the phone's own id for it; Number is what the phone saw.
type RejectedCall struct {
	ClientID   string    `json:"-"`
	Number     string    `json:"number"`
	RejectedAt time.Time `json:"at"`
}

// RecordRejectedCalls stores what the phone reports and returns how many were new. A call the server
// already has — the same client id from the same phone, re-sent after a lost response — is skipped,
// never stored twice and never an error, so the phone can acknowledge it either way.
func (s *Store) RecordRejectedCalls(ctx context.Context, deviceID uuid.UUID, calls []RejectedCall) (int, error) {
	stored := 0
	for _, c := range calls {
		tag, err := s.pool.Exec(ctx,
			`INSERT INTO rejected_calls (device_id, client_id, number, rejected_at)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (device_id, client_id) DO NOTHING`,
			deviceID, c.ClientID, c.Number, c.RejectedAt)
		if err != nil {
			return stored, mapErr(err)
		}
		stored += int(tag.RowsAffected())
	}
	return stored, nil
}

// ListRejectedCalls is a phone's rejected calls since the given time, newest first, never nil.
func (s *Store) ListRejectedCalls(ctx context.Context, deviceID uuid.UUID, since time.Time) ([]RejectedCall, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT client_id, number, rejected_at FROM rejected_calls
		  WHERE device_id = $1 AND rejected_at >= $2
		  ORDER BY rejected_at DESC, id DESC`, deviceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RejectedCall{}
	for rows.Next() {
		var c RejectedCall
		if err := rows.Scan(&c.ClientID, &c.Number, &c.RejectedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneRejectedCalls deletes rejected calls older than the retention window (FR-31.5: one year).
func (s *Store) PruneRejectedCalls(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM rejected_calls WHERE rejected_at < NOW() - $1::interval`, olderThan.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
