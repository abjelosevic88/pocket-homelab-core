package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Service is one entry a paired device receives: a third-party app with
// admin-entered credentials, or a registered Pocket app (AppID set).
type Service struct {
	ID            int64             `json:"id"`
	Type          string            `json:"type"`
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	PublicURL     string            `json:"public_url"`
	Fields        map[string]string `json:"fields"` // decrypted; secrets masked by the server before output
	AppID         int64             `json:"app_id,omitempty"`
	Enabled       bool              `json:"enabled"`
	Audience      string            `json:"audience"`       // all | admins | users
	AudienceUsers []int64           `json:"audience_users"` // when audience = users
	Notes         string            `json:"notes"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
}

// EffectivePublicURL is what a device should use.
func (sv *Service) EffectivePublicURL() string {
	if sv.PublicURL != "" {
		return sv.PublicURL
	}
	return sv.URL
}

const serviceCols = `id, type, name, url, public_url, fields, app_id, enabled, notes, created_at, updated_at, audience, audience_users`

// VisibleTo reports whether a user receives this service in their profile.
func (sv *Service) VisibleTo(u *User) bool {
	return sv.Enabled && sv.InAudience(u)
}

// InAudience reports whether a user is in the service's audience, enabled or not.
func (sv *Service) InAudience(u *User) bool {
	if u == nil {
		return false
	}
	switch sv.Audience {
	case "admins":
		return u.IsAdmin()
	case "users":
		for _, id := range sv.AudienceUsers {
			if id == u.ID {
				return true
			}
		}
		return false
	}
	return true
}

func (s *Store) scanService(sc interface{ Scan(...any) error }) (*Service, error) {
	var sv Service
	var fields string
	var appID sql.NullInt64
	var en int
	var aud string
	if err := sc.Scan(&sv.ID, &sv.Type, &sv.Name, &sv.URL, &sv.PublicURL, &fields, &appID, &en, &sv.Notes, &sv.CreatedAt, &sv.UpdatedAt, &sv.Audience, &aud); err != nil {
		return nil, notFound(err)
	}
	sv.Enabled = en == 1
	sv.AudienceUsers = []int64{}
	_ = json.Unmarshal([]byte(aud), &sv.AudienceUsers)
	if sv.Audience == "" {
		sv.Audience = "all"
	}
	if appID.Valid {
		sv.AppID = appID.Int64
	}
	raw := map[string]string{}
	_ = json.Unmarshal([]byte(fields), &raw)
	sv.Fields = make(map[string]string, len(raw))
	for k, v := range raw {
		pv, err := s.Box.Open(v)
		if err != nil {
			return nil, fmt.Errorf("decrypt service %d field %s: %w", sv.ID, k, err)
		}
		sv.Fields[k] = pv
	}
	return &sv, nil
}

// ListServices returns all services (enabled or not).
func (s *Store) ListServices(ctx context.Context) ([]Service, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+serviceCols+` FROM services ORDER BY name, type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Service{}
	for rows.Next() {
		sv, err := s.scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sv)
	}
	return out, rows.Err()
}

// GetService loads by id.
func (s *Store) GetService(ctx context.Context, id int64) (*Service, error) {
	return s.scanService(s.DB.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE id = ?`, id))
}

// GetServiceByType loads by catalog type.
func (s *Store) GetServiceByType(ctx context.Context, typ string) (*Service, error) {
	return s.scanService(s.DB.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE type = ?`, typ))
}

// GetServiceByApp loads the service row that mirrors a registered app.
func (s *Store) GetServiceByApp(ctx context.Context, appID int64) (*Service, error) {
	return s.scanService(s.DB.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE app_id = ?`, appID))
}

func (s *Store) sealFields(f map[string]string, secret func(string) bool) string {
	out := make(map[string]string, len(f))
	for k, v := range f {
		if secret != nil && secret(k) {
			out[k] = s.Box.Seal(v)
		} else {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// SaveService inserts or updates. secret says which field keys are encrypted at rest.
func (s *Store) SaveService(ctx context.Context, sv *Service, secret func(string) bool) error {
	var appID any
	if sv.AppID > 0 {
		appID = sv.AppID
	}
	switch sv.Audience {
	case "admins", "users":
	default:
		sv.Audience = "all"
	}
	if sv.AudienceUsers == nil {
		sv.AudienceUsers = []int64{}
	}
	aud, _ := json.Marshal(sv.AudienceUsers)
	if sv.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO services (type, name, url, public_url, fields, app_id, enabled, notes, audience, audience_users) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sv.Type, sv.Name, sv.URL, sv.PublicURL, s.sealFields(sv.Fields, secret), appID, boolInt(sv.Enabled), sv.Notes, sv.Audience, string(aud))
		if err != nil {
			return conflict(err)
		}
		sv.ID, _ = res.LastInsertId()
	} else {
		_, err := s.DB.ExecContext(ctx, `UPDATE services SET type = ?, name = ?, url = ?, public_url = ?, fields = ?, app_id = ?, enabled = ?, notes = ?, audience = ?, audience_users = ?, updated_at = ? WHERE id = ?`,
			sv.Type, sv.Name, sv.URL, sv.PublicURL, s.sealFields(sv.Fields, secret), appID, boolInt(sv.Enabled), sv.Notes, sv.Audience, string(aud), Now(), sv.ID)
		if err != nil {
			return conflict(err)
		}
	}
	if n, err := s.GetService(ctx, sv.ID); err == nil {
		*sv = *n
	}
	return nil
}

// DeleteService removes a service.
func (s *Store) DeleteService(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id)
	return err
}

// ---- per-user overrides ----

// Override replaces a service's URL and/or fields for one user.
type Override struct {
	ServiceID int64             `json:"service_id"`
	UserID    int64             `json:"user_id"`
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	Source    string            `json:"source"` // user (set by hand) | connector (written by a connector)
	UpdatedAt string            `json:"updated_at"`
}

// Override sources.
const (
	OverrideByUser      = "user"
	OverrideByConnector = "connector"
)

// ListOverrides returns a user's overrides keyed by service id.
func (s *Store) ListOverrides(ctx context.Context, userID int64) (map[int64]Override, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT service_id, user_id, url, fields, source, updated_at FROM service_overrides WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Override{}
	for rows.Next() {
		var o Override
		var f string
		if err := rows.Scan(&o.ServiceID, &o.UserID, &o.URL, &f, &o.Source, &o.UpdatedAt); err != nil {
			return nil, err
		}
		raw := map[string]string{}
		_ = json.Unmarshal([]byte(f), &raw)
		o.Fields = make(map[string]string, len(raw))
		for k, v := range raw {
			pv, err := s.Box.Open(v)
			if err != nil {
				return nil, fmt.Errorf("decrypt override %d/%d %s: %w", o.ServiceID, o.UserID, k, err)
			}
			o.Fields[k] = pv
		}
		out[o.ServiceID] = o
	}
	return out, rows.Err()
}

// SaveOverride upserts; an override with no URL and no fields is deleted.
// Source defaults to OverrideByUser.
func (s *Store) SaveOverride(ctx context.Context, o *Override, secret func(string) bool) error {
	if o.Source != OverrideByConnector {
		o.Source = OverrideByUser
	}
	clean := map[string]string{}
	for k, v := range o.Fields {
		if v != "" {
			clean[k] = v
		}
	}
	if o.URL == "" && len(clean) == 0 {
		return s.DeleteOverride(ctx, o.ServiceID, o.UserID)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO service_overrides (service_id, user_id, url, fields, source, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_id, user_id) DO UPDATE SET url = excluded.url, fields = excluded.fields, source = excluded.source, updated_at = excluded.updated_at`,
		o.ServiceID, o.UserID, o.URL, s.sealFields(clean, secret), o.Source, Now())
	return err
}

// DeleteOverride removes one.
func (s *Store) DeleteOverride(ctx context.Context, serviceID, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM service_overrides WHERE service_id = ? AND user_id = ?`, serviceID, userID)
	return err
}
