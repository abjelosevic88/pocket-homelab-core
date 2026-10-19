package server

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/oidc"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

const (
	codeTTL    = 5 * time.Minute
	idTokenTTL = time.Hour
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
)

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, s.oidc.Discovery())
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, s.oidc.JWKS())
}

func oidcRedirectErr(w http.ResponseWriter, r *http.Request, redirect, state, code, desc string) {
	u, err := url.Parse(redirect)
	if err != nil || redirect == "" {
		http.Error(w, code+": "+desc, http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleAuthorize implements the authorization-code flow. GET without a
// session redirects to the SPA login with ?next=; the SPA posts the user back
// here. First-party clients get no consent screen: one box, one household.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
	}
	q := r.Form
	if r.Method == http.MethodGet {
		q = r.URL.Query()
	}
	clientID, redirect, state := q.Get("client_id"), q.Get("redirect_uri"), q.Get("state")
	cl, err := s.store.GetOIDCClient(r.Context(), clientID)
	if err != nil {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	}
	if !cl.AllowsRedirect(redirect) {
		http.Error(w, "redirect_uri not registered for this client", http.StatusBadRequest)
		return
	}
	if q.Get("response_type") != "code" {
		oidcRedirectErr(w, r, redirect, state, "unsupported_response_type", "only code is supported")
		return
	}
	scope := q.Get("scope")
	if !strings.Contains(" "+scope+" ", " openid ") {
		oidcRedirectErr(w, r, redirect, state, "invalid_scope", "openid scope required")
		return
	}
	challenge, method := q.Get("code_challenge"), q.Get("code_challenge_method")
	if cl.Public() && challenge == "" {
		oidcRedirectErr(w, r, redirect, state, "invalid_request", "public clients must use PKCE")
		return
	}
	if challenge != "" && strings.ToUpper(method) == "PLAIN" {
		challenge = "plain:" + challenge
	}
	u := s.sessionUser(r)
	if u == nil {
		if q.Get("prompt") == "none" {
			oidcRedirectErr(w, r, redirect, state, "login_required", "no session")
			return
		}
		next := "/oidc/authorize?" + r.URL.RawQuery
		if r.Method == http.MethodPost {
			next = "/oidc/authorize?" + q.Encode()
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	if !s.clientAdmits(r.Context(), cl, u) {
		oidcRedirectErr(w, r, redirect, state, "access_denied", "this app is not shared with you")
		return
	}
	code := &store.OIDCCode{ClientID: cl.ClientID, UserID: u.ID, RedirectURI: redirect, Scope: scope, Nonce: q.Get("nonce"), CodeChallenge: challenge}
	if err := s.store.CreateOIDCCode(r.Context(), code, codeTTL); err != nil {
		oidcRedirectErr(w, r, redirect, state, "server_error", err.Error())
		return
	}
	ru, _ := url.Parse(redirect)
	rq := ru.Query()
	rq.Set("code", code.Code)
	if state != "" {
		rq.Set("state", state)
	}
	rq.Set("iss", s.oidc.Issuer)
	ru.RawQuery = rq.Encode()
	http.Redirect(w, r, ru.String(), http.StatusFound)
}

func tokenErr(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// clientAuth resolves the relying party from Basic auth or the form body.
func (s *Server) clientAuth(r *http.Request) (*store.OIDCClient, bool) {
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if id == "" {
		return nil, false
	}
	cl, err := s.store.GetOIDCClient(r.Context(), id)
	if err != nil {
		return nil, false
	}
	if cl.Public() {
		return cl, secret == ""
	}
	return cl, secret != "" && cl.SecretHash == crypto.Hash(secret)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenErr(w, 400, "invalid_request", "form body required")
		return
	}
	cl, ok := s.clientAuth(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="pocket-core"`)
		tokenErr(w, 401, "invalid_client", "client authentication failed")
		return
	}
	ctx := r.Context()
	var userID int64
	var scope, nonce string
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		c, err := s.store.ConsumeOIDCCode(ctx, r.PostFormValue("code"))
		if err != nil || c.ClientID != cl.ClientID {
			tokenErr(w, 400, "invalid_grant", "code invalid or expired")
			return
		}
		if ru := r.PostFormValue("redirect_uri"); ru != "" && ru != c.RedirectURI {
			tokenErr(w, 400, "invalid_grant", "redirect_uri mismatch")
			return
		}
		ch, method := c.CodeChallenge, "S256"
		if strings.HasPrefix(ch, "plain:") {
			ch, method = strings.TrimPrefix(ch, "plain:"), "plain"
		}
		if !oidc.PKCEOK(ch, method, r.PostFormValue("code_verifier")) {
			tokenErr(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		userID, scope, nonce = c.UserID, c.Scope, c.Nonce
	case "refresh_token":
		rt := r.PostFormValue("refresh_token")
		t, err := s.store.GetOIDCToken(ctx, rt)
		if err != nil || t.Kind != "refresh" || t.ClientID != cl.ClientID {
			tokenErr(w, 400, "invalid_grant", "refresh token invalid")
			return
		}
		_ = s.store.DeleteOIDCToken(ctx, rt)
		userID, scope = t.UserID, t.Scope
	default:
		tokenErr(w, 400, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	u, err := s.store.GetUser(ctx, userID)
	if err != nil || u.Disabled {
		tokenErr(w, 400, "invalid_grant", "user gone")
		return
	}
	if !s.clientAdmits(ctx, cl, u) { // the audience may have changed since the code or refresh token was issued
		tokenErr(w, 400, "invalid_grant", "this app is not shared with you")
		return
	}
	access, err := s.store.CreateOIDCToken(ctx, &store.OIDCToken{Kind: "access", ClientID: cl.ClientID, UserID: u.ID, Scope: scope}, accessTTL)
	if err != nil {
		tokenErr(w, 500, "server_error", err.Error())
		return
	}
	idTok, err := s.oidc.IDToken(u, cl.ClientID, nonce, idTokenTTL, s.clientClaims(ctx, cl, u))
	if err != nil {
		tokenErr(w, 500, "server_error", err.Error())
		return
	}
	out := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()), "id_token": idTok, "scope": scope}
	// Refresh tokens are always issued: one box, one household, and the apps
	// that log in here expect them regardless of offline_access.
	if rt, err := s.store.CreateOIDCToken(ctx, &store.OIDCToken{Kind: "refresh", ClientID: cl.ClientID, UserID: u.ID, Scope: scope}, refreshTTL); err == nil {
		out["refresh_token"] = rt
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUserinfo(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="pocket-core"`)
		writeErr(w, http.StatusUnauthorized, "bearer token required")
		return
	}
	p, err := s.resolveToken(r.Context(), tok, "")
	if err != nil || p == nil || p.User == nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	claims := oidc.Claims(p.User)
	if t, err := s.store.GetOIDCToken(r.Context(), tok); err == nil && t.Kind == "access" {
		if cl, err := s.store.GetOIDCClient(r.Context(), t.ClientID); err == nil {
			for k, v := range s.clientClaims(r.Context(), cl, p.User) {
				if _, std := claims[k]; !std {
					claims[k] = v
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, claims)
}

// clientClaims are the claims that depend on which app is signing in. For a
// connector's client: pocket_uid = the app account Core linked the person to
// (Nextcloud matches sign-ins on it). No link, no claim: the app refuses the
// sign-in instead of guessing an account by name.
func (s *Server) clientClaims(ctx context.Context, cl *store.OIDCClient, u *store.User) map[string]any {
	if cl == nil || cl.ServiceID == 0 || u == nil {
		return nil
	}
	a, err := s.store.GetConnectorAccount(ctx, cl.ServiceID, u.ID)
	if err != nil || a.RemoteID == "" || a.Status == connectors.StatusError {
		return nil
	}
	return map[string]any{"pocket_uid": a.RemoteID}
}

// handleEndSession clears the browser session and bounces to the RP.
func (s *Server) handleEndSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	s.setSessionCookie(w, "", -1)
	to := r.URL.Query().Get("post_logout_redirect_uri")
	if to != "" {
		if u, err := url.Parse(to); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			if st := r.URL.Query().Get("state"); st != "" {
				q := u.Query()
				q.Set("state", st)
				u.RawQuery = q.Encode()
			}
			http.Redirect(w, r, u.String(), http.StatusFound)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Signed out</title><p style="font-family:system-ui;padding:2em">Signed out of ` + html.EscapeString(s.cfg.OIDCIssuer) + `. <a href="/">Back to Pocket Core</a></p>`))
}

// ---- clients (admin) ----

func (s *Server) handleListOIDCClients(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListOIDCClients(r.Context())
	if err != nil {
		s.fail(w, err, "list clients")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type oidcClientIn struct {
	ClientID     *string   `json:"client_id"`
	Name         *string   `json:"name"`
	RedirectURIs *[]string `json:"redirect_uris"`
	Public       *bool     `json:"public"`
	RotateSecret bool      `json:"rotate_secret"`
}

func (s *Server) handleCreateOIDCClient(w http.ResponseWriter, r *http.Request) {
	var in oidcClientIn
	if err := decode(r, &in); err != nil || in.Name == nil || in.RedirectURIs == nil {
		writeErr(w, http.StatusBadRequest, "name and redirect_uris required")
		return
	}
	cl := &store.OIDCClient{Name: strings.TrimSpace(*in.Name), RedirectURIs: cleanURIs(*in.RedirectURIs)}
	if in.ClientID != nil && strings.TrimSpace(*in.ClientID) != "" {
		cl.ClientID = strings.TrimSpace(*in.ClientID)
	}
	newSecret := in.Public == nil || !*in.Public
	secret, err := s.store.SaveOIDCClient(r.Context(), cl, newSecret)
	if err != nil {
		s.fail(w, err, "create client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"client": cl, "client_secret": secret, "issuer": s.oidc.Issuer})
}

func cleanURIs(in []string) []string {
	out := []string{}
	for _, u := range in {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func (s *Server) handleUpdateOIDCClient(w http.ResponseWriter, r *http.Request) {
	cl, err := s.store.GetOIDCClientByID(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get client")
		return
	}
	var in oidcClientIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Name != nil {
		cl.Name = strings.TrimSpace(*in.Name)
	}
	if in.RedirectURIs != nil {
		cl.RedirectURIs = cleanURIs(*in.RedirectURIs)
	}
	secret, err := s.store.SaveOIDCClient(r.Context(), cl, in.RotateSecret)
	if err != nil {
		s.fail(w, err, "update client")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client": cl, "client_secret": secret})
}

func (s *Server) handleDeleteOIDCClient(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteOIDCClient(r.Context(), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete client")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
