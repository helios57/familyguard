-- What ran WHEN, not just for how long (FR-3.7).
--
-- `usage_samples` answers "how long was this app in the foreground on this day", which is what a
-- quota needs and what the console's Activity list shows. It cannot answer the question the owner
-- actually asked — *"I want the recording and precise tracking what app was running when for how
-- long"* — because a day total has no shape: forty minutes of YouTube is the same row whether it
-- was one sitting after school or twenty two-minute visits spread through a lesson.
--
-- The phone already computes this. `SpanFolder` folds the platform's ACTIVITY_RESUMED/PAUSED events
-- into spans with their TRUE start — `ForegroundSpan`'s own KDoc says *"a record of what ran when
-- wants the true start"* — and then `UsageTracker` clamps them to the poll window, adds them up per
-- day, and throws the spans away. This table is where they land instead.
--
-- ### Intervals, stored raw, never split at midnight
--
-- A session that runs from 23:50 to 00:10 is one sitting, and splitting it into two rows would make
-- the console draw two. The day a session belongs to is a question the READER asks, with the
-- child's timezone in hand, as an overlap: `ended_at > day_start AND started_at < day_end`. That is
-- also the only form that stays correct when a family moves timezone, which a stored `day` column
-- would not.
--
-- ### The primary key is the idempotency
--
-- (device_id, package_name, started_at). A phone that loses its network re-sends what it could not
-- deliver, and a session is identified by when it began — so a re-send updates the row it already
-- wrote rather than drawing the same sitting twice. `ended_at` merges with GREATEST for the same
-- reason `usage_samples.foreground_ms` does: a report that arrives twice, out of order, or after a
-- retry must never be able to SHORTEN a session the server already knows about.
--
-- ### Retention
--
-- Nothing deletes from this table. The owner's ruling on the data this product keeps was *"No need
-- to delete the data, keep it for at least 1 year"*, and at the volume one phone produces — a few
-- hundred sessions a day, so ~100k rows a year — a year is not a number worth engineering for.
CREATE TABLE usage_sessions (
  device_id    UUID        NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  package_name TEXT        NOT NULL,
  started_at   TIMESTAMPTZ NOT NULL,
  ended_at     TIMESTAMPTZ NOT NULL,
  reported_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (device_id, package_name, started_at),
  -- A zero-length session is not a sitting, it is a transition the platform reported twice. The
  -- device filters them out; this makes it impossible for a device that does not.
  CONSTRAINT usage_sessions_order_check CHECK (ended_at > started_at)
);

-- The one query the console makes: everything this device ran inside a window, in order. Leading
-- with started_at rather than with the package, because no view of this data is per-package first.
CREATE INDEX usage_sessions_window_idx ON usage_sessions (device_id, started_at);
