CREATE TABLE pocket_ask_connections (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 grant_id TEXT NOT NULL REFERENCES mcp_oauth_grants(id) ON DELETE CASCADE
);
