-- Published event identities survive retries, and sequence numbers never rewind
-- when retention removes the newest (or every) event.
ALTER TABLE events ADD COLUMN publish_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX events_publish_key ON events(source, publish_key) WHERE publish_key <> '';
CREATE TABLE event_sequence (singleton INTEGER PRIMARY KEY CHECK(singleton = 1), value INTEGER NOT NULL);
INSERT INTO event_sequence VALUES (1, (SELECT COALESCE(MAX(id), 0) FROM events));

-- Keep permission decisions when a manifest temporarily removes a use.
ALTER TABLE app_links ADD COLUMN declared INTEGER NOT NULL DEFAULT 1;

-- Compact receipts survive event retention, preventing delayed retries from
-- creating new events. Payload contents remain subject to normal retention.
CREATE TABLE event_receipts (
 source TEXT NOT NULL, publish_key TEXT NOT NULL, request_hash TEXT NOT NULL,
 event_id INTEGER NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(source,publish_key)
);
