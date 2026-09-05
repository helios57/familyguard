package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const apiKeyCols = `id, name, prefix, parent_id, created_at, last_used_at, revoked_at`

func scanAPIKey(row pgx.Row) (*APIKey, error) {
	var k APIKey
	if err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.ParentID, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
		return nil, mapErr(err)
	}
	return &k, nil
}

// CreateAPIKey stores a key. Only the hash is written; the caller holds the plaintext and shows it
// once.
func (s *Store) CreateAPIKey(ctx context.Context, parentID uuid.UUID, name, prefix string, tokenHash []byte) (*APIKey, error) {
	return scanAPIKey(s.pool.QueryRow(ctx,
		`INSERT INTO api_keys (id, name, prefix, token_hash, parent_id)
		 VALUES ($1, $2, $3, $4, $5) RETURNING `+apiKeyCols,
		uuid.New(), name, prefix, tokenHash, parentID))
}

// ListAPIKeys returns every key, revoked ones included.
//
// Revoked keys stay listed on purpose. A key that vanishes when it is revoked leaves a parent with
// no way to answer "was that credential ever used, and when did it stop" — which is the first
// question after a laptop goes missing, and the audit log alone cannot answer it because it records
// the parent the key acted as, not the key.
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// ParentByAPIKeyHash resolves a live key to the parent it acts as, and records the use.
//
// The revocation check is in the WHERE clause rather than in Go, so a key revoked between the
// lookup and the check cannot slip through: there is no gap to slip through. `last_used_at` is
// written in the same statement for the same reason it is written at all — a key nobody can tell
// is unused is a key nobody ever revokes.
//
// It returns the parent rather than the key because that is what the caller needs, and it does so
// in ONE statement: a lookup followed by a separate read could see a parent that was deleted in
// between, which is the same hole re-reading the session against the database exists to close.
func (s *Store) ParentByAPIKeyHash(ctx context.Context, tokenHash []byte, now time.Time) (*Parent, error) {
	return scanParent(s.pool.QueryRow(ctx,
		`WITH used AS (
		     UPDATE api_keys SET last_used_at = $2
		      WHERE token_hash = $1 AND revoked_at IS NULL
		     RETURNING parent_id
		 )
		 SELECT `+parentCols+` FROM parents WHERE id = (SELECT parent_id FROM used)`,
		tokenHash, now))
}

// RevokeAPIKey marks a key unusable. Idempotent: revoking a revoked key reports the row unchanged
// rather than failing, because the operator's intent is already satisfied.
func (s *Store) RevokeAPIKey(ctx context.Context, id uuid.UUID, now time.Time) (*APIKey, error) {
	return scanAPIKey(s.pool.QueryRow(ctx,
		`UPDATE api_keys SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1 RETURNING `+apiKeyCols,
		id, now))
}

// DeleteAPIKey removes a key row entirely, returning what it removed so the caller can say which
// key it was in the audit entry — after the row is gone, its id resolves to nothing.
func (s *Store) DeleteAPIKey(ctx context.Context, id uuid.UUID) (*APIKey, error) {
	return scanAPIKey(s.pool.QueryRow(ctx,
		`DELETE FROM api_keys WHERE id = $1 RETURNING `+apiKeyCols, id))
}
