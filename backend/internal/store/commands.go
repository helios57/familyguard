package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const commandCols = `id, device_id, type, params, state, issued_by,
	COALESCE(result, '{}'::jsonb), COALESCE(error, ''), created_at, delivered_at, acked_at, expires_at`

// QueueCommand records a command for a device. It is always created QUEUED: no caller can create a
// row that already claims delivery, which is the structural half of NFR-3. The other half is that
// only MarkDelivered advances it, and only the command fetch calls that — a push wake-up says
// "there is something to fetch" and never records delivery of its own.
func (s *Store) QueueCommand(ctx context.Context, deviceID uuid.UUID, cmdType string, params map[string]any, issuedBy *uuid.UUID, ttl time.Duration) (*Command, error) {
	if params == nil {
		params = map[string]any{}
	}
	return scanCommand(s.pool.QueryRow(ctx,
		`INSERT INTO commands (id, device_id, type, params, state, issued_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW() + $7::interval)
		 RETURNING `+commandCols,
		uuid.New(), deviceID, cmdType, params, CmdQueued, issuedBy, ttl.String()))
}

// PendingCommands returns the unexpired QUEUED commands for a device, oldest first. This is the
// queue drain a device runs on connect, so a command issued while it was offline is delivered
// rather than lost (FR-9.2).
func (s *Store) PendingCommands(ctx context.Context, deviceID uuid.UUID) ([]Command, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+commandCols+` FROM commands
		  WHERE device_id = $1 AND state = $2 AND expires_at > NOW()
		  ORDER BY created_at`, deviceID, CmdQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectCommands(rows)
}

// MarkDelivered advances QUEUED -> DELIVERED. The WHERE clause pins the source state, so a second
// call is a no-op that returns ErrNotFound rather than re-stamping the timestamp.
func (s *Store) MarkDelivered(ctx context.Context, id uuid.UUID) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`UPDATE commands SET state = $2, delivered_at = NOW() WHERE id = $1 AND state = $3
		 RETURNING delivered_at`,
		id, CmdDelivered, CmdQueued).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	return at, nil
}

// AckCommand records the device's own report of the outcome. It is scoped by device id as well as
// command id: an authenticated device cannot acknowledge another device's command.
//
// Only a DELIVERED or QUEUED command can be acknowledged; acking an EXPIRED one fails, so a late
// ack cannot resurrect a command the parent was already told had expired.
func (s *Store) AckCommand(ctx context.Context, deviceID, id uuid.UUID, ok bool, result map[string]any, errMsg string) (*Command, error) {
	if result == nil {
		result = map[string]any{}
	}
	state := CmdAcked
	if !ok {
		state = CmdFailed
	}
	return scanCommand(s.pool.QueryRow(ctx,
		`UPDATE commands SET state = $3, acked_at = NOW(), result = $4, error = NULLIF($5, '')
		  WHERE id = $1 AND device_id = $2 AND state IN ($6, $7)
		 RETURNING `+commandCols,
		id, deviceID, state, result, errMsg, CmdQueued, CmdDelivered))
}

// ExpireCommands stamps commands whose deadline passed. Returns how many changed, so the caller
// can log a real number rather than assert success.
func (s *Store) ExpireCommands(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE commands SET state = $1 WHERE state IN ($2, $3) AND expires_at <= NOW()`,
		CmdExpired, CmdQueued, CmdDelivered)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ListCommands returns recent commands for a device, newest first.
func (s *Store) ListCommands(ctx context.Context, deviceID uuid.UUID, limit int) ([]Command, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+commandCols+` FROM commands WHERE device_id = $1 ORDER BY created_at DESC LIMIT $2`,
		deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectCommands(rows)
}

// GetCommand returns one command, scoped to nothing — callers must check ownership.
func (s *Store) GetCommand(ctx context.Context, id uuid.UUID) (*Command, error) {
	return scanCommand(s.pool.QueryRow(ctx, `SELECT `+commandCols+` FROM commands WHERE id = $1`, id))
}

func scanCommand(row pgx.Row) (*Command, error) {
	var c Command
	if err := row.Scan(&c.ID, &c.DeviceID, &c.Type, &c.Params, &c.State, &c.IssuedBy,
		&c.Result, &c.Error, &c.CreatedAt, &c.DeliveredAt, &c.AckedAt, &c.ExpiresAt); err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

func collectCommands(rows pgx.Rows) ([]Command, error) {
	out := []Command{}
	for rows.Next() {
		var c Command
		if err := rows.Scan(&c.ID, &c.DeviceID, &c.Type, &c.Params, &c.State, &c.IssuedBy,
			&c.Result, &c.Error, &c.CreatedAt, &c.DeliveredAt, &c.AckedAt, &c.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
