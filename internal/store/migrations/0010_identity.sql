-- Keep existing OIDC subjects; new accounts get non-reusable subjects.
ALTER TABLE users ADD COLUMN subject TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN auth_revision INTEGER NOT NULL DEFAULT 1;
UPDATE users SET subject = CAST(id AS TEXT);
CREATE UNIQUE INDEX users_subject ON users(subject);
