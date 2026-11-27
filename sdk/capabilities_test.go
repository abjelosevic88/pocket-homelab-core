package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testCaps = Capabilities{
	Search:  &Searchable{What: "Your notes", Kinds: []string{"note"}, Bangs: []string{"notes"}},
	Day:     &DayFeed{Label: "Notes", One: "note", Many: "notes"},
	Widget:  &Widget{Title: "Recent notes"},
	Capture: &QuickCapture{Kinds: []CaptureKind{{Kind: "note", Label: "Note"}, {Kind: "task", Label: "Task", Fields: []string{"due"}}}},
}

func TestCapabilitiesLinkAllows(t *testing.T) {
	req := func(m, p string) *http.Request { return httptest.NewRequest(m, p, nil) }
	all := []string{ScopeSearch, ScopeDay, ScopeWidget, ScopeCapture}
	cases := []struct {
		name   string
		caps   Capabilities
		scopes []string
		r      *http.Request
		want   bool
	}{
		{"search", testCaps, []string{ScopeSearch}, req("GET", SearchPath+"?q=x"), true},
		{"search needs its scope", testCaps, []string{ScopeDay, ScopeWidget, ScopeCapture, "notes:read"}, req("GET", SearchPath), false},
		{"search never POST", testCaps, all, req("POST", SearchPath), false},
		{"search not declared", Capabilities{Day: testCaps.Day}, all, req("GET", SearchPath), false},
		{"day", testCaps, []string{ScopeDay}, req("GET", DayPath+"2026-11-27?tz=Europe/Sarajevo"), true},
		{"day needs its scope", testCaps, []string{ScopeSearch}, req("GET", DayPath+"2026-11-27"), false},
		{"day wants a date", testCaps, all, req("GET", DayPath+"today"), false},
		{"day no deeper path", testCaps, all, req("GET", DayPath+"2026-11-27/extra"), false},
		{"widget", testCaps, []string{ScopeWidget}, req("GET", WidgetPath), true},
		{"widget needs its scope", testCaps, []string{ScopeCapture}, req("GET", WidgetPath), false},
		{"capture POST", testCaps, []string{ScopeCapture}, req("POST", CapturePath), true},
		{"capture never GET", testCaps, all, req("GET", CapturePath), false},
		{"capture never PUT or DELETE", testCaps, all, req("DELETE", CapturePath), false},
		{"capture needs its scope", testCaps, []string{ScopeSearch, ScopeDay, ScopeWidget}, req("POST", CapturePath), false},
		{"capture with no kinds", Capabilities{Capture: &QuickCapture{}}, all, req("POST", CapturePath), false},
		{"other routes stay closed", testCaps, all, req("GET", "/api/v1/notes"), false},
		{"no prefix tricks", testCaps, all, req("GET", SearchPath+"/../notes"), false},
		{"nothing declared", Capabilities{}, all, req("GET", WidgetPath), false},
	}
	for _, c := range cases {
		if got := c.caps.LinkAllows(c.scopes, c.r); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestRegisterAddsCapabilityOffers(t *testing.T) {
	var got Manifest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"app":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, Options{Token: "pca_x"})
	m := Manifest{Slug: "notes", Name: "Pocket Notes", URL: "http://notes", Category: "Everyday", Capabilities: testCaps,
		Offers: []Offer{{Scope: ScopeSearch, Name: "Its own words"}},
		Uses:   []Use{UseEvery(ScopeDay, "the journal")}}
	if _, err := c.Register(context.Background(), "", m); err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, o := range got.Offers {
		scopes[o.Scope] = o.Name
	}
	if len(scopes) != 4 || scopes[ScopeSearch] != "Its own words" || scopes[ScopeDay] == "" || scopes[ScopeWidget] == "" || scopes[ScopeCapture] == "" {
		t.Fatalf("offers %+v", got.Offers)
	}
	// the manifest carries the descriptions flat, and "every app" as "*"
	if got.Search == nil || got.Search.Bangs[0] != "notes" || got.Day.Label != "Notes" || got.Category != "Everyday" || len(got.Capture.Kinds) != 2 {
		t.Fatalf("capabilities lost on the wire: %+v", got.Capabilities)
	}
	if len(got.Uses) != 1 || got.Uses[0].App != "*" || got.Uses[0].Scopes[0] != ScopeDay {
		t.Fatalf("uses %+v", got.Uses)
	}
	// an app without capabilities offers nothing new
	got = Manifest{}
	_, _ = c.Register(context.Background(), "", Manifest{Slug: "x", Name: "X", URL: "http://x"})
	if len(got.Offers) != 0 {
		t.Fatalf("offers out of nothing: %+v", got.Offers)
	}
}

func TestSearchHandler(t *testing.T) {
	var gotQ string
	var gotLimit int
	h := SearchHandler(func(r *http.Request, q string, limit int) ([]SearchItem, error) {
		gotQ, gotLimit = q, limit
		if q == "boom" {
			return nil, errors.New("sqlite: table notes_fts is locked")
		}
		return []SearchItem{{ID: "1", Title: "  Bread\n recipe ", Snippet: strings.Repeat("a ", 400), URL: "/note/1"}, {ID: "2"}, {ID: "3", Title: "Third"}, {ID: "4", Title: "Fourth"}}, nil
	})
	get := func(target string) (int, SearchResults, string) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", target, nil))
		var out SearchResults
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Body.String()
	}
	code, out, _ := get(SearchPath + "?q=%20bread%20&limit=2")
	if code != 200 || gotQ != "bread" || gotLimit != 2 || len(out.Items) != 2 || out.Items[0].Title != "Bread recipe" || out.Items[1].ID != "3" {
		t.Fatalf("search: %d %q %d %+v", code, gotQ, gotLimit, out)
	}
	if n := len([]rune(out.Items[0].Snippet)); n > 300 {
		t.Fatalf("snippet not cut: %d", n)
	}
	if _, _, _ = get(SearchPath + "?q=x"); gotLimit != 8 {
		t.Fatalf("default limit %d", gotLimit)
	}
	if _, _, _ = get(SearchPath + "?q=x&limit=9999"); gotLimit != 50 {
		t.Fatalf("limit cap %d", gotLimit)
	}
	gotQ = "untouched"
	if code, out, body := get(SearchPath + "?q=%20"); code != 200 || gotQ != "untouched" || out.Items == nil || !strings.Contains(body, `"items":[]`) {
		t.Fatalf("empty query must not search: %d %q %s", code, gotQ, body)
	}
	if code, _, body := get(SearchPath + "?q=boom"); code != 500 || strings.Contains(body, "sqlite") {
		t.Fatalf("errors must not leak: %d %s", code, body)
	}
}

func TestDayHandler(t *testing.T) {
	var gotTZ *time.Location
	h := DayHandler(func(r *http.Request, date string, tz *time.Location) (*Day, error) {
		gotTZ = tz
		if date == "2026-01-01" {
			return nil, nil
		}
		return &Day{Summary: "2 notes", Items: []DayItem{{Title: "One", Time: "09:30", URL: "/note/1"}, {Title: " "}}, Metrics: []Metric{{"Notes", "2"}}}, nil
	})
	get := func(target string) (int, Day, string) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", target, nil))
		var out Day
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Body.String()
	}
	code, out, _ := get(DayPath + "2026-11-27?tz=Europe/Sarajevo")
	if code != 200 || out.Date != "2026-11-27" || out.Summary != "2 notes" || len(out.Items) != 1 || out.Metrics[0].Value != "2" || gotTZ == nil || gotTZ.String() != "Europe/Sarajevo" {
		t.Fatalf("day: %d %+v tz=%v", code, out, gotTZ)
	}
	if code, out, body := get(DayPath + "2026-01-01"); code != 200 || out.Date != "2026-01-01" || !strings.Contains(body, `"items":[]`) || gotTZ != nil {
		t.Fatalf("empty day: %d %s", code, body)
	}
	for _, bad := range []string{"today", "2026-13-40", "26-1-1"} {
		if code, _, _ := get(DayPath + bad); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
}

func TestWidgetHandler(t *testing.T) {
	h := WidgetHandler(func(r *http.Request) (*WidgetData, error) {
		items := make([]WidgetItem, 20)
		for i := range items {
			items[i].Title = "Row"
		}
		return &WidgetData{Line: "20 things", Items: items, Metrics: make([]Metric, 9)}, nil
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", WidgetPath, nil))
	var out WidgetData
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out.Line != "20 things" || len(out.Items) != 8 || len(out.Metrics) != 4 {
		t.Fatalf("widget: %d %+v", rec.Code, out)
	}
	rec = httptest.NewRecorder()
	WidgetHandler(func(r *http.Request) (*WidgetData, error) { return nil, nil })(rec, httptest.NewRequest("GET", WidgetPath, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty widget: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCaptureHandler(t *testing.T) {
	calls := 0
	h := testCaps.Capture.CaptureHandler(func(r *http.Request, in CaptureInput) (*CaptureResult, error) {
		calls++
		if in.Text == "full" {
			return nil, fmt.Errorf("%w: the list is full", ErrCapture)
		}
		return &CaptureResult{ID: "7", Title: in.Kind + ":" + in.Text, URL: "/note/7"}, nil
	})
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", CapturePath, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	const id = `"request_id":"0123456789abcdef"`
	if code, body := post(`{` + id + `,"text":" milk "}`); code != 201 || !strings.Contains(body, `"note:milk"`) {
		t.Fatalf("default kind: %d %s", code, body)
	}
	if code, body := post(`{` + id + `,"kind":"task","text":"x","due":"2026-12-01"}`); code != 201 || !strings.Contains(body, `"task:x"`) {
		t.Fatalf("task: %d %s", code, body)
	}
	before := calls
	for name, body := range map[string]string{
		"unknown kind": `{` + id + `,"kind":"expense","text":"x"}`,
		"no id":        `{"text":"x"}`,
		"short id":     `{"request_id":"abc","text":"x"}`,
		"empty":        `{` + id + `,"text":"  "}`,
		"bad date":     `{` + id + `,"text":"x","due":"tomorrow"}`,
		"not json":     `text=x`,
	} {
		if code, _ := post(body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if calls != before {
		t.Fatalf("a refused capture reached the app %d times", calls-before)
	}
	if code, body := post(`{` + id + `,"text":"full"}`); code != 400 || !strings.Contains(body, "the list is full") {
		t.Fatalf("the app's own refusal: %d %s", code, body)
	}
	// nothing declared: nothing can be kept
	rec := httptest.NewRecorder()
	(*QuickCapture)(nil).CaptureHandler(nil)(rec, httptest.NewRequest("POST", CapturePath, strings.NewReader(`{}`)))
	if rec.Code != 404 {
		t.Fatalf("no capture declared: %d", rec.Code)
	}
}

// The asking side: Capable lists who offers a scope; SearchIn acts for the
// person in the app and makes item addresses openable.
func TestCapableAndSearchIn(t *testing.T) {
	var app *httptest.Server
	app = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pcx_tok" || r.URL.Path != SearchPath || r.URL.Query().Get("q") != "bread" || r.URL.Query().Get("limit") != "5" {
			http.Error(w, `{"error":"this link does not cover that"}`, http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"1","title":"Bread","url":"/note/1"},{"id":"2","title":"Out","url":"https://else.example/x","link":"https://bread.example"}]}`))
	}))
	defer app.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/links/status":
			if r.URL.Query().Get("scope") != ScopeSearch || r.URL.Query().Get("user") != "a@b.c" {
				t.Errorf("status query %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[
			 {"app":"notes","scope":"pocket:search","state":"on","on":true,"target":{"name":"Pocket Notes","icon":"https://notes.example/favicon.svg","public_url":"https://notes.example","category":"Everyday","search":{"what":"Your notes","bangs":["notes"]}}},
			 {"app":"vault","scope":"pocket:search","state":"off_person","on":false,"detail":"switched off by the person","target":{"name":"Pocket Vault"}},
			 {"app":"gone","scope":"pocket:search","state":"target_missing","on":false},
			 {"app":"notes","scope":"notes:read","state":"on","on":true}]`))
		case "/api/v1/links/token":
			var in struct {
				App    string   `json:"app"`
				Scopes []string `json:"scopes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.App == "vault" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"off","reason":"off_person"}`))
				return
			}
			if len(in.Scopes) != 1 || in.Scopes[0] != ScopeSearch {
				t.Errorf("token scopes %v", in.Scopes)
			}
			_, _ = w.Write([]byte(`{"access_token":"pcx_tok","url":"` + app.URL + `","public_url":"https://notes.example/"}`))
		}
	}))
	defer core.Close()
	c := New(core.URL, Options{Token: "pca_x"})
	ctx := context.Background()
	apps, err := c.Capable(ctx, "a@b.c", ScopeSearch)
	if err != nil || len(apps) != 2 {
		t.Fatalf("capable: %+v %v", apps, err)
	}
	if n := apps[0]; n.App != "notes" || !n.On || n.Name != "Pocket Notes" || n.Icon == "" || n.Category != "Everyday" || n.Search == nil || n.Search.Bangs[0] != "notes" {
		t.Fatalf("notes: %+v", n)
	}
	if v := apps[1]; v.App != "vault" || v.On || v.State != "off_person" {
		t.Fatalf("vault: %+v", v)
	}
	items, err := c.SearchIn(ctx, "a@b.c", "notes", "bread", 5)
	if err != nil || len(items) != 2 || items[0].URL != "https://notes.example/note/1" || items[1].URL != "https://else.example/x" || items[1].Link != "https://bread.example" {
		t.Fatalf("search in: %+v %v", items, err)
	}
	var le *LinkError
	if _, err := c.SearchIn(ctx, "a@b.c", "vault", "x", 0); !errors.As(err, &le) || le.Reason != "off_person" {
		t.Fatalf("a link that is off: %v", err)
	}
	if _, err := New("", Options{}).Capable(ctx, "a@b.c", ScopeSearch); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no core: %v", err)
	}
}
