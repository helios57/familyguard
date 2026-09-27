-- FR-23: a profile's alarm clock. The phone holds the rule and computes the next ring itself, so it
-- rings with no connection and across a timezone change; nothing here is an instant.

-- One row per weekday that rings. Monday is 1, Sunday 7; a missing row is a day without an alarm.
CREATE TABLE alarm_times (
  child_id UUID     NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  weekday  SMALLINT NOT NULL CHECK (weekday BETWEEN 1 AND 7),
  time     TEXT     NOT NULL CHECK (time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  PRIMARY KEY (child_id, weekday)
);

-- A change for one calendar day in the profile's timezone: a time, or NULL for no alarm that day.
CREATE TABLE alarm_overrides (
  child_id   UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  day        DATE        NOT NULL,
  time       TEXT        CHECK (time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  set_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  set_by     UUID        REFERENCES parents(id) ON DELETE SET NULL,
  PRIMARY KEY (child_id, day)
);

-- FR-23.4: whether the phone may show the alarm over the lock screen (Android 14+ can withhold it).
-- NULL is a phone that has not said; only a measured false is a finding.
ALTER TABLE device_state ADD COLUMN alarm_full_screen BOOLEAN;
