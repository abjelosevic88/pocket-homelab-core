package store

import (
	"context"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

type MCPTerminalPairing struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Days      int    `json:"expires_in_days"`
	ExpiresAt string `json:"expires_at"`
}

func pairingCodeHash(code string) string {
	return crypto.Hash(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", "")))
}

func (s *Store) CreateMCPTerminalPairing(ctx context.Context, name, scope string, days int) (string, string, error) {
	// Purge on creation as well as in the janitor so abandoned attempts cannot
	// fill the bounded table until the next hourly sweep.
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM mcp_terminal_pairings WHERE expires_at<=?`, Now()); err != nil {
		return "", "", err
	}
	secret, code := crypto.Token("pcmp_", 32), crypto.Code(10)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO mcp_terminal_pairings(request_hash,user_code_hash,name,scope,days,expires_at)
 SELECT ?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM mcp_terminal_pairings)<1000`, crypto.Hash(secret), pairingCodeHash(code), name, scope, days, Stamp(time.Now().Add(10*time.Minute)))
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", "", ErrConflict
	}
	return secret, code[:5] + "-" + code[5:], nil
}

func (s *Store) GetMCPTerminalPairing(ctx context.Context, code string) (*MCPTerminalPairing, error) {
	var p MCPTerminalPairing
	err := s.DB.QueryRowContext(ctx, `SELECT name,scope,days,expires_at FROM mcp_terminal_pairings WHERE user_code_hash=? AND expires_at>? AND user_id IS NULL`, pairingCodeHash(code), Now()).Scan(&p.Name, &p.Scope, &p.Days, &p.ExpiresAt)
	return &p, notFound(err)
}

func (s *Store) DecideMCPTerminalPairing(ctx context.Context, code string, user *User, approve bool) (string, error) {
	completion := crypto.Token("pcmp_done_", 32)
	query := `UPDATE mcp_terminal_pairings SET user_id=?,auth_revision=?,completion_hash=? WHERE user_code_hash=? AND user_id IS NULL AND expires_at>?`
	args := []any{user.ID, user.AuthRevision, crypto.Hash(completion), pairingCodeHash(code), Now()}
	if !approve {
		query = `DELETE FROM mcp_terminal_pairings WHERE user_code_hash=? AND user_id IS NULL AND expires_at>?`
		args = []any{pairingCodeHash(code), Now()}
		completion = ""
	}
	res, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrNotFound
	}
	return completion, nil
}

// Redemption and credential issuance are one transaction. Both the initiating
// terminal secret and the browser's result are required; neither works alone.
func (s *Store) ExchangeMCPTerminalPairing(ctx context.Context, secret, completion string, binding func(string) (any, error)) (*MCPGrant, string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var p MCPTerminalPairing
	var user User
	err = tx.QueryRowContext(ctx, `SELECT p.name,p.scope,p.days,u.id,u.auth_revision FROM mcp_terminal_pairings p JOIN users u ON u.id=p.user_id
 WHERE p.request_hash=? AND p.completion_hash=? AND p.expires_at>? AND u.disabled=0 AND u.auth_revision=p.auth_revision`, crypto.Hash(secret), crypto.Hash(completion), Now()).Scan(&p.Name, &p.Scope, &p.Days, &user.ID, &user.AuthRevision)
	if err != nil {
		return nil, "", notFound(err)
	}
	authorization, err := binding(p.Scope)
	if err != nil {
		return nil, "", err
	}
	grant, token, err := createMCPConnectionTokenTx(ctx, tx, &user, p.Name, p.Scope, p.Days, authorization)
	if err != nil {
		return nil, "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM mcp_terminal_pairings WHERE request_hash=?`, crypto.Hash(secret)); err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return grant, token, nil
}
