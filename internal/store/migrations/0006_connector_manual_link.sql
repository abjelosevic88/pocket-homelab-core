-- An admin tied the person to an existing account by hand (apps without
-- emails, like Jellyfin); automatic provisioning leaves such a link alone.
ALTER TABLE connector_accounts ADD COLUMN manual INTEGER NOT NULL DEFAULT 0;
