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
	if reads != 13 || len(tools) != 14 {
		t.Fatalf("league tools: %d reads of %d", reads, len(tools))
	}
	// answering for oneself is the only thing the AI may do: no chat, no results, no games planned or cancelled
	for name, op := range tools {
		if op.Method != "GET" && name != "pocket_league_answer" {
			t.Fatalf("unexpected league write %s %s", op.Method, op.Path)
		}
		for _, part := range []string{"/messages", "/matches/", "/cancel", "/votes", "/pick", "/teams", "/mvp", "/make", "/members/"} {
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
