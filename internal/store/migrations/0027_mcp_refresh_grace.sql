-- When a refresh token was exchanged. A second exchange shortly afterwards
-- (two sessions of one assistant refreshing at once, or an answer that never
-- arrived) gets new tokens instead of ending the connection.
ALTER TABLE mcp_oauth_secrets ADD COLUMN spent_at TEXT NOT NULL DEFAULT '';
