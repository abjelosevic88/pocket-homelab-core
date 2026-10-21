package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

type MCPOAuthClient struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"client_name"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
	SecretHash   string   `json:"-"`
	RedirectURIs []string `json:"redirect_uris"`
}

func (s *Store) CreateMCPOAuthClient(ctx context.Context, c *MCPOAuthClient) error {
	c.ClientID = crypto.Token("mcp_", 24)
	redirects, _ := json.Marshal(c.RedirectURIs)
	// Bound unauthenticated registration storage. Existing clients remain valid.
	res, err := s.DB.ExecContext(ctx, `INSERT INTO mcp_oauth_clients(client_id,name,auth_method,secret_hash,redirect_uris,created_at)
 SELECT ?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM mcp_oauth_clients WHERE kind='oauth') < 1000`, c.ClientID, c.Name, c.AuthMethod, c.SecretHash, string(redirects), Now())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) GetMCPOAuthClient(ctx context.Context, id string) (*MCPOAuthClient, error) {
	var c MCPOAuthClient
	var redirects string
	err := s.DB.QueryRowContext(ctx, `SELECT client_id,name,auth_method,secret_hash,redirect_uris FROM mcp_oauth_clients WHERE client_id=?`, id).Scan(&c.ClientID, &c.Name, &c.AuthMethod, &c.SecretHash, &redirects)
	if err != nil {
		return nil, notFound(err)
	}
	if err := json.Unmarshal([]byte(redirects), &c.RedirectURIs); err != nil {
		return nil, err
	}
	return &c, nil
}

type MCPGrant struct {
	ID           string `json:"id"`
	ClientID     string `json:"client_id"`
	ClientName   string `json:"client_name"`
	Scope        string `json:"scope"`
	Kind         string `json:"kind"`
	UserID       int64  `json:"-"`
	AuthRevision int64  `json:"-"`
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at"`
}

func (s *Store) ListMCPGrants(ctx context.Context, userID int64) ([]MCPGrant, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT g.id,g.client_id,c.name,g.created_at,g.expires_at,g.scope,c.kind FROM mcp_oauth_grants g JOIN mcp_oauth_clients c ON c.client_id=g.client_id JOIN users u ON u.id=g.user_id WHERE g.user_id=? AND u.auth_revision=g.auth_revision AND u.disabled=0 ORDER BY g.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPGrant{}
	for rows.Next() {
		var g MCPGrant
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.CreatedAt, &g.ExpiresAt, &g.Scope, &g.Kind); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMCPGrant(ctx context.Context, id string, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM mcp_oauth_grants WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	return s.purgeMCPTokenClients(ctx)
}

// MCPOAuthSecret is a one-time consent request, code, refresh token, or access token.
type MCPOAuthSecret struct {
	Kind, ClientID, GrantID string
	UserID                  int64
	Data                    json.RawMessage
	ExpiresAt               string
}

func (s *Store) CreateMCPConsent(ctx context.Context, clientID string, userID int64, data any) (string, error) {
	plain := crypto.Token("", 32)
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO mcp_oauth_secrets(hash,kind,client_id,user_id,data,expires_at) VALUES (?,'consent',?,?,?,?)`, crypto.Hash(plain), clientID, userID, string(encoded), Stamp(time.Now().Add(5*time.Minute)))
	return plain, err
}

// ConsumeMCPConsent deletes atomically so replayed approvals cannot create grants.
func (s *Store) ConsumeMCPConsent(ctx context.Context, plain string, userID int64) (*MCPOAuthSecret, error) {
	var v MCPOAuthSecret
	var data string
	err := s.DB.QueryRowContext(ctx, `DELETE FROM mcp_oauth_secrets WHERE hash=? AND kind='consent' AND user_id=? AND expires_at>? RETURNING client_id,user_id,data,expires_at`, crypto.Hash(plain), userID, Now()).Scan(&v.ClientID, &v.UserID, &data, &v.ExpiresAt)
	v.Data = json.RawMessage(data)
	return &v, notFound(err)
}

func (s *Store) CreateMCPGrantCode(ctx context.Context, clientID string, user *User, data any, scopes ...string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	scope := "status:read"
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	grantID := crypto.Token("", 24)
	code := crypto.Token("", 32)
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_grants(id,client_id,user_id,auth_revision,created_at,expires_at,scope) VALUES (?,?,?,?,?,?,?)`, grantID, clientID, user.ID, user.AuthRevision, Now(), Stamp(time.Now().Add(30*24*time.Hour)), scope)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_secrets(hash,kind,client_id,user_id,grant_id,data,expires_at) VALUES (?,'code',?,?,?,?,?)`, crypto.Hash(code), clientID, user.ID, grantID, string(encoded), Stamp(time.Now().Add(5*time.Minute)))
	if err != nil {
		return "", err
	}
	return code, tx.Commit()
}

func (s *Store) GetMCPSecret(ctx context.Context, plain, kind, clientID string) (*MCPOAuthSecret, error) {
	var v MCPOAuthSecret
	var data string
	err := s.DB.QueryRowContext(ctx, `SELECT t.kind,t.client_id,t.user_id,t.grant_id,t.data,t.expires_at
 FROM mcp_oauth_secrets t JOIN mcp_oauth_grants g ON g.id=t.grant_id JOIN users u ON u.id=t.user_id
 WHERE t.hash=? AND t.kind=? AND (?='' OR t.client_id=?) AND t.expires_at>? AND g.expires_at>? AND u.disabled=0 AND u.auth_revision=g.auth_revision`, crypto.Hash(plain), kind, clientID, clientID, Now(), Now()).Scan(&v.Kind, &v.ClientID, &v.UserID, &v.GrantID, &data, &v.ExpiresAt)
	v.Data = json.RawMessage(data)
	return &v, notFound(err)
}

// ExchangeMCPSecret marks a validated code/refresh token spent and issues both new
// tokens in one transaction. Concurrent reuse cannot create another token pair.
func (s *Store) ExchangeMCPSecret(ctx context.Context, plain string, v *MCPOAuthSecret) (string, string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE mcp_oauth_secrets SET kind='spent_' || kind WHERE hash=? AND kind=? AND client_id=? AND expires_at>?
 AND EXISTS(SELECT 1 FROM mcp_oauth_grants g JOIN users u ON u.id=g.user_id WHERE g.id=mcp_oauth_secrets.grant_id AND g.expires_at>? AND u.disabled=0 AND u.auth_revision=g.auth_revision)`, crypto.Hash(plain), v.Kind, v.ClientID, Now(), Now())
	if err != nil {
		return "", "", err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", "", ErrNotFound
	}
	access, refresh := crypto.Token("pcma_", 32), crypto.Token("pcmr_", 32)
	for _, t := range []struct {
		plain, kind string
		ttl         time.Duration
	}{{access, "access", 15 * time.Minute}, {refresh, "refresh", 30 * 24 * time.Hour}} {
		_, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_secrets(hash,kind,client_id,user_id,grant_id,data,expires_at) VALUES (?,?,?,?,?,?,?)`, crypto.Hash(t.plain), t.kind, v.ClientID, v.UserID, v.GrantID, string(v.Data), Stamp(time.Now().Add(t.ttl)))
		if err != nil {
			return "", "", err
		}
	}
	return access, refresh, tx.Commit()
}

func (s *Store) RevokeMCPSecret(ctx context.Context, plain, clientID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM mcp_oauth_grants WHERE client_id=? AND id IN (SELECT grant_id FROM mcp_oauth_secrets WHERE hash=? AND client_id=?)`, clientID, crypto.Hash(plain), clientID)
	return err
}

// MCPVisibleApps reads only operational metadata; it never decrypts service keys.
func (s *Store) MCPVisibleApps(ctx context.Context, user *User) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT a.slug,s.audience,s.audience_users FROM apps a JOIN services s ON s.app_id=a.id WHERE s.enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var slug, audience, raw string
		if err := rows.Scan(&slug, &audience, &raw); err != nil {
			return nil, err
		}
		var ids []int64
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, err
		}
		sv := Service{Audience: audience, AudienceUsers: ids}
		if sv.InAudience(user) {
			out = append(out, slug)
		}
	}
	return out, rows.Err()
}

// Reuse of a consumed credential indicates a stolen token or concurrent client
// use. Revoke the entire connection, including the current rotated refresh token.
func (s *Store) RevokeSpentMCPSecret(ctx context.Context, plain, kind, clientID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM mcp_oauth_grants WHERE client_id=? AND id IN (SELECT grant_id FROM mcp_oauth_secrets WHERE hash=? AND kind=? AND client_id=?)`, clientID, crypto.Hash(plain), "spent_"+kind, clientID)
	return err
}
