package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// CreateMCPAppToken is an internal, short-lived credential for exactly one app.
// Its pcu_ prefix lets existing Pocket SDKs route it to Core introspection. It
// is deliberately absent from api_tokens and cannot authenticate to Core APIs.
func (s *Store) CreateMCPAppToken(ctx context.Context, grantID string, data any) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	token := crypto.Token("pcu_mcp_", 32)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO mcp_oauth_secrets(hash,kind,client_id,user_id,grant_id,data,expires_at)
 SELECT ?,'app',client_id,user_id,id,?,? FROM mcp_oauth_grants WHERE id=? AND expires_at>?`, crypto.Hash(token), string(raw), Stamp(time.Now().Add(time.Minute)), grantID, Now())
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", ErrNotFound
	}
	return token, nil
}
func (s *Store) DeleteMCPAppToken(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM mcp_oauth_secrets WHERE hash=? AND kind='app'`, crypto.Hash(token))
	return err
}

// ReserveMCPAction serializes retries across connections belonging to the same
// user. An empty existing receipt means the outcome is unknown: never resend.
func (s *Store) ReserveMCPAction(ctx context.Context, userID int64, id, digest, tool string) (bool, string, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO mcp_app_operations(user_id,request_id,digest,tool,created_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, userID, id, digest, tool, Now())
	if err != nil {
		return false, "", err
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, "", nil
	}
	var previous, result string
	if err := s.DB.QueryRowContext(ctx, `SELECT digest,result FROM mcp_app_operations WHERE user_id=? AND request_id=?`, userID, id).Scan(&previous, &result); err != nil {
		return false, "", err
	}
	if previous != digest {
		return false, "", errors.New("request_id already belongs to a different operation")
	}
	return false, result, nil
}
func (s *Store) FinishMCPAction(ctx context.Context, userID int64, id string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE mcp_app_operations SET result=? WHERE user_id=? AND request_id=?`, string(raw), userID, id)
	return err
}
