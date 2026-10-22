package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPAppBrowserLinks(t *testing.T) {
	cases := []struct{ app, path, input, data, source, item string }{
		{"collaborator", "/reviews", `{}`, `[{"id":7,"title":"Review"}]`, "/reviews", "/reviews/7"},
		{"collaborator", "/reviews/{id}/", `{"id":7}`, `{"id":7,"live_now":[{"id":99}]}`, "/reviews/7", "/reviews/7"},
		{"collaborator", "/reviews/{review_id}/comments", `{"review_id":7}`, `[{"id":99,"page":1}]`, "/reviews/7", ""},
		{"invoicing", "/invoices/{id}", `{"id":7}`, `{"id":7,"items":[{"id":99}]}`, "/invoices/7", "/invoices/7"},
		{"notes", "/notes", `{}`, `{"id":8,"title":"New note"}`, "/note/8", "/note/8"},
		{"documents", "/documents", `{}`, `{"results":[{"id":9}],"count":1}`, "/documents", "/documents/9"},
		{"tasks", "/tasks/{id}", `{"id":3}`, `{"id":3}`, "/inbox?task=3", "/inbox?task=3"},
		{"kitchen", "/lists/{list_id}/items", `{"list_id":4}`, `{"id":90}`, "/shopping/4", ""},
		{"documents", "/documents/{id}/text", `{"id":9}`, `"document text"`, "/documents/9", ""},
		{"day", "/days/{date}", `{"date":"2026-10-22"}`, `{"date":"2026-10-22"}`, "/day/2026-10-22", ""},
		{"office", "/recent", `{}`, `[]`, "/", ""},
		{"notes", "/notes", `{}`, `{"id":"../../bad","item_url":"https://evil.test"}`, "/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.app+tc.path, func(t *testing.T) {
			var input map[string]any
			var data any
			json.Unmarshal([]byte(tc.input), &input)
			json.Unmarshal([]byte(tc.data), &data)
			r := addMCPAppLinks(mcpAppResult(map[string]any{"result": data}, false), "https://apps.example/prefix/", &mcpAppTool{App: tc.app, Path: tc.path}, input)
			e := r.StructuredContent.(map[string]any)
			if e["app_url"] != "https://apps.example/prefix/" || e["source_url"] != "https://apps.example/prefix"+tc.source {
				t.Fatalf("wrong navigation: %v", e)
			}
			if tc.item != "" && !strings.Contains(r.Content[0].(*mcp.TextContent).Text, `"item_url":"https://apps.example/prefix`+tc.item+`"`) {
				t.Fatalf("item link missing: %v", e)
			}
			if strings.Contains(r.Content[0].(*mcp.TextContent).Text, "/reviews/99") || strings.Contains(r.Content[0].(*mcp.TextContent).Text, "/invoices/99") {
				t.Fatal("child ID mistaken for review")
			}
		})
	}
	for _, base := range []string{"javascript:alert(1)", "https://user:secret@example.com", "https://example.com/?token=secret", "//example.com"} {
		r := addMCPAppLinks(mcpAppResult(map[string]any{"result": nil}, false), base, &mcpAppTool{App: "notes", Path: "/notes"}, nil)
		if _, ok := r.StructuredContent.(map[string]any)["app_url"]; ok {
			t.Fatalf("unsafe registered URL %q", base)
		}
	}
	failure := mcpAppError("denied")
	if got := addMCPAppLinks(failure, "https://apps.example", &mcpAppTool{App: "notes", Path: "/notes"}, nil); got != failure {
		t.Fatal("error was decorated")
	}
}

func TestMCPReviewLinksUsePublicURLAndRespectAccess(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/reviews":
			fmt.Fprint(w, `[{"id":7,"title":"Review"}]`)
		case "/api/v1/reviews/7/comments":
			fmt.Fprint(w, `[{"id":99,"page":1}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	a, _ := f.app("collaborator", fake.URL, "all")
	if _, err := f.st.DB.Exec(`UPDATE apps SET public_url=? WHERE id=?`, "https://collaborate.example", a.ID); err != nil {
		t.Fatal(err)
	}
	access, _ := f.appAccess("status:read apps:read")
	session := mcpSessionWithToken(t, f.env, access)
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"pocket_collaborator_reviews", map[string]any{}, `"item_url":"https://collaborate.example/reviews/7"`},
		{"pocket_collaborator_comments", map[string]any{"review_id": 7}, `"source_url":"https://collaborate.example/reviews/7"`},
	} {
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil {
			t.Fatal(err)
		}
		body := r.Content[0].(*mcp.TextContent).Text
		if r.IsError || !strings.Contains(body, tc.want) || strings.Contains(body, fake.URL) {
			t.Fatalf("wrong public links: %s", body)
		}
	}
	if _, err := f.st.DB.Exec(`UPDATE services SET audience='admins' WHERE type='collaborator'`); err != nil {
		t.Fatal(err)
	}
	r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_collaborator_reviews", Arguments: map[string]any{}})
	if err == nil && !r.IsError {
		t.Fatal("revoked review access accepted")
	}
}
