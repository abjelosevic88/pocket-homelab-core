-- MCP credentials are isolated from ordinary API and OIDC credentials.
CREATE TABLE mcp_oauth_clients (
 client_id TEXT PRIMARY KEY, name TEXT NOT NULL, auth_method TEXT NOT NULL,
 secret_hash TEXT NOT NULL DEFAULT '', redirect_uris TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE mcp_oauth_grants (
 id TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES mcp_oauth_clients(client_id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 auth_revision INTEGER NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL
);
CREATE INDEX mcp_grants_user ON mcp_oauth_grants(user_id);
CREATE TABLE mcp_oauth_secrets (
 hash TEXT PRIMARY KEY, kind TEXT NOT NULL,
 client_id TEXT NOT NULL REFERENCES mcp_oauth_clients(client_id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 grant_id TEXT REFERENCES mcp_oauth_grants(id) ON DELETE CASCADE,
 data TEXT NOT NULL DEFAULT '{}', expires_at TEXT NOT NULL
);
CREATE INDEX mcp_secrets_expiry ON mcp_oauth_secrets(expires_at);
