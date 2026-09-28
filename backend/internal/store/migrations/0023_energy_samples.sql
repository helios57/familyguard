-- FR-26.5: what FamilyGuard spends on a phone, as the phone measured it, one row per heartbeat that
-- carried a report. Counters are cumulative since `since` (when the phone's process started), so the
-- spend over an interval is the difference of two rows with the same `since`; a new `since` is a
-- restart and starts a new run rather than producing a negative difference.
--
-- The battery columns are the heartbeat's own, stored beside the counters because the question this
-- table answers is "how fast does the battery fall, and how much of that is us".
CREATE TABLE energy_samples (
  id              BIGSERIAL   PRIMARY KEY,
  device_id       UUID        NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  since           TIMESTAMPTZ NOT NULL,
  battery_level   SMALLINT    CHECK (battery_level BETWEEN 0 AND 100),
  charging        BOOLEAN,
  cpu_ms          BIGINT      NOT NULL CHECK (cpu_ms >= 0),
  rx_bytes        BIGINT      NOT NULL CHECK (rx_bytes >= 0),
  tx_bytes        BIGINT      NOT NULL CHECK (tx_bytes >= 0),
  stream_opens    BIGINT      NOT NULL CHECK (stream_opens >= 0),
  events          BIGINT      NOT NULL CHECK (events >= 0),
  polls           BIGINT      NOT NULL CHECK (polls >= 0),
  pushes          BIGINT      NOT NULL CHECK (pushes >= 0),
  other_syncs     BIGINT      NOT NULL CHECK (other_syncs >= 0),
  -- Reported by builds that have the modes (FR-26.1) and the screen-off route (FR-26.4); NULL from
  -- one that does not, which is "not measured", never zero.
  active_ms       BIGINT      CHECK (active_ms >= 0),
  passive_ms      BIGINT      CHECK (passive_ms >= 0),
  route_full_ms   BIGINT      CHECK (route_full_ms >= 0),
  route_dns_ms    BIGINT      CHECK (route_dns_ms >= 0)
);
CREATE INDEX energy_samples_device_at ON energy_samples (device_id, at);
