-- Pocket Hub notifications: one APNs registration per paired device. Revoking the device
-- removes it (and so stops its notifications). public_key is the install's X25519 key that
-- every message is sealed to; the relay never sees the text.
CREATE TABLE push_registrations (
  id           INTEGER PRIMARY KEY,
  device_id    INTEGER NOT NULL UNIQUE REFERENCES devices(id) ON DELETE CASCADE,
  apns_token   TEXT NOT NULL,
  environment  TEXT NOT NULL DEFAULT 'production',   -- development | production
  topic        TEXT NOT NULL,
  public_key   TEXT NOT NULL,
  enabled      INTEGER NOT NULL DEFAULT 1,
  sound        INTEGER NOT NULL DEFAULT 1,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  last_sent_at TEXT,
  last_error   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX push_registrations_token ON push_registrations(apns_token);
