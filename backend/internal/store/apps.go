package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const appCols = `id, package_name, version_code, version_name, label, sha256, signer_sha256,
	size_bytes, min_sdk, file_name, source, created_at`

func scanApp(row pgx.Row) (*App, error) {
	var a App
	if err := row.Scan(&a.ID, &a.PackageName, &a.VersionCode, &a.VersionName, &a.Label, &a.SHA256,
		&a.SignerSHA256, &a.SizeBytes, &a.MinSDK, &a.FileName, &a.Source, &a.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

// RegisterApp adds one APK to the catalog, enforcing the signer pin.
//
// Both halves are one transaction, and that ordering is the point: the pin is claimed first, so two
// concurrent registrations of the same new package cannot each see "no pin yet" and insert
// different keys. The second one blocks on the row and then fails the comparison.
//
// A repeat of an identical (package, version) is not an error — the directory scan re-runs on every
// start and would otherwise fail loudly for doing exactly what it is for. It returns the existing
// row. A DIFFERENT file under the same version is refused: a version code is what the phone
// compares, so two distinct artifacts sharing one is the state in which a device can never tell it
// has the wrong bytes.
func (s *Store) RegisterApp(ctx context.Context, a App) (*App, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var pinned string
	err = tx.QueryRow(ctx,
		`INSERT INTO app_signers (package_name, signer_sha256) VALUES ($1, $2)
		 ON CONFLICT (package_name) DO UPDATE SET package_name = EXCLUDED.package_name
		 RETURNING signer_sha256`,
		a.PackageName, a.SignerSHA256).Scan(&pinned)
	if err != nil {
		return nil, mapErr(err)
	}
	// DO UPDATE rather than DO NOTHING so that the existing row is RETURNED. With DO NOTHING a
	// conflicting insert returns no rows at all, which arrives here as ErrNoRows — indistinguishable
	// from a package that has no pin, and the comparison below would never run.
	if pinned != a.SignerSHA256 {
		return nil, ErrSignerChanged
	}

	existing, err := scanApp(tx.QueryRow(ctx,
		`SELECT `+appCols+` FROM apps WHERE package_name = $1 AND version_code = $2`,
		a.PackageName, a.VersionCode))
	switch {
	case err == nil && existing.SHA256 == a.SHA256:
		return existing, tx.Commit(ctx)
	case err == nil:
		return nil, ErrConflict
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}

	created, err := scanApp(tx.QueryRow(ctx,
		`INSERT INTO apps (id, package_name, version_code, version_name, label, sha256,
		                   signer_sha256, size_bytes, min_sdk, file_name, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING `+appCols,
		uuid.New(), a.PackageName, a.VersionCode, a.VersionName, a.Label, a.SHA256,
		a.SignerSHA256, a.SizeBytes, a.MinSDK, a.FileName, a.Source))
	if err != nil {
		return nil, err
	}
	return created, tx.Commit(ctx)
}

// ListApps returns the catalog, newest version of each package first.
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appCols+` FROM apps ORDER BY package_name, version_code DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// GetApp returns one catalog entry.
func (s *Store) GetApp(ctx context.Context, id uuid.UUID) (*App, error) {
	return scanApp(s.pool.QueryRow(ctx, `SELECT `+appCols+` FROM apps WHERE id = $1`, id))
}

// LatestApp returns the newest registered version of a package.
func (s *Store) LatestApp(ctx context.Context, packageName string) (*App, error) {
	return scanApp(s.pool.QueryRow(ctx,
		`SELECT `+appCols+` FROM apps WHERE package_name = $1 ORDER BY version_code DESC LIMIT 1`,
		packageName))
}

// AppVersion returns one exact version of a package, which is what a download request addresses.
func (s *Store) AppVersion(ctx context.Context, packageName string, versionCode int64) (*App, error) {
	return scanApp(s.pool.QueryRow(ctx,
		`SELECT `+appCols+` FROM apps WHERE package_name = $1 AND version_code = $2`,
		packageName, versionCode))
}

// DeleteApp removes one version from the catalog and reports the row it removed, so the caller can
// delete the file it named. The signer pin is deliberately left behind — see the migration.
func (s *Store) DeleteApp(ctx context.Context, id uuid.UUID) (*App, error) {
	return scanApp(s.pool.QueryRow(ctx, `DELETE FROM apps WHERE id = $1 RETURNING `+appCols, id))
}

// ---- what a child is declared to have ----------------------------------------------------------

// ManagedPackages lists the package names declared for a child.
func (s *Store) ManagedPackages(ctx context.Context, childID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT package_name FROM child_managed_apps WHERE child_id = $1 ORDER BY package_name`, childID)
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

// ManagedAppsForChild returns the highest version of each application a child is declared to have.
//
// One statement rather than ManagedPackages followed by a LatestApp per row: this runs on every
// device sync, and a per-package round trip would make the cost of a child's app list grow with the
// list. DISTINCT ON gives Postgres the whole decision.
//
// A declaration whose package has no version in the catalog produces NO ROW. That is deliberate and
// it is the reason this is an inner join: the device converges on what it is given, so an entry it
// cannot install would be a permanent failure rather than a missing app. The console reads the
// declaration separately (ManagedPackages) and shows the gap — the phone is told only what exists.
func (s *Store) ManagedAppsForChild(ctx context.Context, childID uuid.UUID) ([]App, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (a.package_name) `+prefixedAppCols("a")+`
		   FROM child_managed_apps m
		   JOIN apps a ON a.package_name = m.package_name
		  WHERE m.child_id = $1
		  ORDER BY a.package_name, a.version_code DESC`, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// prefixedAppCols qualifies appCols for a join. Written rather than hand-maintained as a second
// list: a column added to appCols and forgotten here is a scan that fails at runtime on a query
// nobody runs in a unit test.
func prefixedAppCols(alias string) string {
	parts := strings.Split(appCols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// DeclareManagedApp adds a package to a child's set. Repeating it is a no-op rather than a
// conflict: the console's checkbox is a desired state, not an event.
func (s *Store) DeclareManagedApp(ctx context.Context, childID uuid.UUID, packageName string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO child_managed_apps (child_id, package_name) VALUES ($1, $2)
		 ON CONFLICT (child_id, package_name) DO NOTHING`, childID, packageName)
	return mapErr(err)
}

// WithdrawManagedApp removes a package from a child's set.
//
// It reports whether a row went away. That distinction reaches the API as 204 versus 404, and it
// matters here more than it usually would: withdrawing an app is what causes a phone to UNINSTALL
// it, so "there was nothing to withdraw" and "the withdrawal is queued" must not look the same to a
// parent watching for the app to disappear.
func (s *Store) WithdrawManagedApp(ctx context.Context, childID uuid.UUID, packageName string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM child_managed_apps WHERE child_id = $1 AND package_name = $2`, childID, packageName)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() > 0, nil
}
