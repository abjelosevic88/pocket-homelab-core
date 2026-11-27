package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
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

// AIConnectionChoices are the lifetimes (days) an admin can pick for assistant
// connections; DefaultAIConnectionDays applies until one is chosen.
var AIConnectionChoices = []int{1, 7, 30, 90, 365}

const DefaultAIConnectionDays = 30

// MCPRefreshGrace is how long a refresh token that was already exchanged is
// still honoured. Assistants run several sessions on one saved sign-in, and
// two of them refreshing at the same moment both send the same token.
const MCPRefreshGrace = 5 * time.Minute

func ValidAIConnectionDays(days int) bool {
	for _, d := range AIConnectionChoices {
		if d == days {
			return true
		}
	}
	return false
}

// AIConnectionDays is this Core's setting: how many days an assistant's
// sign-in lasts without being used, and the longest a connection token may be
// made for.
func (s *Store) AIConnectionDays(ctx context.Context) int {
	if d, err := strconv.Atoi(s.GetKV(ctx, "ai_connection_days")); err == nil && ValidAIConnectionDays(d) {
		return d
	}
	return DefaultAIConnectionDays
}

func (s *Store) aiConnectionTTL(ctx context.Context) time.Duration {
	return time.Duration(s.AIConnectionDays(ctx)) * 24 * time.Hour
}

// CapAIConnections applies a shorter setting to the connections that already
// exist. Pocket Ask keeps its own lifetime (ExtendAskConnection).
func (s *Store) CapAIConnections(ctx context.Context, days int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE mcp_oauth_grants SET expires_at=? WHERE expires_at>?
 AND NOT EXISTS(SELECT 1 FROM pocket_ask_connections a WHERE a.grant_id=mcp_oauth_grants.id)`,
		Stamp(time.Now().Add(time.Duration(days)*24*time.Hour)), Stamp(time.Now().Add(time.Duration(days)*24*time.Hour)))
	return err
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
	ttl := s.aiConnectionTTL(ctx)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_grants(id,client_id,user_id,auth_revision,created_at,expires_at,scope) VALUES (?,?,?,?,?,?,?)`, grantID, clientID, user.ID, user.AuthRevision, Now(), Stamp(time.Now().Add(ttl)), scope)
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
// tokens in one transaction. A code works once. A refresh token that was already
// exchanged is honoured again for MCPRefreshGrace (see SpentMCPRefresh); after
// that, presenting it ends the connection (RevokeSpentMCPSecret).
//
// A refresh also moves the connection's end to the full lifetime from now, so a
// sign-in that is being used does not run out.
func (s *Store) ExchangeMCPSecret(ctx context.Context, plain string, v *MCPOAuthSecret) (string, string, error) {
	ttl := s.aiConnectionTTL(ctx)
	kind := strings.TrimPrefix(v.Kind, "spent_")
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	now := time.Now()
	const grantValid = `EXISTS(SELECT 1 FROM mcp_oauth_grants g JOIN users u ON u.id=g.user_id WHERE g.id=mcp_oauth_secrets.grant_id AND g.expires_at>? AND u.disabled=0 AND u.auth_revision=g.auth_revision)`
	res, err := tx.ExecContext(ctx, `UPDATE mcp_oauth_secrets SET kind='spent_' || kind, spent_at=? WHERE hash=? AND kind=? AND client_id=? AND expires_at>? AND `+grantValid,
		Stamp(now), crypto.Hash(plain), kind, v.ClientID, Stamp(now), Stamp(now))
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if kind != "refresh" {
			return "", "", ErrNotFound
		}
		var one int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM mcp_oauth_secrets WHERE hash=? AND kind='spent_refresh' AND client_id=? AND spent_at>? AND expires_at>? AND `+grantValid,
			crypto.Hash(plain), v.ClientID, Stamp(now.Add(-MCPRefreshGrace)), Stamp(now), Stamp(now)).Scan(&one)
		if err != nil {
			return "", "", notFound(err)
		}
	}
	until := Stamp(now.Add(ttl))
	if kind == "refresh" {
		if _, err = tx.ExecContext(ctx, `UPDATE mcp_oauth_grants SET expires_at=? WHERE id=?`, until, v.GrantID); err != nil {
			return "", "", err
		}
	}
	access, refresh := crypto.Token("pcma_", 32), crypto.Token("pcmr_", 32)
	for _, t := range []struct {
		plain, kind, until string
	}{{access, "access", Stamp(now.Add(15 * time.Minute))}, {refresh, "refresh", until}} {
		_, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_secrets(hash,kind,client_id,user_id,grant_id,data,expires_at) VALUES (?,?,?,?,?,?,?)`, crypto.Hash(t.plain), t.kind, v.ClientID, v.UserID, v.GrantID, string(v.Data), t.until)
		if err != nil {
			return "", "", err
		}
	}
	return access, refresh, tx.Commit()
}

// SpentMCPRefresh finds a refresh token that was exchanged within
// MCPRefreshGrace and whose connection is still valid: the same assistant
// asking twice, not a stolen token turning up later.
func (s *Store) SpentMCPRefresh(ctx context.Context, plain, clientID string) (*MCPOAuthSecret, error) {
	var v MCPOAuthSecret
	var data string
	err := s.DB.QueryRowContext(ctx, `SELECT t.kind,t.client_id,t.user_id,t.grant_id,t.data,t.expires_at
 FROM mcp_oauth_secrets t JOIN mcp_oauth_grants g ON g.id=t.grant_id JOIN users u ON u.id=t.user_id
 WHERE t.hash=? AND t.kind='spent_refresh' AND t.client_id=? AND t.spent_at>? AND t.expires_at>? AND g.expires_at>? AND u.disabled=0 AND u.auth_revision=g.auth_revision`,
		crypto.Hash(plain), clientID, Stamp(time.Now().Add(-MCPRefreshGrace)), Now(), Now()).Scan(&v.Kind, &v.ClientID, &v.UserID, &v.GrantID, &data, &v.ExpiresAt)
	v.Data = json.RawMessage(data)
	return &v, notFound(err)
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

// Reuse of a consumed credential (a code, or a refresh token after
// MCPRefreshGrace) indicates a stolen token. Revoke the entire connection,
// including the current rotated refresh token. Returns the person and the
// time the credential was first used when a connection was ended, for the log.
func (s *Store) RevokeSpentMCPSecret(ctx context.Context, plain, kind, clientID string) (userID int64, spentAt string, err error) {
	var grantID string
	err = s.DB.QueryRowContext(ctx, `SELECT t.grant_id,t.user_id,t.spent_at FROM mcp_oauth_secrets t JOIN mcp_oauth_grants g ON g.id=t.grant_id WHERE t.hash=? AND t.kind=? AND t.client_id=?`,
		crypto.Hash(plain), "spent_"+kind, clientID).Scan(&grantID, &userID, &spentAt)
	if err != nil {
		if notFound(err) == ErrNotFound {
			return 0, "", nil
		}
		return 0, "", err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM mcp_oauth_grants WHERE id=? AND client_id=?`, grantID, clientID)
	return userID, spentAt, err
}
