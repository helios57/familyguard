-- FR-25: a profile's calendar, read-only. The address is a credential (a secret calendar address
-- reads the calendar), so it lives here and nowhere else — not in the audit log, not in a log line.
-- The body is the last copy that was read AND parsed; a failed read keeps it and records the error.
CREATE TABLE calendar_sources (
  child_id        UUID        PRIMARY KEY REFERENCES children(id) ON DELETE CASCADE,
  url             TEXT        NOT NULL CHECK (length(url) BETWEEN 1 AND 2000),
  body            BYTEA,
  fetched_at      TIMESTAMPTZ,
  last_attempt_at TIMESTAMPTZ,
  last_error      TEXT        NOT NULL DEFAULT ''
);
