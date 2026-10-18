-- Pocket Core: users, sessions, tokens, apps, services, events, webhooks, pairing, OIDC.

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'member',   -- admin | member
  disabled      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX sessions_user ON sessions(user_id);

-- Personal access tokens (pcu_) for scripts, Raycast, Homepage widgets.
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Registered Pocket apps. Each holds one app token (pca_) it uses to talk to Core.
CREATE TABLE apps (
  id           INTEGER PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,              -- habits, kitchen, day…
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  icon         TEXT NOT NULL DEFAULT '',          -- absolute URL or path under url
  version      TEXT NOT NULL DEFAULT '',
  url          TEXT NOT NULL,                     -- how Core and siblings reach it
  public_url   TEXT NOT NULL DEFAULT '',          -- what a phone opens
  health_path  TEXT NOT NULL DEFAULT '/healthz',
  env_prefix   TEXT NOT NULL DEFAULT '',          -- HABITS → HABITS_URL / HABITS_API_KEY in a profile
  manifest     TEXT NOT NULL DEFAULT '{}',        -- the full manifest as sent
  token_hash   TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'unknown',   -- ok | down | unknown
  last_seen_at TEXT,
  last_check_at TEXT,
  last_error   TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Services a paired device gets in its profile: third-party apps with
-- admin-entered credentials, or registered Pocket apps (app_id set).
CREATE TABLE services (
  id          INTEGER PRIMARY KEY,
  type        TEXT NOT NULL,                      -- catalog key: jellyfin, immich, habits…
  name        TEXT NOT NULL DEFAULT '',
  url         TEXT NOT NULL DEFAULT '',           -- internal URL
  public_url  TEXT NOT NULL DEFAULT '',           -- URL devices use; falls back to url
  fields      TEXT NOT NULL DEFAULT '{}',         -- JSON {KEY_SUFFIX: value}; secrets encrypted
  app_id      INTEGER REFERENCES apps(id) ON DELETE CASCADE,
  enabled     INTEGER NOT NULL DEFAULT 1,
  notes       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX services_type ON services(type);

-- Paired devices (the iPhone / Mac client). Token prefix pcd_.
CREATE TABLE devices (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  platform     TEXT NOT NULL DEFAULT '',          -- ios | macos | other
  token_hash   TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL DEFAULT '',
  last_seen_at TEXT,
  last_ip      TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Short-lived pairing codes; one claim each.
CREATE TABLE pairings (
  id         INTEGER PRIMARY KEY,
  code       TEXT NOT NULL UNIQUE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  claimed_at TEXT,
  device_id  INTEGER REFERENCES devices(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- The event log. Apps publish, Core fans out (SSE + webhooks) and keeps history.
CREATE TABLE events (
  id         INTEGER PRIMARY KEY,
  topic      TEXT NOT NULL,                       -- "kitchen.cooked", "habits.marked"
  source     TEXT NOT NULL DEFAULT '',            -- app slug or user email
  subject    TEXT NOT NULL DEFAULT '',            -- free id: "recipe:12"
  payload    TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX events_topic ON events(topic, id);
CREATE INDEX events_created ON events(created_at);

CREATE TABLE webhooks (
  id         INTEGER PRIMARY KEY,
  app_id     INTEGER REFERENCES apps(id) ON DELETE CASCADE,
  name       TEXT NOT NULL DEFAULT '',
  url        TEXT NOT NULL,
  topics     TEXT NOT NULL DEFAULT '*',           -- comma list, "*" or prefix "kitchen.*"
  secret     TEXT NOT NULL DEFAULT '',            -- HMAC key (encrypted)
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX webhooks_app_url ON webhooks(app_id, url);

CREATE TABLE deliveries (
  id            INTEGER PRIMARY KEY,
  webhook_id    INTEGER NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  event_id      INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  status        TEXT NOT NULL DEFAULT 'pending',  -- pending | ok | failed
  attempts      INTEGER NOT NULL DEFAULT 0,
  next_at       TEXT NOT NULL,
  last_code     INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT NOT NULL DEFAULT '',
  delivered_at  TEXT,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX deliveries_due ON deliveries(status, next_at);

-- OIDC: relying parties, auth codes, issued tokens, signing key.
CREATE TABLE oidc_clients (
  id            INTEGER PRIMARY KEY,
  client_id     TEXT NOT NULL UNIQUE,
  secret_hash   TEXT NOT NULL DEFAULT '',          -- empty = public client (PKCE required)
  name          TEXT NOT NULL,
  redirect_uris TEXT NOT NULL DEFAULT '',          -- newline separated
  app_id        INTEGER REFERENCES apps(id) ON DELETE CASCADE,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE oidc_codes (
  code           TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  redirect_uri   TEXT NOT NULL,
  scope          TEXT NOT NULL DEFAULT '',
  nonce          TEXT NOT NULL DEFAULT '',
  code_challenge TEXT NOT NULL DEFAULT '',
  expires_at     TEXT NOT NULL
);

CREATE TABLE oidc_tokens (
  token_hash  TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,                       -- access | refresh
  client_id   TEXT NOT NULL,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  scope       TEXT NOT NULL DEFAULT '',
  expires_at  TEXT NOT NULL,
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);
