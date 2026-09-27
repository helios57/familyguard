-- FR-22: the daily plan, the task days, and earned time (Bonuszeit).
--
-- Groups and tasks are RETIRED rather than deleted, so a day that is past keeps the names it was
-- planned with and the owner's rule holds that the family's records are kept (at least a year).

CREATE TABLE plan_groups (
  id             UUID        PRIMARY KEY,
  child_id       UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  title          TEXT        NOT NULL CHECK (length(title) BETWEEN 1 AND 80),
  -- Bit 0 is Monday … bit 6 is Sunday; at least one day.
  weekdays       SMALLINT    NOT NULL CHECK (weekdays BETWEEN 1 AND 127),
  starts_at      TEXT        NOT NULL CHECK (starts_at ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  ends_at        TEXT        NOT NULL CHECK (ends_at ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  earned_minutes INTEGER     NOT NULL CHECK (earned_minutes BETWEEN 0 AND 1440),
  position       INTEGER     NOT NULL DEFAULT 0,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  retired_at     TIMESTAMPTZ,
  CHECK (starts_at < ends_at)
);
CREATE INDEX plan_groups_child ON plan_groups (child_id) WHERE retired_at IS NULL;

CREATE TABLE plan_tasks (
  id         UUID        PRIMARY KEY,
  group_id   UUID        NOT NULL REFERENCES plan_groups(id) ON DELETE CASCADE,
  title      TEXT        NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
  note       TEXT        NOT NULL DEFAULT '' CHECK (length(note) <= 200),
  position   INTEGER     NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  retired_at TIMESTAMPTZ
);
CREATE INDEX plan_tasks_group ON plan_tasks (group_id) WHERE retired_at IS NULL;

-- One row per task per day once anything happened to it. No row is "open".
CREATE TABLE task_days (
  child_id    UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  day         DATE        NOT NULL,
  task_id     UUID        NOT NULL REFERENCES plan_tasks(id) ON DELETE CASCADE,
  state       TEXT        NOT NULL CHECK (state IN ('REPORTED', 'CONFIRMED', 'REJECTED')),
  reported_at TIMESTAMPTZ,
  reported_by UUID        REFERENCES devices(id) ON DELETE SET NULL,
  decided_at  TIMESTAMPTZ,
  decided_by  UUID        REFERENCES parents(id) ON DELETE SET NULL,
  PRIMARY KEY (child_id, day, task_id)
);

-- A group whose tasks are all confirmed earns one credit for the day; undoing a confirmation
-- withdraws it (the row stays, withdrawn_at set).
CREATE TABLE earned_credits (
  id           UUID        PRIMARY KEY,
  child_id     UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  group_id     UUID        NOT NULL REFERENCES plan_groups(id) ON DELETE CASCADE,
  day          DATE        NOT NULL,
  minutes      INTEGER     NOT NULL CHECK (minutes BETWEEN 1 AND 1440),
  earned_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  confirmed_by UUID        REFERENCES parents(id) ON DELETE SET NULL,
  withdrawn_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX earned_credits_one_per_group_day
  ON earned_credits (child_id, group_id, day) WHERE withdrawn_at IS NULL;

-- The part of a package's day that the phone paid from earned time, cumulative like foreground_ms.
ALTER TABLE usage_samples ADD COLUMN earned_ms BIGINT NOT NULL DEFAULT 0 CHECK (earned_ms >= 0);

-- A fifth answer for an app: it runs only on earned time.
ALTER TABLE app_rules DROP CONSTRAINT app_rules_action_check;
ALTER TABLE app_rules ADD CONSTRAINT app_rules_action_check
  CHECK (action IN ('ALLOW', 'BLOCK', 'LIMIT', 'BONUS'));
