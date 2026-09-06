-- FR-18: one blocklist for the whole family, so "get Facebook off the phones" is one decision
-- rather than one per child and one more for every child added later.
--
-- `app_rules` already blocks a package, and what it produces is exactly right — suspended and
-- hidden, whether or not the package is currently installed, so a reinstall is covered. What it
-- cannot express is "on every phone this family owns". It is keyed by `child_id`, so the same
-- decision has to be repeated per child, and a child added next year silently starts without it.
-- That is the defect these tables remove: the scope of the decision now matches the scope of the
-- intent.
--
-- Per-child rules are untouched and still win where they disagree: a child-level ALLOW exempts that
-- child from a family entry, and a child-level BLOCK is never overruled by one.
--
-- ---- why there are two tables and no INSERT ------------------------------------------------------
--
-- The curated set of known bloatware is NOT seeded here. It lives in Go, in
-- `store.DefaultBlockedPackages`, next in kind to `policy.DefaultCriticalPackages` and
-- `policy.YouTubePackages` — curated package lists that ship in the binary and are unioned at read
-- time. Three reasons, and the first is a rule this project already enforces:
--
--   1. NFR-5. A deployable schema creates tables and plants no rows, and `TestAFreshSystemShowsAsEmpty`
--      scans these files to prove it. The first version of this migration did seed, and that test
--      failed it — correctly. (It would also have seeded nothing: migrations run before Bootstrap
--      creates the family row, so the INSERT ... SELECT FROM families matched zero rows and produced
--      an empty list that looked exactly like a working one.)
--   2. A list in code reaches deployments that already exist. Adding a package to a seed helps only
--      installations created after it; adding one to the constant reaches the family whose phone is
--      already enrolled, which is the case that matters.
--   3. It is reviewable as code, next to the reasoning about which packages are safe to hide.
--
-- So the effective list is (curated set MINUS dismissals) UNION the parent's own entries, composed
-- in `blocklist.go`. `family_blocklist_dismissals` is what makes a parent's deletion of a curated
-- entry permanent (FR-18.5): without it, a list assembled from a constant would put the entry back
-- on the next restart, with no error and nothing for the parent to read.

-- Packages this family added itself.
CREATE TABLE family_blocked_packages (
    family_id    UUID NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    package_name TEXT NOT NULL,
    -- Display only, and free to be empty. The console prefers the label the phone reported in
    -- `installed_apps` when it has one; this is what to show for a package no enrolled phone has
    -- reported yet.
    label        TEXT NOT NULL DEFAULT '',
    -- Why this package is on the list, in a parent's words. It is the column that makes the list
    -- reviewable a year from now: a bare package name gives nobody a way to decide whether removing
    -- it is safe.
    reason       TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (family_id, package_name)
);

-- Entries of the curated set this family removed. One row is a decision, not an absence, which is
-- why it is recorded rather than inferred.
CREATE TABLE family_blocklist_dismissals (
    family_id    UUID NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    package_name TEXT NOT NULL,
    dismissed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (family_id, package_name)
);

-- Every enrolled phone has to notice that the curated set now applies. A device fetches new state
-- when the version it is holding is older than the one the server reports, so without this the list
-- would sit in the binary being correct and unenforced until something unrelated bumped a version.
--
-- No-op on a fresh database, which has no policies yet. It is here for the deployment that already
-- has a phone on it.
UPDATE policies SET version = version + 1, updated_at = NOW();
