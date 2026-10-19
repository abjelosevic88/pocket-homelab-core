package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
)

// AppLink lets one app act for a person in another app, for one scope.
type AppLink struct {
	ID         int64  `json:"id"`
	AppID      int64  `json:"app_id"`
	AppSlug    string `json:"app_slug"`
	AppName    string `json:"app_name"`
	TargetSlug string `json:"target_slug"`
	Scope      string `json:"scope"`
	Reason     string `json:"reason"`
	Enabled    bool   `json:"enabled"`
	Uses       int64  `json:"uses"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	CreatedAt  string `json:"created_at"`
	OptOuts    int    `json:"opt_outs"`
}

const linkCols = `l.id, l.app_id, a.slug, a.name, l.target_slug, l.scope, l.reason, l.enabled, l.uses, l.last_used_at, l.created_at,
	(SELECT COUNT(*) FROM app_link_optouts o WHERE o.link_id = l.id)`

func scanLink(sc interface{ Scan(...any) error }) (*AppLink, error) {
	var l AppLink
	var en int
	var used sql.NullString
	if err := sc.Scan(&l.ID, &l.AppID, &l.AppSlug, &l.AppName, &l.TargetSlug, &l.Scope, &l.Reason, &en, &l.Uses, &used, &l.CreatedAt, &l.OptOuts); err != nil {
		return nil, notFound(err)
	}
	l.Enabled, l.LastUsedAt = en == 1, nullStr(used)
	return &l, nil
}

func (s *Store) queryLinks(ctx context.Context, where string, args ...any) ([]AppLink, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+linkCols+` FROM app_links l JOIN apps a ON a.id = l.app_id `+where+` ORDER BY a.name, l.target_slug, l.scope`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// ListLinks returns every link on the box.
func (s *Store) ListLinks(ctx context.Context) ([]AppLink, error) { return s.queryLinks(ctx, "") }

// ListAppLinks returns the links an app declared.
func (s *Store) ListAppLinks(ctx context.Context, appID int64) ([]AppLink, error) {
	return s.queryLinks(ctx, "WHERE l.app_id = ?", appID)
}

// GetLink loads one link.
func (s *Store) GetLink(ctx context.Context, id int64) (*AppLink, error) {
	return scanLink(s.DB.QueryRowContext(ctx, `SELECT `+linkCols+` FROM app_links l JOIN apps a ON a.id = l.app_id WHERE l.id = ?`, id))
}

// FindLink loads the link for (app, target, scope).
func (s *Store) FindLink(ctx context.Context, appID int64, target, scope string) (*AppLink, error) {
	return scanLink(s.DB.QueryRowContext(ctx, `SELECT `+linkCols+` FROM app_links l JOIN apps a ON a.id = l.app_id WHERE l.app_id = ? AND l.target_slug = ? AND l.scope = ?`, appID, target, scope))
}

// LinkWant is one (target, scope) an app's manifest asks for.
type LinkWant struct {
	Target, Scope, Reason string
}

// SyncAppLinks makes the app's links match its manifest: new ones are
// created switched on, existing ones keep the admin's switch and the
// people's opt-outs, ones the manifest dropped are deleted.
func (s *Store) SyncAppLinks(ctx context.Context, appID int64, wants []LinkWant) error {
	keep := map[[2]string]bool{}
	for _, w := range wants {
		keep[[2]string{w.Target, w.Scope}] = true
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO app_links (app_id, target_slug, scope, reason) VALUES (?, ?, ?, ?)
			ON CONFLICT(app_id, target_slug, scope) DO UPDATE SET reason = excluded.reason`, appID, w.Target, w.Scope, w.Reason); err != nil {
			return err
		}
	}
	existing, err := s.ListAppLinks(ctx, appID)
	if err != nil {
		return err
	}
	for _, l := range existing {
		if !keep[[2]string{l.TargetSlug, l.Scope}] {
			if _, err := s.DB.ExecContext(ctx, `DELETE FROM app_links WHERE id = ?`, l.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetLinkEnabled flips the admin's box-wide switch.
func (s *Store) SetLinkEnabled(ctx context.Context, id int64, on bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE app_links SET enabled = ? WHERE id = ?`, boolInt(on), id)
	return err
}

// LinkOptedOut reports whether a person switched a link off.
func (s *Store) LinkOptedOut(ctx context.Context, linkID, userID int64) bool {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_link_optouts WHERE link_id = ? AND user_id = ?`, linkID, userID).Scan(&n)
	return n > 0
}

// SetLinkOptOut records (off) or clears (on) a person's own switch.
func (s *Store) SetLinkOptOut(ctx context.Context, linkID, userID int64, off bool) error {
	var err error
	if off {
		_, err = s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO app_link_optouts (link_id, user_id) VALUES (?, ?)`, linkID, userID)
	} else {
		_, err = s.DB.ExecContext(ctx, `DELETE FROM app_link_optouts WHERE link_id = ? AND user_id = ?`, linkID, userID)
	}
	return err
}

// TouchLinks counts one issued token against each link.
func (s *Store) TouchLinks(ctx context.Context, ids []int64) {
	for _, id := range ids {
		_, _ = s.DB.ExecContext(ctx, `UPDATE app_links SET uses = uses + 1, last_used_at = ? WHERE id = ?`, Now(), id)
	}
}

// LinkToken is an issued pcx_ token.
type LinkToken struct {
	AppID      int64
	TargetSlug string
	UserID     int64
	Scope      string // space-separated
	ExpiresAt  string
}

// CreateLinkToken stores a token hash and returns the plain token.
func (s *Store) CreateLinkToken(ctx context.Context, t *LinkToken, ttl time.Duration) (string, error) {
	plain := crypto.Token("pcx_", 32)
	t.ExpiresAt = Stamp(time.Now().Add(ttl))
	_, err := s.DB.ExecContext(ctx, `INSERT INTO link_tokens (token_hash, app_id, target_slug, user_id, scope, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		crypto.Hash(plain), t.AppID, t.TargetSlug, t.UserID, t.Scope, t.ExpiresAt)
	return plain, err
}

// GetLinkToken resolves a pcx_ token, ErrNotFound when expired or unknown.
func (s *Store) GetLinkToken(ctx context.Context, plain string) (*LinkToken, error) {
	var t LinkToken
	if err := s.DB.QueryRowContext(ctx, `SELECT app_id, target_slug, user_id, scope, expires_at FROM link_tokens WHERE token_hash = ?`, crypto.Hash(plain)).
		Scan(&t.AppID, &t.TargetSlug, &t.UserID, &t.Scope, &t.ExpiresAt); err != nil {
		return nil, notFound(err)
	}
	if ts, err := time.Parse(time.RFC3339Nano, t.ExpiresAt); err != nil || ts.Before(time.Now()) {
		return nil, ErrNotFound
	}
	return &t, nil
}
