package store

import (
	"context"
	"database/sql"
)

// ConnectorHook is one push subscription a connector made in its app.
type ConnectorHook struct {
	ID               string `json:"id"`
	ServiceID        int64  `json:"service_id"`
	UserID           int64  `json:"user_id"` // 0 = a global subscription
	Trigger          string `json:"trigger"`
	RemoteWorkflowID string `json:"remote_workflow_id"`
	SecretHash       string `json:"-"`
	State            string `json:"state"`
	Error            string `json:"error"`
	Received         int64  `json:"received"`
	Published        int64  `json:"published"`
	Dropped          int64  `json:"dropped"`
	LastDrop         string `json:"last_drop"`
	LastReceivedAt   string `json:"last_received_at,omitempty"`
	CheckedAt        string `json:"checked_at,omitempty"`
	CreatedAt        string `json:"created_at"`
}

const hookCols = `id, service_id, user_id, trigger, remote_workflow_id, secret_hash, state, error, received, published, dropped, last_drop, last_received_at, checked_at, created_at`

func scanHook(sc interface{ Scan(...any) error }) (*ConnectorHook, error) {
	var h ConnectorHook
	var uid sql.NullInt64
	var lr, ck sql.NullString
	if err := sc.Scan(&h.ID, &h.ServiceID, &uid, &h.Trigger, &h.RemoteWorkflowID, &h.SecretHash, &h.State, &h.Error,
		&h.Received, &h.Published, &h.Dropped, &h.LastDrop, &lr, &ck, &h.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	h.UserID = uid.Int64
	h.LastReceivedAt, h.CheckedAt = nullStr(lr), nullStr(ck)
	return &h, nil
}

// GetConnectorHook loads one by its id (the part of the URL).
func (s *Store) GetConnectorHook(ctx context.Context, id string) (*ConnectorHook, error) {
	return scanHook(s.DB.QueryRowContext(ctx, `SELECT `+hookCols+` FROM connector_hooks WHERE id = ?`, id))
}

func (s *Store) listHooks(ctx context.Context, where string, args ...any) ([]ConnectorHook, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+hookCols+` FROM connector_hooks WHERE `+where+` ORDER BY service_id, IFNULL(user_id, 0), trigger`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectorHook{}
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// ListConnectorHooks lists a service's hooks.
func (s *Store) ListConnectorHooks(ctx context.Context, serviceID int64) ([]ConnectorHook, error) {
	return s.listHooks(ctx, `service_id = ?`, serviceID)
}

// ListUserConnectorHooks lists one person's hooks across services.
func (s *Store) ListUserConnectorHooks(ctx context.Context, userID int64) ([]ConnectorHook, error) {
	return s.listHooks(ctx, `user_id = ?`, userID)
}

// SaveConnectorHook inserts a hook or, for an existing id, records a new
// subscription for it (remote id and secret), keeping its counters.
func (s *Store) SaveConnectorHook(ctx context.Context, h *ConnectorHook) error {
	var uid any
	if h.UserID != 0 {
		uid = h.UserID
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO connector_hooks (id, service_id, user_id, trigger, remote_workflow_id, secret_hash, state, error, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, 'ok', '', ?)
		ON CONFLICT(id) DO UPDATE SET remote_workflow_id = excluded.remote_workflow_id, secret_hash = excluded.secret_hash, state = 'ok', error = '', checked_at = excluded.checked_at`,
		h.ID, h.ServiceID, uid, h.Trigger, h.RemoteWorkflowID, h.SecretHash, Now())
	if err != nil {
		return err
	}
	if n, err := s.GetConnectorHook(ctx, h.ID); err == nil {
		*h = *n
	}
	return nil
}

// SetConnectorHookCheck records the outcome of a check (state ok or error).
func (s *Store) SetConnectorHookCheck(ctx context.Context, id, state, msg string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE connector_hooks SET state = ?, error = ?, checked_at = ? WHERE id = ?`, state, msg, Now(), id)
	return err
}

// RecordConnectorHookDelivery counts one delivery: published, or dropped and why.
func (s *Store) RecordConnectorHookDelivery(ctx context.Context, id string, published bool, drop string) error {
	pub, dr := 0, 0
	if published {
		pub = 1
	} else if drop != "" {
		dr = 1
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE connector_hooks SET received = received + 1, published = published + ?, dropped = dropped + ?,
		last_drop = CASE WHEN ? = '' THEN last_drop ELSE ? END, last_received_at = ? WHERE id = ?`, pub, dr, drop, drop, Now(), id)
	return err
}

// DeleteConnectorHook removes one.
func (s *Store) DeleteConnectorHook(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM connector_hooks WHERE id = ?`, id)
	return err
}
