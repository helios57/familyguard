-- FamilyGuard MDM — initial schema.
--
-- Single tenant by design (REQUIREMENTS.md §5): one family row exists, and every other table
-- hangs off it. The family_id columns record which family a row belongs to; they are NOT a
-- tenant filter, and no query here uses them as one — see 0003, which is what actually holds the
-- table to a single row.

CREATE TABLE families (
    id         UUID PRIMARY KEY,
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE parents (
    id            UUID PRIMARY KEY,
    family_id     UUID        NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    -- Email is the join key against a Google ID token, so it is folded to lowercase at every
    -- boundary and constrained here. A free-text natural key that differs only in case splits one
    -- person into two accounts the first time someone signs in from a different device.
    email         TEXT        NOT NULL UNIQUE CHECK (email = lower(email) AND position('@' IN email) > 1),
    display_name  TEXT        NOT NULL DEFAULT '',
    google_sub    TEXT UNIQUE,
    role          TEXT        NOT NULL CHECK (role IN ('PRIMARY_ADMIN', 'ADMIN', 'GUARDIAN')),
    -- No column for a Factory Reset Protection account: FRP is deliberately not registered (FR-2.3),
    -- so a factory reset always recovers the phone without this control plane.
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE children (
    id         UUID PRIMARY KEY,
    family_id  UUID        NOT NULL REFERENCES families (id) ON DELETE CASCADE,
    name       TEXT        NOT NULL,
    birth_year INT CHECK (birth_year IS NULL OR (birth_year BETWEEN 1900 AND 2200)),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX children_family_idx ON children (family_id);

CREATE TABLE devices (
    id                    UUID PRIMARY KEY,
    child_id              UUID        NOT NULL REFERENCES children (id) ON DELETE CASCADE,
    name                  TEXT        NOT NULL,
    -- Enrollment credential. Stored hashed: a database read must not yield a usable token.
    -- Consumed exactly once (FR-1.4) by clearing enrollment_token_hash inside the exchange.
    enrollment_token_hash BYTEA UNIQUE,
    enrollment_expires_at TIMESTAMPTZ,
    enrolled_at           TIMESTAMPTZ,
    device_token_hash     BYTEA UNIQUE,
    -- Per-device recovery code (FR-12.3). Never shared between devices, never in source.
    recovery_code         TEXT,
    recovery_salt         BYTEA,
    recovery_iterations   INT,
    recovery_hash         BYTEA,
    model                 TEXT        NOT NULL DEFAULT '',
    os_version            TEXT        NOT NULL DEFAULT '',
    -- A parent's LOCK_NOW is state, not just a command (FR-9.1). A command expires and a rebooted
    -- device would come back unlocked; a flag survives both, and UNLOCK_DEVICE clears it.
    locked                BOOLEAN     NOT NULL DEFAULT FALSE,
    -- The packages this device says must never be suspended: its own dialer, launcher and IME,
    -- reported at enrollment. The server unions them with the built-in list and never narrows it,
    -- so a device that reports nothing still keeps emergency calling (FR-13.2).
    critical_packages     TEXT[]      NOT NULL DEFAULT '{}',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX devices_child_idx ON devices (child_id);

CREATE TABLE device_state (
    device_id      UUID PRIMARY KEY REFERENCES devices (id) ON DELETE CASCADE,
    battery_level  INT CHECK (battery_level IS NULL OR battery_level BETWEEN 0 AND 100),
    charging       BOOLEAN,
    screen_on      BOOLEAN,
    connectivity   TEXT        NOT NULL DEFAULT '',
    policy_version BIGINT      NOT NULL DEFAULT 0,
    last_seen_at   TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE policies (
    child_id             UUID PRIMARY KEY REFERENCES children (id) ON DELETE CASCADE,
    tracking_only        BOOLEAN     NOT NULL DEFAULT FALSE,
    allow_child_installs BOOLEAN     NOT NULL DEFAULT TRUE,
    youtube_blocked      BOOLEAN     NOT NULL DEFAULT FALSE,
    daily_limit_minutes  INT         NOT NULL DEFAULT 0 CHECK (daily_limit_minutes BETWEEN 0 AND 1440),
    bedtime_enabled      BOOLEAN     NOT NULL DEFAULT FALSE,
    bedtime_start        TEXT        NOT NULL DEFAULT '21:00' CHECK (bedtime_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    bedtime_end          TEXT        NOT NULL DEFAULT '07:00' CHECK (bedtime_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    dns_host             TEXT        NOT NULL DEFAULT 'family.adguard-dns.com',
    -- The day boundary and the bedtime window are wall-clock local (FR-3.4, FR-4.1), so the zone
    -- is policy, not a device setting: a child changing the device's time zone must not move
    -- bedtime.
    timezone             TEXT        NOT NULL DEFAULT 'Europe/Zurich' CHECK (timezone <> ''),
    -- Monotonically increasing; the device compares it to decide whether to re-apply.
    version              BIGINT      NOT NULL DEFAULT 1,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE app_rules (
    child_id     UUID NOT NULL REFERENCES children (id) ON DELETE CASCADE,
    package_name TEXT NOT NULL,
    action       TEXT NOT NULL CHECK (action IN ('ALLOW', 'BLOCK')),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (child_id, package_name)
);

CREATE TABLE blocked_domains (
    child_id   UUID NOT NULL REFERENCES children (id) ON DELETE CASCADE,
    -- Folded to lowercase and stripped of a trailing dot at the boundary, and constrained here,
    -- so "Example.com" and "example.com" cannot become two rules only one of which can be removed.
    domain     TEXT NOT NULL CHECK (domain = lower(domain) AND domain NOT LIKE '%.'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (child_id, domain)
);

-- baseline marks the apps that were on the phone the first time it told us what it had. It is what
-- FR-5.4 measures novelty against, and it is deliberately NOT derived from first_seen_at or from
-- devices.enrolled_at: the first inventory report necessarily arrives after enrolment, so every row
-- it writes has first_seen_at > enrolled_at and comparing the two sweeps the entire preinstalled
-- catalogue into the approval queue the moment a parent turns free installation off.
CREATE TABLE installed_apps (
    device_id     UUID NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    package_name  TEXT NOT NULL,
    label         TEXT NOT NULL DEFAULT '',
    system_app    BOOLEAN NOT NULL DEFAULT FALSE,
    baseline      BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    removed_at    TIMESTAMPTZ,
    PRIMARY KEY (device_id, package_name)
);

CREATE TABLE usage_samples (
    device_id     UUID NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    day           DATE NOT NULL,
    package_name  TEXT NOT NULL,
    foreground_ms BIGINT NOT NULL CHECK (foreground_ms >= 0),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (device_id, day, package_name)
);
CREATE INDEX usage_samples_day_idx ON usage_samples (device_id, day);

CREATE TABLE commands (
    id           UUID PRIMARY KEY,
    device_id    UUID NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    type         TEXT NOT NULL,
    params       JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- QUEUED -> DELIVERED -> ACKED, or FAILED / EXPIRED. A command is never created in a state
    -- that claims delivery (NFR-3); only the hub may advance it.
    state        TEXT NOT NULL CHECK (state IN ('QUEUED', 'DELIVERED', 'ACKED', 'FAILED', 'EXPIRED')),
    issued_by    UUID REFERENCES parents (id) ON DELETE SET NULL,
    result       JSONB,
    error        TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    acked_at     TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX commands_pending_idx ON commands (device_id, created_at) WHERE state = 'QUEUED';
CREATE INDEX commands_device_idx ON commands (device_id, created_at DESC);

CREATE TABLE locations (
    id          UUID PRIMARY KEY,
    device_id   UUID NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    latitude    DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude   DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    accuracy_m  DOUBLE PRECISION,
    captured_at TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX locations_device_idx ON locations (device_id, captured_at DESC);

CREATE TABLE recovery_events (
    id          UUID PRIMARY KEY,
    device_id   UUID NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    succeeded   BOOLEAN NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX recovery_events_device_idx ON recovery_events (device_id, occurred_at DESC);

CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    actor_type  TEXT NOT NULL CHECK (actor_type IN ('PARENT', 'DEVICE', 'SYSTEM')),
    actor_id    TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    detail      JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX audit_log_time_idx ON audit_log (occurred_at DESC);
