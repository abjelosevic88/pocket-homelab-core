package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Pocket Ask lasts while Pocket AI uses it: using the connection's token
// moves its end to 30 days out again; other AI connections keep their end.
func TestAskConnectionExtendsWhileUsed(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var askToken string
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Token string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		askToken = in.Token
		w.WriteHeader(204)
	}))
	defer ai.Close()
	f.app("ai", ai.URL, "all")
	if res, body := f.request("POST", "/api/v1/ask/connect", `{"approved":true}`, oauthTestOrigin, true); res.StatusCode != 204 || askToken == "" {
		t.Fatalf("connect: %d %s", res.StatusCode, body)
	}
	other := f.connectionToken("status:read")
	ctx := context.Background()
	grant, err := f.s.askGrant(ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	soon := store.Stamp(time.Now().Add(3 * 24 * time.Hour))
	for _, id := range []string{grant, other.Connection.ID} {
		f.st.DB.Exec(`UPDATE mcp_oauth_grants SET expires_at=? WHERE id=?`, soon, id)
		f.st.DB.Exec(`UPDATE mcp_oauth_secrets SET expires_at=? WHERE grant_id=?`, soon, id)
	}
	ends := func(id string) (g, s time.Time) {
		var gs, ss string
		f.st.DB.QueryRow(`SELECT expires_at FROM mcp_oauth_grants WHERE id=?`, id).Scan(&gs)
		f.st.DB.QueryRow(`SELECT expires_at FROM mcp_oauth_secrets WHERE grant_id=? AND kind='access'`, id).Scan(&ss)
		g, _ = time.Parse(time.RFC3339Nano, gs)
		s, _ = time.Parse(time.RFC3339Nano, ss)
		return
	}
	for _, tok := range []string{askToken, other.Token} {
		mcpSessionWithToken(t, f.env, tok).Close()
	}
	g, s := ends(grant)
	if time.Until(g) < 29*24*time.Hour || time.Until(s) < 29*24*time.Hour {
		t.Fatalf("Ask connection not extended: %v %v", g, s)
	}
	g, s = ends(other.Connection.ID)
	if time.Until(g) > 4*24*time.Hour || time.Until(s) > 4*24*time.Hour {
		t.Fatal("another AI connection was extended")
	}
	// an ended connection is not brought back
	past := store.Stamp(time.Now().Add(-time.Hour))
	f.st.DB.Exec(`UPDATE mcp_oauth_grants SET expires_at=? WHERE id=?`, past, grant)
	if err := f.st.ExtendAskConnection(ctx, grant, f.user.ID, past); err != nil {
		t.Fatal(err)
	}
	if g, _ = ends(grant); g.After(time.Now()) {
		t.Fatal("expired Ask connection revived")
	}
	if !strings.HasPrefix(askToken, "pcma_") {
		t.Fatal("unexpected token")
	}
}
