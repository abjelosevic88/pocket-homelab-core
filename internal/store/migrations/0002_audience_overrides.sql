-- Who receives a service in their profile, and per-person credential overrides.
ALTER TABLE services ADD COLUMN audience TEXT NOT NULL DEFAULT 'all';          -- all | admins | users
ALTER TABLE services ADD COLUMN audience_users TEXT NOT NULL DEFAULT '[]';     -- JSON list of user ids when audience = users

CREATE TABLE service_overrides (
  service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  url        TEXT NOT NULL DEFAULT '',          -- optional per-user URL (e.g. a user-specific Navidrome)
  fields     TEXT NOT NULL DEFAULT '{}',        -- JSON {KEY: value}; secrets encrypted; overrides the service's fields
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (service_id, user_id)
);
