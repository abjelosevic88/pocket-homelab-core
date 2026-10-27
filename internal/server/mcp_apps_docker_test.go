package server

// Optional compatibility test against actual application images. Every app runs
// with an empty tmpfs, a temporary Core, and test identities; no production data
// or credentials are mounted. Run with POCKET_MCP_DOCKER_TEST=1.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

func TestMCPApplicationDockerCompatibility(t *testing.T) {
	if os.Getenv("POCKET_MCP_DOCKER_TEST") != "1" {
		t.Skip("set POCKET_MCP_DOCKER_TEST=1 with local Pocket application images")
	}
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	access, _ := f.appAccess("status:read apps:read apps:write")
	secret, err := f.st.GetMCPSecret(context.Background(), access, "access", "")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := f.st.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slugs := []string{"notes", "tasks", "calendar", "habits", "day", "inventory", "bookshelf", "clips", "contacts", "documents", "files", "garage", "kitchen", "money", "split", "invoicing", "review", "travel", "office", "podcasts", "subtitles", "hub"}
	ctx := context.WithValue(context.Background(), mcpAccountKey{}, mcpAccount{UserID: f.user.ID, GrantID: secret.GrantID, Scope: "status:read apps:read apps:write"})
	ctx = context.WithValue(ctx, mcpClientKey{}, config.MCPClient{ID: "compatibility-test", Apps: slugs})
	for _, slug := range slugs {
		t.Run(slug, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			origin := fmt.Sprintf("http://127.0.0.1:%d", port)
			name := fmt.Sprintf("pocket-mcp-test-%s-%d", slug, port)
			cmd := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "host", "--tmpfs", "/data:rw,mode=1777", "-e", "DATA_DIR=/data", "-e", "MEDIA_DIRS=/data/media", "-e", "HOST=127.0.0.1", "-e", fmt.Sprintf("PORT=%d", port), "-e", "BASE_URL="+origin, "-e", "CORE_APP_URL="+origin, "-e", "CORE_URL="+f.srv.URL, "-e", "CORE_REGISTRATION_KEY="+settings.RegistrationKey, "-e", "AUTH_MODE=local", "-e", "ADMIN_EMAIL=fixture-admin@example.test", "-e", "ADMIN_PASSWORD=test-only-long-password", "pocket-"+slug+":local")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("start isolated %s: %s %v", slug, out, err)
			}
			t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
			var app *store.App
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
				app, err = f.st.GetAppBySlug(ctx, slug)
				if err != nil {
					continue
				}
				client := http.Client{Timeout: time.Second}
				res, e := client.Get(origin + "/healthz")
				if e == nil {
					res.Body.Close()
					if res.StatusCode == 200 {
						break
					}
				}
			}
			if app == nil {
				t.Fatal("isolated app did not register")
			}
			sv, err := f.st.GetServiceByApp(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			sv.Enabled, sv.Audience = true, "all"
			if err := f.st.SaveService(ctx, sv, func(string) bool { return false }); err != nil {
				t.Fatal(err)
			}

			fixture := func(method, path, contentType, body string) any {
				t.Helper()
				token, err := f.st.CreateMCPAppToken(ctx, secret.GrantID, mcpAuthorization{Resource: f.s.mcpResource(), Scope: "status:read apps:read apps:write", Target: slug})
				if err != nil {
					t.Fatal(err)
				}
				defer f.st.DeleteMCPAppToken(ctx, token)
				req, _ := http.NewRequest(method, origin+"/api/v1"+path, strings.NewReader(body))
				req.Header.Set("Content-Type", contentType)
				req.Header.Set("Authorization", "Bearer "+token)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				raw, _ := io.ReadAll(res.Body)
				if res.StatusCode < 200 || res.StatusCode >= 300 {
					t.Fatalf("test fixture %s: %d %s", path, res.StatusCode, raw)
				}
				var value any
				if json.Unmarshal(raw, &value) != nil {
					t.Fatalf("non-JSON fixture response: %s", raw)
				}
				return value
			}
			count := 0
			call := func(name string, args map[string]any) any {
				t.Helper()
				var op *mcpAppTool
				for _, candidate := range mcpAppCatalog {
					if candidate.Name == "pocket_"+slug+"_"+name {
						op = candidate
						break
					}
				}
				if op == nil {
					t.Fatalf("missing tool %s", name)
				}
				if args == nil {
					args = map[string]any{}
				}
				if op.Method != "GET" {
					count++
					args["request_id"] = fmt.Sprintf("compatibility-%s-%04d", slug, count)
				}
				raw, _ := json.Marshal(args)
				result := f.s.callMCPApp(ctx, op, raw)
				if result.IsError {
					b, _ := json.Marshal(result.StructuredContent)
					t.Fatalf("%s: %s", op.Name, b)
				}
				return result.StructuredContent.(map[string]any)["result"]
			}
			// Exercise every collection read that does not require an existing item.
			for _, op := range mcpAppCatalog {
				if op.App == slug && op.Method == "GET" && !strings.Contains(op.Path, "{") && !(slug == "files" && op.Path != "/spaces") && !(slug == "office" && strings.Contains(op.Path, "/sources/")) {
					if op.Path == "/search" {
						continue
					}
					args := map[string]any{}
					if slug == "calendar" && strings.HasSuffix(op.Path, "/events") {
						args["query"] = map[string]any{"from": "2026-10-01", "to": "2026-11-01"}
					}
					call(strings.TrimPrefix(op.Name, "pocket_"+slug+"_"), args)
				}
			}
			write := func(name string, data map[string]any) any { return call(name, map[string]any{"data": data}) }
			id := func(value any) any {
				m := value.(map[string]any)
				if b, ok := m["book"].(map[string]any); ok {
					m = b
				}
				return m["id"]
			}
			switch slug {
			case "notes":
				n := write("create", map[string]any{"title": "MCP test", "content": "Original"})
				nid := id(n)
				call("append", map[string]any{"id": nid, "data": map[string]any{"text": "More"}})
				current := call("get", map[string]any{"id": nid}).(map[string]any)
				call("update", map[string]any{"id": nid, "data": map[string]any{"title": "Updated", "etag": current["etag"]}})
				other := &store.User{Email: "other-fixture@example.test", Name: "Other", Role: "member"}
				if err := f.st.CreateUser(ctx, other); err != nil {
					t.Fatal(err)
				}
				code, err := f.st.CreateMCPGrantCode(ctx, f.clientID, other, mcpAuthorization{Resource: f.s.mcpResource(), Scope: "status:read apps:read apps:write"}, "status:read apps:read apps:write")
				if err != nil {
					t.Fatal(err)
				}
				grant, err := f.st.GetMCPSecret(ctx, code, "code", f.clientID)
				if err != nil {
					t.Fatal(err)
				}
				otherCtx := context.WithValue(ctx, mcpAccountKey{}, mcpAccount{UserID: other.ID, GrantID: grant.GrantID, Scope: "status:read apps:read apps:write"})
				for _, op := range mcpAppCatalog {
					if op.Name == "pocket_notes_get" || op.Name == "pocket_notes_append" {
						arguments := map[string]any{"id": nid}
						if op.Method != "GET" {
							arguments["request_id"] = "other-user-denied-0001"
							arguments["data"] = map[string]any{"text": "Unauthorized"}
						}
						raw, _ := json.Marshal(arguments)
						if !f.s.callMCPApp(otherCtx, op, raw).IsError {
							t.Fatal("application allowed another user to access a private note")
						}
					}
				}
			case "tasks":
				n := write("create", map[string]any{"title": "MCP test"})
				call("update", map[string]any{"id": id(n), "data": map[string]any{"notes": "Updated"}})
				call("complete", map[string]any{"id": id(n), "data": map[string]any{"done": true}})
			case "calendar":
				calendars := call("calendars", nil).([]any)
				n := write("create_event", map[string]any{"calendar_id": id(calendars[0]), "title": "MCP test", "start": "2026-10-21T09:00:00", "end": "2026-10-21T09:30:00", "tz": "Europe/Sarajevo"})
				call("update_event", map[string]any{"id": id(n), "data": map[string]any{"description": "Updated"}})
			case "contacts":
				books := call("addressbooks", nil).([]any)
				n := write("create", map[string]any{"addressbook_id": id(books[0]), "card": map[string]any{"fn": "MCP Test", "emails": []any{map[string]any{"value": "test@example.test"}}}})
				call("label", map[string]any{"id": id(n), "data": map[string]any{"starred": true}})
			case "files":
				write("mkdir", map[string]any{"space": "home", "path": "mcp"})
				write("save_text", map[string]any{"space": "home", "path": "mcp/test.txt", "content": "Original"})
				if call("read_text", map[string]any{"query": map[string]any{"space": "home", "path": "mcp/test.txt"}}) != "Original" {
					t.Fatal("text file round trip failed")
				}
				write("save_text", map[string]any{"space": "home", "path": "mcp/test.txt", "content": "Updated"})
				if call("read_text", map[string]any{"query": map[string]any{"space": "home", "path": "mcp/test.txt"}}) != "Updated" {
					t.Fatal("text update failed")
				}
			case "money":
				account := fixture("POST", "/accounts", "application/json", `{"name":"Test","currency":"EUR","kind":"cash"}`)
				n := write("create_transaction", map[string]any{"account_id": id(account), "amount": 1250, "kind": "expense", "date": "2026-10-21", "description": "MCP test"})
				call("update_transaction", map[string]any{"id": id(n), "data": map[string]any{"notes": "Updated"}})
			case "office":
				sources := call("sources", nil).([]any)
				var source any
				for _, v := range sources {
					if v.(map[string]any)["kind"] == "drafts" {
						source = id(v)
					}
				}
				if source == nil {
					t.Fatal("missing drafts source")
				}
				call("create", map[string]any{"source_id": source, "data": map[string]any{"name": "MCP test", "type": "docx"}})
				call("rename", map[string]any{"source_id": source, "data": map[string]any{"from": "MCP test.docx", "name": "Updated.docx"}})
			case "podcasts":
				feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/rss+xml")
					fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Test show</title><link>https://example.test</link><description>Test</description><item><title>Test episode</title><guid>test-episode</guid><enclosure url="https://example.test/test.mp3" length="1000" type="audio/mpeg"/><pubDate>Wed, 21 Oct 2026 10:00:00 GMT</pubDate></item></channel></rss>`)
				}))
				defer feed.Close()
				n := write("subscribe", map[string]any{"url": feed.URL})
				call("favorite", map[string]any{"id": id(n), "data": map[string]any{"favorite": true}})
				episodes := call("episodes", nil).(map[string]any)["episodes"].([]any)
				if len(episodes) == 0 {
					t.Fatal("feed episodes missing")
				}
				call("save_episode", map[string]any{"id": id(episodes[0]), "data": map[string]any{}})
				call("queue_episode", map[string]any{"id": id(episodes[0]), "data": map[string]any{}})
			case "documents":
				fixture("POST", "/documents?name=test.txt", "text/plain", "Test document")
				docs := call("list", nil).(map[string]any)["results"].([]any)
				if len(docs) == 0 {
					t.Fatal("document fixture missing")
				}
				call("annotate", map[string]any{"id": id(docs[0]), "data": map[string]any{"text": "MCP annotation"}})
				call("update", map[string]any{"id": id(docs[0]), "data": map[string]any{"title": "Updated"}})
			case "day":
				write("append", map[string]any{"date": "2026-10-21", "text": "Test"})
				call("update", map[string]any{"date": "2026-10-21", "data": map[string]any{"highlight": "Updated"}})
			case "habits":
				write("journal", map[string]any{"date": "2026-10-21", "journal": "Test"})
				habit := fixture("POST", "/habits", "application/json", `{"name":"Test habit"}`)
				call("mark", map[string]any{"id": id(habit), "date": "2026-10-21", "data": map[string]any{"status": 1}})
			case "inventory":
				n := write("create", map[string]any{"name": "MCP test", "quantity": 2})
				call("adjust", map[string]any{"id": id(n), "data": map[string]any{"delta": 1}})
				call("update", map[string]any{"id": id(n), "data": map[string]any{"notes": "Updated"}})
			case "bookshelf":
				n := write("create", map[string]any{"title": "MCP test", "pages": 100})
				call("progress", map[string]any{"id": id(n), "data": map[string]any{"page": 10}})
			case "clips":
				n := write("create", map[string]any{"content": "MCP test"})
				call("update", map[string]any{"id": id(n), "data": map[string]any{"pinned": true}})
			case "garage":
				n := write("create_vehicle", map[string]any{"name": "MCP test"})
				call("update_vehicle", map[string]any{"id": id(n), "data": map[string]any{"notes": "Updated"}})
				call("add_note", map[string]any{"vehicle_id": id(n), "data": map[string]any{"body": "Test"}})
			case "kitchen":
				n := write("create_recipe", map[string]any{"title": "MCP test", "ingredients": []any{map[string]any{"name": "flour", "qty": 100, "unit": "g"}}, "steps": []string{"Mix"}})
				call("update_recipe", map[string]any{"id": id(n), "data": map[string]any{"notes": "Updated"}})
				list := fixture("POST", "/lists", "application/json", `{"name":"Test list"}`)
				item := call("add_shopping_item", map[string]any{"list_id": id(list), "data": map[string]any{"name": "Milk", "qty": 1}})
				call("update_shopping_item", map[string]any{"list_id": id(list), "id": id(item), "data": map[string]any{"checked": true}})
			case "split":
				n := write("create_group", map[string]any{"name": "MCP test", "currency": "EUR"})
				group := call("get_group", map[string]any{"id": id(n)}).(map[string]any)
				member := group["my_member_id"]
				if g, ok := group["group"].(map[string]any); ok {
					member = g["my_member_id"]
				}
				call("create_expense", map[string]any{"group_id": id(n), "data": map[string]any{"title": "Test", "amount": 1250, "paid_by": member, "date": "2026-10-21", "split_mode": "even", "shares": []any{map[string]any{"member_id": member, "share": 1}}}})
			case "invoicing":
				n := write("create_client", map[string]any{"name": "MCP test"})
				call("update_client", map[string]any{"id": id(n), "data": map[string]any{"notes": "Updated"}})
				write("create_draft", map[string]any{"client_id": id(n), "issue_date": "2026-10-21", "due_date": "2026-11-21", "items": []any{map[string]any{"description": "Test", "quantity": 1, "unit_price": 10}}})
			case "review":
				n := write("create", map[string]any{"title": "MCP test"})
				task := call("create_task", map[string]any{"review_id": id(n), "data": map[string]any{"title": "Test"}})
				call("update_task", map[string]any{"review_id": id(n), "task_id": id(task), "data": map[string]any{"status": "done"}})
			case "travel":
				n := write("create_trip", map[string]any{"name": "MCP test"})
				call("update_trip", map[string]any{"id": id(n), "data": map[string]any{"notes": "Updated"}})
				call("add_journal", map[string]any{"trip_id": id(n), "data": map[string]any{"body": "Test"}})
			case "subtitles":
				// Seed only this throwaway container; no production library is mounted.
				setup := exec.Command("docker", "exec", "-i", name, "sh", "-c", "mkdir -p /data/media && cat > /data/media/Test.en.srt")
				setup.Stdin = strings.NewReader("1\n00:00:01,000 --> 00:00:03,000\nTest subtitle\n\n")
				if out, err := setup.CombinedOutput(); err != nil {
					t.Fatalf("subtitle fixture: %v %s", err, out)
				}
				fixture("POST", "/library/scan", "application/json", "{}")
				files := call("files", nil).(map[string]any)["items"].([]any)
				if len(files) == 0 {
					t.Fatal("subtitle fixture missing")
				}
				call("text", map[string]any{"id": id(files[0])})
				call("nudge", map[string]any{"id": id(files[0]), "data": map[string]any{"ms": 100}})
				call("pin", map[string]any{"id": id(files[0]), "data": map[string]any{"note": "Test pin"}})
			case "hub":
				write("update_preferences", map[string]any{"theme": "dark"})
				write("update_preferences", map[string]any{"density": "compact"})
				prefs := call("preferences", nil).(map[string]any)
				if prefs["theme"] != "dark" {
					t.Fatal("preference update lost omitted fields")
				}
			}
			t.Logf("%s: collection reads and %d writes passed", slug, count)
		})
	}
}
