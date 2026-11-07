-- The apps a new device starts with, chosen when the pairing code is made
-- (JSON object slug → on/off; empty = every app's default).
ALTER TABLE remote_codes ADD COLUMN apps TEXT NOT NULL DEFAULT '';
