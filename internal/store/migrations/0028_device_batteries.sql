-- Battery levels a paired device reports for itself and what it carries (iPhone, Watch, Mac):
-- PUT /devices/me/battery replaces the device's rows; GET /devices/batteries shows the person's.
-- Revoking the device removes them.
CREATE TABLE device_batteries (
  device_id   INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  key         TEXT NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'other',   -- iphone | ipad | mac | watch | other
  level       INTEGER NOT NULL,                -- 0–100
  charging    INTEGER NOT NULL DEFAULT 0,
  measured_at TEXT NOT NULL,                   -- RFC 3339 UTC, seconds
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (device_id, key)
);
