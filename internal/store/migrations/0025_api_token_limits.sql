-- Personal API tokens: an expiry, and admin rights only when asked for
-- ("Can manage this box"). Tokens made before keep what they could do.
ALTER TABLE api_tokens ADD COLUMN expires_at TEXT;
ALTER TABLE api_tokens ADD COLUMN can_manage INTEGER NOT NULL DEFAULT 0;
UPDATE api_tokens SET can_manage = 1;
