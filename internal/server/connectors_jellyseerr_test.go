package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// fakeJellyseerr answers the Seerr v1 calls the connector makes.
type fakeJellyseerr struct {
	*httptest.Server
	mu       sync.Mutex
	users    []map[string]any
	requests []map[string]any
	issues   []map[string]any
	imports  []string
}

func newFakeJellyseerr(t *testing.T) *fakeJellyseerr {
	f := &fakeJellyseerr{
		users: []map[string]any{
			{"id": 1, "email": "aleks@b.c", "displayName": "root", "jellyfinUserId": "aaaa1111", "jellyfinUsername": "root", "permissions": 2},
			{"id": 2, "email": "kiddo", "displayName": "kiddo", "jellyfinUserId": "bbbb2222", "jellyfinUsername": "kiddo", "permissions": 32},
		},
	}
	req := func(id, status, media int, by int) map[string]any {
		return map[string]any{"id": id, "status": status, "type": "movie", "is4k": false, "createdAt": "2026-10-18T10:00:00.000Z", "updatedAt": "2026-10-18T10:00:00.000Z",
			"media": map[string]any{"tmdbId": 100 + id, "status": media, "status4k": 1}, "requestedBy": map[string]any{"id": by, "displayName": "x"}}
	}
	f.requests = []map[string]any{req(1, 2, 3, 1), req(2, 1, 2, 2)}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("X-Api-Key") != "seerr-key" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		page := func(list []map[string]any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"pageInfo": map[string]any{"results": len(list)}, "results": list})
		}
		p := r.URL.Path
		switch {
		case p == "/api/v1/status":
			_, _ = w.Write([]byte(`{"version":"3.5.0"}`))
		case p == "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(f.users[0])
		case p == "/api/v1/user":
			page(f.users)
		case p == "/api/v1/user/2":
			_ = json.NewEncoder(w).Encode(f.users[1])
		case p == "/api/v1/user/import-from-jellyfin":
			var in struct {
				IDs []string `json:"jellyfinUserIds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.imports = append(f.imports, in.IDs...)
			u := map[string]any{"id": 3, "email": "newbie", "displayName": "newbie", "jellyfinUserId": in.IDs[0], "jellyfinUsername": "newbie", "permissions": 32}
			f.users = append(f.users, u)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode([]any{u})
		case p == "/api/v1/request":
			page(f.requests)
		case p == "/api/v1/issue":
			page(f.issues)
		case strings.HasPrefix(p, "/api/v1/movie/"):
			_, _ = w.Write([]byte(`{"title":"Movie ` + strings.TrimPrefix(p, "/api/v1/movie/") + `","releaseDate":"1999-04-01"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestJellyseerrConnector(t *testing.T) {
	e := newEnv(t)
	js := newFakeJellyseerr(t)
	call := func(method, path string, body any, out any) int {
		t.Helper()
		code, txt := e.raw(method, path, "", body)
		if out != nil {
			_ = json.Unmarshal([]byte(txt), out)
		}
		return code
	}
	call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)
	ids := map[string]int64{}
	for _, em := range []string{"kid@b.c", "newbie@b.c", "loner@b.c"} {
		var u struct {
			ID int64 `json:"id"`
		}
		call("POST", "/api/v1/users", map[string]string{"email": em, "password": "password123", "role": "member"}, &u)
		ids[em] = u.ID
	}
	// Core's Jellyfin connector linked kid and newbie to their Jellyfin accounts.
	var jf struct {
		ID int64 `json:"id"`
	}
	call("POST", "/api/v1/services", map[string]any{"type": "jellyfin", "url": "http://jf.invalid", "fields": map[string]string{"API_KEY": "x"}}, &jf)
	for em, rid := range map[string]string{"kid@b.c": "BBBB-2222", "newbie@b.c": "cccc3333"} {
		if err := e.st.SaveConnectorAccount(t.Context(), &store.ConnectorAccount{ServiceID: jf.ID, UserID: ids[em], RemoteID: rid, Status: "linked"}); err != nil {
			t.Fatal(err)
		}
	}
	var sv struct {
		ID int64 `json:"id"`
	}
	call("POST", "/api/v1/services", map[string]any{"type": "jellyseerr", "url": js.URL, "public_url": "https://req.example", "fields": map[string]string{"API_KEY": "seerr-key"}}, &sv)
	base := "/api/v1/services/" + itoa(sv.ID) + "/connector"
	var tr struct {
		OK   bool `json:"ok"`
		Info struct {
			Version string `json:"version"`
			Users   int    `json:"users"`
			Admin   bool   `json:"admin"`
		} `json:"info"`
	}
	call("POST", base+"/test", nil, &tr)
	if !tr.OK || tr.Info.Version != "3.5.0" || tr.Info.Users != 2 || !tr.Info.Admin {
		t.Fatalf("test %+v", tr)
	}
	if c := call("PUT", base, map[string]any{"sso_enabled": true}, nil); c != 400 {
		t.Fatalf("no sign-in switch for Jellyseerr, got %d", c)
	}
	call("PUT", base, map[string]any{"enabled": true}, nil)
	var prov struct {
		Results []struct {
			Email, Status, Error, Note string
			RemoteID                   string `json:"remote_id"`
		}
	}
	call("POST", base+"/provision", map[string]any{"all": true}, &prov)
	got := map[string]string{}
	for _, r := range prov.Results {
		got[r.Email] = r.Status + "/" + r.RemoteID
		if r.Email == "loner@b.c" && !strings.Contains(r.Error, "Jellyfin connector first") {
			t.Fatalf("loner %+v", r)
		}
	}
	// aleks by email, kid through their Jellyfin account (dashes and case don't matter), newbie imported.
	if got["aleks@b.c"] != "linked/1" || got["kid@b.c"] != "linked/2" || got["newbie@b.c"] != "linked/3" || got["loner@b.c"] != "error/" {
		t.Fatalf("provision %v", got)
	}
	if len(js.imports) != 1 || js.imports[0] != "cccc3333" {
		t.Fatalf("imports %v", js.imports)
	}

	// Events: the first check is a baseline, then status changes become events for the requester.
	call("PUT", base, map[string]any{"events_enabled": true}, nil)
	var pr struct {
		Baseline bool `json:"baseline"`
		Events   int  `json:"events"`
	}
	call("POST", base+"/poll", nil, &pr)
	if !pr.Baseline {
		t.Fatalf("first poll %+v", pr)
	}
	js.mu.Lock()
	js.requests[0]["media"].(map[string]any)["status"] = 5 // aleks's movie arrived
	js.requests[1]["status"] = 2                           // kid's request approved
	js.requests = append(js.requests, map[string]any{"id": 3, "status": 1, "type": "movie", "createdAt": "2026-10-19T10:00:00.000Z", "updatedAt": "2026-10-19T10:00:00.000Z",
		"media": map[string]any{"tmdbId": 103, "status": 2}, "requestedBy": map[string]any{"id": 3, "displayName": "newbie"}})
	js.issues = append(js.issues, map[string]any{"id": 1, "issueType": 3, "status": 1, "media": map[string]any{"tmdbId": 101, "mediaType": "movie"}, "createdBy": map[string]any{"id": 2}})
	js.mu.Unlock()
	call("POST", base+"/poll", nil, &pr)
	var evs []struct {
		Topic   string         `json:"topic"`
		Payload map[string]any `json:"payload"`
	}
	call("GET", "/api/v1/events?source=connector:jellyseerr", nil, &evs)
	seen := map[string]string{}
	for _, ev := range evs {
		seen[ev.Topic] = ev.Payload["user_email"].(string) + " " + ev.Payload["title"].(string)
	}
	want := map[string]string{
		"jellyseerr.media_available":  "aleks@b.c Movie 101",
		"jellyseerr.request_approved": "kid@b.c Movie 102",
		"jellyseerr.request_created":  "newbie@b.c Movie 103",
		"jellyseerr.issue_created":    "kid@b.c Movie 101",
	}
	if len(evs) != 4 {
		t.Fatalf("events %+v", evs)
	}
	for k, v := range want {
		if seen[k] != v {
			t.Fatalf("%s = %q, want %q (all %v)", k, seen[k], v, seen)
		}
	}
	// Nothing changed: nothing new.
	call("POST", base+"/poll", nil, &pr)
	if pr.Events != 0 {
		t.Fatalf("a quiet poll published %d", pr.Events)
	}
}
