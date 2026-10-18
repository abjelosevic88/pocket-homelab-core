package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// Device is a paired client (iPhone, Mac, …) holding a pcd_ token.
type Device struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	TokenPrefix string `json:"token_prefix"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
	LastIP      string `json:"last_ip"`
	CreatedAt   string `json:"created_at"`
}

const deviceCols = `id, user_id, name, platform, token_prefix, last_seen_at, last_ip, created_at`

func scanDevice(sc interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var seen sql.NullString
	if err := sc.Scan(&d.ID, &d.UserID, &d.Name, &d.Platform, &d.TokenPrefix, &seen, &d.LastIP, &d.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	d.LastSeenAt = nullStr(seen)
	return &d, nil
}

// ListDevices lists devices, all of them for admins (userID 0) or one user's.
func (s *Store) ListDevices(ctx context.Context, userID int64) ([]Device, error) {
	q := `SELECT ` + deviceCols + ` FROM devices ORDER BY id`
	args := []any{}
	if userID > 0 {
		q = `SELECT ` + deviceCols + ` FROM devices WHERE user_id = ? ORDER BY id`
		args = append(args, userID)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// GetDevice loads by id.
func (s *Store) GetDevice(ctx context.Context, id int64) (*Device, error) {
	return scanDevice(s.DB.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = ?`, id))
}

// CreateDevice mints a device token and returns the plain secret.
func (s *Store) CreateDevice(ctx context.Context, d *Device) (string, error) {
	plain := crypto.Token("pcd_", 32)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO devices (user_id, name, platform, token_hash, token_prefix) VALUES (?, ?, ?, ?, ?)`,
		d.UserID, d.Name, d.Platform, crypto.Hash(plain), crypto.Prefix(plain, 10))
	if err != nil {
		return "", err
	}
	d.ID, _ = res.LastInsertId()
	if nd, err := s.GetDevice(ctx, d.ID); err == nil {
		*d = *nd
	}
	return plain, nil
}

// GetDeviceByToken resolves a pcd_ token and touches last_seen.
func (s *Store) GetDeviceByToken(ctx context.Context, token, ip string) (*Device, error) {
	d, err := scanDevice(s.DB.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE token_hash = ?`, crypto.Hash(token)))
	if err != nil {
		return nil, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE devices SET last_seen_at = ?, last_ip = ? WHERE id = ?`, Now(), ip, d.ID)
	return d, nil
}

// DeleteDevice revokes a device.
func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	return err
}

// ---- pairing codes ----

// Pairing is a one-shot code a device exchanges for a token.
type Pairing struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	UserID    int64  `json:"user_id"`
	ExpiresAt string `json:"expires_at"`
	ClaimedAt string `json:"claimed_at,omitempty"`
	DeviceID  int64  `json:"device_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

// CreatePairing mints a code for the user.
func (s *Store) CreatePairing(ctx context.Context, userID int64, ttl time.Duration) (*Pairing, error) {
	for i := 0; i < 5; i++ {
		code := crypto.Code(8)
		res, err := s.DB.ExecContext(ctx, `INSERT INTO pairings (code, user_id, expires_at) VALUES (?, ?, ?)`, code, userID, Stamp(time.Now().Add(ttl)))
		if err != nil {
			if conflict(err) == ErrConflict {
				continue
			}
			return nil, err
		}
		id, _ := res.LastInsertId()
		return &Pairing{ID: id, Code: code, UserID: userID, ExpiresAt: Stamp(time.Now().Add(ttl)), CreatedAt: Now()}, nil
	}
	return nil, ErrConflict
}

// ClaimPairing marks a code used and returns it, or ErrNotFound when the code
// is unknown, expired or already claimed.
func (s *Store) ClaimPairing(ctx context.Context, code string, deviceID int64) (*Pairing, error) {
	var p Pairing
	var claimed sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT id, code, user_id, expires_at, claimed_at, created_at FROM pairings WHERE code = ?`, code).Scan(&p.ID, &p.Code, &p.UserID, &p.ExpiresAt, &claimed, &p.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	if claimed.Valid {
		return nil, ErrNotFound
	}
	if t, err := time.Parse(time.RFC3339Nano, p.ExpiresAt); err != nil || t.Before(time.Now()) {
		return nil, ErrNotFound
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE pairings SET claimed_at = ?, device_id = ? WHERE id = ?`, Now(), deviceID, p.ID); err != nil {
		return nil, err
	}
	return &p, nil
}

// PeekPairing returns a code's row without claiming it.
func (s *Store) PeekPairing(ctx context.Context, code string) (*Pairing, error) {
	var p Pairing
	var claimed sql.NullString
	var dev sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT id, code, user_id, expires_at, claimed_at, device_id, created_at FROM pairings WHERE code = ?`, code).Scan(&p.ID, &p.Code, &p.UserID, &p.ExpiresAt, &claimed, &dev, &p.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	p.ClaimedAt = nullStr(claimed)
	if dev.Valid {
		p.DeviceID = dev.Int64
	}
	return &p, nil
}
