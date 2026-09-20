-- The on-device advertising and tracker filter, as a per-child switch and a list url (FR-6.6).
--
-- Two columns rather than one, because they answer different questions and the console has to be
-- able to show both: `ad_filter` is what the parent asked for, and `ad_filter_list_url` is what the
-- phone would filter from. A switch that is on with no url is a filter that does not run, and the
-- engine says so — see policy.Compute — so the pair is what makes that state explainable rather
-- than a phone that silently does nothing.
--
-- **The url and nothing else.** This project ships the fetcher and never the list data: the lists
-- worth pointing at are published under the GPL and this is MIT, so a copy of one in this database
-- (or in this repository) would be a licensing decision nobody made. The device fetches the bytes,
-- hashes them, and reports the hash back; the server stores where they came from.
--
-- DEFAULT FALSE and DEFAULT '', so every existing row keeps exactly the behaviour it has today.
-- A migration is a new way to say something, never a policy change applied to children whose
-- parents did not ask for it — and this one can put a tunnel between every app on a phone and the
-- network, which is the last thing that should arrive by surprise.
ALTER TABLE policies ADD COLUMN ad_filter BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE policies ADD COLUMN ad_filter_list_url TEXT NOT NULL DEFAULT '';

-- What the PHONE reports about its filter, which is a different question from what the parent asked
-- for above.
--
-- `ad_filter` says a parent turned the switch on. It does not say the phone has a list, that the
-- list compiled, or that the tunnel came up — and every one of those can fail quietly: a captive
-- portal serves a login page with a perfectly good 200, a watchdog stands a tunnel down that
-- carried nothing, a platform declines always-on. Without a number from the device the console
-- would show "Ad filter: on" over a phone filtering nothing, which is the failure with no symptom.
--
-- Nullable, all three, because NULL means "this phone has not said" — an older DPC does not send
-- them — and that is a third state, distinct from zero rules and from a fresh install. Only a
-- measured zero is a finding.
ALTER TABLE device_state ADD COLUMN ad_filter_rules INTEGER;
ALTER TABLE device_state ADD COLUMN ad_filter_fetched_at TIMESTAMPTZ;
ALTER TABLE device_state ADD COLUMN ad_filter_running BOOLEAN;
