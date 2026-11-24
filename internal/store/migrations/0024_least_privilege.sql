-- A paired device acts for its owner, but manages the box (admin routes) only
-- when an admin asked for that while making the pairing code. Devices paired
-- before this keep what they could already do; Core → Devices can take it away.
ALTER TABLE devices ADD COLUMN can_manage INTEGER NOT NULL DEFAULT 0;
UPDATE devices SET can_manage = 1 WHERE user_id IN (SELECT id FROM users WHERE role = 'admin');
ALTER TABLE pairings ADD COLUMN can_manage INTEGER NOT NULL DEFAULT 0;
-- A web session a device opened carries the device's limit.
ALTER TABLE sessions ADD COLUMN limited INTEGER NOT NULL DEFAULT 0;
