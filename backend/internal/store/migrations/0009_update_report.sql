-- What the phone's last self-update attempt did, when it did not end with a new build running.
--
-- FR-15's acknowledgement is sent BEFORE the install, because the install kills the process that
-- would otherwise send it. So "ACKED: downloaded and verified; installing now" is a statement about
-- the future, and until this column there was nowhere for the past tense to go: on 2026-09-06 the
-- pilot phone reported that ack, stayed on the previous build, and kept heartbeating — every signal
-- a parent could see was green while nothing had been installed.
--
-- Empty is the ordinary state and means "this phone has nothing to report", which is different from
-- a phone that never says anything at all: an older DPC omits the field entirely and TouchDevice
-- leaves whatever is here rather than clearing it.
ALTER TABLE device_state ADD COLUMN update_error TEXT NOT NULL DEFAULT '';

-- When the phone first reported the text above. Null whenever update_error is empty, so the two
-- cannot disagree about whether there is a problem.
ALTER TABLE device_state ADD COLUMN update_error_at TIMESTAMPTZ;
