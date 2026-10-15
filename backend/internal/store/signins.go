package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A parent's sign-in (FR-29) is a row, not a signed token: it can be extended while it is used and
// ended the moment a parent signs out, and neither is possible for a credential the server cannot
// reach once it has handed it out.
//
// "Live" is one predicate everywhere — not ended, and used after the idle cutoff — and every
// function takes the cutoff from its caller, so the idle timeout is configuration rather than a
// constant three functions would each have to agree on.

const signInCols = `s.id, s.parent_id, p.email, p.display_name, s.label, s.created_at, s.last_used_at`

func scanSignIn(row pgx.Row) (*SignIn, error) {
	var si SignIn
	if err := row.Scan(&si.ID, &si.ParentID, &si.ParentEmail, &si.ParentName, &si.Label,
		&si.CreatedAt, &si.LastUsedAt); err != nil {
		return nil, mapErr(err)
	}
	return &si, nil
}

// CreateSignIn records a new sign-in. Only the hash is written; the caller holds the token.
func (s *Store) CreateSignIn(ctx context.Context, parentID uuid.UUID, label string, tokenHash []byte, now time.Time) (*SignIn, error) {
	id := uuid.New()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO parent_sign_ins (id, parent_id, token_hash, label, created_at, last_used_at)
		 VALUES ($1, $2, $3, $4, $5, $5)`,
		id, parentID, tokenHash, label, now); err != nil {
		return nil, err
	}
	// The cutoff only has to admit the row just written; the database keeps microseconds and may
	// round `now`, so it is not `now` itself.
	return s.SignInByID(ctx, id, now.Add(-time.Minute))
}

// ParentBySignInHash resolves a live sign-in to its parent and records the use, in one statement.
//
// The liveness check is in the UPDATE's WHERE clause, as for API keys: a sign-in ended or lapsed
// between a lookup and a check cannot slip through a gap that does not exist. Moving last_used_at
// in the same statement is what makes FR-29.2 hold — the request that proves the sign-in is in use
// is the one that extends it. The parent is read in the same statement too, so a parent deleted in
// between is not returned.
func (s *Store) ParentBySignInHash(ctx context.Context, tokenHash []byte, now, cutoff time.Time) (*Parent, uuid.UUID, error) {
	var (
		signIn uuid.UUID
		p      Parent
	)
	err := s.pool.QueryRow(ctx,
		`WITH used AS (
		     UPDATE parent_sign_ins SET last_used_at = $2
		      WHERE token_hash = $1 AND ended_at IS NULL AND last_used_at > $3
		     RETURNING id, parent_id
		 )
		 SELECT (SELECT id FROM used), `+parentCols+` FROM parents WHERE id = (SELECT parent_id FROM used)`,
		tokenHash, now, cutoff).
		Scan(&signIn, &p.ID, &p.FamilyID, &p.Email, &p.DisplayName, &p.GoogleSub, &p.Role, &p.CreatedAt, &p.LastLoginAt)
	if err != nil {
		return nil, uuid.Nil, mapErr(err)
	}
	return &p, signIn, nil
}

// SignInByID returns one live sign-in, or ErrNotFound for an ended, lapsed or unknown one.
func (s *Store) SignInByID(ctx context.Context, id uuid.UUID, cutoff time.Time) (*SignIn, error) {
	return scanSignIn(s.pool.QueryRow(ctx,
		`SELECT `+signInCols+` FROM parent_sign_ins s JOIN parents p ON p.id = s.parent_id
		  WHERE s.id = $1 AND s.ended_at IS NULL AND s.last_used_at > $2`, id, cutoff))
}

// ListSignIns returns every live sign-in of the family, most recently used first. Ended and lapsed
// ones are not listed: they can no longer do anything, and a list a parent scans for "is somebody
// still signed in" must not make them read past rows that cannot be.
func (s *Store) ListSignIns(ctx context.Context, cutoff time.Time) ([]SignIn, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+signInCols+` FROM parent_sign_ins s JOIN parents p ON p.id = s.parent_id
		  WHERE s.ended_at IS NULL AND s.last_used_at > $1
		  ORDER BY s.last_used_at DESC, s.id`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SignIn{}
	for rows.Next() {
		si, err := scanSignIn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *si)
	}
	return out, rows.Err()
}

// EndSignIn ends a live sign-in. ErrNotFound when it was not live, so ending one twice is reported
// rather than silently accepted: the second caller's picture of the list was out of date.
func (s *Store) EndSignIn(ctx context.Context, id uuid.UUID, now, cutoff time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE parent_sign_ins SET ended_at = $2 WHERE id = $1 AND ended_at IS NULL AND last_used_at > $3`,
		id, now, cutoff)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneSignIns deletes sign-ins that have been over — ended, or lapsed — for longer than keepDays
// (FR-29.5). Until then an ended one stays, unlisted, so "when was it last used" is answerable after
// a phone goes missing.
func (s *Store) PruneSignIns(ctx context.Context, keepDays int, idle time.Duration, now time.Time) (int64, error) {
	keep := time.Duration(keepDays) * 24 * time.Hour
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM parent_sign_ins
		  WHERE (ended_at IS NOT NULL AND ended_at < $1)
		     OR (ended_at IS NULL AND last_used_at < $2)`,
		now.Add(-keep), now.Add(-keep-idle))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
