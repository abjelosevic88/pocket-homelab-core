-- Agent keys (a program on a computer, not a person's browser) reach only
-- the app's /api/v1/agent/ paths and never ask for a passkey. '' = a device.
ALTER TABLE remote_codes ADD COLUMN scope TEXT NOT NULL DEFAULT '';
ALTER TABLE remote_keys ADD COLUMN scope TEXT NOT NULL DEFAULT '';
