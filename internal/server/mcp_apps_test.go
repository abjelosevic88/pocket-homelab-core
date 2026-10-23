package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (f *oauthFixture) appAccess(scope string) (string, string) {
	f.t.Helper()
	q := f.authorizeQuery()
	q.Set("scope", scope)
	res, body := f.request("GET", "/mcp/oauth/authorize?"+q.Encode(), "", "", true)
	if res.StatusCode != 200 {
		f.t.Fatalf("application consent: %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "Allow application access") {
		f.t.Fatal("missing application consent")
	}
	match := regexp.MustCompile(`name="request" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		f.t.Fatal("missing consent nonce")
	}
	res, _ = f.request("POST", "/mcp/oauth/authorize", url.Values{"request": {match[1]}, "decision": {"approve"}}.Encode(), oauthTestOrigin, true)
	target, err := url.Parse(res.Header.Get("Location"))
	if err != nil || res.StatusCode != 303 {
		f.t.Fatal("consent failed")
	}
	return f.exchange(target.Query().Get("code"))
}
func (f *oauthFixture) app(slug, target, audience string) (*store.App, string) {
	f.t.Helper()
	a := &store.App{Slug: slug, Name: slug, URL: target}
	token, err := f.st.CreateApp(context.Background(), a)
	if err != nil {
		f.t.Fatal(err)
	}
	sv := &store.Service{Type: slug, Name: slug, URL: target, AppID: a.ID, Enabled: true, Audience: audience}
	if err = f.st.SaveService(context.Background(), sv, func(string) bool { return false }); err != nil {
		f.t.Fatal(err)
	}
	return a, token
}
func toolNames(t *testing.T, s *mcp.ClientSession) map[string]bool {
	t.Helper()
	r, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, tool := range r.Tools {
		out[tool.Name] = true
	}
	return out
}
func TestMCPApplicationScopes(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	f.app("notes", "http://127.0.0.1:1", "all")
	f.app("money", "http://127.0.0.1:1", "admins")
	status, _ := f.exchange(f.code())
	statusTools := toolNames(t, mcpSessionWithToken(t, f.env, status))
	if len(statusTools) != 4 {
		t.Fatal("existing status grant gained content access")
	}
	read, _ := f.appAccess("status:read apps:read")
	readTools := toolNames(t, mcpSessionWithToken(t, f.env, read))
	if !readTools["pocket_notes_get"] || readTools["pocket_notes_create"] || readTools["pocket_money_accounts"] {
		t.Fatal("read grant tool isolation failed")
	}
	write, refresh := f.appAccess("status:read apps:read apps:write")
	session := mcpSessionWithToken(t, f.env, write)
	if !toolNames(t, session)["pocket_notes_create"] {
		t.Fatal("write tools unavailable")
	}
	f.s.cfg.MCPAppToolsEnabled = false
	if len(toolNames(t, session)) != 4 {
		t.Fatal("disabled application tools remain available")
	}
	f.s.cfg.MCPAppToolsEnabled = true
	grants, err := f.st.ListMCPGrants(context.Background(), f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range grants {
		if g.Scope == "status:read apps:read apps:write" {
			found = true
		}
	}
	if !found {
		t.Fatal("scope not persisted")
	}
	form := url.Values{"client_id": {f.clientID}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}, "resource": {oauthTestOrigin + "/mcp"}, "scope": {"status:read apps:read apps:write admin"}}
	res, _ := f.request("POST", "/mcp/oauth/token", form.Encode(), "", false)
	if res.StatusCode != 400 {
		t.Fatal("refresh scope escalation accepted")
	}
	form.Del("scope")
	res, body := f.request("POST", "/mcp/oauth/token", form.Encode(), "", false)
	if res.StatusCode != 200 || !strings.Contains(body, "apps:write") {
		t.Fatal("refresh lost approved scope")
	}
	if _, err = f.st.DB.Exec(`UPDATE services SET audience='admins' WHERE type='notes'`); err != nil {
		t.Fatal(err)
	}
	if toolNames(t, session)["pocket_notes_create"] {
		t.Fatal("revoked app remains available")
	}
}

func TestMCPApplicationIdentityAndRetries(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var hits atomic.Int32
	var appToken string
	var ephemeral string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ephemeral = token
		var result map[string]any
		if code := f.do("POST", "/api/v1/auth/introspect", appToken, map[string]any{"token": token}, &result); code != 200 || result["active"] != true {
			t.Errorf("app introspection: %d %v", code, result)
		}
		user, ok := result["user"].(map[string]any)
		if !ok || user["email"] != f.user.Email {
			t.Error("wrong user forwarded")
		}
		if r.URL.Path != "/api/v1/notes" || r.Method != "POST" {
			t.Errorf("unexpected path/method %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing idempotency key")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":17,"title":"Test","api_token":"private","nested":{"password":"private"},"echo":%q}`, token)
	}))
	defer fake.Close()
	_, appToken = f.app("notes", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read apps:write")
	session := mcpSessionWithToken(t, f.env, access)
	args := map[string]any{"request_id": "operation-00000001", "data": map[string]any{"title": "Test", "content": "Hello"}}
	call := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_notes_create", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := call(args)
	if first.IsError {
		t.Fatalf("create: %v", first.Content)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "pcu_mcp_") {
		t.Fatal("credentials leaked")
	}
	second := call(args)
	if second.IsError || hits.Load() != 1 {
		t.Fatal("retry repeated operation or failed")
	}
	var inactive map[string]any
	f.do("POST", "/api/v1/auth/introspect", appToken, map[string]any{"token": ephemeral}, &inactive)
	if inactive["active"] != false {
		t.Fatal("internal token survived request")
	}
	args["data"] = map[string]any{"title": "Different", "content": "Hello"}
	if !call(args).IsError || hits.Load() != 1 {
		t.Fatal("request ID reused with different arguments")
	}
	args["request_id"] = "operation-00000002"
	args["data"] = map[string]any{"title": "Test", "content": "Hello", "admin": true}
	if !call(args).IsError || hits.Load() != 1 {
		t.Fatal("unknown fields forwarded")
	}
}
func TestMCPApplicationCredentialBoundary(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	_, notesToken := f.app("notes", "http://127.0.0.1:1", "all")
	_, tasksToken := f.app("tasks", "http://127.0.0.1:1", "all")
	access, _ := f.appAccess("status:read apps:read")
	secret, err := f.st.GetMCPSecret(context.Background(), access, "access", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.st.CreateMCPAppToken(context.Background(), secret.GrantID, mcpAuthorization{Resource: f.s.mcpResource(), Scope: "status:read apps:read", Target: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	active := func(caller string) bool {
		t.Helper()
		var body map[string]any
		f.do("POST", "/api/v1/auth/introspect", caller, map[string]any{"token": token}, &body)
		return body["active"] == true
	}
	if !active(notesToken) || active(tasksToken) || active(access) {
		t.Fatal("credential audience isolation failed")
	}
	if f.do("GET", "/api/v1/settings", token, nil, nil) != 401 {
		t.Fatal("internal credential accepted by Core API")
	}
	if err = f.st.DeleteMCPGrant(context.Background(), secret.GrantID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if active(notesToken) {
		t.Fatal("revoked grant still authorized")
	}
}
func TestMCPApplicationResponseBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		status        int
		mime          string
	}{
		{"redirect", "", 302, "application/json"}, {"html", "<html>sign in</html>", 200, "text/html"},
		{"too-large", strings.Repeat("x", (512<<10)+1), 200, "text/plain"}, {"bad-json", "oops", 200, "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOAuthFixture(t)
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", tc.mime)
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.content)
			}))
			defer upstream.Close()
			r := f.s.requestMCPApp(context.Background(), &mcpAppTool{Method: "GET"}, upstream.URL, "test-token", "", nil)
			if !r.IsError || requests.Load() != 1 {
				t.Fatal("invalid response accepted or redirect followed")
			}
		})
	}
}
func TestMCPApplicationConcurrentReservation(t *testing.T) {
	f := newOAuthFixture(t)
	var fresh atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := f.st.ReserveMCPAction(context.Background(), f.user.ID, "concurrent-operation", "same-digest", "test")
			if err != nil {
				t.Error(err)
			}
			if ok {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatal("concurrent duplicate reservations")
	}
	if err := f.st.FinishMCPAction(context.Background(), f.user.ID, "concurrent-operation", mcpAppError("unknown outcome")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB.Exec(`UPDATE mcp_app_operations SET created_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if err := f.st.PurgeExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok, receipt, err := f.st.ReserveMCPAction(context.Background(), f.user.ID, "concurrent-operation", "same-digest", "test")
	if err != nil || ok || receipt != "" {
		t.Fatal("expired receipt was retained or old operation replayed")
	}
}

func TestMCPAppCredentialMetadataRedaction(t *testing.T) {
	value := scrubMCPAppData(map[string]any{"title": "Readable", "credentials": "hidden", "source": "https://user:password@example.test/feed?token=secret&q=topic", "nested": []any{map[string]any{"refresh_token": "hidden", "text": "internal-secret"}}}, "internal-secret")
	raw, _ := json.Marshal(value)
	for _, forbidden := range []string{"password", "hidden", "internal-secret", "token=secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("metadata leaked %s", forbidden)
		}
	}
	if !strings.Contains(string(raw), "Readable") || !strings.Contains(string(raw), "q=topic") {
		t.Fatal("ordinary metadata was lost")
	}
}

func TestMCPApplicationCatalogBoundary(t *testing.T) {
	names := map[string]bool{}
	reads, writes := map[string]bool{}, map[string]bool{}
	for _, op := range mcpAppCatalog {
		if names[op.Name] {
			t.Fatalf("duplicate tool %s", op.Name)
		}
		names[op.Name] = true
		switch op.Method {
		case "GET":
			reads[op.App] = true
		case "POST", "PUT", "PATCH":
			writes[op.App] = true
		default:
			t.Fatalf("prohibited method %s", op.Method)
		}
		for _, part := range []string{"/admin", "/settings", "/auth", "/delete", "/share", "/invite", "/send", "/restart", "/exec", "/scan"} {
			if strings.Contains(op.Path, part) {
				t.Fatalf("administration/deletion route exposed: %s", op.Path)
			}
		}
	}
	for _, app := range strings.Fields("bookshelf calendar clips collaborator contacts day documents files garage habits hub inventory invoicing kitchen money movement notes office podcasts projects split subtitles tasks travel") {
		if !reads[app] || !writes[app] {
			t.Errorf("%s missing read or create/update support", app)
		}
	}
}

func TestMCPProjectsRememberTakesTeamKey(t *testing.T) {
	for _, op := range mcpAppCatalog {
		if op.Name != "pocket_projects_remember" {
			continue
		}
		if op.Method != "POST" || op.Path != "/teams/{team}/memory" {
			t.Fatalf("remember: %s %s", op.Method, op.Path)
		}
		team := op.Schema["properties"].(mcpFields)["team"].(map[string]any)
		re := regexp.MustCompile(team["pattern"].(string))
		for v, ok := range map[string]bool{"AS": true, "web": true, "12": true, "../x": false, "AS/1": false, "": false} {
			if re.MatchString(v) != ok {
				t.Fatalf("team %q accepted=%v", v, !ok)
			}
		}
		return
	}
	t.Fatal("pocket_projects_remember missing")
}

func TestMCPMoneySummaryUsesOneAuthorizedReport(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/reports/overview" || r.URL.Query().Get("from") != "2026-10-01" || r.URL.Query().Get("to") != "2026-10-31" {
			t.Errorf("unexpected report request %s %s", r.Method, r.URL.String())
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer pcu_mcp_") {
			t.Error("missing delegated identity")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"from":"2026-10-01","to":"2026-10-31","currency":"EUR","expense":1250,"categories":[{"name":"Food","sum":1250,"count":1}]}`))
	}))
	defer fake.Close()
	f.app("money", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read")
	session := mcpSessionWithToken(t, f.env, access)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_money_summary", Arguments: map[string]any{"query": map[string]any{"from": "2026-10-01", "to": "2026-10-31"}}})
	if err != nil || result.IsError || calls.Load() != 1 {
		t.Fatalf("report failed: %v, calls=%d", err, calls.Load())
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "categories") || !strings.Contains(string(raw), "1250") {
		t.Fatal("aggregate results missing")
	}
	if _, err := f.st.DB.Exec(`UPDATE services SET audience='admins' WHERE type='money'`); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_money_summary", Arguments: map[string]any{}})
	if err == nil && !result.IsError {
		t.Fatal("revoked Money access still worked")
	}
	if calls.Load() != 1 {
		t.Fatal("revoked read reached Money")
	}
}

func TestMCPProjectsLinkPR(t *testing.T) {
	for _, op := range mcpAppCatalog {
		if op.Name != "pocket_projects_link_pr" {
			continue
		}
		if op.Method != "POST" || op.Path != "/issues/{id}/pull-requests" {
			t.Fatalf("link_pr: %s %s", op.Method, op.Path)
		}
		props := op.Schema["properties"].(mcpFields)
		if props["id"] == nil {
			t.Fatal("link_pr lacks the issue id")
		}
		data := props["data"].(map[string]any)
		fields := data["properties"].(mcpFields)
		for _, f := range []string{"url", "title", "branch", "repo", "number"} {
			if fields[f] == nil {
				t.Fatalf("link_pr lacks %s", f)
			}
		}
		if req, _ := data["required"].([]string); len(req) != 1 || req[0] != "url" {
			t.Fatalf("link_pr required: %v", data["required"])
		}
		return
	}
	t.Fatal("pocket_projects_link_pr missing")
}
