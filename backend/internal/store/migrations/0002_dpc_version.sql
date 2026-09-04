-- The DPC build each phone is actually running.
--
-- Added with UPDATE_APP (FR-15.4): the server can now replace the app on a phone, and a fleet you can
-- update is worthless without a way to see which phone took the update. Both columns are NOT NULL
-- with a zero default rather than nullable, because every read of them is a comparison against the
-- version the server hosts, and a NULL would make that comparison silently true-ish in whichever
-- direction the reader happened to write it.
--
-- The empty string and 0 are the honest "has not reported yet" — a device enrolled before this
-- migration keeps sending heartbeats and fills them in on the first one after its update, and until
-- then the console says "not reported" rather than inventing a version.
ALTER TABLE device_state
    ADD COLUMN app_version_name TEXT   NOT NULL DEFAULT '',
    ADD COLUMN app_version_code BIGINT NOT NULL DEFAULT 0;
