-- FR-21: a parent's or guardian's pause, and a day's time that can go down as well as up.
--
-- The pause is a column of the policy rather than a command row, for the reason bedtime is: a state
-- survives a reboot, a lost event and an offline phone, and it ends only when someone unpauses.
-- paused_by keeps who did it for as long as that parent exists; the audit log keeps it for good.
ALTER TABLE policies ADD COLUMN paused    BOOLEAN     NOT NULL DEFAULT FALSE;
ALTER TABLE policies ADD COLUMN paused_at TIMESTAMPTZ;
ALTER TABLE policies ADD COLUMN paused_by UUID REFERENCES parents(id) ON DELETE SET NULL;

-- A day's adjustment is signed: negative is time taken away. The server keeps the day's limit
-- between zero and 1440 minutes; these checks are the outer bound.
ALTER TABLE bonus_minutes DROP CONSTRAINT bonus_minutes_minutes_check;
ALTER TABLE bonus_minutes ADD CONSTRAINT bonus_minutes_minutes_check
  CHECK (minutes BETWEEN -1440 AND 1440);
ALTER TABLE day_limits DROP CONSTRAINT day_limits_bonus_minutes_check;
ALTER TABLE day_limits ADD CONSTRAINT day_limits_bonus_minutes_check
  CHECK (bonus_minutes BETWEEN -1440 AND 1440);
