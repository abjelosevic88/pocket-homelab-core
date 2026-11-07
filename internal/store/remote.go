package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// RemoteDevice is a phone or laptop paired for remote access.
type RemoteDevice struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	UserName   string `json:"user_name"`
	UserEmail  string `json:"user_email"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	LastIP     string `json:"last_ip"`
	// Apps maps app slug → the device's own switch (absent = app default).
	Apps map[string]bool `json:"apps"`
	// Keys lists the app addresses this device has paired.
	Keys []RemoteKey `json:"keys"`
}

// RemoteKey is one device key, valid for exactly one app address.
type RemoteKey struct {
	ID            int64  `json:"id"`
	DeviceID      int64  `json:"device_id"`
	App           string `json:"app"`
	PublicKey     string `json:"-"`
	CreatedAt     string `json:"created_at"`
	LastSeenAt    string `json:"last_seen_at"`
	PasskeyID     string `json:"-"`
	PasskeyPublic string `json:"-"`
	PasskeyAlg    int    `json:"-"`
	HasPasskey    bool   `json:"has_passkey"`
}

// RemoteApp is the admin's switches for one app; zero value = not set.
type RemoteApp struct {
	App           string `json:"app"`
	Exposed       bool   `json:"exposed"`
	DeviceDefault bool   `json:"device_default"`
	Passkey       bool   `json:"passkey"`
}

// RemoteCode is a pending one-time pairing code.
type RemoteCode struct {
	UserID   int64
	DeviceID int64 // 0 = a new device
	App      string
	// Apps are the switches a new device starts with (nil = app defaults).
	Apps map[string]bool
}

func (s *Store) ListRemoteApps(ctx context.Context) (map[string]RemoteApp, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT app, exposed, device_default, passkey FROM remote_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]RemoteApp{}
	for rows.Next() {
		var a RemoteApp
		if err := rows.Scan(&a.App, &a.Exposed, &a.DeviceDefault, &a.Passkey); err != nil {
			return nil, err
		}
		out[a.App] = a
	}
	return out, rows.Err()
}

func (s *Store) PutRemoteApp(ctx context.Context, a RemoteApp) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO remote_apps(app, exposed, device_default, passkey) VALUES(?,?,?,?)
 ON CONFLICT(app) DO UPDATE SET exposed=excluded.exposed, device_default=excluded.device_default, passkey=excluded.passkey`,
		a.App, boolInt(a.Exposed), boolInt(a.DeviceDefault), boolInt(a.Passkey))
	return err
}

// CreateRemoteCode stores a one-time code and returns it in plain text.
func (s *Store) CreateRemoteCode(ctx context.Context, c RemoteCode, ttl time.Duration) (string, error) {
	code := crypto.Token("", 18)
	var dev any
	if c.DeviceID > 0 {
		dev = c.DeviceID
	}
	apps := ""
	if c.Apps != nil {
		b, err := json.Marshal(c.Apps)
		if err != nil {
			return "", err
		}
		apps = string(b)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO remote_codes(hash, user_id, device_id, app, apps, expires_at) VALUES(?,?,?,?,?,?)`,
		crypto.Hash(code), c.UserID, dev, c.App, apps, Stamp(time.Now().Add(ttl)))
	return code, err
}

// TakeRemoteCode consumes a code: it works once, before it expires.
func (s *Store) TakeRemoteCode(ctx context.Context, code string) (*RemoteCode, error) {
	var c RemoteCode
	var dev sql.NullInt64
	var apps string
	err := s.DB.QueryRowContext(ctx, `DELETE FROM remote_codes WHERE hash=? AND expires_at>? RETURNING user_id, device_id, app, apps`,
		crypto.Hash(code), Now()).Scan(&c.UserID, &dev, &c.App, &apps)
	if err != nil {
		return nil, notFound(err)
	}
	c.DeviceID = dev.Int64
	if apps != "" {
		_ = json.Unmarshal([]byte(apps), &c.Apps)
	}
	return &c, nil
}

func (s *Store) CreateRemoteDevice(ctx context.Context, userID int64, name string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO remote_devices(user_id, name, created_at) VALUES(?,?,?)`, userID, name, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// PutRemoteKey pairs a key for (device, app), replacing an older key there.
func (s *Store) PutRemoteKey(ctx context.Context, deviceID int64, app, publicKey string) (*RemoteKey, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM remote_keys WHERE (device_id=? AND app=?) OR public_key=?`, deviceID, app, publicKey); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO remote_keys(device_id, app, public_key, created_at) VALUES(?,?,?,?)`, deviceID, app, publicKey, Now())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetRemoteKey(ctx, id)
}

const remoteKeyCols = `id, device_id, app, public_key, created_at, last_seen_at, passkey_id, passkey_public, passkey_alg`

func scanRemoteKey(sc interface{ Scan(...any) error }) (*RemoteKey, error) {
	var k RemoteKey
	if err := sc.Scan(&k.ID, &k.DeviceID, &k.App, &k.PublicKey, &k.CreatedAt, &k.LastSeenAt, &k.PasskeyID, &k.PasskeyPublic, &k.PasskeyAlg); err != nil {
		return nil, notFound(err)
	}
	k.HasPasskey = k.PasskeyID != ""
	return &k, nil
}

func (s *Store) GetRemoteKey(ctx context.Context, id int64) (*RemoteKey, error) {
	return scanRemoteKey(s.DB.QueryRowContext(ctx, `SELECT `+remoteKeyCols+` FROM remote_keys WHERE id=?`, id))
}

func (s *Store) GetRemoteKeyByPublic(ctx context.Context, publicKey string) (*RemoteKey, error) {
	return scanRemoteKey(s.DB.QueryRowContext(ctx, `SELECT `+remoteKeyCols+` FROM remote_keys WHERE public_key=?`, publicKey))
}

func (s *Store) SetRemotePasskey(ctx context.Context, keyID int64, credID, public string, alg int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE remote_keys SET passkey_id=?, passkey_public=?, passkey_alg=? WHERE id=?`, credID, public, alg, keyID)
	return err
}

// TouchRemoteKey records a session start.
func (s *Store) TouchRemoteKey(ctx context.Context, k *RemoteKey, ip string) {
	now := Now()
	_, _ = s.DB.ExecContext(ctx, `UPDATE remote_keys SET last_seen_at=? WHERE id=?`, now, k.ID)
	_, _ = s.DB.ExecContext(ctx, `UPDATE remote_devices SET last_seen_at=?, last_ip=? WHERE id=?`, now, ip, k.DeviceID)
}

func (s *Store) GetRemoteDevice(ctx context.Context, id int64) (*RemoteDevice, error) {
	list, err := s.listRemoteDevices(ctx, `WHERE d.id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// ListRemoteDevices lists everybody's devices (userID 0) or one person's.
func (s *Store) ListRemoteDevices(ctx context.Context, userID int64) ([]RemoteDevice, error) {
	if userID > 0 {
		return s.listRemoteDevices(ctx, `WHERE d.user_id=?`, userID)
	}
	return s.listRemoteDevices(ctx, ``)
}

func (s *Store) listRemoteDevices(ctx context.Context, where string, args ...any) ([]RemoteDevice, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT d.id, d.user_id, u.name, u.email, d.name, d.created_at, d.last_seen_at, d.last_ip
 FROM remote_devices d JOIN users u ON u.id=d.user_id `+where+` ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	out := []RemoteDevice{}
	idx := map[int64]int{}
	for rows.Next() {
		var d RemoteDevice
		if err := rows.Scan(&d.ID, &d.UserID, &d.UserName, &d.UserEmail, &d.Name, &d.CreatedAt, &d.LastSeenAt, &d.LastIP); err != nil {
			rows.Close()
			return nil, err
		}
		d.Apps, d.Keys = map[string]bool{}, []RemoteKey{}
		idx[d.ID] = len(out)
		out = append(out, d)
	}
	rows.Close()
	if len(out) == 0 {
		return out, rows.Err()
	}
	krows, err := s.DB.QueryContext(ctx, `SELECT `+remoteKeyCols+` FROM remote_keys ORDER BY app`)
	if err != nil {
		return nil, err
	}
	for krows.Next() {
		k, err := scanRemoteKey(krows)
		if err != nil {
			krows.Close()
			return nil, err
		}
		if i, ok := idx[k.DeviceID]; ok {
			out[i].Keys = append(out[i].Keys, *k)
		}
	}
	krows.Close()
	arows, err := s.DB.QueryContext(ctx, `SELECT device_id, app, allowed FROM remote_device_apps`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var id int64
		var app string
		var allowed bool
		if err := arows.Scan(&id, &app, &allowed); err != nil {
			return nil, err
		}
		if i, ok := idx[id]; ok {
			out[i].Apps[app] = allowed
		}
	}
	return out, arows.Err()
}

func (s *Store) RenameRemoteDevice(ctx context.Context, id int64, name string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE remote_devices SET name=? WHERE id=?`, name, id)
	return err
}

// DeleteRemoteDevice revokes the device: every key, switch and token goes.
func (s *Store) DeleteRemoteDevice(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM remote_devices WHERE id=?`, id)
	return err
}

// DeleteRemoteKey forgets one app address on a device (it can pair again from the launcher).
func (s *Store) DeleteRemoteKey(ctx context.Context, deviceID, keyID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM remote_keys WHERE id=? AND device_id=?`, keyID, deviceID)
	return err
}

func (s *Store) SetRemoteDeviceApp(ctx context.Context, deviceID int64, app string, allowed bool) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO remote_device_apps(device_id, app, allowed) VALUES(?,?,?)
 ON CONFLICT(device_id, app) DO UPDATE SET allowed=excluded.allowed`, deviceID, app, boolInt(allowed))
	return err
}

// RemoteDeviceApp returns the device's own switch for an app, if it has one.
func (s *Store) RemoteDeviceApp(ctx context.Context, deviceID int64, app string) (allowed, set bool, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT allowed FROM remote_device_apps WHERE device_id=? AND app=?`, deviceID, app).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return allowed, err == nil, err
}

// CreateRemoteToken mints the pcu_rly_ token Core injects for one session.
// The pcu_ prefix routes it to Core introspection in every Pocket SDK; it
// is not an API token and does not open Core's own API (except as "core").
func (s *Store) CreateRemoteToken(ctx context.Context, keyID int64, app string, ttl time.Duration) (string, error) {
	tok := crypto.Token("pcu_rly_", 32)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO remote_tokens(hash, key_id, app, expires_at) VALUES(?,?,?,?)`,
		crypto.Hash(tok), keyID, app, Stamp(time.Now().Add(ttl)))
	return tok, err
}

// RemoteToken resolves a live token to its key and app.
func (s *Store) RemoteToken(ctx context.Context, tok string) (keyID int64, app, expires string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT key_id, app, expires_at FROM remote_tokens WHERE hash=? AND expires_at>?`, crypto.Hash(tok), Now()).
		Scan(&keyID, &app, &expires)
	return keyID, app, expires, notFound(err)
}

func (s *Store) DeleteRemoteToken(ctx context.Context, tok string) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM remote_tokens WHERE hash=?`, crypto.Hash(tok))
}

// ResetRemote forgets every device (used when the box key is replaced).
func (s *Store) ResetRemote(ctx context.Context) error {
	for _, q := range []string{`DELETE FROM remote_tokens`, `DELETE FROM remote_codes`, `DELETE FROM remote_devices`} {
		if _, err := s.DB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
