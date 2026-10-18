package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// OIDCClient is a relying party.
type OIDCClient struct {
	ID           int64    `json:"id"`
	ClientID     string   `json:"client_id"`
	HasSecret    bool     `json:"has_secret"`
	SecretHash   string   `json:"-"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	AppID        int64    `json:"app_id,omitempty"`
	CreatedAt    string   `json:"created_at"`
}

// Public reports whether this is a PKCE-only client without a secret.
func (c *OIDCClient) Public() bool { return c.SecretHash == "" }

// AllowsRedirect checks a redirect_uri exactly.
func (c *OIDCClient) AllowsRedirect(uri string) bool {
	for _, u := range c.RedirectURIs {
		if u == uri {
			return true
		}
	}
	return false
}

const oidcClientCols = `id, client_id, secret_hash, name, redirect_uris, app_id, created_at`

func scanOIDCClient(sc interface{ Scan(...any) error }) (*OIDCClient, error) {
	var c OIDCClient
	var uris string
	var appID sql.NullInt64
	if err := sc.Scan(&c.ID, &c.ClientID, &c.SecretHash, &c.Name, &uris, &appID, &c.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	c.HasSecret = c.SecretHash != ""
	if appID.Valid {
		c.AppID = appID.Int64
	}
	for _, u := range strings.Split(uris, "\n") {
		if u = strings.TrimSpace(u); u != "" {
			c.RedirectURIs = append(c.RedirectURIs, u)
		}
	}
	if c.RedirectURIs == nil {
		c.RedirectURIs = []string{}
	}
	return &c, nil
}

// ListOIDCClients lists relying parties.
func (s *Store) ListOIDCClients(ctx context.Context) ([]OIDCClient, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+oidcClientCols+` FROM oidc_clients ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OIDCClient{}
	for rows.Next() {
		c, err := scanOIDCClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetOIDCClient loads by client_id.
func (s *Store) GetOIDCClient(ctx context.Context, clientID string) (*OIDCClient, error) {
	return scanOIDCClient(s.DB.QueryRowContext(ctx, `SELECT `+oidcClientCols+` FROM oidc_clients WHERE client_id = ?`, clientID))
}

// GetOIDCClientByID loads by row id.
func (s *Store) GetOIDCClientByID(ctx context.Context, id int64) (*OIDCClient, error) {
	return scanOIDCClient(s.DB.QueryRowContext(ctx, `SELECT `+oidcClientCols+` FROM oidc_clients WHERE id = ?`, id))
}

// SaveOIDCClient inserts or updates; when secret is non-empty it is (re)set.
// The plain secret is returned for display once.
func (s *Store) SaveOIDCClient(ctx context.Context, c *OIDCClient, newSecret bool) (string, error) {
	uris := strings.Join(c.RedirectURIs, "\n")
	var appID any
	if c.AppID > 0 {
		appID = c.AppID
	}
	plain := ""
	if newSecret {
		plain = crypto.Token("pcs_", 32)
		c.SecretHash = crypto.Hash(plain)
	}
	if c.ID == 0 {
		if c.ClientID == "" {
			c.ClientID = crypto.Token("", 12)
		}
		res, err := s.DB.ExecContext(ctx, `INSERT INTO oidc_clients (client_id, secret_hash, name, redirect_uris, app_id) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(client_id) DO UPDATE SET name = excluded.name, redirect_uris = excluded.redirect_uris, app_id = COALESCE(excluded.app_id, oidc_clients.app_id), secret_hash = CASE WHEN excluded.secret_hash = '' THEN oidc_clients.secret_hash ELSE excluded.secret_hash END`,
			c.ClientID, c.SecretHash, c.Name, uris, appID)
		if err != nil {
			return "", conflict(err)
		}
		if id, _ := res.LastInsertId(); id > 0 {
			c.ID = id
		}
	} else {
		_, err := s.DB.ExecContext(ctx, `UPDATE oidc_clients SET name = ?, redirect_uris = ?, app_id = ?, secret_hash = CASE WHEN ? = '' THEN secret_hash ELSE ? END WHERE id = ?`,
			c.Name, uris, appID, c.SecretHash, c.SecretHash, c.ID)
		if err != nil {
			return "", conflict(err)
		}
	}
	if n, err := s.GetOIDCClient(ctx, c.ClientID); err == nil {
		*c = *n
	}
	return plain, nil
}

// DeleteOIDCClient removes a relying party.
func (s *Store) DeleteOIDCClient(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM oidc_clients WHERE id = ?`, id)
	return err
}

// OIDCCode is a pending authorization code.
type OIDCCode struct {
	Code          string
	ClientID      string
	UserID        int64
	RedirectURI   string
	Scope         string
	Nonce         string
	CodeChallenge string
	ExpiresAt     string
}

// CreateOIDCCode stores a code valid for ttl.
func (s *Store) CreateOIDCCode(ctx context.Context, c *OIDCCode, ttl time.Duration) error {
	c.Code = crypto.Token("", 32)
	c.ExpiresAt = Stamp(time.Now().Add(ttl))
	_, err := s.DB.ExecContext(ctx, `INSERT INTO oidc_codes (code, client_id, user_id, redirect_uri, scope, nonce, code_challenge, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Code, c.ClientID, c.UserID, c.RedirectURI, c.Scope, c.Nonce, c.CodeChallenge, c.ExpiresAt)
	return err
}

// ConsumeOIDCCode fetches and deletes a code (single use).
func (s *Store) ConsumeOIDCCode(ctx context.Context, code string) (*OIDCCode, error) {
	var c OIDCCode
	if err := s.DB.QueryRowContext(ctx, `SELECT code, client_id, user_id, redirect_uri, scope, nonce, code_challenge, expires_at FROM oidc_codes WHERE code = ?`, code).Scan(&c.Code, &c.ClientID, &c.UserID, &c.RedirectURI, &c.Scope, &c.Nonce, &c.CodeChallenge, &c.ExpiresAt); err != nil {
		return nil, notFound(err)
	}
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM oidc_codes WHERE code = ?`, code)
	if t, err := time.Parse(time.RFC3339Nano, c.ExpiresAt); err != nil || t.Before(time.Now()) {
		return nil, ErrNotFound
	}
	return &c, nil
}

// OIDCToken is an issued access or refresh token.
type OIDCToken struct {
	Kind      string
	ClientID  string
	UserID    int64
	Scope     string
	ExpiresAt string
}

// CreateOIDCToken stores a token hash and returns the plain token.
func (s *Store) CreateOIDCToken(ctx context.Context, t *OIDCToken, ttl time.Duration) (string, error) {
	prefix := "pco_"
	if t.Kind == "refresh" {
		prefix = "pcr_"
	}
	plain := crypto.Token(prefix, 32)
	t.ExpiresAt = Stamp(time.Now().Add(ttl))
	_, err := s.DB.ExecContext(ctx, `INSERT INTO oidc_tokens (token_hash, kind, client_id, user_id, scope, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		crypto.Hash(plain), t.Kind, t.ClientID, t.UserID, t.Scope, t.ExpiresAt)
	return plain, err
}

// GetOIDCToken resolves a token, nil when expired or unknown.
func (s *Store) GetOIDCToken(ctx context.Context, plain string) (*OIDCToken, error) {
	var t OIDCToken
	if err := s.DB.QueryRowContext(ctx, `SELECT kind, client_id, user_id, scope, expires_at FROM oidc_tokens WHERE token_hash = ?`, crypto.Hash(plain)).Scan(&t.Kind, &t.ClientID, &t.UserID, &t.Scope, &t.ExpiresAt); err != nil {
		return nil, notFound(err)
	}
	if ts, err := time.Parse(time.RFC3339Nano, t.ExpiresAt); err != nil || ts.Before(time.Now()) {
		return nil, ErrNotFound
	}
	return &t, nil
}

// DeleteOIDCToken revokes a token (refresh rotation).
func (s *Store) DeleteOIDCToken(ctx context.Context, plain string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM oidc_tokens WHERE token_hash = ?`, crypto.Hash(plain))
	return err
}
