-- A web session a device opened belongs to that device: revoking the device
-- (or changing whether it may manage the box) ends it. Sessions from before
-- can't be told apart from password sign-ins, so everyone signs in to Core's
-- own pages once more; apps keep their own sessions.
ALTER TABLE sessions ADD COLUMN device_id INTEGER REFERENCES devices(id) ON DELETE CASCADE;
CREATE INDEX sessions_device ON sessions(device_id);
DELETE FROM sessions;
