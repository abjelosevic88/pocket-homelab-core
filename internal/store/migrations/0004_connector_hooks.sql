-- Push subscriptions a connector created in its app (Immich workflows with a webhook step).
-- The app calls POST /api/v1/connectors/hooks/{id} with the secret in the X-Pocket-Hook header.
CREATE TABLE connector_hooks (
  id                 TEXT PRIMARY KEY,                -- random; part of the URL the app calls
  service_id         INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  user_id            INTEGER,                         -- the person it was made for (with their key); NULL = one global subscription
  trigger            TEXT NOT NULL,                   -- the app's trigger name (AssetCreate…)
  remote_workflow_id TEXT NOT NULL DEFAULT '',        -- the app's id for the subscription
  secret_hash        TEXT NOT NULL,                   -- SHA-256 of the header secret; the secret itself lives only in the app
  state              TEXT NOT NULL DEFAULT 'ok',      -- ok | error (last check)
  error              TEXT NOT NULL DEFAULT '',
  received           INTEGER NOT NULL DEFAULT 0,
  published          INTEGER NOT NULL DEFAULT 0,
  dropped            INTEGER NOT NULL DEFAULT 0,
  last_drop          TEXT NOT NULL DEFAULT '',
  last_received_at   TEXT,
  checked_at         TEXT,
  created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX connector_hooks_one ON connector_hooks (service_id, IFNULL(user_id, 0), trigger);
