package store

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const policyCols = `child_id, tracking_only, allow_child_installs, allow_debugging, youtube_blocked,
	daily_limit_minutes, bedtime_enabled, bedtime_start, bedtime_end, dns_host, timezone, version, updated_at`

// NormalizeDomain folds a hostname to the stored form: lowercase, no trailing dot, no scheme, no
// path. Both add and remove go through it, which is what makes removal actually remove — a rule
// stored as "Example.com." could never be matched by a delete of "example.com".
func NormalizeDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "https://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	d = strings.TrimSuffix(d, ".")
	return d
}

// GetPolicy returns a child's policy.
func (s *Store) GetPolicy(ctx context.Context, childID uuid.UUID) (*Policy, error) {
	return scanPolicy(s.pool.QueryRow(ctx, `SELECT `+policyCols+` FROM policies WHERE child_id = $1`, childID))
}

// PolicyUpdate carries the fields a parent may change. A nil pointer means "leave alone", which is
// what lets the console PATCH one toggle without resending — and silently reverting — the rest.
type PolicyUpdate struct {
	TrackingOnly       *bool
	AllowChildInstalls *bool
	AllowDebugging     *bool
	YouTubeBlocked     *bool
	DailyLimitMinutes  *int
	BedtimeEnabled     *bool
	BedtimeStart       *string
	BedtimeEnd         *string
	DNSHost            *string
	Timezone           *string
}

// UpdatePolicy applies a partial update and bumps the version. Every field is symmetric: the same
// call that sets a flag can clear it, so no policy state is one-way (FR-4.2, FR-7.3).
func (s *Store) UpdatePolicy(ctx context.Context, childID uuid.UUID, u PolicyUpdate) (*Policy, error) {
	return scanPolicy(s.pool.QueryRow(ctx,
		`UPDATE policies SET
		     tracking_only        = COALESCE($2, tracking_only),
		     allow_child_installs = COALESCE($3, allow_child_installs),
		     allow_debugging      = COALESCE($4, allow_debugging),
		     youtube_blocked      = COALESCE($5, youtube_blocked),
		     daily_limit_minutes  = COALESCE($6, daily_limit_minutes),
		     bedtime_enabled      = COALESCE($7, bedtime_enabled),
		     bedtime_start        = COALESCE($8, bedtime_start),
		     bedtime_end          = COALESCE($9, bedtime_end),
		     dns_host             = COALESCE($10, dns_host),
		     timezone             = COALESCE($11, timezone),
		     version              = version + 1,
		     updated_at           = NOW()
		  WHERE child_id = $1
		 RETURNING `+policyCols,
		childID, u.TrackingOnly, u.AllowChildInstalls, u.AllowDebugging, u.YouTubeBlocked,
		u.DailyLimitMinutes, u.BedtimeEnabled, u.BedtimeStart, u.BedtimeEnd, u.DNSHost, u.Timezone))
}

// BumpPolicyVersion increments the version without changing a field, used when an app rule or a
// blocked domain changes so the device notices there is new state to fetch.
func (s *Store) BumpPolicyVersion(ctx context.Context, childID uuid.UUID) (int64, error) {
	var v int64
	err := s.pool.QueryRow(ctx,
		`UPDATE policies SET version = version + 1, updated_at = NOW() WHERE child_id = $1 RETURNING version`,
		childID).Scan(&v)
	return v, mapErr(err)
}

// ---- app rules ------------------------------------------------------------

// SetAppRule records an allow or block decision for one package.
func (s *Store) SetAppRule(ctx context.Context, childID uuid.UUID, pkg, action string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app_rules (child_id, package_name, action) VALUES ($1, $2, $3)
		 ON CONFLICT (child_id, package_name) DO UPDATE SET action = EXCLUDED.action, updated_at = NOW()`,
		childID, pkg, action)
	return mapErr(err)
}

// DeleteAppRule removes a rule entirely, returning the package to its default state.
func (s *Store) DeleteAppRule(ctx context.Context, childID uuid.UUID, pkg string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM app_rules WHERE child_id = $1 AND package_name = $2`, childID, pkg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAppRules returns every rule for a child.
func (s *Store) ListAppRules(ctx context.Context, childID uuid.UUID) ([]AppRule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT child_id, package_name, action, updated_at FROM app_rules
		  WHERE child_id = $1 ORDER BY package_name`, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppRule{}
	for rows.Next() {
		var r AppRule
		if err := rows.Scan(&r.ChildID, &r.PackageName, &r.Action, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BlockedPackages returns just the package names the child may not use, which is the shape the
// enforcement engine consumes.
func (s *Store) BlockedPackages(ctx context.Context, childID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT package_name FROM app_rules WHERE child_id = $1 AND action = $2 ORDER BY package_name`,
		childID, ActionBlock)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- blocked domains ------------------------------------------------------

// AddBlockedDomain adds a custom domain block. Adding a domain that is already blocked succeeds
// idempotently rather than erroring, because the console's failure mode should not be a red toast
// for a state the parent already wanted.
func (s *Store) AddBlockedDomain(ctx context.Context, childID uuid.UUID, domain string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO blocked_domains (child_id, domain) VALUES ($1, $2)
		 ON CONFLICT (child_id, domain) DO NOTHING`, childID, NormalizeDomain(domain))
	return mapErr(err)
}

// RemoveBlockedDomain deletes a custom domain block. The delete is a real delete: nothing here is
// append-only, so removing a domain restores access (FR-6.5).
func (s *Store) RemoveBlockedDomain(ctx context.Context, childID uuid.UUID, domain string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM blocked_domains WHERE child_id = $1 AND domain = $2`, childID, NormalizeDomain(domain))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListBlockedDomains returns the child's custom blocked domains.
func (s *Store) ListBlockedDomains(ctx context.Context, childID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT domain FROM blocked_domains WHERE child_id = $1 ORDER BY domain`, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanPolicy(row pgx.Row) (*Policy, error) {
	var p Policy
	if err := row.Scan(&p.ChildID, &p.TrackingOnly, &p.AllowChildInstalls, &p.AllowDebugging,
		&p.YouTubeBlocked, &p.DailyLimitMinutes, &p.BedtimeEnabled, &p.BedtimeStart, &p.BedtimeEnd,
		&p.DNSHost, &p.Timezone, &p.Version, &p.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}
