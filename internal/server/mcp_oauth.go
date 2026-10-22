package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/go-chi/chi/v5"
	"golang.org/x/time/rate"
)

const mcpOAuthScope = "status:read"

func (s *Server) mcpResource() string { return strings.TrimRight(s.cfg.BaseURL, "/") + "/mcp" }
func (s *Server) mcpIssuer() string   { return s.mcpResource() + "/oauth" }
func (s *Server) mcpChallenge(w http.ResponseWriter) {
	challenge := `Bearer realm="pocket-status"`
	if s.cfg.MCPOAuthEnabled {
		challenge += `, resource_metadata="` + strings.TrimRight(s.cfg.BaseURL, "/") + `/.well-known/oauth-protected-resource/mcp", scope="status:read"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
}

func (s *Server) mcpOAuthRoutes(r chi.Router) {
	if !s.cfg.MCPOAuthEnabled {
		return
	}
	r.Get("/mcp-consent.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(mcpConsentCSS))
	})
	r.Get("/mcp-completion.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(mcpCompletionJS))
	})
	r.Get("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, 200, map[string]any{"resource": s.mcpResource(), "resource_name": "Pocket Core", "authorization_servers": []string{s.mcpIssuer()}, "scopes_supported": s.mcpSupportedScopes(), "bearer_methods_supported": []string{"header"}})
	})
	discovery := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, 200, map[string]any{"issuer": s.mcpIssuer(), "authorization_endpoint": s.mcpIssuer() + "/authorize", "token_endpoint": s.mcpIssuer() + "/token", "registration_endpoint": s.mcpIssuer() + "/register", "revocation_endpoint": s.mcpIssuer() + "/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"}, "scopes_supported": s.mcpSupportedScopes(), "authorization_response_iss_parameter_supported": true})
	}
	r.Get("/.well-known/oauth-authorization-server/mcp/oauth", discovery)
	r.Get("/mcp/oauth/.well-known/oauth-authorization-server", discovery)
	// Registration is unauthenticated by design, bounded independently of MCP calls.
	registrationLimit := rate.NewLimiter(rate.Every(10*time.Second), 10)
	oauthLimit := rate.NewLimiter(rate.Every(time.Second/5), 30)
	r.Route("/mcp/oauth", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Referrer-Policy", "no-referrer")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				if !oauthLimit.Allow() {
					w.Header().Set("Retry-After", "1")
					tokenErr(w, 429, "temporarily_unavailable", "retry shortly")
					return
				}
				_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
				defer http.NewResponseController(w).SetReadDeadline(time.Time{})
				r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
				next.ServeHTTP(w, r)
			})
		})
		r.Post("/register", func(w http.ResponseWriter, r *http.Request) {
			if !registrationLimit.Allow() {
				w.Header().Set("Retry-After", "10")
				tokenErr(w, 429, "temporarily_unavailable", "registration limit reached")
				return
			}
			s.handleMCPRegister(w, r)
		})
		r.Get("/authorize", s.handleMCPAuthorize)
		r.Post("/authorize", s.handleMCPConsent)
		r.Post("/token", s.handleMCPToken)
		r.Post("/revoke", s.handleMCPRevokeToken)
	})
}

var mcpCallbackHostRE = regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]+$`)

func validMCPRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || !mcpCallbackHostRE.MatchString(u.Host) || u.User != nil || u.Fragment != "" || len(raw) > 2048 {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")
}

func (s *Server) handleMCPRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string   `json:"client_name"`
		Redirects  []string `json:"redirect_uris"`
		AuthMethod string   `json:"token_endpoint_auth_method"`
		Grants     []string `json:"grant_types"`
		Responses  []string `json:"response_types"`
		Scope      string   `json:"scope"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Redirects) == 0 || len(in.Redirects) > 10 || len(in.Name) > 120 {
		tokenErr(w, 400, "invalid_client_metadata", "supply a client name and 1–10 redirect URIs")
		return
	}
	for _, uri := range in.Redirects {
		if !validMCPRedirect(uri) {
			tokenErr(w, 400, "invalid_redirect_uri", "use HTTPS or a literal loopback HTTP callback")
			return
		}
	}
	for _, grant := range in.Grants {
		if grant != "authorization_code" && grant != "refresh_token" {
			tokenErr(w, 400, "invalid_client_metadata", "unsupported grant type")
			return
		}
	}
	for _, response := range in.Responses {
		if response != "code" {
			tokenErr(w, 400, "invalid_client_metadata", "only code responses are supported")
			return
		}
	}
	if in.Scope != "" && !s.mcpValidScopes(in.Scope) {
		tokenErr(w, 400, "invalid_scope", "unsupported scope; request the scopes advertised in discovery")
		return
	}
	if in.AuthMethod == "" {
		in.AuthMethod = "client_secret_basic"
	}
	if in.AuthMethod != "none" && in.AuthMethod != "client_secret_basic" && in.AuthMethod != "client_secret_post" {
		tokenErr(w, 400, "invalid_client_metadata", "unsupported client authentication method")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "AI assistant"
	}
	c := &store.MCPOAuthClient{Name: in.Name, AuthMethod: in.AuthMethod, RedirectURIs: in.Redirects}
	secret := ""
	if c.AuthMethod != "none" {
		secret = crypto.Token("mcps_", 32)
		c.SecretHash = crypto.Hash(secret)
	}
	if err := s.store.CreateMCPOAuthClient(r.Context(), c); err != nil {
		tokenErr(w, 503, "temporarily_unavailable", "client registration unavailable")
		return
	}
	out := map[string]any{"client_id": c.ClientID, "client_name": c.Name, "redirect_uris": c.RedirectURIs, "token_endpoint_auth_method": c.AuthMethod, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "scope": strings.Join(s.mcpSupportedScopes(), " "), "client_id_issued_at": time.Now().Unix()}
	if secret != "" {
		out["client_secret"] = secret
		out["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, out)
}

type mcpAuthorization struct {
	Redirect  string `json:"redirect"`
	State     string `json:"state"`
	Challenge string `json:"challenge"`
	Resource  string `json:"resource"`
	Scope     string `json:"scope,omitempty"`
	Target    string `json:"target,omitempty"`
}

var mcpChallengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var mcpVerifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

var mcpConsentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect your assistant · Pocket Core</title><link rel="stylesheet" href="/mcp-consent.css"></head>
<body><main><p>Pocket Core</p><h1>Connect {{.Name}}?</h1>
<p>Signed in as <strong>{{.Email}}</strong>.</p>
<p>This assistant can identify your Core account (name, email, account identifiers and role) and see this connection's permissions.</p>
<p>This assistant will be able to check Core's availability and the status of Pocket apps shared with your account, including apps shared with you later.</p>
{{if .Write}}<p><strong>It can read your application content and create or update items in the Pocket apps shared with you.</strong> This includes personal notes, tasks, events and other supported app records. Deletion, sharing controls, sending messages and service administration are excluded.</p>
{{else if .Read}}<p><strong>It can read your application content</strong> in supported Pocket apps shared with you. It cannot create or change items.</p>
{{else}}<p>It cannot read notes, tasks or other application content, or change your data.</p>{{end}}
<p>The connection lasts up to 30 days. You can disconnect it at any time under <a href="/ai-connections">AI connections</a>.</p>
<p>Client names are supplied by the application. Approve only if you started this connection.</p>
{{if .Terminal}}<p><strong>Connecting from a terminal?</strong> Approve and copy the sign-in response back into Claude Code or Codex. This page stays on Core.</p>{{else}}<p>Return address: <code>{{.Redirect}}</code></p>{{end}}
<form method="post" action="/mcp/oauth/authorize"><input type="hidden" name="request" value="{{.Request}}">
{{if .Terminal}}<button class="primary" name="decision" value="approve_manual">Approve and copy sign-in response</button><button name="decision" value="deny_manual">Cancel</button>
<details><summary>Return directly to an app on this computer</summary><p>Use this when your assistant is waiting for a local browser callback.</p><button name="decision" value="approve">{{if .Read}}Allow application access{{else}}Allow status access{{end}}</button><p>Return address: <code>{{.Redirect}}</code></p></details>
{{else}}<button class="primary" name="decision" value="approve">{{if .Read}}Allow application access{{else}}Allow status access{{end}}</button><button name="decision" value="deny">Cancel</button>{{end}}
</form></main></body></html>`))

func mcpTerminalCallback(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")
}

var mcpCompletionPage = template.Must(template.New("completion").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Finish connecting · Pocket Core</title><link rel="stylesheet" href="/mcp-consent.css"><script src="/mcp-completion.js" defer></script></head>
<body><main><p>Pocket Core</p>{{if .Cancelled}}<h1>Connection cancelled</h1><p>No access was granted. Return to your terminal and press Ctrl+C to cancel sign-in.</p>
{{else}}<h1>Finish in your terminal</h1><p>Access approved. Copy this sign-in response and paste it into the waiting Claude Code or Codex terminal.</p>
<label for="sign-in-response">Sign-in response</label><textarea id="sign-in-response" rows="5" readonly spellcheck="false" autocomplete="off">{{.Response}}</textarea>
<button id="copy-response" class="primary" type="button">Copy sign-in response</button><p id="copy-status" role="status" aria-live="polite"></p>
<p>Paste the entire response as text; you do not need to open its address. It expires in five minutes and can only be used by the terminal that started this sign-in.</p>
<p>Your assistant will save the connection and manage its credentials. No separate token setup is needed.</p>{{end}}
<p><a href="/ai-connections">Back to AI connections</a></p></main></body></html>`))

const mcpCompletionJS = `const field = document.getElementById('sign-in-response');
const button = document.getElementById('copy-response');
if (field && button) {
 field.addEventListener('focus', () => field.select());
 button.addEventListener('click', async () => {
  const status = document.getElementById('copy-status');
  try { await navigator.clipboard.writeText(field.value); button.textContent = 'Copied'; status.textContent = 'Now paste it into your terminal.'; }
  catch { field.focus(); field.select(); status.textContent = 'Copy the selected response, then paste it into your terminal.'; }
 });
}`

func (s *Server) mcpShowCompletion(w http.ResponseWriter, response string, cancelled bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = mcpCompletionPage.Execute(w, map[string]any{"Response": response, "Cancelled": cancelled})
}

func (s *Server) handleMCPAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := s.store.GetMCPOAuthClient(r.Context(), q.Get("client_id"))
	if err != nil {
		tokenErr(w, 400, "invalid_request", "unknown client")
		return
	}
	redirect := q.Get("redirect_uri")
	allowed := false
	for _, uri := range c.RedirectURIs {
		if uri == redirect {
			allowed = true
		}
	}
	if !allowed {
		tokenErr(w, 400, "invalid_request", "redirect URI is not registered")
		return
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || !mcpChallengeRE.MatchString(q.Get("code_challenge")) || len(q.Get("state")) > 2048 {
		s.mcpRedirectErr(w, r, redirect, q.Get("state"), "invalid_request", "authorization code with S256 PKCE is required")
		return
	}
	if q.Get("resource") != s.mcpResource() {
		s.mcpRedirectErr(w, r, redirect, q.Get("state"), "invalid_target", "resource must be this Core's MCP URL")
		return
	}
	if !s.mcpValidScopes(q.Get("scope")) {
		s.mcpRedirectErr(w, r, redirect, q.Get("state"), "invalid_scope", "request supported scopes; apps:write also requires apps:read")
		return
	}
	u := s.sessionUser(r)
	if u == nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape("/mcp/oauth/authorize?"+q.Encode()), 302)
		return
	}
	if u.Disabled {
		tokenErr(w, 403, "access_denied", "account unavailable")
		return
	}
	pending, err := s.store.CreateMCPConsent(r.Context(), c.ClientID, u.ID, mcpAuthorization{Redirect: redirect, State: q.Get("state"), Challenge: q.Get("code_challenge"), Resource: s.mcpResource(), Scope: canonicalMCPScopes(q.Get("scope"))})
	if err != nil {
		tokenErr(w, 503, "temporarily_unavailable", "authorization unavailable")
		return
	}
	// Keep the same-origin POST's Origin header available for CSRF validation,
	// while withholding the authorization URL from the external callback.
	w.Header().Set("Referrer-Policy", "same-origin")
	// Chromium also applies form-action to the final OAuth redirect.
	callback, _ := url.Parse(redirect)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self' "+callback.Scheme+"://"+callback.Host+"; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = mcpConsentPage.Execute(w, map[string]any{"Name": c.Name, "Email": u.Email, "Redirect": redirect, "Request": pending, "Read": hasMCPScope(q.Get("scope"), "apps:read"), "Write": hasMCPScope(q.Get("scope"), "apps:write"), "Terminal": mcpTerminalCallback(redirect)})
}

func (s *Server) handleMCPConsent(w http.ResponseWriter, r *http.Request) {
	u := s.sessionUser(r)
	base, _ := url.Parse(s.cfg.BaseURL)
	if u == nil || u.Disabled {
		tokenErr(w, 401, "access_denied", "sign in to Core")
		return
	}
	if base == nil || len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != base.Scheme+"://"+base.Host {
		tokenErr(w, 403, "access_denied", "origin rejected")
		return
	}
	if r.ParseForm() != nil || (r.PostForm.Get("decision") != "approve" && r.PostForm.Get("decision") != "deny" && r.PostForm.Get("decision") != "approve_manual" && r.PostForm.Get("decision") != "deny_manual") {
		tokenErr(w, 400, "invalid_request", "approval decision required")
		return
	}
	pending, err := s.store.ConsumeMCPConsent(r.Context(), r.PostForm.Get("request"), u.ID)
	if err != nil {
		tokenErr(w, 400, "invalid_request", "approval expired or already used; reconnect your client")
		return
	}
	var in mcpAuthorization
	if json.Unmarshal(pending.Data, &in) != nil {
		tokenErr(w, 400, "invalid_request", "invalid approval")
		return
	}
	manual := strings.HasSuffix(r.PostForm.Get("decision"), "_manual")
	if manual && !mcpTerminalCallback(in.Redirect) {
		tokenErr(w, 400, "invalid_request", "manual completion is only available for local terminal callbacks")
		return
	}
	if r.PostForm.Get("decision") == "deny_manual" {
		s.mcpShowCompletion(w, "", true)
		return
	}
	if r.PostForm.Get("decision") == "deny" {
		s.mcpRedirectErr(w, r, in.Redirect, in.State, "access_denied", "connection declined")
		return
	}
	code, err := s.store.CreateMCPGrantCode(r.Context(), pending.ClientID, u, in, in.scopes())
	if err != nil {
		tokenErr(w, 503, "temporarily_unavailable", "authorization unavailable")
		return
	}
	redirect, _ := url.Parse(in.Redirect)
	q := redirect.Query()
	q.Set("code", code)
	q.Set("state", in.State)
	q.Set("iss", s.mcpIssuer())
	redirect.RawQuery = q.Encode()
	if manual {
		s.mcpShowCompletion(w, redirect.String(), false)
		return
	}
	http.Redirect(w, r, redirect.String(), 303)
}

func (s *Server) mcpOAuthClientAuth(r *http.Request) (*store.MCPOAuthClient, bool) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c, err := s.store.GetMCPOAuthClient(r.Context(), id)
	if err != nil {
		return nil, false
	}
	if c.AuthMethod == "none" {
		return c, !basic && secret == ""
	}
	if (c.AuthMethod == "client_secret_basic") != basic || secret == "" {
		return nil, false
	}
	return c, subtle.ConstantTimeCompare([]byte(crypto.Hash(secret)), []byte(c.SecretHash)) == 1
}

func (s *Server) handleMCPToken(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		tokenErr(w, 400, "invalid_request", "form required")
		return
	}
	c, ok := s.mcpOAuthClientAuth(r)
	if !ok {
		tokenErr(w, 401, "invalid_client", "client authentication failed")
		return
	}
	resource := r.PostForm.Get("resource")
	if resource != "" && resource != s.mcpResource() {
		tokenErr(w, 400, "invalid_target", "resource must be this Core's MCP URL")
		return
	}

	var kind, plain string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		kind, plain = "code", r.PostForm.Get("code")
	case "refresh_token":
		kind, plain = "refresh", r.PostForm.Get("refresh_token")
	default:
		tokenErr(w, 400, "unsupported_grant_type", "unsupported grant type")
		return
	}
	v, err := s.store.GetMCPSecret(r.Context(), plain, kind, c.ClientID)
	if err != nil {
		_ = s.store.RevokeSpentMCPSecret(r.Context(), plain, kind, c.ClientID)
		tokenErr(w, 400, "invalid_grant", "credential expired, revoked, or already used")
		return
	}
	var in mcpAuthorization
	if json.Unmarshal(v.Data, &in) != nil || in.Resource != s.mcpResource() {
		tokenErr(w, 400, "invalid_grant", "resource binding rejected")
		return
	}
	if scope := r.PostForm.Get("scope"); scope != "" && (!s.mcpValidScopes(scope) || canonicalMCPScopes(scope) != in.scopes()) {
		tokenErr(w, 400, "invalid_scope", "scope must match the approved connection")
		return
	}
	if kind == "code" {
		verifier := r.PostForm.Get("code_verifier")
		sum := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		if json.Unmarshal(v.Data, &in) != nil || resource != in.Resource || resource != s.mcpResource() || r.PostForm.Get("redirect_uri") != in.Redirect || !mcpVerifierRE.MatchString(verifier) || subtle.ConstantTimeCompare([]byte(in.Challenge), []byte(challenge)) != 1 {
			tokenErr(w, 400, "invalid_grant", "redirect, resource or PKCE verification failed")
			return
		}
	}
	access, refresh, err := s.store.ExchangeMCPSecret(r.Context(), plain, v)
	if err != nil {
		_ = s.store.RevokeSpentMCPSecret(r.Context(), plain, kind, c.ClientID)
		tokenErr(w, 400, "invalid_grant", "credential expired, revoked, or already used")
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 900, "scope": in.scopes()})
}

func (s *Server) handleMCPRevokeToken(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		tokenErr(w, 400, "invalid_request", "form required")
		return
	}
	c, ok := s.mcpOAuthClientAuth(r)
	if !ok {
		tokenErr(w, 401, "invalid_client", "client authentication failed")
		return
	}
	if s.store.RevokeMCPSecret(r.Context(), r.PostForm.Get("token"), c.ClientID) != nil {
		tokenErr(w, 503, "temporarily_unavailable", "revocation unavailable")
		return
	}
	w.WriteHeader(200)
}

func (s *Server) handleMCPConnections(w http.ResponseWriter, r *http.Request) {
	u := s.sessionUser(r)
	if u == nil {
		writeErr(w, 401, "sign in to Core")
		return
	}
	grants, err := s.store.ListMCPGrants(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err, "list AI connections")
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": s.cfg.MCPOAuthEnabled, "app_tools_enabled": s.cfg.MCPAppToolsEnabled, "scopes": s.mcpSupportedScopes(), "url": s.mcpResource(), "connections": grants})
}

func (s *Server) handleMCPDisconnect(w http.ResponseWriter, r *http.Request) {
	u := s.sessionUser(r)
	if u == nil {
		writeErr(w, 401, "sign in to Core")
		return
	}
	// This endpoint only accepts browser sessions, so never exempt CSRF by a bearer header.
	if !s.mcpConnectionOrigin(r) {
		writeErr(w, 403, "origin rejected")
		return
	}
	if err := s.store.DeleteMCPGrant(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		s.fail(w, err, "disconnect AI client")
		return
	}
	w.WriteHeader(204)
}

const mcpConsentCSS = `:root{color-scheme:light dark;font:16px/1.6 system-ui,sans-serif}body{margin:0;background:light-dark(#f5f6f8,#171a20);color:light-dark(#222936,#e6e9ef)}main{max-width:580px;margin:5vh auto;padding:28px;background:light-dark(white,#222732);border-radius:16px}h1{line-height:1.2}a{color:light-dark(#325bc7,#a6bfff)}code{overflow-wrap:anywhere}form{display:flex;flex-wrap:wrap;gap:12px;margin-top:24px}button{font:inherit;padding:10px 18px;border:1px solid #8091ad;border-radius:8px;cursor:pointer}button.primary,button[value=approve]{background:#3159bd;color:white}textarea{display:block;box-sizing:border-box;width:100%;font:14px/1.5 ui-monospace,monospace;padding:12px;margin:8px 0 16px;border:1px solid #8091ad;border-radius:8px;resize:vertical;overflow-wrap:anywhere}details{width:100%;margin-top:12px}summary{cursor:pointer}button:focus-visible,a:focus-visible,textarea:focus-visible{outline:3px solid #d8911b;outline-offset:3px}@media(max-width:640px){main{margin:16px;padding:22px}}`

// Include the issuer on errors too, as advertised by RFC 9207 metadata.
func (s *Server) mcpRedirectErr(w http.ResponseWriter, r *http.Request, redirect, state, code, desc string) {
	u, err := url.Parse(redirect)
	if err != nil {
		tokenErr(w, 400, "invalid_request", "invalid redirect")
		return
	}
	q := u.Query()
	q.Set("iss", s.mcpIssuer())
	u.RawQuery = q.Encode()
	oidcRedirectErr(w, r, u.String(), state, code, desc)
}

func (s *Server) mcpSupportedScopes() []string {
	if s.cfg.MCPAppToolsEnabled {
		return []string{"status:read", "apps:read", "apps:write"}
	}
	return []string{"status:read"}
}
func hasMCPScope(scope, want string) bool {
	for _, v := range strings.Fields(scope) {
		if v == want {
			return true
		}
	}
	return false
}
func canonicalMCPScopes(scope string) string {
	out := []string{}
	for _, v := range []string{"status:read", "apps:read", "apps:write"} {
		if hasMCPScope(scope, v) {
			out = append(out, v)
		}
	}
	return strings.Join(out, " ")
}
func (s *Server) mcpValidScopes(scope string) bool {
	if !hasMCPScope(scope, "status:read") {
		return false
	}
	for _, v := range strings.Fields(scope) {
		found := false
		for _, allowed := range s.mcpSupportedScopes() {
			if v == allowed {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return !hasMCPScope(scope, "apps:write") || hasMCPScope(scope, "apps:read")
}
func (a mcpAuthorization) scopes() string {
	if a.Scope == "" {
		return mcpOAuthScope
	}
	return a.Scope
}
