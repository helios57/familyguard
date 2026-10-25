-- FR-30: learning apps earn time, and a daily limit of 0 can be a limit.
--
-- ### families.learning_prefixes
--
-- The package-name prefixes that make an app a learning app, for every child of the family. A family
-- setting rather than a per-child rule, so an app published under the prefix next month earns from
-- its first minute on every phone with nothing set per child. Empty is "no learning apps", which is
-- what every family has until a parent says otherwise.
ALTER TABLE families ADD COLUMN learning_prefixes TEXT[] NOT NULL DEFAULT '{}';

-- ### policies.daily_limit_enabled
--
-- Until now 0 minutes meant "no daily limit", so "nothing without extra or earned time" could not be
-- said at all. The switch says whether there is a limit; the minutes say how large it is. Every
-- existing policy keeps its meaning: on exactly where the minutes were above 0.
ALTER TABLE policies ADD COLUMN daily_limit_enabled BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE policies SET daily_limit_enabled = daily_limit_minutes > 0;

-- ### day_limits.limit_enabled
--
-- The switch as it was on the day. NULL on a row written before this column existed, which reads as
-- the old meaning: a limit exactly when the minutes were above 0.
ALTER TABLE day_limits ADD COLUMN limit_enabled BOOLEAN;

-- ### usage_samples.learning_ms
--
-- Of foreground_ms, the part the phone counted as learning: while the package was a learning app and
-- the phone was enforcing. Cumulative for the day and merged with GREATEST like the total, so a
-- re-sent report never doubles a credit and a credit, once reported, never shrinks. Counted by the
-- phone at the moment it measures, so setting a prefix turns no earlier minute into a credit.
ALTER TABLE usage_samples ADD COLUMN learning_ms BIGINT NOT NULL DEFAULT 0 CHECK (learning_ms >= 0);
