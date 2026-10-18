package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// App is a registered Pocket app.
type App struct {
	ID          int64           `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Version     string          `json:"version"`
	URL         string          `json:"url"`
	PublicURL   string          `json:"public_url"`
	HealthPath  string          `json:"health_path"`
	EnvPrefix   string          `json:"env_prefix"`
	Manifest    json.RawMessage `json:"manifest"`
	TokenPrefix string          `json:"token_prefix"`
	Status      string          `json:"status"`
	LastSeenAt  string          `json:"last_seen_at,omitempty"`
	LastCheckAt string          `json:"last_check_at,omitempty"`
	LastError   string          `json:"last_error"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// EffectivePublicURL is what a device should open.
func (a *App) EffectivePublicURL() string {
	if a.PublicURL != "" {
		return a.PublicURL
	}
	return a.URL
}

const appCols = `id, slug, name, description, icon, version, url, public_url, health_path, env_prefix, manifest, token_prefix, status, last_seen_at, last_check_at, last_error, created_at, updated_at`

func scanApp(sc interface{ Scan(...any) error }) (*App, error) {
	var a App
	var m string
	var seen, check sql.NullString
	if err := sc.Scan(&a.ID, &a.Slug, &a.Name, &a.Description, &a.Icon, &a.Version, &a.URL, &a.PublicURL, &a.HealthPath, &a.EnvPrefix, &m, &a.TokenPrefix, &a.Status, &seen, &check, &a.LastError, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	a.Manifest = json.RawMessage(m)
	a.LastSeenAt, a.LastCheckAt = nullStr(seen), nullStr(check)
	return &a, nil
}

// ListApps returns every registered app.
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+appCols+` FROM apps ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// GetApp loads by id.
func (s *Store) GetApp(ctx context.Context, id int64) (*App, error) {
	return scanApp(s.DB.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps WHERE id = ?`, id))
}

// GetAppBySlug loads by slug.
func (s *Store) GetAppBySlug(ctx context.Context, slug string) (*App, error) {
	return scanApp(s.DB.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps WHERE slug = ?`, slug))
}

// GetAppByToken resolves a pca_ token and touches last_seen.
func (s *Store) GetAppByToken(ctx context.Context, token string) (*App, error) {
	a, err := scanApp(s.DB.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps WHERE token_hash = ?`, crypto.Hash(token)))
	if err != nil {
		return nil, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE apps SET last_seen_at = ? WHERE id = ?`, Now(), a.ID)
	return a, nil
}

// CreateApp inserts a new app and returns the plain app token (shown once).
func (s *Store) CreateApp(ctx context.Context, a *App) (string, error) {
	plain := crypto.Token("pca_", 32)
	if a.HealthPath == "" {
		a.HealthPath = "/healthz"
	}
	if len(a.Manifest) == 0 {
		a.Manifest = json.RawMessage("{}")
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO apps (slug, name, description, icon, version, url, public_url, health_path, env_prefix, manifest, token_hash, token_prefix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Slug, a.Name, a.Description, a.Icon, a.Version, a.URL, a.PublicURL, a.HealthPath, a.EnvPrefix, string(a.Manifest), crypto.Hash(plain), crypto.Prefix(plain, 10))
	if err != nil {
		return "", conflict(err)
	}
	a.ID, _ = res.LastInsertId()
	if na, err := s.GetApp(ctx, a.ID); err == nil {
		*a = *na
	}
	return plain, nil
}

// UpdateApp saves the descriptive fields (a re-registration or an admin edit).
func (s *Store) UpdateApp(ctx context.Context, a *App) error {
	if a.HealthPath == "" {
		a.HealthPath = "/healthz"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE apps SET name = ?, description = ?, icon = ?, version = ?, url = ?, public_url = ?, health_path = ?, env_prefix = ?, manifest = ?, updated_at = ?, last_seen_at = ? WHERE id = ?`,
		a.Name, a.Description, a.Icon, a.Version, a.URL, a.PublicURL, a.HealthPath, a.EnvPrefix, string(a.Manifest), Now(), Now(), a.ID)
	return conflict(err)
}

// RotateAppToken issues a fresh pca_ token.
func (s *Store) RotateAppToken(ctx context.Context, id int64) (string, error) {
	plain := crypto.Token("pca_", 32)
	_, err := s.DB.ExecContext(ctx, `UPDATE apps SET token_hash = ?, token_prefix = ?, updated_at = ? WHERE id = ?`, crypto.Hash(plain), crypto.Prefix(plain, 10), Now(), id)
	return plain, err
}

// SetAppHealth records the result of a health check.
func (s *Store) SetAppHealth(ctx context.Context, id int64, status, version, errMsg string) error {
	if version != "" {
		_, err := s.DB.ExecContext(ctx, `UPDATE apps SET status = ?, version = ?, last_check_at = ?, last_error = ? WHERE id = ?`, status, version, Now(), errMsg, id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE apps SET status = ?, last_check_at = ?, last_error = ? WHERE id = ?`, status, Now(), errMsg, id)
	return err
}

// DeleteApp removes an app, its services, webhooks and OIDC clients.
func (s *Store) DeleteApp(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
	return err
}
