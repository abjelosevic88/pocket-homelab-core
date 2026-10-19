-- Connectors: Core acting on a third-party service (accounts, sign-in, events). See docs/connectors.md.
CREATE TABLE connectors (
  id                  INTEGER PRIMARY KEY,
  service_id          INTEGER NOT NULL UNIQUE REFERENCES services(id) ON DELETE CASCADE,
  enabled             INTEGER NOT NULL DEFAULT 0,      -- master switch; off = Core does nothing on its own
  provision_on_create INTEGER NOT NULL DEFAULT 0,      -- make an account when an admin creates a person
  sso_enabled         INTEGER NOT NULL DEFAULT 0,      -- "Sign in with Pocket Core" configured in the app
  events_enabled      INTEGER NOT NULL DEFAULT 0,      -- poll the app and publish <type>.* events
  poll_interval_s     INTEGER NOT NULL DEFAULT 300,
  state               TEXT NOT NULL DEFAULT '',        -- connector-owned JSON (poll baseline)
  last_poll_at        TEXT,
  last_error          TEXT NOT NULL DEFAULT '',
  created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE connector_accounts (
  id         INTEGER PRIMARY KEY,
  service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  remote_id  TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL CHECK (status IN ('ok', 'linked', 'error')),
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (service_id, user_id)
);

-- OIDC clients a connector created for a service; the service's audience is enforced at /oidc/authorize.
ALTER TABLE oidc_clients ADD COLUMN service_id INTEGER REFERENCES services(id) ON DELETE CASCADE;

-- Who wrote a per-person override: 'user' (the person or an admin, by hand) or 'connector'.
-- A connector only ever replaces its own.
ALTER TABLE service_overrides ADD COLUMN source TEXT NOT NULL DEFAULT 'user';
