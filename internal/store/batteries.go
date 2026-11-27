package store

import "context"

// DeviceBattery is one battery a paired device reported: its own (key "self") or one it carries.
type DeviceBattery struct {
	DeviceID   int64  `json:"device_id"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Level      int    `json:"level"`
	Charging   bool   `json:"charging"`
	MeasuredAt string `json:"measured_at"`
}

// ReplaceDeviceBatteries swaps all of a device's battery rows for bs.
func (s *Store) ReplaceDeviceBatteries(ctx context.Context, deviceID int64, bs []DeviceBattery) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM device_batteries WHERE device_id = ?`, deviceID); err != nil {
		return err
	}
	now := Now()
	for _, b := range bs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_batteries (device_id, key, name, kind, level, charging, measured_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, deviceID, b.Key, b.Name, b.Kind, b.Level, boolInt(b.Charging), b.MeasuredAt, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UserDeviceBatteries lists the batteries of a person's devices measured at or after since
// (RFC 3339 UTC, the same format as measured_at), by device then key.
func (s *Store) UserDeviceBatteries(ctx context.Context, userID int64, since string) ([]DeviceBattery, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT b.device_id, d.name, d.platform, b.key, b.name, b.kind, b.level, b.charging, b.measured_at
		FROM device_batteries b JOIN devices d ON d.id = b.device_id
		WHERE d.user_id = ? AND b.measured_at >= ? ORDER BY b.device_id, b.key`, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceBattery{}
	for rows.Next() {
		var b DeviceBattery
		var ch int
		if err := rows.Scan(&b.DeviceID, &b.DeviceName, &b.Platform, &b.Key, &b.Name, &b.Kind, &b.Level, &ch, &b.MeasuredAt); err != nil {
			return nil, err
		}
		b.Charging = ch == 1
		out = append(out, b)
	}
	return out, rows.Err()
}
