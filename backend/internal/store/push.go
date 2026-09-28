package store

import (
	"context"

	"github.com/google/uuid"
)

// SetPushToken records the phone's push address, its Firebase Installation ID (FR-26.3); "" clears
// it. Reports whether it differs from what was held.
func (s *Store) SetPushToken(ctx context.Context, deviceID uuid.UUID, token string) (bool, error) {
	var before string
	err := s.pool.QueryRow(ctx, `
		WITH old AS (SELECT push_token FROM device_state WHERE device_id = $1)
		INSERT INTO device_state (device_id, last_seen_at, push_token) VALUES ($1, NOW(), $2)
		ON CONFLICT (device_id) DO UPDATE SET push_token = EXCLUDED.push_token
		RETURNING COALESCE((SELECT push_token FROM old), '')`, deviceID, token).Scan(&before)
	return before != token, err
}

// PushToken is the phone's registration token, or "" when it has none.
func (s *Store) PushToken(ctx context.Context, deviceID uuid.UUID) (string, error) {
	var token string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT push_token FROM device_state WHERE device_id = $1), '')`,
		deviceID).Scan(&token)
	return token, err
}

// DropPushToken clears the token FCM refused, and only that one: a phone that has reported a new
// token since keeps it.
func (s *Store) DropPushToken(ctx context.Context, deviceID uuid.UUID, token string) error {
	_, err := s.pool.Exec(ctx, `UPDATE device_state SET push_token = '' WHERE device_id = $1 AND push_token = $2`,
		deviceID, token)
	return err
}
