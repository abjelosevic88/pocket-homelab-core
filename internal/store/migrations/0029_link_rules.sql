-- Link rules: an app asking for one scope in every app that offers it
-- (manifest uses with app "*"), e.g. Pocket Search and pocket:search. The rule
-- holds the admin's one switch; Core keeps an ordinary link (app_links row,
-- rule_id = the rule) to each app that offers the scope, so the per-app and
-- per-person switches, tokens and the Links page work as for any link. A new
-- link starts as its rule stands.
CREATE TABLE app_link_rules (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id     INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE, -- the app that acts
  scope      TEXT NOT NULL,
  reason     TEXT NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 0,          -- what a new link under the rule starts as
  declared   INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (app_id, scope)
);

-- 0 = the app named the target itself; otherwise the rule that made the link.
ALTER TABLE app_links ADD COLUMN rule_id INTEGER NOT NULL DEFAULT 0;
CREATE INDEX app_links_rule ON app_links(rule_id) WHERE rule_id > 0;
