-- Pairing secrets and one-use completion codes are stored only as hashes.
CREATE TABLE mcp_terminal_pairings (
 request_hash TEXT PRIMARY KEY,
 user_code_hash TEXT NOT NULL UNIQUE,
 name TEXT NOT NULL,
 scope TEXT NOT NULL,
 days INTEGER NOT NULL,
 expires_at TEXT NOT NULL,
 user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
 auth_revision INTEGER,
 completion_hash TEXT
);
CREATE INDEX mcp_terminal_pairings_expiry ON mcp_terminal_pairings(expires_at);
