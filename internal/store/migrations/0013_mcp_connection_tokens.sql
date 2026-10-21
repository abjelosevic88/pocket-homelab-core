-- Personal connection tokens have no OAuth callbacks or refresh flow.
ALTER TABLE mcp_oauth_clients ADD COLUMN kind TEXT NOT NULL DEFAULT 'oauth';
