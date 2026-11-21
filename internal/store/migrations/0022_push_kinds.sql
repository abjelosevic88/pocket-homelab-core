-- Which admin alerts a Pocket Core app registration wants (comma list; '' = the defaults,
-- 'none' = nothing). Pocket Hub registrations leave it empty.
ALTER TABLE push_registrations ADD COLUMN kinds TEXT NOT NULL DEFAULT '';
