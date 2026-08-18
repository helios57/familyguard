package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const parentCols = `id, family_id, email, display_name, COALESCE(google_sub, ''), role, created_at, last_login_at`

// NormalizeEmail folds an address to the form stored in the database. Every boundary that accepts
// an email calls this — folding only on read would leave differently-cased duplicates in the table
// for the next writer to trip over.
func NormalizeEmail(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// Bootstrap ensures exactly one family exists and that each seed email is a parent. It runs on
// every start and is a no-op once the family has parents, so it can never resurrect a parent the
// primary admin deliberately removed.
//
// This is the only way a parent comes into existence without an existing parent authorising it.
func (s *Store) Bootstrap(ctx context.Context, familyName string, emails []string) (*Family, error) {
	var fam Family
	err := s.tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id, name, created_at FROM families ORDER BY created_at LIMIT 1`).
			Scan(&fam.ID, &fam.Name, &fam.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			fam = Family{ID: uuid.New(), Name: familyName, CreatedAt: time.Now().UTC()}
			if _, err := tx.Exec(ctx,
				`INSERT INTO families (id, name) VALUES ($1, $2)`, fam.ID, fam.Name); err != nil {
				return fmt.Errorf("create family: %w", err)
			}
		} else if err != nil {
			return err
		}

		var parentCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM parents WHERE family_id = $1`, fam.ID).
			Scan(&parentCount); err != nil {
			return err
		}
		if parentCount > 0 {
			return nil
		}
		for i, raw := range emails {
			email := NormalizeEmail(raw)
			if email == "" {
				continue
			}
			role := RoleAdmin
			if i == 0 {
				role = RolePrimaryAdmin
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO parents (id, family_id, email, role) VALUES ($1, $2, $3, $4)
				 ON CONFLICT (email) DO NOTHING`,
				uuid.New(), fam.ID, email, role); err != nil {
				return fmt.Errorf("seed parent: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &fam, nil
}

// GetFamily returns the single family row.
func (s *Store) GetFamily(ctx context.Context) (*Family, error) {
	var f Family
	err := s.pool.QueryRow(ctx, `SELECT id, name, created_at FROM families ORDER BY created_at LIMIT 1`).
		Scan(&f.ID, &f.Name, &f.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &f, nil
}

// ParentByEmail is the authorization decision for parent sign-in: a Google identity that does not
// already correspond to a parent row is rejected. There is no auto-provisioning.
func (s *Store) ParentByEmail(ctx context.Context, email string) (*Parent, error) {
	return scanParent(s.pool.QueryRow(ctx,
		`SELECT `+parentCols+` FROM parents WHERE email = $1`, NormalizeEmail(email)))
}

// ParentByID looks a parent up by its own id.
func (s *Store) ParentByID(ctx context.Context, id uuid.UUID) (*Parent, error) {
	return scanParent(s.pool.QueryRow(ctx, `SELECT `+parentCols+` FROM parents WHERE id = $1`, id))
}

// ListParents returns every parent in the family.
func (s *Store) ListParents(ctx context.Context) ([]Parent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+parentCols+` FROM parents ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Parent{}
	for rows.Next() {
		p, err := scanParentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CreateParent adds a parent. Only a PRIMARY_ADMIN reaches this, enforced in the handler.
func (s *Store) CreateParent(ctx context.Context, familyID uuid.UUID, email, role string) (*Parent, error) {
	p, err := scanParent(s.pool.QueryRow(ctx,
		`INSERT INTO parents (id, family_id, email, role) VALUES ($1, $2, $3, $4)
		 RETURNING `+parentCols,
		uuid.New(), familyID, NormalizeEmail(email), role))
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	return p, err
}

// DeleteParent removes a parent. Refusing to remove the last PRIMARY_ADMIN is enforced here, in
// the same transaction as the delete, so two concurrent requests cannot both pass the check.
func (s *Store) DeleteParent(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx, `SELECT role FROM parents WHERE id = $1 FOR UPDATE`, id).Scan(&role); err != nil {
			return mapErr(err)
		}
		if role == RolePrimaryAdmin {
			var remaining int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM parents WHERE role = $1 AND id <> $2`, RolePrimaryAdmin, id).
				Scan(&remaining); err != nil {
				return err
			}
			if remaining == 0 {
				return fmt.Errorf("%w: cannot remove the last primary admin", ErrConflict)
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM parents WHERE id = $1`, id)
		return err
	})
}

// RecordParentLogin stamps the login and stores the Google subject.
//
// It deliberately records nothing for Factory Reset Protection. FRP takes a Google *account id*,
// which is not the OIDC subject and cannot be derived from it — registering a subject there produces
// a phone that, after a factory reset, demands an account nobody can sign into. See FR-2.3.
func (s *Store) RecordParentLogin(ctx context.Context, id uuid.UUID, googleSub, displayName string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE parents
		    SET last_login_at = NOW(),
		        google_sub    = COALESCE(NULLIF($2, ''), google_sub),
		        display_name  = COALESCE(NULLIF($3, ''), display_name)
		  WHERE id = $1`, id, googleSub, displayName)
	return err
}

// ---- children -------------------------------------------------------------

const childCols = `id, family_id, name, birth_year, created_at`

// CreateChild adds a child and its default policy row in one transaction, so a child can never
// exist without a policy for the device to fetch. Several children per family (FR-11.2), each
// with an independent policy — nothing here is per-family, and that is the point.
func (s *Store) CreateChild(ctx context.Context, familyID uuid.UUID, name string, birthYear *int) (*Child, error) {
	var c Child
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO children (id, family_id, name, birth_year) VALUES ($1, $2, $3, $4)
			 RETURNING `+childCols,
			uuid.New(), familyID, name, birthYear).
			Scan(&c.ID, &c.FamilyID, &c.Name, &c.BirthYear, &c.CreatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO policies (child_id) VALUES ($1)`, c.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListChildren returns every child in the family.
func (s *Store) ListChildren(ctx context.Context) ([]Child, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+childCols+` FROM children ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Child{}
	for rows.Next() {
		var c Child
		if err := rows.Scan(&c.ID, &c.FamilyID, &c.Name, &c.BirthYear, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChild returns one child.
func (s *Store) GetChild(ctx context.Context, id uuid.UUID) (*Child, error) {
	var c Child
	err := s.pool.QueryRow(ctx, `SELECT `+childCols+` FROM children WHERE id = $1`, id).
		Scan(&c.ID, &c.FamilyID, &c.Name, &c.BirthYear, &c.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

// UpdateChild renames a child or corrects the birth year.
func (s *Store) UpdateChild(ctx context.Context, id uuid.UUID, name string, birthYear *int) (*Child, error) {
	var c Child
	err := s.pool.QueryRow(ctx,
		`UPDATE children SET name = COALESCE(NULLIF($2, ''), name), birth_year = $3
		  WHERE id = $1 RETURNING `+childCols, id, name, birthYear).
		Scan(&c.ID, &c.FamilyID, &c.Name, &c.BirthYear, &c.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

// DeleteChild removes a child and, by cascade, its devices, policy and history.
func (s *Store) DeleteChild(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM children WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- scanning helpers -----------------------------------------------------

type rowScanner interface{ Scan(dest ...any) error }

func scanParent(row pgx.Row) (*Parent, error) {
	p, err := scanParentRow(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return p, nil
}

func scanParentRow(row rowScanner) (*Parent, error) {
	var p Parent
	if err := row.Scan(&p.ID, &p.FamilyID, &p.Email, &p.DisplayName, &p.GoogleSub,
		&p.Role, &p.CreatedAt, &p.LastLoginAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
