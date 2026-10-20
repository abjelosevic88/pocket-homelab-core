package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// User is an account on the box.
type User struct {
	ID           int64  `json:"id"`
	Subject      string `json:"subject"`
	AuthRevision int64  `json:"auth_revision"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// IsAdmin reports whether the user may manage the box.
func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

const userCols = `subject, auth_revision, id, email, name, password_hash, role, disabled, created_at, updated_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var dis int
	if err := sc.Scan(&u.Subject, &u.AuthRevision, &u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &dis, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	u.Disabled = dis == 1
	return &u, nil
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

// ListUsers returns every account.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// GetUser loads by id.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetUserByEmail loads by email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

// CreateUser inserts an account.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	if u.Role == "" {
		u.Role = "member"
	}
	u.Subject = crypto.Token("person_", 24)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO users (email, name, password_hash, role, subject) VALUES (?, ?, ?, ?, ?)`, u.Email, u.Name, u.PasswordHash, u.Role, u.Subject)
	if err != nil {
		return conflict(err)
	}
	u.ID, _ = res.LastInsertId()
	nu, err := s.GetUser(ctx, u.ID)
	if err == nil {
		*u = *nu
	}
	return nil
}

// UpdateUser saves name, password, role and disabled.
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET auth_revision = auth_revision + CASE WHEN disabled <> ? OR password_hash <> ? OR role <> ? THEN 1 ELSE 0 END, email = ?, name = ?, password_hash = ?, role = ?, disabled = ?, updated_at = ? WHERE id = ?`,
		boolInt(u.Disabled), u.PasswordHash, u.Role, u.Email, u.Name, u.PasswordHash, u.Role, boolInt(u.Disabled), Now(), u.ID)
	return conflict(err)
}

// DeleteUser removes an account and everything it owns.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// ---- sessions ----

// CreateSession opens a browser session.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	id := crypto.Token("", 32)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`, id, userID, Stamp(time.Now().Add(ttl)))
	return id, err
}

// GetSessionUser resolves a session cookie to its user.
func (s *Store) GetSessionUser(ctx context.Context, id string) (*User, error) {
	var userID int64
	var exp string
	if err := s.DB.QueryRowContext(ctx, `SELECT user_id, expires_at FROM sessions WHERE id = ?`, id).Scan(&userID, &exp); err != nil {
		return nil, notFound(err)
	}
	if t, err := time.Parse(time.RFC3339Nano, exp); err != nil || t.Before(time.Now()) {
		_ = s.DeleteSession(ctx, id)
		return nil, ErrNotFound
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrNotFound
	}
	return u, nil
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// PurgeExpired drops expired sessions, codes, tokens and pairings.
func (s *Store) PurgeExpired(ctx context.Context) error {
	now := Now()
	for _, q := range []string{
		`DELETE FROM sessions WHERE expires_at < ?`,
		`DELETE FROM oidc_codes WHERE expires_at < ?`,
		`DELETE FROM oidc_tokens WHERE expires_at < ?`,
		`DELETE FROM link_tokens WHERE expires_at < ?`,
		`DELETE FROM pairings WHERE claimed_at IS NULL AND expires_at < ?`,
	} {
		if _, err := s.DB.ExecContext(ctx, q, now); err != nil {
			return err
		}
	}
	return nil
}

// ---- personal API tokens ----

// APIToken is a personal access token (secret shown once).
type APIToken struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// CreateAPIToken mints a pcu_ token for a user and returns the plain secret.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name string) (*APIToken, string, error) {
	plain := crypto.Token("pcu_", 32)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, prefix) VALUES (?, ?, ?, ?)`, userID, name, crypto.Hash(plain), crypto.Prefix(plain, 10))
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	return &APIToken{ID: id, UserID: userID, Name: name, Prefix: crypto.Prefix(plain, 10), CreatedAt: Now()}, plain, nil
}

// ListAPITokens lists a user's tokens.
func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, name, prefix, last_used_at, created_at FROM api_tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var lu sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &lu, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.LastUsedAt = nullStr(lu)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes a token owned by the user.
func (s *Store) DeleteAPIToken(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// GetUserByAPIToken resolves a pcu_ token.
func (s *Store) GetUserByAPIToken(ctx context.Context, token string) (*User, error) {
	var id, userID int64
	if err := s.DB.QueryRowContext(ctx, `SELECT id, user_id FROM api_tokens WHERE token_hash = ?`, crypto.Hash(token)).Scan(&id, &userID); err != nil {
		return nil, notFound(err)
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, Now(), id)
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrNotFound
	}
	return u, nil
}

func (s *Store) GetUserBySubject(ctx context.Context, subject string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE subject = ?`, subject))
}
