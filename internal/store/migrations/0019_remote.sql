-- Remote access through an encrypted relay (docs/remote.md).
-- A remote device is one phone or laptop; it holds one key per app address.
CREATE TABLE remote_devices (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL DEFAULT '',
  last_ip      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE remote_keys (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id      INTEGER NOT NULL REFERENCES remote_devices(id) ON DELETE CASCADE,
  app            TEXT NOT NULL,
  public_key     TEXT NOT NULL UNIQUE,
  created_at     TEXT NOT NULL,
  last_seen_at   TEXT NOT NULL DEFAULT '',
  passkey_id     TEXT NOT NULL DEFAULT '',
  passkey_public TEXT NOT NULL DEFAULT '',
  passkey_alg    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (device_id, app)
);
-- A device's own switch per app; no row = the app's default for new devices.
CREATE TABLE remote_device_apps (
  device_id INTEGER NOT NULL REFERENCES remote_devices(id) ON DELETE CASCADE,
  app       TEXT NOT NULL,
  allowed   INTEGER NOT NULL,
  PRIMARY KEY (device_id, app)
);
-- Per app: reachable at all, on for new devices, passkey check.
CREATE TABLE remote_apps (
  app            TEXT PRIMARY KEY,
  exposed        INTEGER NOT NULL DEFAULT 0,
  device_default INTEGER NOT NULL DEFAULT 1,
  passkey        INTEGER NOT NULL DEFAULT 0
);
-- One-time pairing codes; device_id NULL pairs a new device.
CREATE TABLE remote_codes (
  hash       TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_id  INTEGER REFERENCES remote_devices(id) ON DELETE CASCADE,
  app        TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
-- pcu_rly_ tokens Core injects into requests it forwards; one per session.
CREATE TABLE remote_tokens (
  hash       TEXT PRIMARY KEY,
  key_id     INTEGER NOT NULL REFERENCES remote_keys(id) ON DELETE CASCADE,
  app        TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
