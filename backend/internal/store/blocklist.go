package store

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---- the family-wide blocklist (FR-18) ------------------------------------

// DefaultBlockedPackages is the curated set: software that arrives on a phone without anybody
// choosing it, and that no child in a family running this needs.
//
// It ships in the binary rather than as seeded rows, next in kind to policy.DefaultCriticalPackages
// and policy.YouTubePackages. Migration 0005 records why in full; the short version is that a
// deployable schema plants no rows (NFR-5), and a list in code reaches the deployment that already
// exists instead of only the next one.
//
// Naming a package no phone has is free. The DPC filters the desired set against what is actually
// installed before it touches anything, so an entry for an app this family does not own does
// nothing at all — which is what makes a broader curated list cheap, and why the choices below are
// governed entirely by what happens when the app IS present.
//
// Blocking com.facebook.katana alone does not work, and that is why three stubs are here with it.
// On a Samsung the Facebook app is delivered by a preinstalled installer (com.facebook.system) with
// its own app manager and service; remove the visible app and the machinery that put it there is
// still resident and still entitled to put it back. All four, or it returns.
//
// What is deliberately NOT here, because hiding it breaks the phone rather than declutters it:
//
//	com.wssyncmldm             Samsung's software update / OTA client. Hiding it stops the phone
//	                           receiving security patches — worse than any app on this list.
//	com.osp.app.signin         Samsung Account. Find My Mobile, backup and Smart Switch hang off it.
//	com.skms.android.agent     Knox attestation, and
//	com.knox.vpn.proxyhandler  Knox's proxy handler. Device-owner enrolment on Samsung hardware runs
//	                           through Knox; interfering with it risks the enrolment itself.
//	com.hiya.star              Backs the Samsung dialer's caller ID. The dialer is protected by
//	                           FR-5.5, and a dependency of a protected package should be too.
//	com.touchtype.swiftkey     A keyboard. Hiding the keyboard in use leaves a phone that cannot
//	                           type, which is indistinguishable from a brick to the child holding
//	                           it. FR-5.5 already protects every ENABLED input method — the device
//	                           reports them and the engine strips them last — so this would in fact
//	                           be caught. It is named here anyway so nobody adds it back believing
//	                           the category is safe: the guard is a backstop, not a licence.
var DefaultBlockedPackages = []FamilyBlockedPackage{
	{PackageName: "com.facebook.appmanager", Label: "Meta App Manager",
		Reason: "Second half of the Meta preinstall machinery."},
	{PackageName: "com.facebook.katana", Label: "Facebook",
		Reason: "Social network, preinstalled by the vendor."},
	{PackageName: "com.facebook.services", Label: "Meta Services",
		Reason: "Background Meta service shipped with the preinstall."},
	{PackageName: "com.facebook.system", Label: "Meta App Installer",
		Reason: "Reinstalls Facebook. Blocking the app without this one does not hold."},
	{PackageName: "com.microsoft.appmanager", Label: "Link to Windows",
		Reason: "Vendor preinstall that mirrors the phone to a PC."},
	{PackageName: "com.microsoft.skydrive", Label: "OneDrive",
		Reason: "Vendor preinstall. Nothing on this phone depends on it."},
	{PackageName: "com.mygalaxy.service", Label: "My Galaxy",
		Reason: "Samsung promotional service."},
}

// IsDefaultBlocked reports whether a package belongs to the curated set.
func IsDefaultBlocked(pkg string) bool {
	for _, d := range DefaultBlockedPackages {
		if d.PackageName == pkg {
			return true
		}
	}
	return false
}

// ListFamilyBlockedPackages returns the effective list: the curated set minus what this family
// dismissed, plus what it added itself, ordered by package name.
//
// Two reads rather than one query with a UNION, because the curated half is not in the database.
// The composition is the same one FamilyBlockedPackageNames performs, and it is written once here
// and reused — two implementations of "what is on the list" is how the console and the phone end up
// disagreeing about why an app is missing.
func (s *Store) ListFamilyBlockedPackages(ctx context.Context) ([]FamilyBlockedPackage, error) {
	dismissed, err := s.stringSet(ctx, `SELECT package_name FROM family_blocklist_dismissals`)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT package_name, label, reason, created_at FROM family_blocked_packages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []FamilyBlockedPackage{}
	own := map[string]bool{}
	for rows.Next() {
		var b FamilyBlockedPackage
		if err := rows.Scan(&b.PackageName, &b.Label, &b.Reason, &b.CreatedAt); err != nil {
			return nil, err
		}
		b.Source = BlocklistSourceParent
		own[b.PackageName] = true
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, d := range DefaultBlockedPackages {
		// A parent's own entry for a curated package wins the display, so the list never shows one
		// package twice. Dismissal is checked second so that re-adding a dismissed entry as the
		// parent's own is coherent rather than invisible.
		if own[d.PackageName] || dismissed[d.PackageName] {
			continue
		}
		e := d
		e.Source = BlocklistSourceBuiltin
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PackageName < out[j].PackageName })
	return out, nil
}

// FamilyBlockedPackageNames returns just the names, which is the shape the enforcement engine
// consumes.
func (s *Store) FamilyBlockedPackageNames(ctx context.Context) ([]string, error) {
	entries, err := s.ListFamilyBlockedPackages(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.PackageName)
	}
	return out, nil
}

// SetFamilyBlockedPackage puts a package on the list, and is also how a dismissed curated entry
// comes back.
//
// Restoring a curated entry lifts the dismissal and keeps the curated label and reason rather than
// taking the caller's: the entry is the one that shipped, and describing it in two ways depending
// on how it was restored would make the list read differently for no reason a parent could see.
func (s *Store) SetFamilyBlockedPackage(ctx context.Context, familyID uuid.UUID, pkg, label, reason string) (*FamilyBlockedPackage, error) {
	var out FamilyBlockedPackage
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM family_blocklist_dismissals WHERE family_id = $1 AND package_name = $2`,
			familyID, pkg); err != nil {
			return err
		}
		if IsDefaultBlocked(pkg) {
			for _, d := range DefaultBlockedPackages {
				if d.PackageName == pkg {
					out = d
					out.Source = BlocklistSourceBuiltin
				}
			}
			return bumpEveryPolicy(ctx, tx)
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO family_blocked_packages (family_id, package_name, label, reason)
			      VALUES ($1, $2, $3, $4)
			 ON CONFLICT (family_id, package_name)
			 DO UPDATE SET label = EXCLUDED.label, reason = EXCLUDED.reason
			   RETURNING package_name, label, reason, created_at`,
			familyID, pkg, label, reason).
			Scan(&out.PackageName, &out.Label, &out.Reason, &out.CreatedAt); err != nil {
			return err
		}
		out.Source = BlocklistSourceParent
		return bumpEveryPolicy(ctx, tx)
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &out, nil
}

// DeleteFamilyBlockedPackage takes a package off the list. The next heartbeat reveals and releases
// it on every phone, because the engine stops naming it and the DPC converges on the difference.
//
// A curated entry deletes like any other, and the deletion is recorded rather than performed: there
// is no row to remove, so what makes it stick is the dismissal. Without that, the list would be
// reassembled from the constant on the next request and the parent's decision would be undone with
// no error and nothing to read.
func (s *Store) DeleteFamilyBlockedPackage(ctx context.Context, familyID uuid.UUID, pkg string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM family_blocked_packages WHERE family_id = $1 AND package_name = $2`,
			familyID, pkg)
		if err != nil {
			return err
		}
		removed := tag.RowsAffected() > 0

		if IsDefaultBlocked(pkg) {
			dismissal, err := tx.Exec(ctx,
				`INSERT INTO family_blocklist_dismissals (family_id, package_name) VALUES ($1, $2)
				 ON CONFLICT (family_id, package_name) DO NOTHING`, familyID, pkg)
			if err != nil {
				return err
			}
			removed = removed || dismissal.RowsAffected() > 0
		}
		// Nothing changed: the package was neither the family's own entry nor a curated one still
		// in effect. Reporting that as success would tell a parent an app is now allowed when the
		// server did nothing at all.
		if !removed {
			return ErrNotFound
		}
		return bumpEveryPolicy(ctx, tx)
	})
}

// stringSet reads a single-column query into a set.
func (s *Store) stringSet(ctx context.Context, sql string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// bumpEveryPolicy makes every enrolled device notice that shared state changed.
//
// One statement rather than a loop over children: a loop would leave a window in which some
// children had been bumped and others had not, and the failure it produces — one phone enforcing
// the new list, another not — is the kind that gets blamed on the phone.
func bumpEveryPolicy(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE policies SET version = version + 1, updated_at = NOW()`)
	return err
}
