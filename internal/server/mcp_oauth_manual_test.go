package server

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

func (f *oauthFixture) manualConsentRequest(query url.Values) string {
	f.t.Helper()
	res, body := f.request("GET", "/mcp/oauth/authorize?"+query.Encode(), "", "", true)
	if res.StatusCode != 200 {
		f.t.Fatalf("consent failed: %d", res.StatusCode)
	}
	matches := regexp.MustCompile(`name="request" value="([^"]+)"`).FindStringSubmatch(body)
	if len(matches) != 2 {
		f.t.Fatal("consent request missing")
	}
	return matches[1]
}

func TestMCPOAuthManualCompletion(t *testing.T) {
	f := newOAuthFixture(t)
	query := f.authorizeQuery()
	query.Set("state", `state-with-&-and-<markup>`)
	request := f.manualConsentRequest(query)
	form := url.Values{"request": {request}, "decision": {"approve_manual"}}
	res, _ := f.request("POST", "/mcp/oauth/authorize", form.Encode(), "https://other.test", true)
	if res.StatusCode != 403 {
		t.Fatal("cross-origin manual approval accepted")
	}
	res, body := f.request("POST", "/mcp/oauth/authorize", form.Encode(), oauthTestOrigin, true)
	if res.StatusCode != 200 || res.Header.Get("Location") != "" {
		t.Fatal("manual completion redirected")
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("completion security headers missing")
	}
	match := regexp.MustCompile(`<textarea[^>]*>([^<]+)</textarea>`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("completion response missing")
	}
	callback, err := url.Parse(html.UnescapeString(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	if callback.Scheme != "http" || callback.Host != "127.0.0.1:12345" || callback.Path != "/callback" || callback.Query().Get("state") != query.Get("state") || callback.Query().Get("iss") != oauthTestOrigin+"/mcp/oauth" {
		t.Fatal("callback binding not preserved")
	}
	if strings.Contains(body, "pcma_") || strings.Contains(body, "pcmr_") || strings.Contains(body, `<markup>`) {
		t.Fatal("completion exposed credentials or unsafe markup")
	}
	code := callback.Query().Get("code")
	form = url.Values{"client_id": {f.clientID}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1:12345/callback"}, "code_verifier": {"wrong-verifier-0123456789012345678901234567890123456789"}, "resource": {oauthTestOrigin + "/mcp"}}
	res, _ = f.request("POST", "/mcp/oauth/token", form.Encode(), "", false)
	if res.StatusCode != 400 {
		t.Fatal("manual response bypassed PKCE")
	}
	access, refresh := f.exchange(code)
	if refresh == "" || len(toolNames(t, mcpSessionWithToken(t, f.env, access))) != 3 {
		t.Fatal("native OAuth credentials missing")
	}
	res, _ = f.request("POST", "/mcp/oauth/authorize", url.Values{"request": {request}, "decision": {"approve_manual"}}.Encode(), oauthTestOrigin, true)
	if res.StatusCode != 400 {
		t.Fatal("manual consent replayed")
	}
}

func TestMCPOAuthManualDenied(t *testing.T) {
	f := newOAuthFixture(t)
	request := f.manualConsentRequest(f.authorizeQuery())
	res, body := f.request("POST", "/mcp/oauth/authorize", url.Values{"request": {request}, "decision": {"deny_manual"}}.Encode(), oauthTestOrigin, true)
	if res.StatusCode != 200 || res.Header.Get("Location") != "" || !strings.Contains(body, "Connection cancelled") || strings.Contains(body, "<textarea") {
		t.Fatal("manual denial did not stay on Core")
	}
	grants, err := f.st.ListMCPGrants(context.Background(), f.user.ID)
	if err != nil || len(grants) != 0 {
		t.Fatal("denial created a grant")
	}
}

func TestMCPOAuthManualOnlyTerminalCallbacks(t *testing.T) {
	f := newOAuthFixture(t)
	c := &store.MCPOAuthClient{Name: "Hosted client", AuthMethod: "none", RedirectURIs: []string{"https://client.example/callback"}}
	if err := f.st.CreateMCPOAuthClient(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	query := f.authorizeQuery()
	query.Set("client_id", c.ClientID)
	query.Set("redirect_uri", c.RedirectURIs[0])
	request := f.manualConsentRequest(query)
	res, _ := f.request("POST", "/mcp/oauth/authorize", url.Values{"request": {request}, "decision": {"approve_manual"}}.Encode(), oauthTestOrigin, true)
	if res.StatusCode != 400 {
		t.Fatal("manual completion enabled for hosted callback")
	}
	grants, _ := f.st.ListMCPGrants(context.Background(), f.user.ID)
	if len(grants) != 0 {
		t.Fatal("invalid manual request created a grant")
	}
}

func TestMCPOAuthLocalhostCallback(t *testing.T) {
	f := newOAuthFixture(t)
	res, _ := f.request("POST", "/mcp/oauth/register", `{"client_name":"Claude Code","redirect_uris":["http://localhost:54321/callback"],"token_endpoint_auth_method":"none"}`, "", false)
	if res.StatusCode != 201 {
		t.Fatal("Claude localhost callback rejected")
	}
	for _, callback := range []string{"http://127.0.0.1:54321/callback", "http://localhost:54321/callback", "http://[::1]:54321/callback"} {
		if !validMCPRedirect(callback) || !mcpTerminalCallback(callback) {
			t.Fatal("valid terminal callback rejected")
		}
	}
	for _, callback := range []string{"http://localhost.evil.test/callback", "http://127.0.0.1.evil.test/callback", "http://evil.test/callback"} {
		if validMCPRedirect(callback) || mcpTerminalCallback(callback) {
			t.Fatal("foreign callback accepted as localhost")
		}
	}
}
