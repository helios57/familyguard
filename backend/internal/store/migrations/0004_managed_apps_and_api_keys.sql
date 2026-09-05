-- FR-16 (a declared set of applications per child) and FR-17 (API keys), which arrive together
-- because the same request asked for both: the catalog has to be reachable by a script, not only by
-- a browser.
--
-- Three concerns, one migration, because they are one feature. Splitting them would produce an
-- intermediate state where the catalog exists and nothing may write to it.

-- ---- the catalog ------------------------------------------------------------------------------
--
-- One row per (package, version). Several versions of one package may be registered at once — that
-- is what makes a rollback a matter of choosing a row rather than re-uploading a file — and the
-- newest is what a child's declared set resolves to.
--
-- Every column except `label` and `source` is read out of the APK by `internal/apk`, never typed by
-- a person. A package name entered by hand is a package name that is eventually wrong, and a wrong
-- one here is a phone installing something nobody chose.
CREATE TABLE apps (
    id            UUID PRIMARY KEY,
    package_name  TEXT   NOT NULL,
    version_code  BIGINT NOT NULL,
    version_name  TEXT   NOT NULL DEFAULT '',
    -- Display only. Taken from the manifest when it is a literal string; a label stored as
    -- @string/app_name resolves through resources.arsc, which this server does not parse, so it
    -- falls back to whatever the uploader supplied and then to the package name.
    label         TEXT   NOT NULL DEFAULT '',
    -- Lowercase hex, over the file as delivered. The phone recomputes it after downloading, so it
    -- has to describe the same bytes end to end.
    sha256        TEXT   NOT NULL,
    signer_sha256 TEXT   NOT NULL,
    size_bytes    BIGINT NOT NULL,
    min_sdk       INTEGER NOT NULL DEFAULT 0,
    -- Relative to APK_DIR, never absolute: the directory moves between the container and a
    -- developer's machine, and a stored absolute path is a row that only resolves on one host.
    file_name     TEXT   NOT NULL,
    -- 'node' (found by the directory scan) or 'upload' (arrived through the API). Kept because the
    -- two have different failure modes — a file that vanishes from the node is an operator action,
    -- an upload that vanishes is a bug — and the audit trail cannot distinguish them afterwards.
    source        TEXT   NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT apps_source_is_known CHECK (source IN ('NODE', 'UPLOAD')),
    CONSTRAINT apps_version_is_unique UNIQUE (package_name, version_code)
);
CREATE INDEX apps_by_package ON apps (package_name, version_code DESC);

-- The trust-on-first-registration pin.
--
-- The first version of a package registered fixes its signer; a later version signed by a different
-- key is refused. This is NOT independent verification and must not be read as such — the server
-- only ever sees what it was given, so the first registration is trusted on its face. What the pin
-- buys is that a *substitution* later is refused, which is the attack that matters once a package
-- is in a child's declared set and installs itself without anyone looking.
--
-- Separate from `apps` rather than derived from it, so that deleting every version of a package does
-- not silently unpin it. Re-registering a package under a new key has to be a deliberate act.
CREATE TABLE app_signers (
    package_name  TEXT PRIMARY KEY,
    signer_sha256 TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- What a child's phone should have installed.
--
-- A set of package NAMES, not of app rows: the child is declared to have "muplay", and which
-- version that resolves to is answered at sync time by the newest row in `apps`. Pinning a version
-- here would mean every new build needs every child edited, and the owner rule for this project is
-- that the latest version is the one that runs.
--
-- ON DELETE CASCADE on the child is obvious. On the package it is deliberately absent: `apps` rows
-- come and go as versions are added and pruned, and a declaration must survive its current version
-- being deleted — otherwise pruning an old APK silently unassigns the app.
CREATE TABLE child_managed_apps (
    child_id     UUID NOT NULL REFERENCES children(id) ON DELETE CASCADE,
    package_name TEXT NOT NULL,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (child_id, package_name)
);

-- ---- API keys ---------------------------------------------------------------------------------
--
-- A key authenticates AS a parent: it carries no authority of its own, so every role check, every
-- audit entry and every rate-limit bucket that already exists keeps working, and revoking the
-- parent revokes the key with it (hence ON DELETE CASCADE, not SET NULL).
--
-- Only the hash is stored, the same way device tokens are — a database dump must not yield usable
-- credentials. `prefix` is the clear, non-secret first component: it is what the console lists, and
-- what an operator matches a key in a script against the row that authorises it.
CREATE TABLE api_keys (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    prefix     TEXT NOT NULL,
    token_hash BYTEA NOT NULL,
    parent_id  UUID NOT NULL REFERENCES parents(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    CONSTRAINT api_keys_prefix_is_unique UNIQUE (prefix),
    CONSTRAINT api_keys_hash_is_unique UNIQUE (token_hash)
);
CREATE INDEX api_keys_by_parent ON api_keys (parent_id);

-- The audit log has to be able to record an API key as the actor, and 0001's CHECK does not list
-- it. Without this the insert fails — and `httpapi.audit` deliberately logs an audit failure rather
-- than failing the request it describes, so the effect is not a mislabelled row but NO ROW AT ALL:
-- every action taken through a key would be missing from the log, silently, while the action
-- itself succeeded. Found by the end-to-end test that counts entries by actor type; nothing else
-- would have shown it, because the request returns 201 either way.
--
-- Written as a constraint swap rather than an edit to 0001, which has been applied in production
-- and will never run again.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_type_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_type_check
    CHECK (actor_type IN ('PARENT', 'DEVICE', 'SYSTEM', 'API_KEY'));
