package store

import (
	"context"
)

// Actor types for the audit log.
const (
	ActorParent = "PARENT"
	ActorDevice = "DEVICE"
	ActorSystem = "SYSTEM"
	// ActorAPIKey is a parent's authority exercised without a parent present (FR-17.4). The actor id
	// is still the parent's, because that is whose authority it is; this says nobody was at a
	// keyboard.
	ActorAPIKey = "API_KEY"
)

// Audit appends one entry. Every state-changing request writes one (FR-14); a mutation with no
// audit row is a bug, because the log is the only way a second parent can see what the first did.
//
// It deliberately takes a context that the caller has already used for the mutation, so an audit
// write that fails surfaces as an error rather than being swallowed on a background goroutine.
func (s *Store) Audit(ctx context.Context, actorType, actorID, action, targetType, targetID string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_log (actor_type, actor_id, action, target_type, target_id, detail)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		actorType, actorID, action, targetType, targetID, detail)
	return err
}

// ListAudit returns the newest entries, capped by limit.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, actor_type, actor_id, action, target_type, target_id, detail, occurred_at
		   FROM audit_log ORDER BY occurred_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorID, &e.Action, &e.TargetType,
			&e.TargetID, &e.Detail, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit deletes entries older than the retention window, keeping the table bounded (NFR-9).
func (s *Store) PruneAudit(ctx context.Context, keepDays int) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM audit_log WHERE occurred_at < NOW() - ($1::int * INTERVAL '1 day')`, keepDays)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
