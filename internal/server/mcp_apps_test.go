package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			r := f.s.requestMCPApp(context.Background(), &mcpAppTool{Method: "GET"}, upstream.URL, "test-token", "", nil, "")
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

// The only tools on a sharing route: the team's shared notes on a Projects
// issue (the word only), and copying one of your own notes there, which the
// person approves call by call. Nothing else may publish or share.
var mcpSharingAllowed = map[string]bool{
	"pocket_projects_shared_notes /issues/{id}/shared-notes":              true,
	"pocket_projects_get_shared_note /issues/{id}/shared-notes/{note}":    true,
	"pocket_projects_create_shared_note /issues/{id}/shared-notes":        true,
	"pocket_projects_update_shared_note /issues/{id}/shared-notes/{note}": true,
	"pocket_projects_share_note /issues/{id}/notes/{note}/share":          true,
	// files a memo into the person's own Notes/Tasks/Calendar/Money (a change: approved per call)
	"pocket_voice_send_memo /memos/{id}/send": true,
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
			if strings.Contains(op.Path, part) && !mcpSharingAllowed[op.Name+" "+op.Path] {
				t.Fatalf("administration/deletion route exposed: %s", op.Path)
			}
		}
	}
	for _, app := range strings.Fields("bookshelf calendar clips contacts day documents files garage habits hub inventory invoicing kitchen money movement notes office podcasts projects review split subtitles tasks travel") {
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

func TestMCPAIScheduleTools(t *testing.T) {
	var list, update *mcpAppTool
	for _, op := range mcpAppCatalog {
		switch op.Name {
		case "pocket_ai_schedules":
			list = op
		case "pocket_ai_update_schedule":
			update = op
		}
	}
	if list == nil || list.Method != "GET" || list.Path != "/schedules" {
		t.Fatalf("schedules list: %+v", list)
	}
	if update == nil || update.Method != "PUT" || update.Path != "/schedules/{schedule}" {
		t.Fatalf("update_schedule: %+v", update)
	}
	props := update.Schema["properties"].(mcpFields)
	if id := props["schedule"].(map[string]any); id["type"] != "integer" {
		t.Fatalf("schedule id must be an integer, got %v", id)
	}
	fields := props["data"].(map[string]any)["properties"].(mcpFields)
	for _, f := range []string{"name", "prompt", "spec", "notify", "enabled"} {
		if fields[f] == nil {
			t.Fatalf("update_schedule lacks %s", f)
		}
	}
	// Only what the schedule does and when; never how it runs or where.
	for _, f := range []string{"provider", "model", "effort", "mode", "speed", "cwd", "host_id"} {
		if fields[f] != nil {
			t.Fatalf("update_schedule must not change %s", f)
		}
	}
	if err := update.resolved.Validate(map[string]any{"schedule": 2, "request_id": "0123456789abcdef", "data": map[string]any{"mode": "bypassPermissions"}}); err == nil {
		t.Fatal("update_schedule accepted a permission-mode change")
	}
	var create *mcpAppTool
	for _, op := range mcpAppCatalog {
		if op.Name == "pocket_ai_create_schedule" {
			create = op
		}
	}
	if create == nil || create.Method != "POST" || create.Path != "/schedules" || create.Kind != "change" {
		t.Fatalf("create_schedule: %+v", create)
	}
	ok := map[string]any{"name": "PRs", "prompt": "Fix my PRs", "spec": "daily 06:00", "provider": "claude", "mode": "auto", "host_id": 2, "cwd": "/work/x"}
	if err := create.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("create_schedule refused a plain schedule: %v", err)
	}
	for _, mode := range []string{"bypassPermissions", "full-access"} {
		bad := map[string]any{"name": "x", "prompt": "y", "spec": "daily 06:00", "mode": mode}
		if err := create.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("create_schedule accepted mode %s", mode)
		}
	}
	// Conversation ids stay UUIDs.
	for _, op := range mcpAppCatalog {
		if op.Name == "pocket_ai_conversation" && op.Schema["properties"].(mcpFields)["id"].(map[string]any)["pattern"] == nil {
			t.Fatal("conversation id lost its UUID pattern")
		}
	}
}

func TestMCPProjectsConnectedWorkspace(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var paths []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path+" ws="+r.Header.Get("X-Pocket-Workspace"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":5,"identifier":"ATH-5","title":"Pilot"}`))
	}))
	defer fake.Close()
	f.app("projects", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read apps:write")
	session := mcpSessionWithToken(t, f.env, access)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || r.IsError {
			t.Fatalf("%s: %v %v", name, err, r)
		}
		return r.StructuredContent.(map[string]any)
	}
	there := "projects.athena.example"
	got := call("pocket_projects_get_issue", map[string]any{"id": "ATH-5", "workspace": there})
	if !strings.HasSuffix(got["source_url"].(string), "/r/"+there+"/issue/ATH-5") || !strings.HasSuffix(got["app_url"].(string), "/r/"+there+"/") {
		t.Fatalf("workspace links: %v %v", got["source_url"], got["app_url"])
	}
	call("pocket_projects_comment", map[string]any{"id": "ATH-5", "workspace": there, "request_id": "operation-ws-000001", "data": map[string]any{"body": "Done"}})
	call("pocket_projects_create_issue", map[string]any{"workspace": "Projects.Athena.Example", "request_id": "operation-ws-000002", "data": map[string]any{"title": "Follow-up", "team": "ATH"}})
	call("pocket_projects_create_project", map[string]any{"workspace": there, "request_id": "operation-ws-000003", "data": map[string]any{"name": "Pilot", "teams": []any{"ATH"}}})
	call("pocket_projects_post_update", map[string]any{"id": 4, "workspace": there, "request_id": "operation-ws-000004", "data": map[string]any{"body": "Fine", "health": "on_track", "project": "Pilot"}})
	own := call("pocket_projects_get_issue", map[string]any{"id": 5})
	call("pocket_projects_workspaces", map[string]any{})
	want := []string{"GET /api/v1/issues/ATH-5 ws=" + there, "POST /api/v1/issues/ATH-5/comments ws=" + there, "POST /api/v1/issues ws=" + there,
		"POST /api/v1/projects ws=" + there, "POST /api/v1/projects/4/updates ws=" + there, "GET /api/v1/issues/5 ws=", "GET /api/v1/workspaces ws="}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("paths:\n%s", strings.Join(paths, "\n"))
	}
	if strings.Contains(own["source_url"].(string), "/r/") {
		t.Fatalf("own instance link: %v", own["source_url"])
	}
	for _, bad := range []any{0, 3, "a/b", "../x", "x y", "host:99999999"} {
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_projects_get_issue", Arguments: map[string]any{"id": 5, "workspace": bad}})
		if err == nil && !r.IsError {
			t.Fatalf("workspace %v accepted", bad)
		}
	}
	for _, bad := range []any{"WEB-7/../x", "../5", "WEB 7", "-5"} {
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_projects_get_issue", Arguments: map[string]any{"id": bad}})
		if err == nil && !r.IsError {
			t.Fatalf("issue id %v accepted", bad)
		}
	}
	if len(paths) != len(want) {
		t.Fatal("an invalid workspace or id reached Projects")
	}
	for _, op := range mcpAppCatalog {
		if op.App != "projects" && op.Schema["properties"].(mcpFields)["workspace"] != nil {
			t.Fatalf("%s has a workspace", op.Name)
		}
	}
}

func TestMCPProjectsSharedNotes(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var seen []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":9,"title":"Plan"}`))
	}))
	defer fake.Close()
	f.app("projects", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read apps:write")
	session := mcpSessionWithToken(t, f.env, access)
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"pocket_projects_shared_notes", map[string]any{"id": 5, "query": map[string]any{"body": "1"}}},
		{"pocket_projects_get_shared_note", map[string]any{"id": 5, "note": 9}},
		{"pocket_projects_create_shared_note", map[string]any{"id": 5, "request_id": "operation-sn-000001", "data": map[string]any{"title": "Plan", "body": "Steps"}}},
		{"pocket_projects_update_shared_note", map[string]any{"id": 5, "note": 9, "request_id": "operation-sn-000002", "data": map[string]any{"body": "More", "base": "2026-10-30 01:00:00"}}},
		{"pocket_projects_share_note", map[string]any{"id": 5, "note": 401, "workspace": "projects.athena.example", "request_id": "operation-sn-000003", "data": map[string]any{}}},
	} {
		if r := call(c.name, c.args); r.IsError {
			t.Fatalf("%s: %v", c.name, r.Content)
		}
	}
	want := []string{
		"GET /api/v1/issues/5/shared-notes?body=1 ",
		"GET /api/v1/issues/5/shared-notes/9 ",
		`POST /api/v1/issues/5/shared-notes {"body":"Steps","title":"Plan"}`,
		`PATCH /api/v1/issues/5/shared-notes/9 {"base":"2026-10-30 01:00:00","body":"More"}`,
		"POST /api/v1/issues/5/notes/401/share {}",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
	// an edit must say which version it read, so a teammate's save is never overwritten blind
	if r := call("pocket_projects_update_shared_note", map[string]any{"id": 5, "note": 9, "request_id": "operation-sn-000004", "data": map[string]any{"body": "Blind"}}); !r.IsError || len(seen) != len(want) {
		t.Fatal("update without base went through")
	}
	for _, op := range mcpAppCatalog {
		if op.Name == "pocket_projects_share_note" && op.Kind != "change" {
			t.Fatalf("share_note kind %s: it can replace an earlier copy", op.Kind)
		}
		if strings.Contains(op.Name, "shared_note") && op.Method == "DELETE" {
			t.Fatal("shared notes are never deleted from a tool")
		}
	}
}

func TestMCPFitnessLogWorkout(t *testing.T) {
	var log *mcpAppTool
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "fitness" {
			if op.Method == "GET" {
				reads++
			}
			if op.Name == "pocket_fitness_log_workout" {
				log = op
			}
		}
	}
	if log == nil || log.Method != "POST" || log.Path != "/workouts/log" || log.Kind != "change" {
		t.Fatalf("log_workout: %+v", log)
	}
	if reads < 10 {
		t.Fatalf("fitness reads: %d", reads)
	}
	// "bench 3x8 at 80, pull-ups 10 and 8, a 5 km run in 26 min"
	ok := map[string]any{"name": "Push day", "date": "2026-11-05", "duration_min": 55, "feel": 4, "exercises": []any{
		map[string]any{"name": "bench", "sets": []any{map[string]any{"weight": 80, "reps": 8}, map[string]any{"weight": 80, "reps": 8}, map[string]any{"weight": 80, "reps": 8}}},
		map[string]any{"name": "Pull Up", "sets": []any{map[string]any{"reps": 10}, map[string]any{"reps": 8, "type": "failure"}}},
		map[string]any{"exercise_id": 70, "sets": []any{map[string]any{"distance_m": 5000, "duration_s": 1560}}},
	}}
	if err := log.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("log_workout refused a plain workout: %v", err)
	}
	for _, bad := range []map[string]any{
		{"name": "No exercises"},
		{"exercises": []any{}},
		{"exercises": []any{map[string]any{"name": "bench", "sets": []any{map[string]any{"weight": 80, "lbs": 176}}}}},
		{"exercises": []any{map[string]any{"name": "bench", "sets": []any{map[string]any{"reps": 8, "type": "superset"}}}}},
		{"feel": 9, "exercises": []any{map[string]any{"name": "bench", "sets": []any{map[string]any{"reps": 8}}}}},
	} {
		if err := log.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("log_workout accepted %v", bad)
		}
	}
}

func TestMCPNutritionLogFood(t *testing.T) {
	var log, water *mcpAppTool
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "nutrition" {
			if op.Method == "GET" {
				reads++
			}
			switch op.Name {
			case "pocket_nutrition_log_food":
				log = op
			case "pocket_nutrition_add_water":
				water = op
			}
		}
	}
	if log == nil || log.Method != "POST" || log.Path != "/entries/log" || log.Kind != "change" {
		t.Fatalf("log_food: %+v", log)
	}
	if water == nil || water.Method != "POST" || water.Path != "/water/{date}/add" || water.Kind != "change" {
		t.Fatalf("add_water: %+v", water)
	}
	if reads != 7 {
		t.Fatalf("nutrition reads: %d", reads)
	}
	// "two eggs and toast for breakfast, 60 g of oats, and grandma's pie"
	ok := map[string]any{"date": "2026-11-05", "meal": "breakfast", "ref": "voice-memo-812", "items": []any{
		map[string]any{"name": "egg", "qty": 2},
		map[string]any{"name": "White bread", "serving": "1 slice", "qty": 2},
		map[string]any{"name": "oats", "amount": 60, "unit": "g"},
		map[string]any{"name": "Grandma's pie", "kcal": 420, "protein": 12, "save": true, "meal": "snacks"},
		map[string]any{"food_id": 7, "qty": 1},
	}}
	if err := log.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("log_food refused a plain breakfast: %v", err)
	}
	for _, bad := range []map[string]any{
		{"meal": "breakfast"},
		{"items": []any{}},
		{"items": []any{map[string]any{"name": "egg", "grams": 50}}},
		{"items": []any{map[string]any{"name": "milk", "amount": 8, "unit": "oz"}}},
		{"meal": "brunch", "items": []any{map[string]any{"name": "egg", "qty": 2}}},
		{"date": "yesterday", "items": []any{map[string]any{"name": "egg", "qty": 2}}},
		{"items": []any{map[string]any{"name": "egg", "qty": 0}}},
		{"items": []any{map[string]any{"name": "pie", "kcal": -10}}},
	} {
		if err := log.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("log_food accepted %v", bad)
		}
	}
	if err := water.resolved.Validate(map[string]any{"date": "2026-11-05", "request_id": "0123456789abcdef", "data": map[string]any{"ml": 500}}); err != nil {
		t.Fatalf("add_water refused two glasses: %v", err)
	}
	for _, bad := range []map[string]any{
		{"date": "2026-11-05", "request_id": "0123456789abcdef", "data": map[string]any{}},
		{"date": "2026-11-05", "request_id": "0123456789abcdef", "data": map[string]any{"ml": 9000}},
		{"date": "today", "request_id": "0123456789abcdef", "data": map[string]any{"ml": 250}},
	} {
		if err := water.resolved.Validate(bad); err == nil {
			t.Fatalf("add_water accepted %v", bad)
		}
	}
}

func TestMCPCRMTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "crm" {
			tools[op.Name] = op
			if op.Method == "GET" {
				reads++
			}
		}
	}
	if reads != 10 || len(tools) != 12 {
		t.Fatalf("crm tools: %d reads of %d", reads, len(tools))
	}
	lead, note := tools["pocket_crm_add_lead"], tools["pocket_crm_add_note"]
	if lead == nil || lead.Method != "POST" || lead.Path != "/deals" || lead.Kind != "add" {
		t.Fatalf("add_lead: %+v", lead)
	}
	if note == nil || note.Method != "POST" || note.Path != "/deals/{id}/activities" || note.Kind != "add" {
		t.Fatalf("add_note: %+v", note)
	}
	// "Bistro Sara wants a website, about 4,500 euros; call Ana on Monday"
	ok := map[string]any{"title": "Website for Bistro Sara", "amount": 4500, "currency": "EUR", "contact_name": "Ana", "contact_email": "ana@bistro.example",
		"company": "Bistro Sara", "next_action": "Call Ana about the quote", "next_due": "2026-11-09", "stage": "Talking", "source": "referral", "note": "Met at the fair"}
	if err := lead.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("add_lead refused a plain lead: %v", err)
	}
	for _, bad := range []map[string]any{
		{"title": "x", "value": 450000},
		{"title": "x", "amount": -5},
		{"title": "x", "currency": "euro"},
		{"title": "x", "next_due": "Monday"},
		{"title": "x", "source": "linkedin"},
		{"title": "x", "stage_id": 2},
	} {
		if err := lead.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("add_lead accepted %v", bad)
		}
	}
	if err := note.resolved.Validate(map[string]any{"id": 12, "request_id": "0123456789abcdef", "data": map[string]any{"kind": "call", "title": "Called Ana", "body": "She liked the quote"}}); err != nil {
		t.Fatalf("add_note refused a call: %v", err)
	}
	if err := note.resolved.Validate(map[string]any{"id": 12, "request_id": "0123456789abcdef", "data": map[string]any{"kind": "won", "body": "x"}}); err == nil {
		t.Fatal("add_note accepted kind won: only the app writes those lines")
	}
}

func TestMCPLeagueTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "league" {
			tools[op.Name] = op
			if op.Method == "GET" {
				reads++
			}
		}
	}
	if reads != 15 || len(tools) != 16 {
		t.Fatalf("league tools: %d reads of %d", reads, len(tools))
	}
	// answering for oneself is the only thing the AI may do: no chat, no results, no games planned or cancelled
	for name, op := range tools {
		if op.Method != "GET" && name != "pocket_league_answer" {
			t.Fatalf("unexpected league write %s %s", op.Method, op.Path)
		}
		for _, part := range []string{"/messages", "/matches/", "/cancel", "/votes", "/pick", "/teams", "/mvp", "/make", "/members/", "/tournaments", "/tmatches"} {
			if op.Method != "GET" && strings.Contains(op.Path, part) {
				t.Fatalf("league write on %s", op.Path)
			}
		}
	}
	ans := tools["pocket_league_answer"]
	if ans == nil || ans.Method != "POST" || ans.Path != "/games/{id}/answer" || ans.Kind != "change" {
		t.Fatalf("answer: %+v", ans)
	}
	// "I'm in for Tuesday"
	for _, ok := range []map[string]any{{"answer": "in"}, {"answer": "maybe", "note": "10 min late"}, {"answer": "out"}, {"answer": ""}} {
		if err := ans.resolved.Validate(map[string]any{"id": 31, "request_id": "0123456789abcdef", "data": ok}); err != nil {
			t.Fatalf("answer refused %v: %v", ok, err)
		}
	}
	for _, bad := range []map[string]any{
		{"answer": "waiting"},
		{"answer": "yes"},
		{"note": "x"},
		{"answer": "in", "user_id": 2},
		{"answer": "in", "body": "see you there"},
		{"answer": "in", "note": strings.Repeat("x", 121)},
	} {
		if err := ans.resolved.Validate(map[string]any{"id": 31, "request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("answer accepted %v", bad)
		}
	}
	if tools["pocket_league_player"] == nil || tools["pocket_league_player"].Path != "/groups/{id}/players/{uid}" {
		t.Fatal("player read missing")
	}
}

// Tournaments are reads only: the list (all groups, or one group's with group_id) and one tournament in full.
func TestMCPLeagueTournaments(t *testing.T) {
	list, one := mcpAppCatalog[0], mcpAppCatalog[0]
	for _, op := range mcpAppCatalog {
		switch op.Name {
		case "pocket_league_tournaments":
			list = op
		case "pocket_league_get_tournament":
			one = op
		}
	}
	if list.Name != "pocket_league_tournaments" || list.Method != "GET" || list.Path != "/tournaments" || list.Within != "/groups/{group_id}/tournaments" || list.Kind != "read" {
		t.Fatalf("tournaments: %+v", list)
	}
	if one.Name != "pocket_league_get_tournament" || one.Method != "GET" || one.Path != "/tournaments/{id}" || one.Kind != "read" {
		t.Fatalf("get_tournament: %+v", one)
	}
	for _, bad := range []map[string]any{{"group_id": 0}, {"group_id": "../x"}, {"status": "live"}} {
		if list.resolved.Validate(bad) == nil {
			t.Fatalf("tournaments accepted %v", bad)
		}
	}
	if one.resolved.Validate(map[string]any{}) == nil {
		t.Fatal("get_tournament without id")
	}
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var paths []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/tournaments/7" {
			w.Write([]byte(`{"tournament":{"id":7,"name":"Autumn Americano","status":"live"},"groups":[{"id":40}],"mine":[90],"podium":[]}`))
			return
		}
		w.Write([]byte(`[{"id":7,"group_id":2,"name":"Autumn Americano","status":"live","mine":true}]`))
	}))
	defer fake.Close()
	f.app("league", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read")
	session := mcpSessionWithToken(t, f.env, access)
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"pocket_league_tournaments", map[string]any{}},
		{"pocket_league_tournaments", map[string]any{"group_id": 2}},
		{"pocket_league_get_tournament", map[string]any{"id": 7}},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil || result.IsError {
			t.Fatalf("%s %v: %v %+v", call.name, call.args, err, result)
		}
		if raw, _ := json.Marshal(result); !strings.Contains(string(raw), "Autumn Americano") {
			t.Fatalf("%s: %s", call.name, raw)
		}
	}
	if strings.Join(paths, " ") != "/api/v1/tournaments /api/v1/groups/2/tournaments /api/v1/tournaments/7" {
		t.Fatalf("league calls: %v", paths)
	}
}

func TestMCPTablesTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "tables" {
			tools[op.Name] = op
			if op.Method == "GET" {
				reads++
			}
		}
	}
	if reads != 9 || len(tools) != 10 {
		t.Fatalf("tables tools: %d reads of %d", reads, len(tools))
	}
	add := tools["pocket_tables_add_row"]
	if add == nil || add.Method != "POST" || add.Path != "/tables/{id}/records" || add.Kind != "add" {
		t.Fatalf("add_row: %+v", add)
	}
	// "Add Fern to my plants, water every 7 days, next on Monday"
	ok := map[string]any{"values": map[string]any{"Name": "Fern", "Every (days)": 7, "Next water": "2026-11-09", "Room": "Kitchen", "Tags": []any{"shade", "easy"}, "Indoor": true}}
	if err := add.resolved.Validate(map[string]any{"id": 3, "request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("add_row refused a plain row: %v", err)
	}
	for _, bad := range []map[string]any{
		{},
		{"values": map[string]any{}},
		{"values": map[string]any{"Name": "Fern"}, "typecast": false},
		{"values": map[string]any{"Name": map[string]any{"nested": 1}}},
		{"fields": map[string]any{"Name": "Fern"}},
	} {
		if err := add.resolved.Validate(map[string]any{"id": 3, "request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("add_row accepted %v", bad)
		}
	}
	if tools["pocket_tables_rows"].resolved.Validate(map[string]any{"id": 3, "query": map[string]any{"view": "Grid"}}) == nil {
		t.Fatal("rows took a view by name: it is the view's id")
	}

	// rows always ask for values by field name; a row added always has its select names read (typecast)
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var seen []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":42,"table_id":3,"number":2,"title":"Fern","values":{}}`))
			return
		}
		w.Write([]byte(`{"records":[{"id":41,"table_id":3,"number":1,"title":"Fern","named":{"Name":"Fern"}}],"total":1,"table":"Plants"}`))
	}))
	defer fake.Close()
	f.app("tables", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read apps:write")
	session := mcpSessionWithToken(t, f.env, access)
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"pocket_tables_rows", map[string]any{"id": 3, "query": map[string]any{"view": 5, "q": "fern"}}},
		{"pocket_tables_add_row", map[string]any{"id": 3, "request_id": "operation-tb-000001", "data": map[string]any{"values": map[string]any{"Name": "Fern", "Room": "Kitchen"}}}},
	} {
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil || r.IsError {
			t.Fatalf("%s: %v %+v", c.name, err, r)
		}
		if raw, _ := json.Marshal(r); !strings.Contains(string(raw), `/r/4`) {
			t.Fatalf("%s: no row link: %s", c.name, raw)
		}
	}
	want := []string{
		"GET /api/v1/tables/3/records?limit=50&named=1&q=fern&view=5 ",
		`POST /api/v1/tables/3/records {"typecast":true,"values":{"Name":"Fern","Room":"Kitchen"}}`,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
}

func TestMCPRentalsTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "rentals" {
			tools[op.Name] = op
			if op.Method == "GET" {
				reads++
			}
		}
	}
	if reads != 12 || len(tools) != 14 {
		t.Fatalf("rentals tools: %d reads of %d", reads, len(tools))
	}
	// Every tool stays inside what Pocket Rentals lets a linked app do (internal/server/links.go linkRoutes there).
	allowed := map[string]*regexp.Regexp{
		"GET":  regexp.MustCompile(`^/api/v1/((properties|units|tenants|leases|requests|expenses|inspections|summary|today|rent|reports|search|lookup|day/\d{4}-\d{2}-\d{2})|(properties|tenants|leases|requests|inspections)/\d+)$`),
		"POST": regexp.MustCompile(`^/api/v1/(requests|entries)$`),
	}
	for name, op := range tools {
		if re := allowed[op.Method]; re == nil || !re.MatchString("/api/v1"+strings.ReplaceAll(op.Path, "{id}", "12")) {
			t.Fatalf("%s: %s %s is not a route Rentals opens to linked apps", name, op.Method, op.Path)
		}
	}
	repair, note := tools["pocket_rentals_report_repair"], tools["pocket_rentals_add_note"]
	if repair == nil || repair.Method != "POST" || repair.Path != "/requests" || repair.Kind != "add" {
		t.Fatalf("report_repair: %+v", repair)
	}
	if note == nil || note.Method != "POST" || note.Path != "/entries" || note.Kind != "add" {
		t.Fatalf("add_note: %+v", note)
	}
	// "The kitchen tap in flat 2 is dripping, Ana says since Monday"
	if err := repair.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"property_id": 2, "unit_id": 11, "title": "Kitchen tap drips", "details": "Ana says since Monday", "priority": "normal"}}); err != nil {
		t.Fatalf("report_repair refused a plain repair: %v", err)
	}
	for _, bad := range []map[string]any{
		{"title": "Tap"},
		{"property_id": 2},
		{"property_id": 2, "title": "Tap", "priority": "asap"},
		{"property_id": 2, "title": "Tap", "status": "done"},
		{"property_id": 2, "title": "Tap", "cost": 5000},
		{"property_id": "Main Street", "title": "Tap"},
	} {
		if err := repair.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("report_repair accepted %v", bad)
		}
	}
	if err := note.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"lease_id": 4, "kind": "call", "title": "Called Ana about the rent", "body": "Pays on Friday"}}); err != nil {
		t.Fatalf("add_note refused a call: %v", err)
	}
	for _, bad := range []map[string]any{
		{"title": "No lease, place or repair"},
		{"lease_id": 4, "kind": "lease", "title": "x"},
		{"lease_id": 4, "kind": "note", "title": "x", "at": "2026-11-07"},
	} {
		if err := note.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("add_note accepted %v", bad)
		}
	}
	if tools["pocket_rentals_rent"].resolved.Validate(map[string]any{"query": map[string]any{"month": "November"}}) == nil {
		t.Fatal("rent took a month by name: it is YYYY-MM")
	}

	// repairs always asks only for the open ones
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var seen []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":7,"property_id":2,"title":"Kitchen tap drips","priority":"urgent","status":"new"}]`))
	}))
	defer fake.Close()
	f.app("rentals", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read apps:write")
	session := mcpSessionWithToken(t, f.env, access)
	r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_rentals_repairs", Arguments: map[string]any{"query": map[string]any{"property": "2"}}})
	if err != nil || r.IsError {
		t.Fatalf("repairs: %v %+v", err, r)
	}
	if raw, _ := json.Marshal(r); !strings.Contains(string(raw), `/repairs/7`) {
		t.Fatalf("repairs: no repair link: %s", raw)
	}
	if strings.Join(seen, "\n") != "GET /api/v1/requests?limit=50&open=1&property=2" {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
}

func TestMCPTrailsTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "trails" {
			tools[op.Name] = op
			if op.Method == "GET" {
				reads++
			}
		}
	}
	if reads != 11 || len(tools) != 13 {
		t.Fatalf("trails tools: %d reads of %d", reads, len(tools))
	}
	// Reads and plan_outing stay inside what Pocket Trails opens to linked apps (internal/server/links.go linkRoutes there).
	// outing_status is the one more: Trails keeps status changes from linked apps, but these tools call it with the
	// person's own (pcu_) token, as the person's phone does for "I'm back".
	allowed := map[string]*regexp.Regexp{
		"GET":  regexp.MustCompile(`^/api/v1/((summary|today|stats|routes|outings|gear|packs|search|day/\d{4}-\d{2}-\d{2})|(routes|outings)/\d+)$`),
		"POST": regexp.MustCompile(`^/api/v1/(outings|outings/\d+/status/(start|back|done|cancel))$`),
	}
	for name, op := range tools {
		path := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(op.Path, "{id}", "12"), "{date}", "2026-11-08"), "{action}", "back")
		if re := allowed[op.Method]; re == nil || !re.MatchString("/api/v1"+path) {
			t.Fatalf("%s: %s %s is not a route for Trails' tools", name, op.Method, op.Path)
		}
		if strings.Join(op.Omit, " ") != "track actual_track controls preview" {
			t.Fatalf("%s: map tracks are not left out: %v", name, op.Omit)
		}
	}
	plan, status := tools["pocket_trails_plan_outing"], tools["pocket_trails_outing_status"]
	if plan == nil || plan.Method != "POST" || plan.Path != "/outings" || plan.Kind != "add" || plan.Body["safety"] != false {
		t.Fatalf("plan_outing: %+v", plan)
	}
	if status == nil || status.Method != "POST" || status.Kind != "change" {
		t.Fatalf("outing_status: %+v", status)
	}
	// "We hike Trebević on Saturday at 8 with Ana, back by five"
	ok := map[string]any{"route_id": 3, "date": "2026-11-14", "start_time": "08:00", "back_by": "2026-11-14T17:00", "people": []any{map[string]any{"name": "Ana", "email": "ana@example.com"}}, "notes": "Meet at the cable car"}
	if err := plan.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("plan_outing refused a plain plan: %v", err)
	}
	if err := plan.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"title": "Run by the river", "activity": "run", "date": "2026-11-09"}}); err != nil {
		t.Fatalf("plan_outing refused an outing without a route: %v", err)
	}
	for _, bad := range []map[string]any{
		{"route_id": 3},
		{"date": "2026-11-14"},
		{"route_id": 3, "date": "Saturday"},
		{"route_id": 3, "date": "2026-11-14", "start_time": "8am"},
		{"route_id": 3, "date": "2026-11-14", "back_by": "17:00"},
		{"route_id": 3, "date": "2026-11-14", "safety": true},
		{"route_id": 3, "date": "2026-11-14", "contact_email": "mum@example.com"},
		{"route_id": 3, "date": "2026-11-14", "alert_after": 60},
		{"route_id": "Trebević", "date": "2026-11-14"},
		{"route_id": 3, "date": "2026-11-14", "activity": "swim"},
		{"route_id": 3, "date": "2026-11-14", "people": []any{map[string]any{"email": "ana@example.com"}}},
	} {
		if err := plan.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("plan_outing accepted %v", bad)
		}
	}
	for _, action := range []string{"start", "back", "done", "cancel"} {
		if err := status.resolved.Validate(map[string]any{"id": 14, "action": action, "request_id": "0123456789abcdef", "data": map[string]any{}}); err != nil {
			t.Fatalf("outing_status refused %s: %v", action, err)
		}
	}
	for _, action := range []any{"plan", "delete", "../x", 1} {
		if status.resolved.Validate(map[string]any{"id": 14, "action": action, "request_id": "0123456789abcdef", "data": map[string]any{}}) == nil {
			t.Fatalf("outing_status took %v", action)
		}
	}
	if tools["pocket_trails_outings"].resolved.Validate(map[string]any{"query": map[string]any{"status": "planned,soon"}}) == nil {
		t.Fatal("outings took a status Trails does not have")
	}
	if tools["pocket_trails_stats"].resolved.Validate(map[string]any{"query": map[string]any{"year": "this year"}}) == nil {
		t.Fatal("stats took a year in words: it is YYYY")
	}

	// tracks never reach the assistant (even one too big to pass whole); a plan never arms "tell someone"; "I'm back" goes to its status
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var seen []string
	long := make([]string, 30000)
	for i := range long {
		long[i] = "[43.851234,18.412345,1150.5,1793664000]"
	}
	track := "[" + strings.Join(long, ",") + "]"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		page := `{"outing":{"id":14,"route_id":3,"status":"planned","actual_track":` + track + `,"items":[{"id":300,"name":"Rain jacket","packed":false}]},"route":{"id":3,"name":"Trebević","distance_m":11800,"track":` + track + `,"controls":[[43.85,18.41]],"preview":[[43.85,18.41]],"waypoints":[{"id":40,"kind":"hut","name":"Pino","at_m":5200}]},"advice":[{"level":"caution","icon":"rain","text":"Rain from 14:00"}],"note":""}`
		if r.Method == "POST" && r.URL.Path == "/api/v1/outings" {
			w.WriteHeader(http.StatusCreated)
		}
		if r.URL.Path == "/api/v1/routes/3" {
			page = `{"route":{"id":3,"name":"Trebević","track":` + track + `,"waypoints":[{"id":40,"kind":"hut","name":"Pino","at_m":5200}]},"outings":[{"id":14}]}`
		}
		w.Write([]byte(page))
	}))
	defer fake.Close()
	f.app("trails", fake.URL, "all")
	access, _ := f.appAccess("status:read apps:read apps:write")
	session := mcpSessionWithToken(t, f.env, access)
	for _, c := range []struct {
		name string
		args map[string]any
		link string
	}{
		{"pocket_trails_get_route", map[string]any{"id": 3}, "/outings/14"},
		{"pocket_trails_get_outing", map[string]any{"id": 14}, "/routes/3"},
		{"pocket_trails_plan_outing", map[string]any{"request_id": "operation-tr-000001", "data": map[string]any{"route_id": 3, "date": "2026-11-14", "start_time": "08:00", "back_by": "2026-11-14T17:00"}}, "/outings/14"},
		{"pocket_trails_outing_status", map[string]any{"id": 14, "action": "back", "request_id": "operation-tr-000002", "data": map[string]any{}}, "/outings/14"},
	} {
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil || r.IsError {
			t.Fatalf("%s: %v %+v", c.name, err, r)
		}
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "1793664000") || strings.Contains(string(raw), `"track"`) || strings.Contains(string(raw), `"preview"`) || strings.Contains(string(raw), `"controls"`) {
			t.Fatalf("%s: a map track reached the assistant (%d bytes)", c.name, len(raw))
		}
		if !strings.Contains(string(raw), `"Pino"`) || !strings.Contains(string(raw), c.link) {
			t.Fatalf("%s: spots or links missing: %s", c.name, raw)
		}
	}
	want := []string{
		"GET /api/v1/routes/3 ",
		"GET /api/v1/outings/14 ",
		`POST /api/v1/outings {"back_by":"2026-11-14T17:00","date":"2026-11-14","route_id":3,"safety":false,"start_time":"08:00"}`,
		"POST /api/v1/outings/14/status/back {}",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
}

func TestMCPJobsTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	reads := 0
	for _, op := range mcpAppCatalog {
		if op.App == "jobs" {
			tools[op.Name] = op
			if op.Method == "GET" {
				reads++
			}
		}
	}
	if reads != 11 || len(tools) != 14 {
		t.Fatalf("jobs tools: %d reads of %d", reads, len(tools))
	}
	add, note, iv := tools["pocket_jobs_add_job"], tools["pocket_jobs_add_note"], tools["pocket_jobs_add_interview"]
	if add == nil || add.Path != "/jobs" || add.Kind != "add" || note == nil || note.Path != "/jobs/{id}/activities" || iv == nil || iv.Path != "/jobs/{id}/interviews" || iv.Kind != "add" {
		t.Fatalf("jobs writes: %+v %+v %+v", add, note, iv)
	}
	ok := map[string]any{"title": "Senior Go developer", "company": "Northwind", "location": "Berlin", "work_mode": "remote", "employment": "full_time",
		"url": "https://jobs.example/1", "min_amount": 70000, "max_amount": 85000, "currency": "EUR", "salary_period": "year", "source": "linkedin",
		"excitement": 4, "apply_by": "2026-12-01", "stage": "applied", "contact_name": "Julia", "contact_email": "julia@example.com",
		"next_action": "Follow up", "next_due": "2026-11-15", "note": "Found on LinkedIn"}
	if err := add.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("add_job refused a plain job: %v", err)
	}
	for _, bad := range []map[string]any{
		{"company": "x"},
		{"title": "x", "salary_min": 7000000},
		{"title": "x", "min_amount": -5},
		{"title": "x", "excitement": 9},
		{"title": "x", "work_mode": "office"},
		{"title": "x", "apply_by": "Friday"},
		{"title": "x", "currency": "euro"},
	} {
		if err := add.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("add_job accepted %v", bad)
		}
	}
	if err := note.resolved.Validate(map[string]any{"id": 3, "request_id": "0123456789abcdef", "data": map[string]any{"kind": "call", "title": "Called Julia"}}); err != nil {
		t.Fatalf("add_note refused a call: %v", err)
	}
	if err := note.resolved.Validate(map[string]any{"id": 3, "request_id": "0123456789abcdef", "data": map[string]any{"kind": "hired", "title": "x"}}); err == nil {
		t.Fatal("add_note accepted kind hired: only the app writes those lines")
	}
	good := map[string]any{"kind": "video", "date": "2026-11-12", "time": "14:30", "minutes": 45, "location": "https://meet.example/x", "with_people": "Julia"}
	if err := iv.resolved.Validate(map[string]any{"id": 3, "request_id": "0123456789abcdef", "data": good}); err != nil {
		t.Fatalf("add_interview refused: %v", err)
	}
	for _, bad := range []map[string]any{{"kind": "video"}, {"date": "2026-11-12", "time": "2pm"}, {"date": "2026-11-12", "kind": "chat"}, {"date": "2026-11-12", "minutes": 2}} {
		if err := iv.resolved.Validate(map[string]any{"id": 3, "request_id": "0123456789abcdef", "data": bad}); err == nil {
			t.Fatalf("add_interview accepted %v", bad)
		}
	}
}

func TestMCPPhotoLabTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	for _, op := range mcpAppCatalog {
		if op.App == "photolab" {
			tools[op.Name] = op
		}
	}
	apply, add := tools["pocket_photolab_apply"], tools["pocket_photolab_add_pictures"]
	if len(tools) != 7 || apply == nil || apply.Kind != "change" || apply.Path != "/images/apply" || add == nil || add.Kind != "add" {
		t.Fatalf("photolab tools: %d %+v %+v", len(tools), apply, add)
	}
	ok := map[string]any{"ids": []any{3, 4}, "look": "film", "strength": 60, "adjust": map[string]any{"exposure": 20}, "aspect": "4:5", "rotate": 90, "black_and_white": true, "long_edge": 1080}
	if err := apply.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": ok}); err != nil {
		t.Fatalf("apply refused a plain change: %v", err)
	}
	for _, bad := range []map[string]any{{"look": "film"}, {"ids": []any{1}, "aspect": "7:3"}, {"ids": []any{1}, "rotate": 45}, {"ids": []any{1}, "url": "x"}} {
		if apply.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": bad}) == nil {
			t.Fatalf("apply took %v", bad)
		}
	}
	if err := add.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"source": "photos", "ids": []any{"0b1c2d3e-0000-4000-8000-000000000001"}}}); err != nil {
		t.Fatalf("add_pictures: %v", err)
	}
}

func TestMCPRoomsTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	for _, op := range mcpAppCatalog {
		if op.App == "rooms" {
			tools[op.Name] = op
		}
	}
	fit, piece := tools["pocket_rooms_fit"], tools["pocket_rooms_add_piece"]
	if len(tools) != 13 || fit == nil || fit.Kind != "read" || fit.Path != "/fit" || piece == nil || piece.Kind != "add" {
		t.Fatalf("rooms tools: %d %+v %+v", len(tools), fit, piece)
	}
	if err := piece.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"name": "Sofa", "width": 228, "depth": 95, "height": 83, "status": "want"}}); err != nil {
		t.Fatalf("add_piece refused a sofa: %v", err)
	}
	if piece.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"name": "Sofa"}}) == nil {
		t.Fatal("add_piece took a piece without its size")
	}
}

func TestMCPPuzzlecraftTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	for _, op := range mcpAppCatalog {
		if op.App == "puzzlecraft" {
			tools[op.Name] = op
		}
	}
	mk := tools["pocket_puzzlecraft_make_puzzle"]
	if len(tools) != 15 || mk == nil || mk.Kind != "add" || tools["pocket_puzzlecraft_today"] == nil {
		t.Fatalf("puzzlecraft tools: %d %+v", len(tools), mk)
	}
	if err := mk.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"kind": "sudoku", "level": "hard"}}); err != nil {
		t.Fatalf("make_puzzle refused a sudoku: %v", err)
	}
	if mk.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"kind": "chess"}}) == nil {
		t.Fatal("make_puzzle took an unknown kind")
	}
}

func TestMCPForumTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	for _, op := range mcpAppCatalog {
		if op.App == "forum" {
			tools[op.Name] = op
		}
	}
	st := tools["pocket_forum_start_topic"]
	if len(tools) != 13 || st == nil || st.Kind != "add" || tools["pocket_forum_reply"] == nil || tools["pocket_forum_topics"] == nil {
		t.Fatalf("forum tools: %d %+v", len(tools), st)
	}
	if err := st.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"forum": "family", "title": "Sunday lunch", "body": "Who is in?"}}); err != nil {
		t.Fatalf("start_topic refused a topic: %v", err)
	}
	if st.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"forum": "family", "title": "x", "body": "y", "kind": "announcement"}}) == nil {
		t.Fatal("start_topic took an announcement")
	}
}

func TestMCPCapsuleTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	for _, op := range mcpAppCatalog {
		if op.App == "capsule" {
			tools[op.Name] = op
		}
	}
	add := tools["pocket_capsule_add_to_capsule"]
	if len(tools) != 10 || add == nil || add.Kind != "add" || tools["pocket_capsule_start_capsule"] == nil || tools["pocket_capsule_capsule"] == nil {
		t.Fatalf("capsule tools: %d %+v", len(tools), add)
	}
	if err := add.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "id": 3, "data": map[string]any{"kind": "letter", "body": "Dear future me"}}); err != nil {
		t.Fatalf("add_to_capsule refused a letter: %v", err)
	}
	if add.resolved.Validate(map[string]any{"request_id": "0123456789abcdef", "id": 3, "data": map[string]any{"kind": "seal"}}) == nil {
		t.Fatal("add_to_capsule took an unknown kind")
	}
	for name := range tools {
		if strings.Contains(name, "seal") || strings.Contains(name, "open") {
			t.Fatalf("a tool may never seal or open a capsule: %s", name)
		}
	}
}

func TestMCPSlidesTools(t *testing.T) {
	tools := map[string]*mcpAppTool{}
	for _, op := range mcpAppCatalog {
		if op.App == "slides" {
			tools[op.Name] = op
		}
	}
	mk := tools["pocket_slides_make_deck"]
	if len(tools) != 11 || mk == nil || mk.Kind != "add" || tools["pocket_slides_add_slide"] == nil || tools["pocket_slides_deck"] == nil {
		t.Fatalf("slides tools: %d %+v", len(tools), mk)
	}
	ok := map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"title": "Team update", "slides": []any{
		map[string]any{"layout": "title", "title": "Team update"},
		map[string]any{"layout": "poll", "title": "Which first?", "data": map[string]any{"options": []any{"A", "B"}, "correct": 1}},
	}}}
	if err := mk.resolved.Validate(ok); err != nil {
		t.Fatalf("make_deck refused a deck: %v", err)
	}
	bad := map[string]any{"request_id": "0123456789abcdef", "data": map[string]any{"title": "x", "slides": []any{map[string]any{"layout": "video"}}}}
	if mk.resolved.Validate(bad) == nil {
		t.Fatal("make_deck took an unknown layout")
	}
	for name := range tools {
		if strings.Contains(name, "present") || strings.Contains(name, "share") || strings.Contains(name, "delete") {
			t.Fatalf("a tool may never present, share or delete: %s", name)
		}
	}
}
