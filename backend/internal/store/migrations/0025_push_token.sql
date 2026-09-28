-- FR-26.3: the Firebase registration token a phone reported, which is how the server wakes it while it
-- rests. Empty is "no token": the phone has no Google Play services, push is not configured, or FCM
-- said the token no longer reaches the app — and that phone polls every 5 minutes instead.
ALTER TABLE device_state ADD COLUMN push_token TEXT NOT NULL DEFAULT '' CHECK (length(push_token) <= 4096);
