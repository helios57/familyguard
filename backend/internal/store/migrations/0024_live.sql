-- FR-27: Live mode. A parent or guardian asks a phone to stay connected and report its position every
-- few seconds until live_until. live_since is when that session began: a guardian may read the
-- positions captured since then, and never the history before it.
ALTER TABLE devices ADD COLUMN live_until TIMESTAMPTZ;
ALTER TABLE devices ADD COLUMN live_since TIMESTAMPTZ;
