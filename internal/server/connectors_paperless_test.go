package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePaperless answers the REST calls the connector makes.
type fakePaperless struct {
	*httptest.Server
	mu        sync.Mutex
	adminTok  string
	users     []map[string]any
	passwords map[string]string
	tokens    map[string]string // token → username
	groups    []map[string]any
	workflows map[int]map[string]any
	nextID    int
}

func newFakePaperless(t *testing.T) *fakePaperless {
	f := &fakePaperless{
		adminTok: "admintok",
		users: []map[string]any{
			{"id": 1, "username": "consumer", "email": "", "is_active": true, "is_superuser": false},
			{"id": 3, "username": "admin", "email": "root@localhost", "is_active": true, "is_superuser": true},
			{"id": 4, "username": "kid", "email": "kid.other@b.c", "is_active": true, "is_superuser": false},
		},
		passwords: map[string]string{}, tokens: map[string]string{"admintok": "admin"},
		workflows: map[int]map[string]any{}, nextID: 10,
	}
	page := func(w http.ResponseWriter, list []map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"count": len(list), "next": nil, "results": list})
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Version", "3.3.2")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p := r.URL.Path
		if p == "/api/token/" {
			u, _ := body["username"].(string)
			if pw, ok := f.passwords[u]; !ok || pw != body["password"] {
				w.WriteHeader(400)
				return
			}
			tok := "tok-" + u
			f.tokens[tok] = u
			_ = json.NewEncoder(w).Encode(map[string]string{"token": tok})
			return
		}
		who := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Token ")]
		if who == "" {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"detail":"Invalid token."}`)
			return
		}
		if p == "/api/profile/generate_auth_token/" && r.Method == http.MethodPost {
			for tok, u := range f.tokens { // one token per account: the old one goes
				if u == who {
					delete(f.tokens, tok)
				}
			}
			f.nextID++
			tok := fmt.Sprintf("tok-%s-%d", who, f.nextID)
			f.tokens[tok] = who
			_ = json.NewEncoder(w).Encode(tok)
			return
		}
		if p == "/api/ui_settings/" {
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"username": who, "is_superuser": who == "admin"}})
			return
		}
		if who != "admin" {
			w.WriteHeader(403)
			return
		}
		switch {
		case p == "/api/users/" && r.Method == http.MethodGet:
			q := strings.ToLower(r.URL.Query().Get("username__iexact"))
			var out []map[string]any
			for _, u := range f.users {
				if q == "" || strings.ToLower(u["username"].(string)) == q {
					out = append(out, u)
				}
			}
			page(w, out)
		case p == "/api/users/" && r.Method == http.MethodPost:
			u := map[string]any{"id": f.nextID, "username": body["username"], "email": body["email"], "first_name": body["first_name"], "last_name": body["last_name"],
				"is_active": true, "is_superuser": body["is_superuser"], "groups": body["groups"]}
			f.nextID++
			f.users = append(f.users, u)
			f.passwords[body["username"].(string)] = body["password"].(string)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(u)
		case p == "/api/groups/" && r.Method == http.MethodGet:
			page(w, f.groups)
		case p == "/api/groups/" && r.Method == http.MethodPost:
			g := map[string]any{"id": f.nextID, "name": body["name"], "permissions": body["permissions"]}
			f.nextID++
			f.groups = append(f.groups, g)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(g)
		case p == "/api/workflows/" && r.Method == http.MethodGet:
			var out []map[string]any
			for _, wf := range f.workflows {
				out = append(out, wf)
			}
			page(w, out)
		case p == "/api/workflows/" && r.Method == http.MethodPost:
			body["id"] = f.nextID
			f.workflows[f.nextID] = body
			f.nextID++
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(body)
		case strings.HasPrefix(p, "/api/workflows/") && r.Method == http.MethodDelete:
			var id int
			_, _ = fmt.Sscanf(strings.TrimPrefix(p, "/api/workflows/"), "%d", &id)
			if _, ok := f.workflows[id]; !ok {
				w.WriteHeader(404)
				return
			}
			delete(f.workflows, id)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestPaperlessConnector(t *testing.T) {
	e := newEnv(t)
	pl := newFakePaperless(t)
	call := func(method, path string, body any, out any) int {
		t.Helper()
		code, txt := e.raw(method, path, "", body)
		if out != nil {
			_ = json.Unmarshal([]byte(txt), out)
		}
		return code
	}
	call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)
	var kid, newbie struct {
		ID int64 `json:"id"`
	}
	call("POST", "/api/v1/users", map[string]string{"email": "kid@b.c", "password": "password123", "role": "member"}, &kid)
	call("POST", "/api/v1/users", map[string]string{"email": "newbie@b.c", "name": "New Bie", "password": "password123", "role": "member"}, &newbie)
	var sv struct {
		ID int64 `json:"id"`
	}
	if c := call("POST", "/api/v1/services", map[string]any{"type": "paperless", "url": pl.URL, "public_url": "https://docs.example", "fields": map[string]string{"TOKEN": pl.adminTok}}, &sv); c != 201 {
		t.Fatalf("create service %d", c)
	}
	base := "/api/v1/services/" + itoa(sv.ID) + "/connector"
	var tr struct {
		OK   bool `json:"ok"`
		Info struct {
			Version string `json:"version"`
			Users   int    `json:"users"`
		} `json:"info"`
	}
	call("POST", base+"/test", nil, &tr)
	if !tr.OK || tr.Info.Version != "3.3.2" || tr.Info.Users != 2 {
		t.Fatalf("test %+v", tr)
	}
	call("PUT", base, map[string]any{"enabled": true}, nil)
	if c := call("PUT", base, map[string]any{"sso_enabled": true}, nil); c != 400 {
		t.Fatalf("sign-in must be refused (set in Paperless's environment), got %d", c)
	}
	// The owner's Paperless account has no matching email: linked by hand.
	if c := call("PUT", base+"/accounts/1", map[string]string{"remote_id": "admin"}, nil); c != 200 {
		t.Fatalf("link owner %d", c)
	}
	var prov struct {
		Results []struct {
			Email, Status, Error, Credential string
			RemoteID                         string `json:"remote_id"`
		}
	}
	call("POST", base+"/provision", map[string]any{"all": true}, &prov)
	if len(prov.Results) != 2 {
		t.Fatalf("provision %+v", prov)
	}
	for _, r := range prov.Results {
		switch r.Email {
		case "kid@b.c":
			if r.Status != "error" || !strings.Contains(r.Error, "another email") {
				t.Fatalf("kid must not take over the kid account: %+v", r)
			}
		case "newbie@b.c":
			if r.Status != "ok" || r.RemoteID != "newbie" || r.Credential != "connector" {
				t.Fatalf("newbie %+v", r)
			}
		}
	}
	pl.mu.Lock()
	nb := pl.users[len(pl.users)-1]
	if len(pl.groups) != 1 || pl.groups[0]["name"] != "Pocket Core" || nb["is_superuser"] != false || nb["first_name"] != "New" || nb["last_name"] != "Bie" {
		t.Fatalf("group %v user %v", pl.groups, nb)
	}
	perms := fmt.Sprint(pl.groups[0]["permissions"])
	if !strings.Contains(perms, "add_document") || strings.Contains(perms, "workflow") || strings.Contains(perms, "user") {
		t.Fatalf("member permissions %s", perms)
	}
	pl.mu.Unlock()
	ovs, _ := e.st.ListOverrides(context.Background(), newbie.ID)
	if ovs[sv.ID].Fields["TOKEN"] != "tok-newbie" {
		t.Fatalf("newbie's own token %+v", ovs[sv.ID].Fields)
	}
	if c := call("PUT", base+"/accounts/"+itoa(kid.ID), map[string]string{"remote_id": "kid"}, nil); c != 200 {
		t.Fatalf("link kid %d", c)
	}

	// Events: two workflows with a JSON webhook carrying Core's secret.
	if c := call("PUT", base, map[string]any{"events_enabled": true}, nil); c != 200 {
		t.Fatalf("events on %d", c)
	}
	pl.mu.Lock()
	hooks := map[string]map[string]any{}
	for _, wf := range pl.workflows {
		act := wf["actions"].([]any)[0].(map[string]any)["webhook"].(map[string]any)
		hooks[wf["name"].(string)] = act
	}
	pl.mu.Unlock()
	added := hooks["Pocket Core · document added"]
	if len(hooks) != 2 || added == nil || added["as_json"] != true || added["params"].(map[string]any)["owner"] != "{{ owner_username }}" {
		t.Fatalf("workflows %v", hooks)
	}
	deliver := func(hook map[string]any, body map[string]string) string {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", strings.Replace(hook["url"].(string), "http://core.test", e.srv.URL, 1), strings.NewReader(string(b)))
		for k, v := range hook["headers"].(map[string]any) {
			req.Header.Set(k, v.(string))
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return string(out)
	}
	now := time.Now().UTC().Add(2 * time.Second).Format(time.RFC3339Nano)
	doc := func(event, owner, added string) map[string]string {
		return map[string]string{"event": event, "doc_id": "7", "title": `Struja "oktobar"`, "owner": owner, "added": added, "created": "2026-10-19", "correspondent": "Elektro"}
	}
	if out := deliver(added, doc("DocumentAdded", "kid", now)); !strings.Contains(out, `"published":true`) {
		t.Fatalf("kid's document: %s", out)
	}
	for why, d := range map[string]map[string]string{
		"before Core connected": doc("DocumentAdded", "kid", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)),
		"no owner":              doc("DocumentAdded", "", now),
		"not a Core user":       doc("DocumentAdded", "stranger", now),
	} {
		if out := deliver(added, d); !strings.Contains(out, why) {
			t.Fatalf("%s: %s", why, out)
		}
	}
	if out := deliver(hooks["Pocket Core · document updated"], doc("DocumentUpdated", "admin", now)); !strings.Contains(out, `"published":true`) {
		t.Fatalf("owner's update: %s", out)
	}
	var evs []struct {
		Topic   string         `json:"topic"`
		Payload map[string]any `json:"payload"`
	}
	call("GET", "/api/v1/events?source=connector:paperless", nil, &evs)
	got := map[string]map[string]any{}
	for _, ev := range evs {
		got[ev.Topic] = ev.Payload
	}
	if a := got["paperless.document_added"]; len(evs) != 2 || a["user_email"] != "kid@b.c" || a["title"] != `Struja "oktobar"` || a["url"] != "https://docs.example/documents/7/details" {
		t.Fatalf("events %+v", evs)
	}
	if u := got["paperless.document_updated"]; u["user_email"] != "aleks@b.c" {
		t.Fatalf("update %+v", u)
	}
	call("PUT", base, map[string]any{"events_enabled": false}, nil)
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if len(pl.workflows) != 0 {
		t.Fatalf("workflows left behind: %v", pl.workflows)
	}
}
