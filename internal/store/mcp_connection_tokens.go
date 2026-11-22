package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// CreateMCPConnectionToken creates a user-approved, MCP-only bearer credential.
// Plaintext is returned once, never persisted. Empty callbacks prevent this
// private client record from participating in authorization-code flows.
func (s *Store) CreateMCPConnectionToken(ctx context.Context, user *User, name, scope string, days int, binding any) (*MCPGrant, string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	grant, token, err := createMCPConnectionTokenTx(ctx, tx, user, name, scope, days, binding)
	if err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return grant, token, nil
}

func createMCPConnectionTokenTx(ctx context.Context, tx *sql.Tx, user *User, name, scope string, days int, binding any) (*MCPGrant, string, error) {
	var err error
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_grants g JOIN mcp_oauth_clients c ON c.client_id=g.client_id WHERE g.user_id=? AND c.kind='token' AND g.expires_at>? AND g.auth_revision=?`, user.ID, Now(), user.AuthRevision).Scan(&count); err != nil {
		return nil, "", err
	}
	if count >= 20 {
		return nil, "", ErrConflict
	}
	clientID := crypto.Token("mcp_manual_", 24)
	grant := &MCPGrant{ID: crypto.Token("", 24), ClientID: clientID, ClientName: name, Kind: "token", Scope: scope, UserID: user.ID, AuthRevision: user.AuthRevision, CreatedAt: Now(), ExpiresAt: Stamp(time.Now().Add(time.Duration(days) * 24 * time.Hour))}
	raw, err := json.Marshal(binding)
	if err != nil {
		return nil, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_clients(client_id,name,auth_method,redirect_uris,created_at,kind) VALUES (?,?,'none','[]',?,'token')`, clientID, name, grant.CreatedAt); err != nil {
		return nil, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_grants(id,client_id,user_id,auth_revision,created_at,expires_at,scope) VALUES(?,?,?,?,?,?,?)`, grant.ID, clientID, user.ID, user.AuthRevision, grant.CreatedAt, grant.ExpiresAt, scope); err != nil {
		return nil, "", err
	}
	token := crypto.Token("pcma_", 32)
	if _, err = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_secrets(hash,kind,client_id,user_id,grant_id,data,expires_at) VALUES (?,'access',?,?,?,?,?)`, crypto.Hash(token), clientID, user.ID, grant.ID, string(raw), grant.ExpiresAt); err != nil {
		return nil, "", err
	}
	return grant, token, nil
}

func (s *Store) purgeMCPTokenClients(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM mcp_oauth_clients WHERE kind='token' AND NOT EXISTS(SELECT 1 FROM mcp_oauth_grants g WHERE g.client_id=mcp_oauth_clients.client_id)`)
	return err
}

// AskConnectionTTL is how long the Pocket Ask connection lasts unused.
const AskConnectionTTL = 30 * 24 * time.Hour

// ExtendAskConnection keeps the person's Pocket Ask connection alive while
// Pocket AI uses it: an access token of the grant in pocket_ask_connections
// gets AskConnectionTTL from now again (at most once a day). Only a grant
// that is still valid is extended, so a disconnect, an expired connection or
// a sign-out everywhere (auth revision) still ends it.
func (s *Store) ExtendAskConnection(ctx context.Context, grantID string, userID int64, expiresAt string) error {
	now := time.Now()
	if expiresAt > Stamp(now.Add(AskConnectionTTL-24*time.Hour)) {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	until := Stamp(now.Add(AskConnectionTTL))
	res, err := tx.ExecContext(ctx, `UPDATE mcp_oauth_grants SET expires_at=? WHERE id=? AND user_id=? AND expires_at>? AND expires_at<?
 AND EXISTS(SELECT 1 FROM pocket_ask_connections a WHERE a.grant_id=mcp_oauth_grants.id AND a.user_id=?)
 AND EXISTS(SELECT 1 FROM users u WHERE u.id=mcp_oauth_grants.user_id AND u.disabled=0 AND u.auth_revision=mcp_oauth_grants.auth_revision)`,
		until, grantID, userID, Stamp(now), until, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mcp_oauth_secrets SET expires_at=? WHERE grant_id=? AND user_id=? AND kind='access' AND expires_at>?`, until, grantID, userID, Stamp(now)); err != nil {
		return err
	}
	return tx.Commit()
}
