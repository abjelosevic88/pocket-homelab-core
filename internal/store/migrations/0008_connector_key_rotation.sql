-- Replacing a person's connector-made key when one of their service links is
-- switched off: when it last worked, what went wrong the last time (the old
-- key may still work then) and a side effect worth showing.
ALTER TABLE connector_accounts ADD COLUMN key_replaced_at TEXT NOT NULL DEFAULT '';
ALTER TABLE connector_accounts ADD COLUMN key_error TEXT NOT NULL DEFAULT '';
ALTER TABLE connector_accounts ADD COLUMN key_note TEXT NOT NULL DEFAULT '';
