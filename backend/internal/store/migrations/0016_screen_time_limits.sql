-- Screen time that is use, extra time for one day, and the limit that applied on a day
-- (FR-3.8, FR-3.11, FR-3.9).
--
-- Measured 2026-09-23 on the family phone: the daily limit was spent with nobody using the phone.
-- It lay on its charger with "Stay awake" on, and 57 of the 120 minutes counted were the HOME
-- SCREEN being shown. A home screen is not use. The phone now reports which packages are its home
-- screen, and the count leaves them out — together with System UI and this system's own app.
--
-- ### bonus_minutes
--
-- Extra minutes for one child on ONE local day, on top of the daily limit. Keyed by the child's
-- calendar day in the policy's timezone, the same day key usage_samples uses, so the bonus and the
-- minutes it offsets can never be attributed to different days. Granting twice adds up; the total
-- is bounded to one day.
CREATE TABLE bonus_minutes (
  child_id   UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  day        DATE        NOT NULL,
  minutes    INTEGER     NOT NULL CHECK (minutes > 0 AND minutes <= 1440),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (child_id, day)
);

-- What the phone says its home screen is, reported on every heartbeat because a child can change
-- launchers. Empty until a phone that knows the field reports it, which leaves the count as it was.
ALTER TABLE device_state ADD COLUMN home_packages TEXT[] NOT NULL DEFAULT '{}';

-- ### day_limits
--
-- The limits that applied on a day, recorded while the day is current. A policy holds only the
-- limit in force NOW, so "what was the limit on Tuesday" had no answer: a chart of last week drawn
-- against today's limit would show a day as over its limit that was within it at the time. The last
-- value written on a day is the one it ends with — a parent who raises the limit at 17:00 has that
-- day's limit raised.
CREATE TABLE day_limits (
  child_id            UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  day                 DATE        NOT NULL,
  daily_limit_minutes INTEGER     NOT NULL CHECK (daily_limit_minutes >= 0),
  bonus_minutes       INTEGER     NOT NULL DEFAULT 0 CHECK (bonus_minutes >= 0),
  -- package name -> that app's own allowance in minutes (LIMIT rules with minutes > 0).
  app_limits          JSONB       NOT NULL DEFAULT '{}'::jsonb,
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (child_id, day)
);
