-- FR-29: a parent's sign-in is a server-side record rather than a signed token with a fixed life.
--
-- The 24-hour signed session this replaces could not be ended before it expired and could not be
-- extended while it was used, so a parent signed in once a day and a lost phone stayed signed in for
-- the rest of its day. A row can do both: `last_used_at` moves with every request, a sign-in idle for
-- longer than SIGN_IN_IDLE_TIMEOUT is refused in the lookup's own WHERE clause, and `ended_at` ends it
-- the moment a parent signs out — wherever a copy of the token is.
--
-- Only the SHA-256 of the token is stored, as for API keys and device tokens: a database dump yields
-- no usable credential. ON DELETE CASCADE is FR-29.5: removing a parent ends their sign-ins.
CREATE TABLE parent_sign_ins (
    id           UUID        PRIMARY KEY,
    parent_id    UUID        NOT NULL REFERENCES parents (id) ON DELETE CASCADE,
    token_hash   BYTEA       NOT NULL,
    label        TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at     TIMESTAMPTZ,
    CONSTRAINT parent_sign_ins_hash_is_unique UNIQUE (token_hash)
);
CREATE INDEX parent_sign_ins_by_parent ON parent_sign_ins (parent_id);
