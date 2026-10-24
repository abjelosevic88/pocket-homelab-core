-- Which app fills a place in Pocket Homelab when a person has both (Money: Pocket Money or Firefly III).
CREATE TABLE app_choices (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  slot       TEXT NOT NULL,
  provider   TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (user_id, slot)
);
