package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const oauthTestOrigin = "https://core.test"
const oauthTestVerifier = "test-verifier-0123456789012345678901234567890123456789"

type oauthFixture struct {
	*env
	user     *store.User
	cookie   *http.Cookie
	clientID string
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	e := newEnv(t)
	e.s.cfg.MCPOAuthEnabled = true
	e.s.cfg.BaseURL = oauthTestOrigin
	e.srv.Close()
	e.srv = httptest.NewServer(e.s.Router())
	t.Cleanup(e.srv.Close)
	u := &store.User{Email: "member@example.test", Name: "Member", Role: "member"}
	if err := e.st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	session, err := e.st.CreateSession(context.Background(), u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := &oauthFixture{env: e, user: u, cookie: &http.Cookie{Name: sessionCookie, Value: session}}
	res, body := f.request("POST", "/mcp/oauth/register", `{"client_name":"Test assistant","redirect_uris":["http://127.0.0.1:12345/callback"],"token_endpoint_auth_method":"none"}`, "", false)
	if res.StatusCode != 201 {
		t.Fatalf("register: %d %s", res.StatusCode, body)
	}
	var c store.MCPOAuthClient
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	f.clientID = c.ClientID
	return f
}

func (f *oauthFixture) request(method, path, body, origin string, cookie bool) (*http.Response, string) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie {
		req.AddCookie(f.cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return res, string(b)
}

func (f *oauthFixture) authorizeQuery() url.Values {
	sum := sha256.Sum256([]byte(oauthTestVerifier))
	return url.Values{"client_id": {f.clientID}, "redirect_uri": {"http://127.0.0.1:12345/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "state": {"test-state"}, "resource": {oauthTestOrigin + "/mcp"}, "scope": {"status:read"}}
}

func (f *oauthFixture) code() string {
	f.t.Helper()
	path := "/mcp/oauth/authorize?" + f.authorizeQuery().Encode()
	res, _ := f.request("GET", path, "", "", false)
	if res.StatusCode != 302 || !strings.HasPrefix(res.Header.Get("Location"), "/login?next=") {
		f.t.Fatal("must sign in")
	}
	res, body := f.request("GET", path, "", "", true)
	if res.StatusCode != 200 || res.Header.Get("Referrer-Policy") != "same-origin" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "http://127.0.0.1:12345") || !strings.Contains(body, "Allow status access") {
		f.t.Fatalf("consent: %d %s", res.StatusCode, body)
	}
	match := regexp.MustCompile(`name="request" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		f.t.Fatal("missing consent request")
	}
	form := url.Values{"request": {match[1]}, "decision": {"approve"}}.Encode()
	res, _ = f.request("POST", "/mcp/oauth/authorize", form, "https://evil.test", true)
	if res.StatusCode != 403 {
		f.t.Fatal("cross-site consent accepted")
	}
	res, _ = f.request("POST", "/mcp/oauth/authorize", form, oauthTestOrigin, true)
	target, err := url.Parse(res.Header.Get("Location"))
	if err != nil || res.StatusCode != 303 || target.Query().Get("state") != "test-state" || target.Query().Get("iss") != oauthTestOrigin+"/mcp/oauth" {
		f.t.Fatalf("approval redirect: %d", res.StatusCode)
	}
	res, _ = f.request("POST", "/mcp/oauth/authorize", form, oauthTestOrigin, true)
	if res.StatusCode != 400 {
		f.t.Fatal("replayed consent accepted")
	}
	return target.Query().Get("code")
}

func (f *oauthFixture) exchange(code string) (string, string) {
	f.t.Helper()
	form := url.Values{"client_id": {f.clientID}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1:12345/callback"}, "code_verifier": {oauthTestVerifier}, "resource": {oauthTestOrigin + "/mcp"}}
	res, body := f.request("POST", "/mcp/oauth/token", form.Encode(), "", false)
	if res.StatusCode != 200 {
		f.t.Fatalf("exchange: %d %s", res.StatusCode, body)
	}
	var pair struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal([]byte(body), &pair); err != nil {
		f.t.Fatal(err)
	}

	return pair.Access, pair.Refresh
}

func TestMCPOAuthPermissionsAndDisconnect(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		slug, audience string
		enabled        bool
		users          []int64
	}{
		{"notes", "all", true, nil}, {"money", "admins", true, nil}, {"tasks", "users", true, []int64{f.user.ID}}, {"files", "users", true, []int64{999}}, {"garage", "all", false, nil},
	} {
		a := &store.App{Slug: tc.slug, Name: tc.slug, URL: "http://127.0.0.1:1"}
		if _, err := f.st.CreateApp(ctx, a); err != nil {
			t.Fatal(err)
		}
		sv := &store.Service{Type: tc.slug, Name: tc.slug, URL: a.URL, AppID: a.ID, Enabled: tc.enabled, Audience: tc.audience, AudienceUsers: tc.users}
		if err := f.st.SaveService(ctx, sv, func(string) bool { return false }); err != nil {
			t.Fatal(err)
		}
	}
	access, _ := f.exchange(f.code())
	session := mcpSessionWithToken(t, f.env, access)
	list := func() string {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pocket_list_apps", Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		return string(b)
	}
	body := list()
	if !strings.Contains(body, "notes") || !strings.Contains(body, "tasks") {
		t.Fatalf("missing permitted apps: %s", body)
	}
	for _, slug := range []string{"money", "files", "garage"} {
		if strings.Contains(body, slug) {
			t.Fatalf("leaked %s", slug)
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pocket_app_status", Arguments: map[string]any{"slug": "money", "fresh": true}})
	if err != nil || !res.IsError {
		t.Fatal("denied app accepted")
	}
	if _, err := f.st.DB.Exec(`UPDATE services SET audience='admins' WHERE type='notes'`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list(), "notes") {
		t.Fatal("permission change was not applied")
	}
	if got := f.do("GET", "/api/v1/settings", access, nil, nil); got != 401 {
		t.Fatalf("MCP token accepted by API: %d", got)
	}
	response, body := f.request("GET", "/api/v1/me/ai-connections", "", "", true)
	var connections struct {
		Connections []store.MCPGrant `json:"connections"`
	}
	if response.StatusCode != 200 || json.Unmarshal([]byte(body), &connections) != nil || len(connections.Connections) != 1 {
		t.Fatalf("connections: %d %s", response.StatusCode, body)
	}
	id := connections.Connections[0].ID
	response, _ = f.request("DELETE", "/api/v1/me/ai-connections/"+id, "", "https://evil.test", true)
	if response.StatusCode != 403 {
		t.Fatal("cross-site disconnect accepted")
	}
	// A different user cannot revoke this user's grant.
	other := &store.User{Email: "other@example.test"}
	if err := f.st.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteMCPGrant(ctx, id, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.GetMCPSecret(ctx, access, "access", ""); err != nil {
		t.Fatal("other user revoked grant")
	}
	response, _ = f.request("DELETE", "/api/v1/me/ai-connections/"+id, "", oauthTestOrigin, true)
	if response.StatusCode != 204 {
		t.Fatalf("disconnect: %d", response.StatusCode)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pocket_list_apps", Arguments: map[string]any{}}); err == nil {
		t.Fatal("disconnected token accepted")
	}
}

func TestMCPOAuthPKCERefreshAndAccountRevocation(t *testing.T) {
	f := newOAuthFixture(t)
	code := f.code()
	bad := url.Values{"client_id": {f.clientID}, "grant_type": {"authorization_code"}, "code": {code}, "resource": {oauthTestOrigin + "/mcp"}, "redirect_uri": {"http://127.0.0.1:12345/callback"}, "code_verifier": {strings.Repeat("a", 43)}}
	res, _ := f.request("POST", "/mcp/oauth/token", bad.Encode(), "", false)
	if res.StatusCode != 400 {
		t.Fatal("invalid PKCE accepted")
	}
	bad.Set("code_verifier", oauthTestVerifier)
	bad.Set("resource", "https://evil.test/mcp")
	res, _ = f.request("POST", "/mcp/oauth/token", bad.Encode(), "", false)
	if res.StatusCode != 400 {
		t.Fatal("invalid audience accepted")
	}
	access, refresh := f.exchange(code)
	form := url.Values{"client_id": {f.clientID}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}, "resource": {oauthTestOrigin + "/mcp"}}
	res, body := f.request("POST", "/mcp/oauth/token", form.Encode(), "", false)
	if res.StatusCode != 200 {
		t.Fatalf("refresh: %d %s", res.StatusCode, body)
	}
	res, _ = f.request("POST", "/mcp/oauth/token", form.Encode(), "", false)
	if res.StatusCode != 400 {
		t.Fatal("replayed refresh accepted")
	}
	var rotated struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal([]byte(body), &rotated); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{access, rotated.Access} {
		if status := f.do("POST", "/mcp", token, nil, nil); status != 401 {
			t.Fatalf("reused refresh did not revoke connection: %d", status)
		}
	}
	if _, err := f.st.GetMCPSecret(context.Background(), rotated.Refresh, "refresh", f.clientID); err == nil {
		t.Fatal("rotated refresh survived replay")
	}

}

func TestMCPOAuthDiscoveryAndValidation(t *testing.T) {
	f := newOAuthFixture(t)
	res, _ := f.request("POST", "/mcp", "", "", false)
	if res.StatusCode != 401 || !strings.Contains(res.Header.Get("WWW-Authenticate"), oauthTestOrigin+"/.well-known/oauth-protected-resource/mcp") {
		t.Fatal("missing protected resource discovery")
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server/mcp/oauth"} {
		res, body := f.request("GET", path, "", "", false)
		if res.StatusCode != 200 || !strings.Contains(body, "status:read") {
			t.Fatalf("discovery: %d %s", res.StatusCode, body)
		}
	}
	q := f.authorizeQuery()
	q.Set("redirect_uri", "https://evil.test/callback")
	res, _ = f.request("GET", "/mcp/oauth/authorize?"+q.Encode(), "", "", true)
	if res.StatusCode != 400 || res.Header.Get("Location") != "" {
		t.Fatal("redirected to unregistered URI")
	}
	q = f.authorizeQuery()
	q.Set("scope", "admin")
	res, _ = f.request("GET", "/mcp/oauth/authorize?"+q.Encode(), "", "", true)
	target, _ := url.Parse(res.Header.Get("Location"))
	if target.Query().Get("error") != "invalid_scope" || target.Query().Get("iss") != oauthTestOrigin+"/mcp/oauth" {
		t.Fatal("unsupported scope accepted or error issuer missing")
	}
	for _, uri := range []string{"http://evil.test/cb", "https://user:pass@evil.test/cb", "https://example.test/cb#fragment", "file:///tmp/callback"} {
		if validMCPRedirect(uri) {
			t.Fatalf("unsafe redirect %s", uri)
		}
	}
	// Public dynamic registrations must never become first-party OIDC clients.
	if _, err := f.st.GetOIDCClient(context.Background(), f.clientID); err == nil {
		t.Fatal("dynamic registration leaked into OIDC")
	}
}

func TestMCPOAuthConcurrentCodeExchangeAndExpiry(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	code := f.code()
	secret, err := f.st.GetMCPSecret(ctx, code, "code", f.clientID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := f.st.ExchangeMCPSecret(ctx, code, secret); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("code used %d times", successes)
	}
	// Expired grants invalidate otherwise unexpired token pairs.
	if _, err := f.st.DB.Exec(`UPDATE mcp_oauth_grants SET expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if err := f.st.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.st.DB.QueryRow(`SELECT COUNT(*) FROM mcp_oauth_secrets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("expired grant tokens not purged")
	}
}

func TestMCPOAuthAccountAndTokenExpiry(t *testing.T) {
	for _, change := range []string{"revision", "disabled", "access_expiry", "resource"} {
		t.Run(change, func(t *testing.T) {
			f := newOAuthFixture(t)
			access, _ := f.exchange(f.code())
			var err error
			switch change {
			case "revision":
				_, err = f.st.DB.Exec(`UPDATE users SET auth_revision=auth_revision+1 WHERE id=?`, f.user.ID)
			case "disabled":
				_, err = f.st.DB.Exec(`UPDATE users SET disabled=1 WHERE id=?`, f.user.ID)
			case "access_expiry":
				_, err = f.st.DB.Exec(`UPDATE mcp_oauth_secrets SET expires_at='2000-01-01T00:00:00Z' WHERE kind='access'`)
			case "resource":
				f.s.cfg.BaseURL = "https://different.test"
			}
			if err != nil {
				t.Fatal(err)
			}
			if status := f.do("POST", "/mcp", access, nil, nil); status != 401 {
				t.Fatalf("%s token accepted: %d", change, status)
			}
		})
	}
}

func TestMCPOAuthCodeReplayRevokesGrant(t *testing.T) {
	f := newOAuthFixture(t)
	code := f.code()
	access, _ := f.exchange(code)
	form := url.Values{"client_id": {f.clientID}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1:12345/callback"}, "code_verifier": {oauthTestVerifier}, "resource": {oauthTestOrigin + "/mcp"}}
	res, _ := f.request("POST", "/mcp/oauth/token", form.Encode(), "", false)
	if res.StatusCode != 400 {
		t.Fatal("replayed code accepted")
	}
	if _, err := f.st.GetMCPSecret(context.Background(), access, "access", ""); err == nil {
		t.Fatal("replayed code did not revoke grant")
	}
}

func TestMCPOAuthConfidentialClients(t *testing.T) {
	for _, method := range []string{"client_secret_basic", "client_secret_post"} {
		t.Run(method, func(t *testing.T) {
			f := newOAuthFixture(t)
			registration, _ := json.Marshal(map[string]any{"client_name": "Hosted assistant", "redirect_uris": []string{"https://assistant.example.test/callback"}, "token_endpoint_auth_method": method})
			res, body := f.request("POST", "/mcp/oauth/register", string(registration), "", false)
			var client struct {
				ID     string `json:"client_id"`
				Secret string `json:"client_secret"`
			}
			if res.StatusCode != 201 || json.Unmarshal([]byte(body), &client) != nil || client.Secret == "" {
				t.Fatal("confidential registration failed")
			}
			sum := sha256.Sum256([]byte(oauthTestVerifier))
			binding := mcpAuthorization{Redirect: "https://assistant.example.test/callback", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Resource: oauthTestOrigin + "/mcp"}
			code, err := f.st.CreateMCPGrantCode(context.Background(), client.ID, f.user, binding)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"wrong-secret", client.Secret} {
				form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {oauthTestVerifier}, "redirect_uri": {binding.Redirect}, "resource": {binding.Resource}}
				if method == "client_secret_post" {
					form.Set("client_id", client.ID)
					form.Set("client_secret", secret)
				}
				req := httptest.NewRequest("POST", "/mcp/oauth/token", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if method == "client_secret_basic" {
					req.SetBasicAuth(client.ID, secret)
				}
				response := httptest.NewRecorder()
				f.s.handleMCPToken(response, req)
				want := 200
				if secret == "wrong-secret" {
					want = 401
				}
				if response.Code != want {
					t.Fatalf("%s exchange status %d want %d", method, response.Code, want)
				}
			}
		})
	}
}
