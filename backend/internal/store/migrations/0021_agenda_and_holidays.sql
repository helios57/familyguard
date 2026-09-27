-- FR-24: a profile's agenda, the family's holidays, and the alarm's "not during holidays".
-- Entries and holidays are RETIRED rather than deleted, like the plan: the family's records are kept.

CREATE TABLE agenda_entries (
  id         UUID        PRIMARY KEY,
  child_id   UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  kind       TEXT        NOT NULL CHECK (kind IN ('RECURRING', 'SINGLE')),
  title      TEXT        NOT NULL CHECK (length(title) BETWEEN 1 AND 80),
  place      TEXT        NOT NULL DEFAULT '' CHECK (length(place) <= 80),
  optional   BOOLEAN     NOT NULL DEFAULT FALSE,
  -- RECURRING: bit 0 Monday … bit 6 Sunday. SINGLE: the date.
  weekdays   SMALLINT    CHECK (weekdays BETWEEN 1 AND 127),
  day        DATE,
  starts_at  TEXT        NOT NULL CHECK (starts_at ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  ends_at    TEXT        NOT NULL CHECK (ends_at ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  position   INTEGER     NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  retired_at TIMESTAMPTZ,
  CHECK (starts_at < ends_at),
  CHECK ((kind = 'RECURRING' AND weekdays IS NOT NULL AND day IS NULL) OR
         (kind = 'SINGLE' AND day IS NOT NULL AND weekdays IS NULL))
);
CREATE INDEX agenda_entries_child ON agenda_entries (child_id) WHERE retired_at IS NULL;

CREATE TABLE holidays (
  id         UUID        PRIMARY KEY,
  family_id  UUID        NOT NULL REFERENCES families(id) ON DELETE CASCADE,
  title      TEXT        NOT NULL CHECK (length(title) BETWEEN 1 AND 80),
  starts_on  DATE        NOT NULL,
  ends_on    DATE        NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  retired_at TIMESTAMPTZ,
  CHECK (starts_on <= ends_on)
);

CREATE TABLE alarm_settings (
  child_id      UUID    PRIMARY KEY REFERENCES children(id) ON DELETE CASCADE,
  skip_holidays BOOLEAN NOT NULL DEFAULT FALSE
);
