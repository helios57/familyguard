-- FR-31: only contacts may call.
--
-- ### policies.calls_contacts_only
--
-- The per-child switch. Off for every child until a parent turns it on: it rejects calls, and a
-- phone that starts rejecting calls nobody asked it to reject is a phone a parent cannot reach.
ALTER TABLE policies ADD COLUMN calls_contacts_only BOOLEAN NOT NULL DEFAULT FALSE;

-- ### families.family_numbers
--
-- The numbers that always get through, even when a child has deleted or never saved them. E.164,
-- normalised by the server before they are stored, so the phone compares like with like.
ALTER TABLE families ADD COLUMN family_numbers TEXT[] NOT NULL DEFAULT '{}';

-- ### device_state.call_screening, device_state.call_screening_held_at
--
-- Whether the phone holds the call-screening role, as it last said; NULL is "has not said", which is
-- what every phone reports while the switch is off. held_at is the first time it said yes and is
-- never cleared: the phone only knows what is true now, and "never granted" (waiting for the parent
-- on the phone) and "taken away" (a fault) need different words in the console.
ALTER TABLE device_state ADD COLUMN call_screening BOOLEAN;
ALTER TABLE device_state ADD COLUMN call_screening_held_at TIMESTAMPTZ;

-- ### rejected_calls
--
-- One row per call the phone rejected. client_id is the phone's own id for the call, so a report
-- re-sent after a lost response is stored once. number is what the phone saw, E.164 where it could
-- form one. Kept a year, then pruned with the locations.
CREATE TABLE rejected_calls (
    id          BIGSERIAL PRIMARY KEY,
    device_id   UUID        NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    client_id   TEXT        NOT NULL,
    number      TEXT        NOT NULL,
    rejected_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (device_id, client_id)
);
CREATE INDEX rejected_calls_device_time ON rejected_calls (device_id, rejected_at DESC);
