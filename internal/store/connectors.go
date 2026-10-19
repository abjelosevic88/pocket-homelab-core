package store

import (
	"context"
	"database/sql"
)

// Connector is Core's settings and bookkeeping for one service's connector.
type Connector struct {
	ID                int64  `json:"id"`
	ServiceID         int64  `json:"service_id"`
	Enabled           bool   `json:"enabled"`
	ProvisionOnCreate bool   `json:"provision_on_create"`
	SSOEnabled        bool   `json:"sso_enabled"`
	EventsEnabled     bool   `json:"events_enabled"`
	PollIntervalS     int    `json:"poll_interval_s"`
	State             string `json:"-"`
	LastPollAt        string `json:"last_poll_at,omitempty"`
	LastError         string `json:"last_error"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

// MinPollInterval and DefaultPollInterval bound poll_interval_s.
const (
	MinPollInterval     = 60
	DefaultPollInterval = 300
)

const connectorCols = `id, service_id, enabled, provision_on_create, sso_enabled, events_enabled, poll_interval_s, state, last_poll_at, last_error, created_at, updated_at`

func scanConnector(sc interface{ Scan(...any) error }) (*Connector, error) {
	var c Connector
	var en, pr, sso, ev int
	var lp sql.NullString
	if err := sc.Scan(&c.ID, &c.ServiceID, &en, &pr, &sso, &ev, &c.PollIntervalS, &c.State, &lp, &c.LastError, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	c.Enabled, c.ProvisionOnCreate, c.SSOEnabled, c.EventsEnabled = en == 1, pr == 1, sso == 1, ev == 1
	c.LastPollAt = nullStr(lp)
	return &c, nil
}

// GetConnector loads a service's connector row (ErrNotFound when never configured).
func (s *Store) GetConnector(ctx context.Context, serviceID int64) (*Connector, error) {
	return scanConnector(s.DB.QueryRowContext(ctx, `SELECT `+connectorCols+` FROM connectors WHERE service_id = ?`, serviceID))
}

// ListConnectors returns every configured connector.
func (s *Store) ListConnectors(ctx context.Context) ([]Connector, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+connectorCols+` FROM connectors ORDER BY service_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connector{}
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// SaveConnector upserts the switches (not state or poll bookkeeping).
func (s *Store) SaveConnector(ctx context.Context, c *Connector) error {
	if c.PollIntervalS <= 0 {
		c.PollIntervalS = DefaultPollInterval
	}
	if c.PollIntervalS < MinPollInterval {
		c.PollIntervalS = MinPollInterval
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO connectors (service_id, enabled, provision_on_create, sso_enabled, events_enabled, poll_interval_s) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET enabled = excluded.enabled, provision_on_create = excluded.provision_on_create, sso_enabled = excluded.sso_enabled,
		events_enabled = excluded.events_enabled, poll_interval_s = excluded.poll_interval_s, updated_at = ?`,
		c.ServiceID, boolInt(c.Enabled), boolInt(c.ProvisionOnCreate), boolInt(c.SSOEnabled), boolInt(c.EventsEnabled), c.PollIntervalS, Now())
	if err != nil {
		return err
	}
	if n, err := s.GetConnector(ctx, c.ServiceID); err == nil {
		*c = *n
	}
	return nil
}

// SaveConnectorPoll records a poll: the new state (on success) and the error text.
func (s *Store) SaveConnectorPoll(ctx context.Context, serviceID int64, state *string, lastErr string) error {
	if state != nil {
		_, err := s.DB.ExecContext(ctx, `UPDATE connectors SET state = ?, last_poll_at = ?, last_error = ? WHERE service_id = ?`, *state, Now(), lastErr, serviceID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE connectors SET last_poll_at = ?, last_error = ? WHERE service_id = ?`, Now(), lastErr, serviceID)
	return err
}

// ResetConnectorState forgets the poll baseline (the next poll records a new one).
func (s *Store) ResetConnectorState(ctx context.Context, serviceID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE connectors SET state = '' WHERE service_id = ?`, serviceID)
	return err
}

// SetConnectorError records an error that did not come from polling (SSO, provisioning).
func (s *Store) SetConnectorError(ctx context.Context, serviceID int64, msg string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE connectors SET last_error = ?, updated_at = ? WHERE service_id = ?`, msg, Now(), serviceID)
	return err
}

// ---- per-person accounts ----

// ConnectorAccount is one person's account in a connected service.
type ConnectorAccount struct {
	ServiceID int64  `json:"service_id"`
	UserID    int64  `json:"user_id"`
	RemoteID  string `json:"remote_id"`
	Status    string `json:"status"` // ok | linked | error
	Error     string `json:"error"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const connectorAccountCols = `service_id, user_id, remote_id, status, error, created_at, updated_at`

func (s *Store) listConnectorAccounts(ctx context.Context, where string, arg int64) ([]ConnectorAccount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+connectorAccountCols+` FROM connector_accounts WHERE `+where+` = ? ORDER BY service_id, user_id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectorAccount{}
	for rows.Next() {
		var a ConnectorAccount
		if err := rows.Scan(&a.ServiceID, &a.UserID, &a.RemoteID, &a.Status, &a.Error, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListConnectorAccounts lists a service's accounts.
func (s *Store) ListConnectorAccounts(ctx context.Context, serviceID int64) ([]ConnectorAccount, error) {
	return s.listConnectorAccounts(ctx, "service_id", serviceID)
}

// ListUserConnectorAccounts lists one person's accounts across services.
func (s *Store) ListUserConnectorAccounts(ctx context.Context, userID int64) ([]ConnectorAccount, error) {
	return s.listConnectorAccounts(ctx, "user_id", userID)
}

// GetConnectorAccount loads one.
func (s *Store) GetConnectorAccount(ctx context.Context, serviceID, userID int64) (*ConnectorAccount, error) {
	var a ConnectorAccount
	err := s.DB.QueryRowContext(ctx, `SELECT `+connectorAccountCols+` FROM connector_accounts WHERE service_id = ? AND user_id = ?`, serviceID, userID).
		Scan(&a.ServiceID, &a.UserID, &a.RemoteID, &a.Status, &a.Error, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// SaveConnectorAccount upserts; an error keeps the remote id already known.
func (s *Store) SaveConnectorAccount(ctx context.Context, a *ConnectorAccount) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO connector_accounts (service_id, user_id, remote_id, status, error) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(service_id, user_id) DO UPDATE SET remote_id = CASE WHEN excluded.remote_id = '' THEN connector_accounts.remote_id ELSE excluded.remote_id END,
		status = excluded.status, error = excluded.error, updated_at = ?`,
		a.ServiceID, a.UserID, a.RemoteID, a.Status, a.Error, Now())
	if err != nil {
		return err
	}
	if n, err := s.GetConnectorAccount(ctx, a.ServiceID, a.UserID); err == nil {
		*a = *n
	}
	return nil
}

// GetOIDCClientByService loads the client a connector made for a service.
func (s *Store) GetOIDCClientByService(ctx context.Context, serviceID int64) (*OIDCClient, error) {
	return scanOIDCClient(s.DB.QueryRowContext(ctx, `SELECT `+oidcClientCols+` FROM oidc_clients WHERE service_id = ? ORDER BY id LIMIT 1`, serviceID))
}
