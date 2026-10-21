package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpTestToken = "test-only-status-credential-0123456789"

func mcpEnv(t *testing.T) *env {
	t.Helper()
	hash := sha256.Sum256([]byte(mcpTestToken))
	t.Setenv("MCP_TOKEN_HASH", hex.EncodeToString(hash[:]))
	t.Setenv("MCP_APPS", "notes")
	return newEnv(t)
}

type mcpAuthTransport struct{ token string }

func (a mcpAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, e *env) *mcp.ClientSession {
	return mcpSessionWithToken(t, e, mcpTestToken)
}

func mcpSessionWithToken(t *testing.T, e *env, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "status-test", Version: "1"}, nil)
	session, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: mcpAuthTransport{token}}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestMCPAuthentication(t *testing.T) {
	t.Setenv("MCP_TOKEN_HASH", "")
	e := newEnv(t)
	if got := e.do("POST", "/mcp", mcpTestToken, nil, nil); got != 404 {
		t.Fatalf("disabled: %d", got)
	}
	t.Setenv("AUTH_MODE", "none")
	e = mcpEnv(t)
	for _, tc := range []struct {
		token, origin string
		want          int
	}{
		{"", "", 401}, {"bad-token", "", 401}, {mcpTestToken, "https://evil.test", 403},
		{mcpTestToken, "null", 403}, {mcpTestToken, "http://core.test", 200}, {mcpTestToken, "", 200},
	} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("origin=%q validToken=%v got %d want %d", tc.origin, tc.token == mcpTestToken, res.StatusCode, tc.want)
		}
	}
}

func TestMCPToolsAndBoundaries(t *testing.T) {
	e := mcpEnv(t)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/healthz" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected upstream path or credentials")
		}
		io.WriteString(w, `{"status":"ok","secret":"do-not-return"}`)
	}))
	defer upstream.Close()
	a := &store.App{Slug: "notes", Name: "Notes", URL: upstream.URL, Version: "1", Manifest: json.RawMessage(`{"secret":"do-not-return"}`)}
	if _, err := e.st.CreateApp(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateApp(context.Background(), &store.App{Slug: "money", Name: "Money", URL: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAppHealth(context.Background(), a.ID, "ok", "1", "private-error"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.Exec("UPDATE apps SET last_check_at = ? WHERE id = ?", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), a.ID); err != nil {
		t.Fatal(err)
	}
	session := mcpSession(t, e)
	list, err := session.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != 3 {
		t.Fatalf("tools: %v %v", list, err)
	}
	for _, tool := range list.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("not read-only: %s", tool.Name)
		}
	}
	call := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call("pocket_list_apps", map[string]any{})
	b, _ := json.Marshal(res.StructuredContent)
	for _, forbidden := range []string{"money", "do-not-return", "private-error", upstream.URL, "token_prefix"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	if !strings.Contains(string(b), `"status":"unknown"`) || !strings.Contains(string(b), `"stale":true`) {
		t.Fatalf("stale health treated as current: %s", b)
	}
	if res := call("pocket_app_status", map[string]any{"slug": "money", "fresh": true}); !res.IsError {
		t.Fatal("denied app accepted")
	}
	if hits.Load() != 0 {
		t.Fatal("denied or cached call performed network I/O")
	}
	res = call("pocket_app_status", map[string]any{"slug": "notes", "fresh": true})
	b, _ = json.Marshal(res.StructuredContent)
	if res.IsError || !strings.Contains(string(b), `"status":"ok"`) || strings.Contains(string(b), "do-not-return") || hits.Load() != 1 {
		t.Fatalf("fresh check: %s", b)
	}
	stored, _ := e.st.GetApp(context.Background(), a.ID)
	if time.Since(mustParseMCPTime(t, stored.LastCheckAt)) < 50*time.Minute {
		t.Fatal("read-only probe modified registry")
	}
	if res := call("pocket_core_status", map[string]any{}); res.IsError {
		t.Fatal("core status failed")
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "restart_service", Arguments: map[string]any{}}); err == nil {
		t.Fatal("unknown mutation tool accepted")
	}
	// This credential must not unlock the ordinary admin API.
	if status := e.do("GET", "/api/v1/settings", mcpTestToken, nil, nil); status != 401 {
		t.Fatalf("MCP token accepted by normal auth: %d", status)
	}
}

func mustParseMCPTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMCPProbeFailures(t *testing.T) {
	e := mcpEnv(t)
	for _, tc := range []struct {
		name         string
		code         int
		body, status string
	}{
		{"unhealthy", 503, `{"secret":"private"}`, "unhealthy"},
		{"degraded", 200, `{"status":"degraded"}`, "unhealthy"},
		{"html", 200, `<html>login</html>`, "unknown"},
		{"unknown", 200, `{"status":"private"}`, "unknown"},
		{"redirect", 302, ``, "unhealthy"},
		{"oversize", 200, `{"status":"ok","data":"` + strings.Repeat("a", 16<<10) + `"}`, "unknown"},
		{"trailing json", 200, `{"status":"ok"}{"status":"down"}`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Location", "/other")
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			}))
			defer u.Close()
			out := e.s.mcpProbe(context.Background(), store.App{URL: u.URL, HealthPath: "/healthz"})
			b, _ := json.Marshal(out)
			if out.Status != tc.status || hits.Load() != 1 || strings.Contains(string(b), "private") {
				t.Fatalf("probe: %s hits=%d", b, hits.Load())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := e.s.mcpProbe(ctx, store.App{URL: "http://127.0.0.1:1", HealthPath: "/healthz"}); out.Status != "unreachable" {
		t.Fatalf("connection failure: %+v", out)
	}
}

func TestMCPClientIsolationAndRevocation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "clients.json")
	client := func(id, token, app string) config.MCPClient {
		hash := sha256.Sum256([]byte(token))
		return config.MCPClient{ID: id, TokenHash: hex.EncodeToString(hash[:]), Apps: []string{app}, ExpiresAt: time.Now().Add(time.Hour)}
	}
	clients := []config.MCPClient{client("notes-client", "notes-secret", "notes"), client("tasks-client", "tasks-secret", "tasks")}
	write := func(value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file+".new", b, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(file+".new", file); err != nil {
			t.Fatal(err)
		}
	}
	write(clients)
	t.Setenv("MCP_TOKEN_HASH", "")
	t.Setenv("MCP_CLIENTS_FILE", file)
	e := newEnv(t)
	for _, slug := range []string{"notes", "tasks"} {
		if _, err := e.st.CreateApp(context.Background(), &store.App{Slug: slug, Name: slug, URL: "http://127.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
	}
	notes := mcpSessionWithToken(t, e, "notes-secret")
	tasks := mcpSessionWithToken(t, e, "tasks-secret")
	list := func(session *mcp.ClientSession, allowed, denied string) {
		t.Helper()
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_list_apps", Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(b), allowed) || strings.Contains(string(b), denied) {
			t.Fatalf("client isolation: %s", b)
		}
	}
	list(notes, "notes", "tasks")
	list(tasks, "tasks", "notes")
	clients[0].Apps = []string{"tasks"}
	write(clients)
	list(notes, "tasks", "notes") // same connection sees changed grants
	clients[0].ExpiresAt = time.Now().Add(-time.Second)
	write(clients)
	if _, err := notes.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_list_apps", Arguments: map[string]any{}}); err == nil {
		t.Fatal("expired credential accepted")
	}
	write([]config.MCPClient{})
	if _, err := tasks.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_list_apps", Arguments: map[string]any{}}); err == nil {
		t.Fatal("revoked credential accepted")
	}
	if err := os.WriteFile(file, []byte("broken-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := e.do("POST", "/mcp", "notes-secret", nil, nil); status != 503 {
		t.Fatalf("invalid configuration did not fail closed: %d", status)
	}
}

func TestMCPRequestLimits(t *testing.T) {
	e := mcpEnv(t)
	req, _ := http.NewRequest("POST", e.srv.URL+"/mcp", strings.NewReader(strings.Repeat("a", 65<<10)))
	req.Header.Set("Authorization", "Bearer "+mcpTestToken)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 413 {
		t.Fatalf("oversize status=%d", res.StatusCode)
	}
	limited := false
	for i := 0; i < 25; i++ {
		if e.do("POST", "/mcp", "invalid", nil, nil) == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("request flood not limited")
	}
}

// Opt-in integration check against a real Notes service, through the actual MCP client.
func TestMCPLiveNotes(t *testing.T) {
	endpoint := os.Getenv("MCP_TEST_NOTES_URL")
	if endpoint == "" {
		t.Skip("set MCP_TEST_NOTES_URL for live Notes integration")
	}
	e := mcpEnv(t)
	if _, err := e.st.CreateApp(context.Background(), &store.App{Slug: "notes", Name: "Notes", URL: endpoint}); err != nil {
		t.Fatal(err)
	}
	session := mcpSession(t, e)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_app_status", Arguments: map[string]any{"slug": "notes", "fresh": true}})
	if err != nil || res.IsError {
		t.Fatalf("live check: %v %v", res, err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	t.Log(string(b))
	if !strings.Contains(string(b), `"status":"ok"`) {
		t.Fatalf("Notes not healthy: %s", b)
	}
}
