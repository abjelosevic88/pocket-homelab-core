package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type createdMCPToken struct {
	Token      string         `json:"token"`
	Connection store.MCPGrant `json:"connection"`
}

func (f *oauthFixture) connectionToken(scope string) createdMCPToken {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "Remote Codex", "scope": scope, "expires_in_days": 7, "approved": true})
	res, raw := f.request("POST", "/api/v1/me/ai-connections/token", string(body), oauthTestOrigin, true)
	var created createdMCPToken
	if res.StatusCode != 201 || json.Unmarshal([]byte(raw), &created) != nil || created.Token == "" {
		f.t.Fatalf("create connection: %d %s", res.StatusCode, raw)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		f.t.Fatal("token response can be cached")
	}
	return created
}
func TestMCPConnectionTokenNoRedirect(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		secret, err := f.st.GetMCPSecret(r.Context(), forwarded, "app", "")
		if err != nil || secret.UserID != f.user.ID {
			t.Error("manual connection did not delegate user identity")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"title":"Test"}`)
	}))
	defer app.Close()
	f.app("notes", app.URL, "all")
	created := f.connectionToken("status:read apps:read apps:write")
	if created.Connection.Kind != "token" {
		t.Fatal("wrong connection kind")
	}
	session := mcpSessionWithToken(t, f.env, created.Token)
	if !toolNames(t, session)["pocket_notes_create"] {
		t.Fatal("approved tools missing")
	}
	written, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_notes_create", Arguments: map[string]any{"request_id": "manual-token-test-0001", "data": map[string]any{"title": "Test", "content": "Hello"}}})
	if err != nil || written.IsError {
		t.Fatal("manual token could not call approved write tool")
	}
	if f.do("GET", "/api/v1/settings", created.Token, nil, nil) != 401 {
		t.Fatal("MCP token accepted by Core API")
	}
	_, listed := f.request("GET", "/api/v1/me/ai-connections", "", "", true)
	if strings.Contains(listed, created.Token) || !strings.Contains(listed, "Remote Codex") {
		t.Fatal("list leaked token or missed connection")
	}
	secret, err := f.st.GetMCPSecret(context.Background(), created.Token, "access", "")
	if err != nil {
		t.Fatal(err)
	}
	if expiry, err := time.Parse(time.RFC3339, secret.ExpiresAt); err != nil || time.Until(expiry) < 6*24*time.Hour || time.Until(expiry) > 7*24*time.Hour {
		t.Fatal("wrong token expiry")
	}
	var hash string
	if err = f.st.DB.QueryRow(`SELECT hash FROM mcp_oauth_secrets WHERE grant_id=?`, created.Connection.ID).Scan(&hash); err != nil || hash != crypto.Hash(created.Token) {
		t.Fatal("token was not hashed")
	}
	c, err := f.st.GetMCPOAuthClient(context.Background(), created.Connection.ClientID)
	if err != nil || len(c.RedirectURIs) != 0 {
		t.Fatal("manual token gained OAuth callbacks")
	}
	other := &store.User{Email: "other-token-user@example.test"}
	if err = f.st.CreateUser(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err = f.st.DeleteMCPGrant(context.Background(), created.Connection.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.GetMCPSecret(context.Background(), created.Token, "access", ""); err != nil {
		t.Fatal("other user revoked token")
	}
	res, _ := f.request("DELETE", "/api/v1/me/ai-connections/"+created.Connection.ID, "", oauthTestOrigin, true)
	if res.StatusCode != 204 {
		t.Fatal("disconnect failed")
	}
	if _, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_core_status", Arguments: map[string]any{}}); err == nil {
		t.Fatal("disconnected token accepted")
	}
	if _, err = f.st.GetMCPOAuthClient(context.Background(), created.Connection.ClientID); err == nil {
		t.Fatal("orphan manual client retained")
	}
}
func TestMCPConnectionTokenConsentAndScopes(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	valid := `{"name":"Remote","scope":"status:read apps:read apps:write","expires_in_days":30,"approved":true}`
	for _, tc := range []struct {
		name, body, origin string
		cookie             bool
		code               int
	}{
		{"no session", valid, oauthTestOrigin, false, 401},
		{"no origin", valid, "", true, 403},
		{"foreign origin", valid, "https://evil.test", true, 403},
		{"no consent", strings.Replace(valid, `"approved":true`, `"approved":false`, 1), oauthTestOrigin, true, 400},
		{"scope escalation", strings.Replace(valid, "status:read apps:read apps:write", "admin", 1), oauthTestOrigin, true, 400},
		{"write without read", strings.Replace(valid, "apps:read ", "", 1), oauthTestOrigin, true, 400},
		{"indefinite token", strings.Replace(valid, ":30", ":0", 1), oauthTestOrigin, true, 400},
		{"unknown fields", strings.TrimSuffix(valid, "}") + `,"user_id":1}`, oauthTestOrigin, true, 400},
		{"trailing object", valid + "{}", oauthTestOrigin, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := f.request("POST", "/api/v1/me/ai-connections/token", tc.body, tc.origin, tc.cookie)
			if res.StatusCode != tc.code {
				t.Fatalf("got %d want %d", res.StatusCode, tc.code)
			}
		})
	}
	f.app("notes", "http://127.0.0.1:1", "all")
	read := f.connectionToken("status:read apps:read")
	names := toolNames(t, mcpSessionWithToken(t, f.env, read.Token))
	if !names["pocket_notes_get"] || names["pocket_notes_create"] {
		t.Fatal("read token gained write scope")
	}
	f.s.cfg.MCPAppToolsEnabled = false
	res, _ := f.request("POST", "/api/v1/me/ai-connections/token", valid, oauthTestOrigin, true)
	if res.StatusCode != 400 {
		t.Fatal("disabled scope granted")
	}
	status := f.connectionToken("status:read")
	if len(toolNames(t, mcpSessionWithToken(t, f.env, status.Token))) != 3 {
		t.Fatal("status token gained app tools")
	}
	f.s.cfg.MCPOAuthEnabled = false
	res, _ = f.request("POST", "/api/v1/me/ai-connections/token", valid, oauthTestOrigin, true)
	if res.StatusCode != 403 {
		t.Fatal("disabled account mode minted token")
	}
}
func TestMCPConnectionTokenExpiryAndLimits(t *testing.T) {
	f := newOAuthFixture(t)
	first := f.connectionToken("status:read")
	for range 19 {
		f.connectionToken("status:read")
	}
	res, _ := f.request("POST", "/api/v1/me/ai-connections/token", `{"name":"Overflow","scope":"status:read","expires_in_days":1,"approved":true}`, oauthTestOrigin, true)
	if res.StatusCode != 409 {
		t.Fatal("unbounded token creation")
	}
	if _, err := f.st.DB.Exec(`UPDATE mcp_oauth_grants SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, first.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if f.do("POST", "/mcp", first.Token, nil, nil) != 401 {
		t.Fatal("expired token accepted")
	}
	f.connectionToken("status:read")
	if err := f.st.PurgeExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.GetMCPOAuthClient(context.Background(), first.Connection.ClientID); err == nil {
		t.Fatal("expired token client retained")
	}
}

func TestMCPConnectionTokenAccountRevocation(t *testing.T) {
	for _, change := range []string{"auth_revision=auth_revision+1", "disabled=1"} {
		t.Run(change, func(t *testing.T) {
			f := newOAuthFixture(t)
			created := f.connectionToken("status:read")
			if _, err := f.st.DB.Exec(fmt.Sprintf("UPDATE users SET %s WHERE id=?", change), f.user.ID); err != nil {
				t.Fatal(err)
			}
			if f.do("POST", "/mcp", created.Token, nil, nil) != 401 {
				t.Fatal("account revocation ignored")
			}
		})
	}
}
