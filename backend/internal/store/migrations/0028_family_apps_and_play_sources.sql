-- FR-16.7 (an application every child has, including a child added later) and FR-16.8 (an
-- application whose builds come from Google Play rather than from an upload).
--
-- ---- the family's set -------------------------------------------------------------------------
--
-- A second declared set beside child_managed_apps, not a flag on it. A row per child would mean
-- "every child" is a fact written N times, and the child created next month would not be one of
-- them — which is exactly the case FR-16.7 exists for. A child's effective set is the union of its
-- own rows and these, computed where the phone's policy is computed (ManagedAppsForChild), so it
-- cannot be stale for a child nobody remembered to update.
--
-- Withdrawing a package here removes it from every child that does not also declare it on its own;
-- a child-level row is a separate decision and is left alone.
CREATE TABLE family_managed_apps (
    family_id    UUID NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    package_name TEXT NOT NULL,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (family_id, package_name)
);

-- ---- Play as a source of builds ---------------------------------------------------------------
--
-- One row per package the server follows on Google Play. The builds themselves still land in
-- `apps` like any other — same parser, same signer pin, same download route — so a Play build is
-- not a second kind of application anywhere downstream; it is only a different way in.
--
-- `tracks` is a list because what a family wants is "the newest release Google has published"
-- and a developer's newest published release can sit on a testing track ahead of production.
-- Only PUBLISHED releases are taken: a release in review is one Google has not finished checking.
--
-- The last check is recorded on the row, success or failure, because "the server follows this app"
-- and "the server has been able to read it" are different facts and the console must show which.
CREATE TABLE play_sources (
    package_name       TEXT        PRIMARY KEY,
    tracks             TEXT[]      NOT NULL CHECK (cardinality(tracks) > 0),
    label              TEXT        NOT NULL DEFAULT '',
    added_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    checked_at         TIMESTAMPTZ,
    -- The newest published version code Play reported at the last successful check; NULL when
    -- nothing is published on any of the tracks yet.
    published_version  BIGINT,
    last_error         TEXT        NOT NULL DEFAULT ''
);

ALTER TABLE apps DROP CONSTRAINT apps_source_is_known;
ALTER TABLE apps ADD CONSTRAINT apps_source_is_known CHECK (source IN ('NODE', 'UPLOAD', 'PLAY'));
