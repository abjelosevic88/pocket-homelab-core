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

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func identityResult(t *testing.T, session *mcp.ClientSession) mcpIdentity {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_whoami", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("whoami: %v %v", result, err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password", "auth_revision", "grant_id", "client_id", "pcma_", "pcmr_"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("identity exposed %s", forbidden)
		}
	}
	var identity mcpIdentity
	if err := json.Unmarshal(raw, &identity); err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestMCPIdentityAndApplicationUserIsolation(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	ctx := context.Background()
	var appToken string
	// Stand in for a Pocket app: introspect the delegated credential through
	// Core, then return a value selected by that authenticated user's subject.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var identity struct {
			Active bool `json:"active"`
			User   struct {
				Subject string `json:"sub"`
			} `json:"user"`
		}
		if f.do("POST", "/api/v1/auth/introspect", appToken, map[string]any{"token": strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")}, &identity) != 200 || !identity.Active {
			t.Error("application credential did not resolve to an active user")
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"personal_value": "value-for-" + identity.User.Subject})
	}))
	defer upstream.Close()
	_, appToken = f.app("hub", upstream.URL, "all")
	f.app("money", "http://127.0.0.1:1", "admins")
	firstAccess, _ := f.appAccess("status:read apps:read apps:write")
	first := mcpSessionWithToken(t, f.env, firstAccess)
	other := &store.User{Name: "Second person", Email: "second@example.test", Role: "admin", PasswordHash: "private-password-hash"}
	if err := f.st.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	cookie, err := f.st.CreateSession(ctx, other.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.cookie = &http.Cookie{Name: sessionCookie, Value: cookie}
	secondAccess, _ := f.appAccess("status:read apps:read")
	second := mcpSessionWithToken(t, f.env, secondAccess)
	for _, tc := range []struct {
		session *mcp.ClientSession
		user    *store.User
		write   bool
	}{{first, f.user, true}, {second, other, false}} {
		got := identityResult(t, tc.session)
		want := mcpIdentityUser{ID: tc.user.ID, Subject: tc.user.Subject, Email: tc.user.Email, Name: tc.user.Name, Role: tc.user.Role}
		if got.User != want || !got.CanReadAppContent || got.CanCreateUpdateItems != tc.write {
			t.Fatalf("wrong identity or permissions: %+v", got)
		}
		apps := strings.Join(got.AllowedApps, ",")
		if !strings.Contains(apps, "hub") || strings.Contains(apps, "money") != tc.user.IsAdmin() {
			t.Fatalf("wrong app visibility: %s", apps)
		}
		result, err := tc.session.CallTool(ctx, &mcp.CallToolParams{Name: "pocket_hub_preferences", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatalf("personal application read: %v %v", result, err)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		if !strings.Contains(string(raw), "value-for-"+tc.user.Subject) {
			t.Fatalf("application did not use whoami's user: %s", raw)
		}
	}
	// Account identity is resolved on each call, not cached on the MCP server.
	if _, err := f.st.DB.Exec(`UPDATE users SET name='Renamed' WHERE id=?`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if got := identityResult(t, first); got.User.Name != "Renamed" {
		t.Fatal("stale profile")
	}
	if err := f.st.DeleteMCPGrant(ctx, mustGrantID(t, f, firstAccess), f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CallTool(ctx, &mcp.CallToolParams{Name: "pocket_whoami", Arguments: map[string]any{}}); err == nil {
		t.Fatal("revoked connection read identity")
	}
}

func mustGrantID(t *testing.T, f *oauthFixture, access string) string {
	t.Helper()
	secret, err := f.st.GetMCPSecret(context.Background(), access, "access", "")
	if err != nil {
		t.Fatal(err)
	}
	return secret.GrantID
}

func TestMCPIdentityStatusAccountAndOperator(t *testing.T) {
	f := newOAuthFixture(t)
	access, _ := f.exchange(f.code())
	session := mcpSessionWithToken(t, f.env, access)
	got := identityResult(t, session)
	if got.User.ID != f.user.ID || got.CanReadAppContent || got.CanCreateUpdateItems || fmt.Sprint(got.Scopes) != "[status:read]" || len(got.AllowedApps) != 0 {
		t.Fatalf("status grant identity/permissions: %+v", got)
	}
	f.s.cfg.MCPAppToolsEnabled = true
	if got := identityResult(t, session); got.CanReadAppContent || got.CanCreateUpdateItems {
		t.Fatal("status scope gained app access")
	}
	operator := mcpSession(t, mcpEnv(t))
	if toolNames(t, operator)["pocket_whoami"] {
		t.Fatal("operator credential exposed personal identity tool")
	}
	result, err := operator.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_whoami", Arguments: map[string]any{}})
	if err == nil && !result.IsError {
		t.Fatal("operator credential read a user profile")
	}
}
