-- FR-28: a child asks for more time from the phone, and a parent answers from the console; the
-- answer is today's extra time (FR-21.2), so it ends at midnight like any other.
--
-- One open request per profile and day (a second tap is the same question); a request from an
-- earlier day is never answered — the day it asked about is over — and stays as it was, kept like
-- every other family record.
CREATE TABLE time_requests (
  id              UUID        PRIMARY KEY,
  child_id        UUID        NOT NULL REFERENCES children(id) ON DELETE CASCADE,
  device_id       UUID        REFERENCES devices(id) ON DELETE SET NULL,
  day             DATE        NOT NULL,
  minutes         INTEGER     NOT NULL CHECK (minutes BETWEEN 5 AND 240),
  note            TEXT        NOT NULL DEFAULT '' CHECK (length(note) <= 140),
  state           TEXT        NOT NULL DEFAULT 'OPEN' CHECK (state IN ('OPEN', 'GRANTED', 'DECLINED')),
  granted_minutes INTEGER     CHECK (granted_minutes BETWEEN 1 AND 1440),
  requested_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  decided_at      TIMESTAMPTZ,
  decided_by      UUID        REFERENCES parents(id) ON DELETE SET NULL,
  CHECK ((state = 'GRANTED') = (granted_minutes IS NOT NULL))
);
CREATE UNIQUE INDEX time_requests_one_open ON time_requests (child_id, day) WHERE state = 'OPEN';
CREATE INDEX time_requests_child_day ON time_requests (child_id, day);

-- FR-28.4: a parent's browser, subscribed to Web Push, so a request reaches a parent whose console
-- is closed. One row per browser; the endpoint is the push service's address for it.
CREATE TABLE web_push_subscriptions (
  id         UUID        PRIMARY KEY,
  parent_id  UUID        NOT NULL REFERENCES parents(id) ON DELETE CASCADE,
  endpoint   TEXT        NOT NULL UNIQUE CHECK (length(endpoint) BETWEEN 1 AND 2048),
  p256dh     TEXT        NOT NULL CHECK (length(p256dh) BETWEEN 1 AND 256),
  auth       TEXT        NOT NULL CHECK (length(auth) BETWEEN 1 AND 64),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_ok_at TIMESTAMPTZ
);
CREATE INDEX web_push_subscriptions_parent ON web_push_subscriptions (parent_id);

-- Keys the server makes for itself on first use and keeps: the VAPID key pair Web Push signs with.
-- In the database rather than in a deployment secret, because a key that changes invalidates every
-- browser's subscription, and the database is the one thing that outlives every deployment.
CREATE TABLE server_keys (
  name       TEXT        PRIMARY KEY,
  value      TEXT        NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
