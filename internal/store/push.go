package store

import (
	"context"
	"database/sql"
)

// PushRegistration is one paired device's way to get Pocket Hub notifications (APNs).
type PushRegistration struct {
	ID          int64  `json:"id"`
	DeviceID    int64  `json:"device_id"`
	APNsToken   string `json:"-"`
	Environment string `json:"environment"`
	Topic       string `json:"topic"`
	PublicKey   string `json:"-"`
	Enabled     bool   `json:"enabled"`
	Sound       bool   `json:"sound"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	LastSentAt  string `json:"last_sent_at,omitempty"`
	LastError   string `json:"last_error"`
}

// DevicePush is what the Devices list shows about a device's notifications.
type DevicePush struct {
	Enabled    bool   `json:"enabled"`
	Sound      bool   `json:"sound"`
	LastSentAt string `json:"last_sent_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

const pushCols = `id, device_id, apns_token, environment, topic, public_key, enabled, sound, created_at, updated_at, last_sent_at, last_error`

func scanPush(sc interface{ Scan(...any) error }) (*PushRegistration, error) {
	var p PushRegistration
	var en, snd int
	var sent sql.NullString
	if err := sc.Scan(&p.ID, &p.DeviceID, &p.APNsToken, &p.Environment, &p.Topic, &p.PublicKey, &en, &snd, &p.CreatedAt, &p.UpdatedAt, &sent, &p.LastError); err != nil {
		return nil, notFound(err)
	}
	p.Enabled, p.Sound, p.LastSentAt = en == 1, snd == 1, nullStr(sent)
	return &p, nil
}

// SavePushRegistration creates or replaces the device's registration. The same APNs token on
// another device row (the install paired again) is dropped, so it isn't notified twice.
func (s *Store) SavePushRegistration(ctx context.Context, p *PushRegistration) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_registrations WHERE apns_token = ? AND topic = ? AND device_id <> ?`, p.APNsToken, p.Topic, p.DeviceID); err != nil {
		return err
	}
	now := Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO push_registrations (device_id, apns_token, environment, topic, public_key, enabled, sound, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
		  last_error = CASE WHEN push_registrations.apns_token <> excluded.apns_token OR push_registrations.public_key <> excluded.public_key THEN '' ELSE push_registrations.last_error END,
		  apns_token = excluded.apns_token, environment = excluded.environment, topic = excluded.topic,
		  public_key = excluded.public_key, enabled = excluded.enabled, sound = excluded.sound, updated_at = excluded.updated_at`,
		p.DeviceID, p.APNsToken, p.Environment, p.Topic, p.PublicKey, boolInt(p.Enabled), boolInt(p.Sound), now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// GetPushRegistration loads a device's registration (ErrNotFound when it has none).
func (s *Store) GetPushRegistration(ctx context.Context, deviceID int64) (*PushRegistration, error) {
	return scanPush(s.DB.QueryRowContext(ctx, `SELECT `+pushCols+` FROM push_registrations WHERE device_id = ?`, deviceID))
}

// DeletePushRegistration removes a device's registration.
func (s *Store) DeletePushRegistration(ctx context.Context, deviceID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM push_registrations WHERE device_id = ?`, deviceID)
	return err
}

// DeletePushRegistrationByID removes one registration (APNs said its token is gone).
func (s *Store) DeletePushRegistrationByID(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM push_registrations WHERE id = ?`, id)
	return err
}

// UserPushRegistrations lists a person's enabled registrations (through their devices).
func (s *Store) UserPushRegistrations(ctx context.Context, userID int64) ([]PushRegistration, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id, p.device_id, p.apns_token, p.environment, p.topic, p.public_key, p.enabled, p.sound, p.created_at, p.updated_at, p.last_sent_at, p.last_error
		FROM push_registrations p JOIN devices d ON d.id = p.device_id WHERE d.user_id = ? AND p.enabled = 1 ORDER BY p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushRegistration{}
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// MarkPushSent records a delivery to the relay (clears the last error).
func (s *Store) MarkPushSent(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE push_registrations SET last_sent_at = ?, last_error = '' WHERE id = ?`, Now(), id)
	return err
}

// MarkPushError records why the last message didn't go out.
func (s *Store) MarkPushError(ctx context.Context, id int64, reason string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE push_registrations SET last_error = ? WHERE id = ?`, reason, id)
	return err
}

// pushByDevice maps device id → its notification state, for the Devices list.
func (s *Store) pushByDevice(ctx context.Context) (map[int64]*DevicePush, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT device_id, enabled, sound, last_sent_at, last_error FROM push_registrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*DevicePush{}
	for rows.Next() {
		var id int64
		var en, snd int
		var sent sql.NullString
		var dp DevicePush
		if err := rows.Scan(&id, &en, &snd, &sent, &dp.LastError); err != nil {
			return nil, err
		}
		dp.Enabled, dp.Sound, dp.LastSentAt = en == 1, snd == 1, nullStr(sent)
		out[id] = &dp
	}
	return out, rows.Err()
}
