-- Existing connections keep their original status-only authorization.
ALTER TABLE mcp_oauth_grants ADD COLUMN scope TEXT NOT NULL DEFAULT 'status:read';
CREATE TABLE mcp_app_operations (
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 request_id TEXT NOT NULL, digest TEXT NOT NULL, tool TEXT NOT NULL,
 result TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 PRIMARY KEY(user_id,request_id)
);
CREATE INDEX mcp_operations_created ON mcp_app_operations(created_at);
