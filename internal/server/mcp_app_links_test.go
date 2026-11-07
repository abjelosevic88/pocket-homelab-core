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
		{"chat", "/conversations", `{}`, `[{"id":4,"members":[]}]`, "/", "/c/4"},
		{"chat", "/search", `{}`, `{"query":"x","results":[{"id":9,"conversation_id":4}]}`, "/search", "/c/4"},
		{"chat", "/summary", `{}`, `{"waiting":[{"id":4}],"mentions":[]}`, "/", "/c/4"},
		{"forms", "/responses", `{}`, `{"items":[{"id":7,"form_id":2}],"total":1,"new":1}`, "/", "/answers/7"},
		{"forms", "/forms", `{}`, `[{"id":2,"handle":"x"}]`, "/forms", "/forms/2"},
		{"forms", "/summary", `{}`, `{"latest":[{"id":7}],"list":[]}`, "/", "/answers/7"},
		{"crm", "/deals", `{}`, `{"items":[{"id":12,"stage_id":2}],"total":1}`, "/deals", "/deals/12"},
		{"crm", "/deals/{id}", `{"id":12}`, `{"deal":{"id":12,"stage_id":2},"activities":[{"id":99,"deal_id":12}],"made":[]}`, "/deals/12", "/deals/12"},
		{"crm", "/today", `{}`, `{"lists":{"overdue":[{"id":12}],"today":[]},"summary":{}}`, "/", "/deals/12"},
		{"crm", "/search", `{"query":{"q":"bistro"}}`, `{"deals":[{"id":12,"url":"x"}],"total":1}`, "/deals", "/deals/12"},
		{"crm", "/day/{date}", `{"date":"2026-11-07"}`, `{"activities":[{"id":99,"deal_id":12}],"due":[]}`, "/", "/deals/12"},
		{"crm", "/deals", `{"data":{"title":"Website"}}`, `{"id":13,"stage_id":1,"value":450000,"amount":4500}`, "/deals/13", "/deals/13"},
		{"crm", "/deals/{id}/activities", `{"id":12}`, `{"id":99,"deal_id":12,"kind":"call"}`, "/deals/12", "/deals/12"},
		{"crm", "/stages", `{}`, `[{"id":2,"name":"Talking"}]`, "/pipeline", ""},
		{"tables", "/bases", `{}`, `[{"id":2,"name":"Home","tables":[{"id":3,"name":"Plants"}]}]`, "/", "/b/2"},
		{"tables", "/bases", `{}`, `[{"id":2,"name":"Home","tables":[{"id":3,"name":"Plants"}]}]`, "/", "/t/3"},
		{"tables", "/tables/{id}", `{"id":3}`, `{"table":{"id":3,"base_id":2},"base":{"id":2},"fields":[{"id":8}],"views":[{"id":5}]}`, "/t/3", "/t/3"},
		{"tables", "/tables/{id}/records", `{"id":3}`, `{"records":[{"id":41,"table_id":3,"number":1}],"total":1,"table":"Plants"}`, "/t/3", "/r/41"},
		{"tables", "/tables/{id}/records", `{"id":3,"data":{"values":{"Name":"Fern"}}}`, `{"id":42,"table_id":3,"number":2,"values":{}}`, "/t/3", "/r/42"},
		{"tables", "/records/{id}", `{"id":41}`, `{"record":{"id":41,"table_id":3},"table":{"id":3},"fields":[],"history":[{"id":99,"record_id":41}]}`, "/r/41", "/r/41"},
		{"tables", "/search", `{}`, `{"query":"fern","items":[{"id":41,"table_id":3,"url":"x"}],"tables":[{"id":3}]}`, "/", "/r/41"},
		{"tables", "/search", `{}`, `{"query":"plants","items":[],"tables":[{"id":3,"url":"x"}]}`, "/", "/t/3"},
		{"tables", "/today", `{}`, `{"date":"2026-11-07","today":[],"late":[{"id":41,"table_id":3}],"soon":[]}`, "/", "/r/41"},
		{"tables", "/summary", `{}`, `{"bases":1,"tables":1,"rows":2,"recent":[{"id":41}]}`, "/", "/r/41"},
		{"tables", "/day/{date}", `{"date":"2026-11-07"}`, `{"date":"2026-11-07","items":[{"id":41}]}`, "/", "/r/41"},
		{"league", "/upcoming", `{}`, `[{"id":31,"group_id":1,"status":"planned"}]`, "/games", "/games/31"},
		{"league", "/games/{id}", `{"id":31}`, `{"game":{"id":31,"group_id":1},"group":{"id":1},"people":[{"user_id":2}],"matches":[]}`, "/games/31", "/games/31"},
		{"league", "/games/{id}/answer", `{"id":31,"data":{"answer":"in"}}`, `{"game":{"id":31},"group":{"id":1},"result":"waiting"}`, "/games/31", "/games/31"},
		{"league", "/summary", `{}`, `{"groups":[{"id":2,"name":"Friday 5-a-side"}],"next_game":{"id":31,"group":"Tuesday Tennis"}}`, "/", "/games/31"},
		{"league", "/summary", `{}`, `{"groups":[{"id":2,"name":"Friday 5-a-side"}]}`, "/", "/groups/2"},
		{"league", "/today", `{}`, `{"next":[],"unanswered":[{"id":33}],"voting":[],"confirm":[],"recent":[]}`, "/", "/games/33"},
		{"league", "/today", `{}`, `{"next":[],"confirm":[{"match":{"id":30,"group_id":1,"game_id":16}}]}`, "/", "/games/16"},
		{"league", "/matches", `{}`, `[{"match":{"id":30,"group_id":1,"game_id":0},"group_name":"Tuesday Tennis"}]`, "/results", "/groups/1?tab=results"},
		{"league", "/groups", `{}`, `[{"id":1,"name":"Tuesday Tennis","next_game":{"id":31}}]`, "/", "/groups/1"},
		{"league", "/groups", `{}`, `[{"id":1,"name":"Tuesday Tennis","next_game":{"id":31}}]`, "/", "/games/31"},
		{"league", "/groups/{id}", `{"id":1}`, `{"group":{"id":1},"members":[{"user_id":2}],"upcoming":[{"id":31}]}`, "/groups/1", "/games/31"},
		{"league", "/groups/{id}/table", `{"id":1}`, `{"period":"all","rows":[{"user_id":2,"position":1}],"group":{"id":1}}`, "/groups/1?tab=table", "/groups/1/players/2"},
		{"league", "/groups/{id}/players/{uid}", `{"id":1,"uid":2}`, `{"row":{"user_id":2}}`, "/groups/1/players/2", ""},
		{"league", "/search", `{"query":{"q":"tennis"}}`, `{"results":[{"kind":"group","id":1},{"kind":"message","id":77}]}`, "/", "/groups/1"},
		{"league", "/day/{date}", `{"date":"2026-11-10"}`, `{"date":"2026-11-10","games":[{"id":31}],"matches":[]}`, "/games", "/games/31"},
		{"league", "/tournaments", `{}`, `[{"id":7,"group_id":1,"name":"Autumn Americano","status":"live","mine":true}]`, "/tournaments", "/tournaments/7"},
		{"league", "/tournaments", `{"group_id":1}`, `[{"id":7,"group_id":1,"status":"finished","winner":"Ana"}]`, "/groups/1?tab=tournaments", "/tournaments/7"},
		{"league", "/tournaments", `{"group_id":"../x"}`, `[]`, "/tournaments", ""},
		{"league", "/tournaments/{id}", `{"id":7}`, `{"tournament":{"id":7,"group_id":1},"group":{"id":1},"groups":[{"id":40,"name":"Group A"}],"entries":[{"id":90}],"matches":[{"id":300}],"mine":[90],"podium":[]}`, "/tournaments/7", "/tournaments/7"},
		{"league", "/tournaments/{id}", `{"id":7}`, `{"tournament":{"id":7},"group":{"id":1},"groups":[]}`, "/tournaments/7", "/groups/1"},
		{"blog", "/posts", `{}`, `{"items":[{"id":5,"slug":"x"}],"total":1}`, "/posts", "/posts/5"},
		{"blog", "/search", `{}`, `{"query":"x","results":[{"id":5}]}`, "/posts", "/posts/5"},
		{"blog", "/summary", `{}`, `{"drafts":[{"id":5}],"recent":[]}`, "/", "/posts/5"},
		{"blog", "/posts/{id}/publish", `{"id":5}`, `{"id":5,"slug":"x","status":"published"}`, "/posts/5", "/posts/5"},
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
		{"fitness", "/exercises", `{}`, `[{"id":60,"name":"Ab Wheel"}]`, "/exercises", "/exercises/60"},
		{"fitness", "/exercises/{id}", `{"id":10}`, `{"exercise":{"id":10},"history":[{"workout_id":96}]}`, "/exercises/10", "/workouts/96"},
		{"nutrition", "/summary", `{}`, `{"date":"2026-11-05","entries":4,"week":{"days":[{"date":"2026-11-03","kcal":2100}]}}`, "/", "/?date=2026-11-03"},
		{"nutrition", "/day/{date}", `{"date":"2026-11-04"}`, `{"date":"2026-11-04","meals":[{"meal":"breakfast","entries":[{"id":31,"food_id":12,"date":"2026-11-04"}]}]}`, "/?date=2026-11-04", "/foods/12"},
		{"nutrition", "/day/{date}", `{"date":"../x"}`, `{"meals":[]}`, "/", ""},
		{"nutrition", "/week", `{"query":{"date":"2026-11-02"}}`, `{"start":"2026-11-02","days":[{"date":"2026-11-02","kcal":1900}]}`, "/?date=2026-11-02", "/?date=2026-11-02"},
		{"nutrition", "/trends", `{"query":{"days":"30"}}`, `{"days":[{"date":"2026-10-07"}],"top":[{"name":"Oats"}]}`, "/trends", "/?date=2026-10-07"},
		{"nutrition", "/entries", `{"query":{"date":"2026-11-04"}}`, `[{"id":31,"date":"2026-11-04","food_id":12}]`, "/?date=2026-11-04", "/?date=2026-11-04"},
		{"nutrition", "/entries", `{}`, `[{"id":31,"date":"2026-11-01"}]`, "/", "/?date=2026-11-01"},
		{"nutrition", "/foods/search", `{"query":{"q":"egg"}}`, `{"foods":[{"id":0,"builtin":"egg"},{"id":12,"name":"Eggs (farm)"}]}`, "/foods", "/foods/12"},
		{"nutrition", "/meals", `{}`, `[{"id":3,"name":"Usual breakfast","items":[{"id":9,"food_id":12}]}]`, "/meals", "/foods/12"},
		{"nutrition", "/entries/log", `{"data":{"meal":"breakfast"}}`, `{"entries":[{"id":40,"date":"2026-11-05","food_id":12}],"count":1,"day":{"text":"…"}}`, "/?date=2026-11-05", "/?date=2026-11-05"},
		{"nutrition", "/water/{date}/add", `{"date":"2026-11-05","data":{"ml":500}}`, `{"date":"2026-11-05","ml":1500}`, "/?date=2026-11-05", ""},
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
	// A tournament's pools ("groups") are not League groups: they get no /groups/ page.
	rt := addMCPAppLinks(mcpAppResult(map[string]any{"result": map[string]any{"tournament": map[string]any{"id": 7.0}, "groups": []any{map[string]any{"id": 40.0, "name": "Group A"}}}}, false),
		"https://league.example", &mcpAppTool{App: "league", Path: "/tournaments/{id}"}, map[string]any{"id": 7.0})
	if text := rt.Content[0].(*mcp.TextContent).Text; strings.Contains(text, "/groups/40") || !strings.Contains(text, "/tournaments/7") {
		t.Fatalf("tournament pool links: %s", text)
	}
	// A built-in food (id 0) has no page in Pocket Nutrition.
	r0 := addMCPAppLinks(mcpAppResult(map[string]any{"result": map[string]any{"foods": []any{map[string]any{"id": 0.0, "builtin": "egg"}}}}, false),
		"https://nutrition.example", &mcpAppTool{App: "nutrition", Path: "/foods/search"}, nil)
	if strings.Contains(r0.Content[0].(*mcp.TextContent).Text, "item_url") {
		t.Fatal("a built-in food got a page")
	}
	// Pocket Health's runs in a Pocket Fitness week have no page in Fitness.
	r := addMCPAppLinks(mcpAppResult(map[string]any{"result": map[string]any{"days": []any{map[string]any{"items": []any{map[string]any{"id": 1793664000.0, "health": true}}}}}}, false),
		"https://fitness.example", &mcpAppTool{App: "fitness", Path: "/week"}, nil)
	if strings.Contains(r.Content[0].(*mcp.TextContent).Text, "/workouts/1793664000") {
		t.Fatal("a Pocket Health run linked as a workout")
	}
	// A Projects read naming no workspace: the links open in the one that answered.
	rp := addMCPAppLinks(mcpAppResult(map[string]any{"workspace": "projects.client.example", "result": []any{map[string]any{"id": 130.0, "identifier": "ATH-111"}}}, false),
		"https://projects.example", &mcpAppTool{App: "projects", Path: "/issues"}, map[string]any{})
	if !strings.Contains(rp.Content[0].(*mcp.TextContent).Text, `"item_url":"https://projects.example/r/projects.client.example/issue/130"`) {
		t.Fatalf("issue link outside the workspace that answered: %s", rp.Content[0].(*mcp.TextContent).Text)
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
