-- App links: one Pocket app acting for a person in another through Core.
-- The acting app declares what it needs in its manifest ("uses"), the target
-- app declares what it allows ("offers"). One row per (app, target, scope).
CREATE TABLE app_links (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE, -- the app that acts
  target_slug  TEXT NOT NULL,                     -- the app it acts in (may register later)
  scope        TEXT NOT NULL,
  reason       TEXT NOT NULL DEFAULT '',          -- why, in the acting app's words
  enabled      INTEGER NOT NULL DEFAULT 1,        -- the admin's box-wide switch
  uses         INTEGER NOT NULL DEFAULT 0,        -- tokens issued
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (app_id, target_slug, scope)
);

-- A person who switched a link off for themselves.
CREATE TABLE app_link_optouts (
  link_id    INTEGER NOT NULL REFERENCES app_links(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (link_id, user_id)
);

-- Short-lived pcx_ tokens: valid only at the target app, only for the scopes.
CREATE TABLE link_tokens (
  token_hash  TEXT PRIMARY KEY,
  app_id      INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  target_slug TEXT NOT NULL,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  scope       TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
