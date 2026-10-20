-- How each person's connection from an app to a target is actually made, as the
-- app reports it: "core" (through Core links), "direct" (a URL and key pasted
-- into the app, which Core can see but not switch) or "off". Report only: Core
-- uses it to draw the full map, never to decide anything.
CREATE TABLE link_modes (
  app_id     INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target     TEXT NOT NULL,               -- app slug or service:<type>
  mode       TEXT NOT NULL,               -- core | direct | off
  updated_at TEXT NOT NULL,
  PRIMARY KEY (app_id, user_id, target)
);
