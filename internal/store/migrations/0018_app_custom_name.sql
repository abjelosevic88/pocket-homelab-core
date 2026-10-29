-- An admin's name for an app; the app's own name stays in `name` and keeps updating on re-registration.
ALTER TABLE apps ADD COLUMN custom_name TEXT NOT NULL DEFAULT '';
