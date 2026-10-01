package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WebPushSubscription is one parent browser's push address (FR-28.4).
type WebPushSubscription struct {
	ID       uuid.UUID
	ParentID uuid.UUID
	Endpoint string
	P256dh   string
	Auth     string
}

// SaveWebPushSubscription records a browser's subscription for a parent. A browser that subscribes
// again under another parent's sign-in moves to that parent: the endpoint is the browser.
func (s *Store) SaveWebPushSubscription(ctx context.Context, parentID uuid.UUID, endpoint, p256dh, auth string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO web_push_subscriptions (id, parent_id, endpoint, p256dh, auth) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (endpoint) DO UPDATE
		   SET parent_id = EXCLUDED.parent_id, p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth`,
		uuid.New(), parentID, endpoint, p256dh, auth)
	return err
}

// DeleteWebPushSubscription removes a browser's subscription; parentID scopes it to the caller,
// uuid.Nil removes it whoever holds it (the push service said the address is gone).
func (s *Store) DeleteWebPushSubscription(ctx context.Context, parentID uuid.UUID, endpoint string) error {
	if parentID == uuid.Nil {
		_, err := s.pool.Exec(ctx, `DELETE FROM web_push_subscriptions WHERE endpoint = $1`, endpoint)
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM web_push_subscriptions WHERE endpoint = $1 AND parent_id = $2`,
		endpoint, parentID)
	return err
}

// HasWebPushSubscription reports whether this endpoint is subscribed for this parent.
func (s *Store) HasWebPushSubscription(ctx context.Context, parentID uuid.UUID, endpoint string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM web_push_subscriptions WHERE endpoint = $1 AND parent_id = $2)`,
		endpoint, parentID).Scan(&ok)
	return ok, err
}

// WebPushSubscriptions is every parent browser that asked to be told (the deployment is one family).
func (s *Store) WebPushSubscriptions(ctx context.Context) ([]WebPushSubscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, parent_id, endpoint, p256dh, auth FROM web_push_subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebPushSubscription
	for rows.Next() {
		var w WebPushSubscription
		if err := rows.Scan(&w.ID, &w.ParentID, &w.Endpoint, &w.P256dh, &w.Auth); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkWebPushDelivered records that a push to this endpoint was accepted.
func (s *Store) MarkWebPushDelivered(ctx context.Context, endpoint string) error {
	_, err := s.pool.Exec(ctx, `UPDATE web_push_subscriptions SET last_ok_at = NOW() WHERE endpoint = $1`, endpoint)
	return err
}

// ServerKey returns the named key, making it with mint on first use. Two servers starting at once
// both mint; the insert that loses reads the winner's, so every caller gets the same key.
func (s *Store) ServerKey(ctx context.Context, name string, mint func() (string, error)) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `SELECT value FROM server_keys WHERE name = $1`, name).Scan(&value)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	minted, err := mint()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO server_keys (name, value) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`,
		name, minted); err != nil {
		return "", err
	}
	err = s.pool.QueryRow(ctx, `SELECT value FROM server_keys WHERE name = $1`, name).Scan(&value)
	return value, err
}
