package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

type ctxKey int

const principalKey ctxKey = 1

const sessionCookie = "pc_session"

// Principal is whoever is calling: a user (session, personal token, OIDC
// access token), a registered app (pca_) or a paired device (pcd_).
type Principal struct {
	Kind   string        `json:"kind"` // user | app | device
	User   *store.User   `json:"user,omitempty"`
	App    *store.App    `json:"app,omitempty"`
	Device *store.Device `json:"device,omitempty"`
	Scope  string        `json:"scope,omitempty"`
}

// IsAdmin reports whether the caller may manage the box. Apps are trusted
// for registry/event work but not for user or service management.
func (p *Principal) IsAdmin() bool { return p != nil && p.User != nil && p.User.IsAdmin() }

// UserID returns the owning user id (devices belong to a user; apps don't).
func (p *Principal) UserID() int64 {
	switch {
	case p == nil:
		return 0
	case p.User != nil:
		return p.User.ID
	case p.Device != nil:
		return p.Device.UserID
	}
	return 0
}

// Label is a short name for event sources and logs.
func (p *Principal) Label() string {
	switch {
	case p == nil:
		return ""
	case p.App != nil:
		return p.App.Slug
	case p.Device != nil:
		return "device:" + p.Device.Name
	case p.User != nil:
		return p.User.Email
	}
	return ""
}

func principalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

func userFrom(ctx context.Context) *store.User {
	if p := principalFrom(ctx); p != nil {
		return p.User
	}
	return nil
}

// scopeUser returns 0 for admins (see everything) or the caller's user id.
func (s *Server) scopeUser(r *http.Request) int64 {
	p := principalFrom(r.Context())
	if p.IsAdmin() || p.App != nil {
		return 0
	}
	return p.UserID()
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.resolve(r)
		if err != nil || p == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="pocket-core"`)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

func (s *Server) requireAdminOrApp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		if !p.IsAdmin() && p.App == nil {
			writeErr(w, http.StatusForbidden, "admins and apps only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principalFrom(r.Context()).IsAdmin() {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer extracts a token from Authorization: Bearer or X-API-Key.
func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// resolveToken identifies any Core-issued token by prefix.
func (s *Server) resolveToken(ctx context.Context, tok, ip string) (*Principal, error) {
	switch {
	case strings.HasPrefix(tok, "pcx_"):
		// Link tokens are for the target app only, never for Core's own API.
		return nil, store.ErrNotFound
	case strings.HasPrefix(tok, "pca_"):
		a, err := s.store.GetAppByToken(ctx, tok)
		if err != nil {
			return nil, err
		}
		return &Principal{Kind: "app", App: a}, nil
	case strings.HasPrefix(tok, "pcd_"):
		d, err := s.store.GetDeviceByToken(ctx, tok, ip)
		if err != nil {
			return nil, err
		}
		u, err := s.store.GetUser(ctx, d.UserID)
		if err != nil || u.Disabled {
			return nil, store.ErrNotFound
		}
		return &Principal{Kind: "device", Device: d, User: u}, nil
	case strings.HasPrefix(tok, "pco_"):
		t, err := s.store.GetOIDCToken(ctx, tok)
		if err != nil {
			return nil, err
		}
		u, err := s.store.GetUser(ctx, t.UserID)
		if err != nil || u.Disabled {
			return nil, store.ErrNotFound
		}
		return &Principal{Kind: "user", User: u, Scope: t.Scope}, nil
	default:
		u, err := s.store.GetUserByAPIToken(ctx, tok)
		if err != nil {
			return nil, err
		}
		return &Principal{Kind: "user", User: u}, nil
	}
}

func (s *Server) resolve(r *http.Request) (*Principal, error) {
	ctx := r.Context()
	if tok := bearer(r); tok != "" {
		return s.resolveToken(ctx, tok, r.RemoteAddr)
	}
	switch s.cfg.AuthMode {
	case "none":
		u, err := s.firstOrSystemUser(ctx, "admin@localhost", "Admin")
		if err != nil {
			return nil, err
		}
		return &Principal{Kind: "user", User: u}, nil
	case "proxy":
		name := r.Header.Get(s.cfg.AuthProxyHeader)
		if name == "" {
			return nil, http.ErrNoCookie
		}
		email := r.Header.Get(s.cfg.AuthProxyEmailHeader)
		if email == "" {
			email = name
			if !strings.Contains(email, "@") {
				email += "@proxy"
			}
		}
		u, err := s.firstOrSystemUser(ctx, strings.ToLower(email), name)
		if err != nil {
			return nil, err
		}
		return &Principal{Kind: "user", User: u}, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, err
	}
	u, err := s.store.GetSessionUser(ctx, c.Value)
	if err != nil {
		return nil, err
	}
	return &Principal{Kind: "user", User: u}, nil
}

// sessionUser is the browser user only (for the OIDC authorize page).
func (s *Server) sessionUser(r *http.Request) *store.User {
	if s.cfg.AuthMode != "local" {
		if p, err := s.resolve(r); err == nil && p != nil && p.Kind == "user" {
			return p.User
		}
		return nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	u, err := s.store.GetSessionUser(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return u
}

func (s *Server) firstOrSystemUser(ctx context.Context, email, name string) (*store.User, error) {
	if u, err := s.store.GetUserByEmail(ctx, email); err == nil {
		return u, nil
	}
	role := "member"
	if n, _ := s.store.CountUsers(ctx); n == 0 {
		role = "admin"
	}
	u := &store.User{Email: email, Name: name, Role: role}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	s.log.Info("auto-provisioned user", "email", email, "mode", s.cfg.AuthMode, "role", role)
	return u, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, id string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/", HttpOnly: true,
		Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.fail(w, err, "count users")
		return
	}
	st, _ := s.store.GetSettings(r.Context())
	p, _ := s.resolve(r)
	var u *store.User
	if p != nil {
		u = p.User
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": n == 0 && s.cfg.AuthMode == "local", "auth_mode": s.cfg.AuthMode,
		"authenticated": p != nil, "user": u, "version": Version, "title": st.Title, "box_name": st.BoxName,
		"sso": s.ssoEnabled() && s.cfg.AuthMode == "local", "sso_name": s.signin().Name, "password_login": s.signin().PasswordLogin || s.cfg.AuthMode != "local",
		"cookie_warning": s.cookieWarning(r),
	})
}

// cookieWarning explains up front why a sign-in would bounce: a Secure session
// cookie on a page the browser loaded over plain http is silently dropped.
func (s *Server) cookieWarning(r *http.Request) string {
	if s.cfg.AuthMode != "local" || !s.cfg.SecureCookies || requestIsHTTPS(r, s.cfg.TrustProxy) {
		return ""
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if config.IsLoopback(host) {
		return ""
	}
	return "This page was opened over plain http, but Core is set to SECURE_COOKIES=true, so your browser will not keep the sign-in. Open Core over https, or set SECURE_COOKIES=false and restart it."
}

func requestIsHTTPS(r *http.Request, trustProxy bool) bool {
	if r.TLS != nil {
		return true
	}
	if !trustProxy {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		s.fail(w, err, "count users")
		return
	}
	if n > 0 || s.cfg.AuthMode != "local" {
		writeErr(w, http.StatusForbidden, "setup already completed")
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
		BoxName  string `json:"box_name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" || len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "email and a password of at least 8 characters are required")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	u := &store.User{Email: in.Email, Name: in.Name, PasswordHash: string(hash), Role: "admin"}
	if err := s.store.CreateUser(ctx, u); err != nil {
		s.fail(w, err, "create user")
		return
	}
	st, _ := s.store.GetSettings(ctx)
	st.SetupComplete = true
	if strings.TrimSpace(in.BoxName) != "" {
		st.BoxName = strings.TrimSpace(in.BoxName)
	}
	_ = s.store.SaveSettings(ctx, st)
	id, err := s.store.CreateSession(ctx, u.ID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	s.setSessionCookie(w, id, s.cfg.SessionTTL)
	s.log.Info("initial setup completed", "email", u.Email)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !s.signin().PasswordLogin {
		writeErr(w, http.StatusForbidden, "password sign-in is switched off — use "+s.signin().Name)
		return
	}
	email := strings.TrimSpace(strings.ToLower(in.Email))
	ip := clientIP(r)
	if s.loginLimit.blocked(ip) || s.loginLimit.blocked("email:"+email) {
		writeErr(w, http.StatusTooManyRequests, "too many failed sign-ins; try again in 15 minutes")
		return
	}
	u, err := s.store.GetUserByEmail(r.Context(), email)
	if err != nil || u.Disabled || u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		s.loginLimit.fail(ip)
		s.loginLimit.fail("email:" + email)
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	s.loginLimit.clear(ip)
	s.loginLimit.clear("email:" + email)
	id, err := s.store.CreateSession(r.Context(), u.ID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	s.setSessionCookie(w, id, s.cfg.SessionTTL)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	s.setSessionCookie(w, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principalFrom(r.Context()))
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusForbidden, "not a user")
		return
	}
	var in struct {
		Name            string `json:"name"`
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Name != "" {
		u.Name = in.Name
	}
	if in.Password != "" {
		if len(in.Password) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		if u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.CurrentPassword)) != nil {
			writeErr(w, http.StatusForbidden, "current password is wrong")
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err, "update user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// handleIntrospect lets a Pocket app validate any Core-issued token: device
// tokens from paired phones, personal tokens, OIDC access tokens. Apps call
// this (with their own pca_ token) instead of keeping their own user list.
// appAdmits reports whether a person may use a registered app: the audience of
// the service mirroring that app decides. Apps without a mirrored service admit everyone.
func (s *Server) appAdmits(ctx context.Context, appID int64, u *store.User) bool {
	if appID == 0 {
		return true
	}
	sv, err := s.store.GetServiceByApp(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	return err == nil && sv.InAudience(u)
}

// clientAdmits extends appAdmits to every OIDC client: a registered app's
// client follows the app's service; a client a connector made for a service
// follows that service's audience; hand-made clients admit everyone.
func (s *Server) clientAdmits(ctx context.Context, cl *store.OIDCClient, u *store.User) bool {
	if cl.AppID > 0 && !s.appAdmits(ctx, cl.AppID, u) {
		return false
	}
	if cl.ServiceID > 0 {
		sv, err := s.store.GetService(ctx, cl.ServiceID)
		if err != nil || !sv.InAudience(u) {
			return false
		}
	}
	return true
}

func (s *Server) handleIntrospect(w http.ResponseWriter, r *http.Request) {
	caller := principalFrom(r.Context())
	if caller.App == nil && !caller.IsAdmin() {
		writeErr(w, http.StatusForbidden, "apps and admins only")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(r, &in); err != nil || in.Token == "" {
		writeErr(w, http.StatusBadRequest, "token required")
		return
	}
	if strings.HasPrefix(in.Token, "pcu_mcp_") {
		s.introspectMCPAppToken(w, r, caller, in.Token)
		return
	}
	if strings.HasPrefix(in.Token, "pcx_") {
		s.introspectLinkToken(w, r, caller, in.Token)
		return
	}
	p, err := s.resolveToken(r.Context(), in.Token, "")
	if err != nil || p == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	// An app only accepts people its service's audience includes.
	if caller.App != nil && p.User != nil && !s.appAdmits(r.Context(), caller.App.ID, p.User) {
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "reason": "not_in_audience"})
		return
	}
	out := map[string]any{"active": true, "kind": p.Kind, "scope": p.Scope, "iss": s.oidc.Issuer}
	if strings.HasPrefix(in.Token, "pco_") {
		if tok, err := s.store.GetOIDCToken(r.Context(), in.Token); err == nil {
			out["expires_at"] = tok.ExpiresAt
		}
	}
	if p.User != nil {
		out["user"] = map[string]any{"id": p.User.ID, "sub": p.User.Subject, "auth_revision": p.User.AuthRevision, "email": p.User.Email, "name": p.User.Name, "role": p.User.Role}
	}
	if p.Device != nil {
		out["device"] = map[string]any{"id": p.Device.ID, "name": p.Device.Name, "platform": p.Device.Platform}
	}
	if p.App != nil {
		out["app"] = map[string]any{"id": p.App.ID, "slug": p.App.Slug, "name": p.App.Name}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- users (admin) ----

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, "list users")
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

// handlePeople lists the household for apps' share pickers: name and email
// of every active account, nothing else.
func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, "list people")
		return
	}
	type person struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	out := []person{}
	for _, u := range users {
		if !u.Disabled {
			out = append(out, person{Email: u.Email, Name: u.Name})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" {
		writeErr(w, http.StatusBadRequest, "email required")
		return
	}
	u := &store.User{Email: in.Email, Name: in.Name, Role: in.Role}
	if in.Password != "" {
		if len(in.Password) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if u.Role != "admin" {
		u.Role = "member"
	}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		s.fail(w, err, "create user")
		return
	}
	s.provisionNewUser(u.ID)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUser(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	var in struct {
		Email    *string `json:"email"`
		Name     *string `json:"name"`
		Password *string `json:"password"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Email != nil && strings.TrimSpace(*in.Email) != "" {
		u.Email = strings.TrimSpace(strings.ToLower(*in.Email))
	}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if in.Password != nil && *in.Password != "" {
		if len(*in.Password) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	me := userFrom(r.Context())
	if in.Role != nil && (*in.Role == "admin" || *in.Role == "member") {
		if me != nil && me.ID == u.ID && *in.Role != "admin" {
			writeErr(w, http.StatusBadRequest, "you cannot demote yourself")
			return
		}
		u.Role = *in.Role
	}
	if in.Disabled != nil {
		if me != nil && me.ID == u.ID && *in.Disabled {
			writeErr(w, http.StatusBadRequest, "you cannot disable yourself")
			return
		}
		u.Disabled = *in.Disabled
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err, "update user")
		return
	}
	s.kickAllHooks() // disabled or role (audience) may have changed
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := idParam(r, "id")
	if me := userFrom(r.Context()); me != nil && me.ID == id {
		writeErr(w, http.StatusBadRequest, "you cannot delete yourself")
		return
	}
	s.dropUserHooks(r.Context(), id, 0) // while their key still exists
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.fail(w, err, "delete user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- personal tokens ----

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusForbidden, "not a user")
		return
	}
	t, err := s.store.ListAPITokens(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err, "list tokens")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusForbidden, "not a user")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = decode(r, &in)
	if in.Name == "" {
		in.Name = "token"
	}
	t, plain, err := s.store.CreateAPIToken(r.Context(), u.ID, in.Name)
	if err != nil {
		s.fail(w, err, "create token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": plain})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusForbidden, "not a user")
		return
	}
	if err := s.store.DeleteAPIToken(r.Context(), u.ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "delete token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- settings ----

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	var in struct {
		Title   *string `json:"title"`
		BoxName *string `json:"box_name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		st.Title = strings.TrimSpace(*in.Title)
	}
	if in.BoxName != nil && strings.TrimSpace(*in.BoxName) != "" {
		st.BoxName = strings.TrimSpace(*in.BoxName)
	}
	if err := s.store.SaveSettings(r.Context(), st); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleRotateRegistrationKey(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	st.RegistrationKey = crypto.Token("pcreg_", 24)
	if err := s.store.SaveSettings(r.Context(), st); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSubject checks an existing app session against current membership.
// Apps receive only subjects admitted to their own audience.
func (s *Server) handleSubject(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.App == nil {
		writeErr(w, 403, "app only")
		return
	}
	var in struct {
		Subject  string `json:"sub"`
		Revision int64  `json:"auth_revision"`
	}
	if decode(r, &in) != nil || in.Subject == "" {
		writeErr(w, 400, "subject required")
		return
	}
	u, err := s.store.GetUserBySubject(r.Context(), in.Subject)
	active := err == nil && !u.Disabled && u.AuthRevision == in.Revision && s.appAdmits(r.Context(), p.App.ID, u)
	writeJSON(w, 200, map[string]any{"active": active, "iss": s.oidc.Issuer})
}
