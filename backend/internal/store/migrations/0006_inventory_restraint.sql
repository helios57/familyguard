-- FR-18.6: the phone says, per app, whether this DPC has it hidden or suspended.
--
-- Without these two columns the console can only show what the server ASKED for. A parent asking
-- "is the bloatware gone?" would be answered from the policy, which is a statement about intent —
-- and the one failure mode this project treats as unacceptable is a control that reports success
-- having evaluated nothing.
--
-- Defaulting to FALSE is honest for existing rows: they were reported by a DPC that did not send
-- the flags, so nothing is known to be restrained. The next inventory a phone sends overwrites
-- them, and the digest on the device covers both fields, so a phone whose restraint state changed
-- re-sends even when its app list did not.
ALTER TABLE installed_apps
    ADD COLUMN hidden    BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN suspended BOOLEAN NOT NULL DEFAULT FALSE;
