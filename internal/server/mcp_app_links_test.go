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
		{"tasks", "/dashboard", `{}`, `{"next":[{"id":3,"title":"Task"}]}`, "/today", "/inbox?task=3"},
		{"review", "/dashboard", `{}`, `{"waiting":[{"review":{"id":7,"participants":[{"id":99}]}}]}`, "/reviews", "/reviews/7"},
		{"money", "/subscriptions", `{}`, `{"items":[{"id":5}]}`, "/subscriptions", "/subscriptions/5"},
		{"garage", "/reminders", `{}`, `[{"id":42,"vehicle_id":6}]`, "/reminders", "/vehicles/6/reminders"},
		{"review", "/reviews", `{}`, `[{"id":7,"title":"Review"}]`, "/reviews", "/reviews/7"},
		{"review", "/reviews/{id}/", `{"id":7}`, `{"id":7,"live_now":[{"id":99}]}`, "/reviews/7", "/reviews/7"},
		{"review", "/reviews/{review_id}/comments", `{"review_id":7}`, `[{"id":99,"page":1}]`, "/reviews/7", ""},
		{"invoicing", "/invoices/{id}", `{"id":7}`, `{"id":7,"items":[{"id":99}]}`, "/invoices/7", "/invoices/7"},
		{"notes", "/notes", `{}`, `{"id":8,"title":"New note"}`, "/note/8", "/note/8"},
		{"documents", "/documents", `{}`, `{"results":[{"id":9}],"count":1}`, "/documents", "/documents/9"},
		{"tasks", "/tasks/{id}", `{"id":3}`, `{"id":3}`, "/inbox?task=3", "/inbox?task=3"},
		{"kitchen", "/lists/{list_id}/items", `{"list_id":4}`, `{"id":90}`, "/shopping/4", ""},
		{"documents", "/documents/{id}/text", `{"id":9}`, `"document text"`, "/documents/9", ""},
		{"day", "/days/{date}", `{"date":"2026-10-22"}`, `{"date":"2026-10-22"}`, "/day/2026-10-22", ""},
		{"office", "/recent", `{}`, `[]`, "/", ""},
		{"sign", "/documents", `{}`, `{"items":[{"id":5}],"total":1}`, "/documents", "/documents/5"},
		{"sign", "/documents/{id}/audit", `{"id":5}`, `[{"id":99,"document_id":5}]`, "/documents/5", ""},
		{"sign", "/inbox", `{}`, `{"waiting":[{"recipient_id":7,"document_id":5}],"done":[]}`, "/", "/inbox/7"},
		{"sign", "/overview", `{}`, `{"to_sign":[{"recipient_id":7}],"waiting":[]}`, "/", "/inbox/7"},
		{"sign", "/summary", `{}`, `{"to_sign":[],"waiting":[{"id":5,"next":"Ana"}]}`, "/", "/documents/5"},
		{"sign", "/day/{date}", `{"date":"2026-11-05"}`, `{"signed":[{"document_id":5,"name":"Ana"}]}`, "/", "/documents/5"},
		{"sign", "/templates/{id}/use", `{"id":2}`, `{"id":11,"kind":"document","status":"draft"}`, "/documents/11", "/documents/11"},
		{"voice", "/memos", `{}`, `{"items":[{"id":5}],"total":1}`, "/memos", "/memos/5"},
		{"voice", "/summary", `{}`, `{"memos":3,"latest_inbox":[{"id":5}]}`, "/", "/memos/5"},
		{"voice", "/day/{date}", `{"date":"2026-11-05"}`, `{"memos":[{"id":5}],"count":1}`, "/memos", "/memos/5"},
		{"voice", "/memos/{id}/send", `{"id":5}`, `{"sent":{"app":"tasks","ref":"99"},"memo":{"id":5}}`, "/memos/5", "/memos/5"},
		{"weather", "/summary", `{}`, `{"place":{"id":4,"is_home":false},"warnings":[{"id":9,"location_id":4}]}`, "/", "/places/4"},
		{"weather", "/summary", `{}`, `{"place":{"id":1,"is_home":true}}`, "/", "/"},
		{"weather", "/forecast", `{"query":{"location":"4"}}`, `{"location":{"id":4}}`, "/places/4", "/places/4"},
		{"weather", "/forecast", `{"query":{"lat":"43.8","lon":"18.4"}}`, `{"location":null}`, "/", ""},
		{"weather", "/forecast", `{"query":{"location":"../x"}}`, `{}`, "/", ""},
		{"weather", "/warnings", `{"query":{"history":"1"}}`, `{"active":[],"history":[{"id":9,"location_id":4}]}`, "/warnings", "/places/4"},
		{"weather", "/day/{date}", `{"date":"2026-11-05"}`, `{"date":"2026-11-05","location":{"id":4}}`, "/diary", "/places/4"},
		{"weather", "/diary", `{}`, `{"location":{"id":4},"days":[]}`, "/diary", "/places/4"},
		{"weather", "/locations", `{}`, `[{"id":1,"is_home":true},{"id":4,"lat":48.2}]`, "/places", "/places/4"},
		{"weather", "/locations", `{}`, `{"id":5,"name":"Vienna","lat":48.2,"lon":16.4}`, "/places", "/places/5"},
		{"fitness", "/summary", `{}`, `{"last_workout":{"id":96},"today":{"routine_id":3},"records":[{"exercise_id":10}]}`, "/", "/workouts/96"},
		{"fitness", "/summary", `{}`, `{"today":{"routine_id":3},"last_workout":null}`, "/", "/routines/3"},
		{"fitness", "/summary", `{}`, `{"records":[{"exercise_id":10,"workout_id":96}]}`, "/", "/exercises/10"},
		{"fitness", "/week", `{}`, `{"days":[{"items":[{"id":1793664000,"health":true},{"id":95}],"planned":null}]}`, "/", "/workouts/95"},
		{"fitness", "/week", `{}`, `{"days":[{"items":[],"planned":{"routine_id":4}}]}`, "/", "/routines/4"},
		{"fitness", "/day/{date}", `{"date":"2026-11-04"}`, `{"workouts":[{"id":96}],"planned":{"routine_id":2}}`, "/history", "/workouts/96"},
		{"fitness", "/workouts", `{"query":{"limit":"3"}}`, `[{"id":96,"sets":[{"id":1520,"exercise_id":10}]}]`, "/history", "/workouts/96"},
		{"fitness", "/workouts/{id}", `{"id":96}`, `{"workout":{"id":96},"routine":{"id":2},"exercises":{"10":{"id":10}}}`, "/workouts/96", "/routines/2"},
		{"fitness", "/workouts/log", `{}`, `{"workout":{"id":97},"records":[{"exercise":"Bench Press (Barbell)"}],"week":{"done":4,"goal":5}}`, "/workouts/97", "/workouts/97"},
		{"fitness", "/records", `{"query":{"exercise":"10"}}`, `{"records":[{"exercise_id":10,"kind":"weight"}],"recent":[]}`, "/progress", "/exercises/10"},
		{"fitness", "/progress", `{}`, `{"weeks":[],"records":[{"exercise_id":1}]}`, "/progress", "/exercises/1"},
		{"fitness", "/routines", `{}`, `[{"id":1,"items":[{"id":1,"exercise_id":1}]}]`, "/routines", "/routines/1"},
		{"fitness", "/plan", `{}`, `[{"weekday":1,"routine_id":1,"routine":"Push day"}]`, "/routines", "/routines/1"},
		{"fitness", "/exercises", `{}`, `[{"id":60,"name":"Ab Wheel"}]`, "/progress", "/exercises/60"},
		{"fitness", "/exercises/{id}", `{"id":10}`, `{"exercise":{"id":10},"history":[{"workout_id":96}]}`, "/exercises/10", "/workouts/96"},
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
	// Pocket Health's runs in a Pocket Fitness week have no page in Fitness.
	r := addMCPAppLinks(mcpAppResult(map[string]any{"result": map[string]any{"days": []any{map[string]any{"items": []any{map[string]any{"id": 1793664000.0, "health": true}}}}}}, false),
		"https://fitness.example", &mcpAppTool{App: "fitness", Path: "/week"}, nil)
	if strings.Contains(r.Content[0].(*mcp.TextContent).Text, "/workouts/1793664000") {
		t.Fatal("a Pocket Health run linked as a workout")
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
	a, _ := f.app("review", fake.URL, "all")
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
		{"pocket_review_list", map[string]any{}, `"item_url":"https://collaborate.example/reviews/7"`},
		{"pocket_review_comments", map[string]any{"review_id": 7}, `"source_url":"https://collaborate.example/reviews/7"`},
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
	if _, err := f.st.DB.Exec(`UPDATE services SET audience='admins' WHERE type='review'`); err != nil {
		t.Fatal(err)
	}
	r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "pocket_review_list", Arguments: map[string]any{}})
	if err == nil && !r.IsError {
		t.Fatal("revoked review access accepted")
	}
}
