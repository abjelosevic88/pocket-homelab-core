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
	ID        int64             `json:"id"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	PublicURL string            `json:"public_url"`
	Fields    map[string]string `json:"fields"` // decrypted; secrets masked by the server before output
	AppID     int64             `json:"app_id,omitempty"`
	Enabled   bool              `json:"enabled"`
	Notes     string            `json:"notes"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
}

// EffectivePublicURL is what a device should use.
func (sv *Service) EffectivePublicURL() string {
	if sv.PublicURL != "" {
		return sv.PublicURL
	}
	return sv.URL
}

const serviceCols = `id, type, name, url, public_url, fields, app_id, enabled, notes, created_at, updated_at`

func (s *Store) scanService(sc interface{ Scan(...any) error }) (*Service, error) {
	var sv Service
	var fields string
	var appID sql.NullInt64
	var en int
	if err := sc.Scan(&sv.ID, &sv.Type, &sv.Name, &sv.URL, &sv.PublicURL, &fields, &appID, &en, &sv.Notes, &sv.CreatedAt, &sv.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	sv.Enabled = en == 1
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
	if sv.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO services (type, name, url, public_url, fields, app_id, enabled, notes) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sv.Type, sv.Name, sv.URL, sv.PublicURL, s.sealFields(sv.Fields, secret), appID, boolInt(sv.Enabled), sv.Notes)
		if err != nil {
			return conflict(err)
		}
		sv.ID, _ = res.LastInsertId()
	} else {
		_, err := s.DB.ExecContext(ctx, `UPDATE services SET type = ?, name = ?, url = ?, public_url = ?, fields = ?, app_id = ?, enabled = ?, notes = ?, updated_at = ? WHERE id = ?`,
			sv.Type, sv.Name, sv.URL, sv.PublicURL, s.sealFields(sv.Fields, secret), appID, boolInt(sv.Enabled), sv.Notes, Now(), sv.ID)
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
