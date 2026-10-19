package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeABS is a small stand-in for an Audiobookshelf 2.37 server.
type fakeABS struct {
	*httptest.Server
	mu        sync.Mutex
	adminKey  string
	users     []map[string]any
	keys      map[string]string // api key → user id
	passwords map[string]string // username → password sent at creation
	auth      map[string]any    // auth settings
	inits     int               // times the OpenID client was (re)built
	patches   int
	sessions  []map[string]any
	progress  map[string][]map[string]any
	items     map[string][]map[string]any // library id → items, oldest first
	episodes  []map[string]any
	n         int
}

func newFakeABS(t *testing.T) *fakeABS {
	f := &fakeABS{adminKey: "abs-admin-key", keys: map[string]string{}, passwords: map[string]string{}, progress: map[string][]map[string]any{},
		items: map[string][]map[string]any{"lib-books": {}, "lib-pods": {}}}
	f.users = []map[string]any{
		{"id": "u-root", "username": "root", "email": "aleks@b.c", "type": "root", "isActive": true, "hasOpenIDLink": true},
		{"id": "u-kid", "username": "kid", "email": nil, "type": "user", "isActive": true, "hasOpenIDLink": false},
	}
	f.auth = map[string]any{
		"authActiveAuthMethods": []any{"openid"}, "authOpenIDIssuerURL": "https://auth.example", "authOpenIDClientID": "pocketid-client",
		"authOpenIDClientSecret": "pocketid-secret", "authOpenIDButtonText": "Login with OpenId", "authOpenIDMatchExistingBy": "email",
		"authOpenIDMobileRedirectURIs": []any{"audiobookshelf://oauth", "audra://oauth"}, "authOpenIDAutoRegister": false,
		"authOpenIDGroupClaim": "groups", "authOpenIDSubfolderForRedirectURLs": "/audiobookshelf",
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		p := r.URL.Path
		if p == "/status" {
			js(map[string]any{"app": "audiobookshelf", "serverVersion": "2.37.1", "isInit": true, "authMethods": f.auth["authActiveAuthMethods"]})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.adminKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		user := func(id string) map[string]any {
			for _, u := range f.users {
				if u["id"] == id {
					return u
				}
			}
			return nil
		}
		switch {
		case p == "/api/users" && r.Method == "GET":
			js(map[string]any{"users": f.users})
		case p == "/api/users" && r.Method == "POST":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			for _, u := range f.users {
				if strings.EqualFold(fmt.Sprint(u["username"]), fmt.Sprint(in["username"])) {
					w.WriteHeader(400)
					_, _ = w.Write([]byte("Username already taken"))
					return
				}
			}
			f.n++
			u := map[string]any{"id": fmt.Sprintf("u-new-%d", f.n), "username": in["username"], "email": in["email"], "type": in["type"],
				"isActive": in["isActive"] == true, "hasOpenIDLink": false}
			f.users = append(f.users, u)
			f.passwords[fmt.Sprint(in["username"])] = fmt.Sprint(in["password"])
			js(map[string]any{"user": u})
		case p == "/api/api-keys" && r.Method == "POST":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if user(fmt.Sprint(in["userId"])) == nil || in["isActive"] != true {
				w.WriteHeader(400)
				return
			}
			f.n++
			k := fmt.Sprintf("abs-key-%d", f.n)
			f.keys[k] = fmt.Sprint(in["userId"])
			js(map[string]any{"apiKey": map[string]any{"apiKey": k, "id": "k" + k, "name": in["name"]}})
		case strings.HasSuffix(p, "/openid-unlink"):
			u := user(strings.TrimSuffix(strings.TrimPrefix(p, "/api/users/"), "/openid-unlink"))
			if u == nil {
				w.WriteHeader(404)
				return
			}
			u["hasOpenIDLink"] = false
		case strings.HasPrefix(p, "/api/users/"):
			u := user(strings.TrimPrefix(p, "/api/users/"))
			if u == nil {
				w.WriteHeader(404)
				return
			}
			if r.Method == "PATCH" {
				var in map[string]any
				_ = json.NewDecoder(r.Body).Decode(&in)
				if u["type"] == "root" {
					w.WriteHeader(403) // the admin key is not root's
					return
				}
				u["email"] = in["email"]
				return
			}
			out := map[string]any{}
			for k, v := range u {
				out[k] = v
			}
			out["mediaProgress"] = f.progress[u["id"].(string)]
			js(out)
		case p == "/api/auth-settings" && r.Method == "GET":
			js(f.auth)
		case p == "/api/auth-settings" && r.Method == "PATCH":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.patches++
			had := hasOpenID(f.auth["authActiveAuthMethods"])
			for k, v := range in {
				if k == "authOpenIDClientSecret" && v == "" {
					v = nil
				}
				f.auth[k] = v
			}
			if !had && hasOpenID(f.auth["authActiveAuthMethods"]) {
				f.inits++ // the only moment Audiobookshelf rebuilds its OpenID client
			}
			js(map[string]any{"updated": true})
		case p == "/api/sessions":
			js(map[string]any{"sessions": f.sessions})
		case p == "/api/libraries":
			js(map[string]any{"libraries": []map[string]any{{"id": "lib-books", "name": "Audiobooks", "mediaType": "book"}, {"id": "lib-pods", "name": "Podcasts", "mediaType": "podcast"}}})
		case strings.HasPrefix(p, "/api/libraries/") && strings.HasSuffix(p, "/items"):
			lib := strings.TrimSuffix(strings.TrimPrefix(p, "/api/libraries/"), "/items")
			list := f.items[lib]
			out := make([]map[string]any, len(list))
			for i := range list {
				out[i] = list[len(list)-1-i] // newest first
			}
			js(map[string]any{"results": out})
		case p == "/api/libraries/lib-pods/recent-episodes":
			js(map[string]any{"episodes": f.episodes})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func hasOpenID(v any) bool {
	switch l := v.(type) {
	case []any:
		for _, x := range l {
			if x == "openid" {
				return true
			}
		}
	case []string:
		for _, x := range l {
			if x == "openid" {
				return true
			}
		}
	}
	return false
}

func TestAudiobookshelfConnector(t *testing.T) {
	e := newEnv(t)
	abs := newFakeABS(t)
	responses := []string{}
	call := func(method, path string, body any, out any) int {
		t.Helper()
		code, txt := e.raw(method, path, "", body)
		responses = append(responses, txt)
		if out != nil {
			_ = json.Unmarshal([]byte(txt), out)
		}
		return code
	}
	call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)
	var kid struct {
		ID int64 `json:"id"`
	}
	call("POST", "/api/v1/users", map[string]string{"email": "kid@b.c", "password": "password123", "role": "member"}, &kid)
	var sv struct {
		ID int64 `json:"id"`
	}
	if c := call("POST", "/api/v1/services", map[string]any{"type": "audiobookshelf", "url": abs.URL, "public_url": "https://abs.example", "fields": map[string]string{"TOKEN": abs.adminKey}}, &sv); c != 201 {
		t.Fatalf("create service %d", c)
	}
	base := "/api/v1/services/" + itoa(sv.ID) + "/connector"

	var tr struct {
		OK   bool   `json:"ok"`
		Err  string `json:"error"`
		Info struct {
			Version string `json:"version"`
			Users   int    `json:"users"`
			Detail  string `json:"detail"`
		} `json:"info"`
	}
	call("POST", base+"/test", nil, &tr)
	if !tr.OK || tr.Info.Version != "2.37.1" || tr.Info.Users != 2 || !strings.Contains(tr.Info.Detail, "https://auth.example") {
		t.Fatalf("test %+v", tr)
	}
	call("PUT", base, map[string]any{"enabled": true}, nil)

	// Provision: aleks matches root by email (linked, no key); kid has no
	// email match, the name "kid" is someone else's, so a new account named
	// by the full email gets its own API key.
	var prov struct {
		Results []struct {
			Email      string `json:"email"`
			Status     string `json:"status"`
			RemoteID   string `json:"remote_id"`
			Credential string `json:"credential"`
			Error      string `json:"error"`
		} `json:"results"`
	}
	call("POST", base+"/provision", map[string]any{"all": true}, &prov)
	if len(prov.Results) != 2 {
		t.Fatalf("provision %+v", prov)
	}
	for _, r := range prov.Results {
		switch r.Email {
		case "aleks@b.c":
			if r.Status != "linked" || r.RemoteID != "u-root" {
				t.Fatalf("aleks %+v", r)
			}
		case "kid@b.c":
			if r.Status != "ok" || r.Credential != "connector" || r.Error != "" || !strings.HasPrefix(r.RemoteID, "u-new-") {
				t.Fatalf("kid %+v", r)
			}
		}
	}
	abs.mu.Lock()
	created := abs.users[len(abs.users)-1]
	pw := abs.passwords["kid@b.c"]
	nkeys := len(abs.keys)
	abs.mu.Unlock()
	if created["username"] != "kid@b.c" || created["email"] != "kid@b.c" || created["isActive"] != true || created["type"] != "user" {
		t.Fatalf("created %+v", created)
	}
	if len(pw) < 32 || nkeys != 1 {
		t.Fatalf("password %d chars, %d keys", len(pw), nkeys)
	}
	for _, txt := range responses {
		if strings.Contains(txt, pw) {
			t.Fatalf("the throwaway password left Core")
		}
	}
	var kidCreds []map[string]any
	call("GET", "/api/v1/users/"+itoa(kid.ID)+"/credentials", nil, &kidCreds)
	if b, _ := json.Marshal(kidCreds); !strings.Contains(string(b), "u-new-") {
		t.Fatalf("kid's override should carry USER_ID: %s", b)
	}

	// Linking by hand lists every account.
	var remote []map[string]any
	if c := call("GET", base+"/remote-accounts", nil, &remote); c != 200 || len(remote) != 3 {
		t.Fatalf("remote accounts %d %+v", c, remote)
	}

	// Sign-in: Core replaces PocketID, keeps a copy, detaches root from the
	// old provider and rebuilds the OpenID client without a restart.
	if c, txt := e.raw("PUT", base, "", map[string]any{"sso_enabled": true}); c != 200 {
		t.Fatalf("sso on %d %s", c, txt)
	}
	abs.mu.Lock()
	a := abs.auth
	root := abs.users[0]
	inits, patches := abs.inits, abs.patches
	abs.mu.Unlock()
	if a["authOpenIDIssuerURL"] != "http://core.test" || a["authOpenIDTokenURL"] != "http://core.test/oidc/token" || a["authOpenIDJwksURL"] != "http://core.test/oidc/jwks" ||
		a["authOpenIDButtonText"] != "Sign in with Pocket Core" || a["authOpenIDMatchExistingBy"] != "email" || a["authOpenIDGroupClaim"] != "" ||
		a["authOpenIDClientSecret"] == "pocketid-secret" || a["authOpenIDAutoRegister"] != false {
		t.Fatalf("auth settings %+v", a)
	}
	if fmt.Sprint(a["authActiveAuthMethods"]) != "[openid]" || fmt.Sprint(a["authOpenIDMobileRedirectURIs"]) != "[audiobookshelf://oauth audra://oauth]" {
		t.Fatalf("methods/redirects %+v / %+v", a["authActiveAuthMethods"], a["authOpenIDMobileRedirectURIs"])
	}
	if inits != 1 || patches != 2 {
		t.Fatalf("the OpenID client must be rebuilt once: inits %d patches %d", inits, patches)
	}
	if root["hasOpenIDLink"] != false {
		t.Fatalf("root still tied to PocketID")
	}
	orig, err := os.ReadFile(filepath.Join(e.dir, "connectors", "audiobookshelf-sso-original.json"))
	if err != nil || !strings.Contains(string(orig), "https://auth.example") {
		t.Fatalf("original backup %v %s", err, orig)
	}
	var redirects struct {
		Redirects []string `json:"redirects"`
	}
	call("GET", base, nil, &redirects)
	if len(redirects.Redirects) != 4 || redirects.Redirects[0] != "https://abs.example/audiobookshelf/auth/openid/callback" {
		t.Fatalf("redirects %+v", redirects)
	}
	// Turning it on again (a relink) keeps the PocketID copy, not Core's own settings.
	call("PUT", base+"/accounts/1", map[string]string{"remote_id": "u-root"}, nil)
	if b, _ := os.ReadFile(filepath.Join(e.dir, "connectors", "audiobookshelf-sso-original.json")); !strings.Contains(string(b), "https://auth.example") {
		t.Fatalf("original overwritten: %s", b)
	}

	// Off: PocketID comes back as it was.
	if c, txt := e.raw("PUT", base, "", map[string]any{"sso_enabled": false}); c != 200 {
		t.Fatalf("sso off %d %s", c, txt)
	}
	abs.mu.Lock()
	a = abs.auth
	abs.mu.Unlock()
	if a["authOpenIDIssuerURL"] != "https://auth.example" || a["authOpenIDClientSecret"] != "pocketid-secret" || a["authOpenIDGroupClaim"] != "groups" ||
		fmt.Sprint(a["authActiveAuthMethods"]) != "[openid]" {
		t.Fatalf("after off %+v", a)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "connectors", "audiobookshelf-sso-original.json")); err == nil {
		t.Fatalf("the original copy should be marked restored")
	}

	// Events: baseline, then listening by root (= aleks), a finished book, new items, a new account.
	now := time.Now()
	ms := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }
	abs.mu.Lock()
	abs.sessions = []map[string]any{{"id": "s-old", "userId": "u-root", "displayTitle": "Old", "startedAt": ms(-48 * time.Hour), "updatedAt": ms(-47 * time.Hour)}}
	abs.items["lib-books"] = []map[string]any{{"id": "li-old", "addedAt": ms(-72 * time.Hour), "mediaType": "book", "media": map[string]any{"metadata": map[string]any{"title": "Old book"}}}}
	abs.mu.Unlock()
	call("PUT", base, map[string]any{"events_enabled": true}, nil)
	var pr map[string]any
	call("POST", base+"/poll", nil, &pr)
	if pr["baseline"] != true {
		t.Fatalf("first poll %+v", pr)
	}
	abs.mu.Lock()
	abs.sessions = append([]map[string]any{
		{"id": "s-now", "userId": "u-root", "libraryItemId": "li-dune", "mediaType": "book", "displayTitle": "Dune", "displayAuthor": "Frank Herbert",
			"duration": 3600.0, "timeListening": 0.0, "currentTime": 10.0, "startedAt": ms(-2 * time.Minute), "updatedAt": ms(-1 * time.Minute),
			"deviceInfo": map[string]any{"clientName": "Abs Android", "deviceName": "Pixel"}, "user": map[string]any{"username": "root"}},
	}, abs.sessions...)
	abs.progress["u-root"] = []map[string]any{{"libraryItemId": "li-book2", "isFinished": true, "finishedAt": ms(-30 * time.Minute), "displayTitle": "Book Two", "duration": 7200.0}}
	abs.items["lib-books"] = append(abs.items["lib-books"], map[string]any{"id": "li-dune", "addedAt": ms(-10 * time.Minute), "mediaType": "book",
		"media": map[string]any{"duration": 3600.0, "metadata": map[string]any{"title": "Dune", "authorName": "Frank Herbert", "seriesName": "Dune #1"}}})
	abs.episodes = []map[string]any{{"id": "ep-1", "libraryItemId": "li-pod", "title": "Episode 1", "addedAt": ms(-5 * time.Minute), "publishedAt": ms(-6 * time.Hour),
		"podcast": map[string]any{"metadata": map[string]any{"title": "Show"}}}}
	abs.users = append(abs.users, map[string]any{"id": "u-guest", "username": "guest", "type": "guest", "isActive": true})
	abs.mu.Unlock()
	call("POST", base+"/poll", nil, &pr)
	if pr["events"] != float64(5) {
		t.Fatalf("second poll %+v", pr)
	}
	type ev struct {
		Topic   string         `json:"topic"`
		Subject string         `json:"subject"`
		Payload map[string]any `json:"payload"`
	}
	topics := func() map[string]map[string]any {
		var evs []ev
		call("GET", "/api/v1/events?topic=audiobookshelf.*", nil, &evs)
		got := map[string]map[string]any{}
		for _, x := range evs {
			got[x.Topic] = x.Payload
		}
		return got
	}
	got := topics()
	if p := got["audiobookshelf.listening_started"]; p == nil || p["user_email"] != "aleks@b.c" || p["title"] != "Dune" || p["author"] != "Frank Herbert" ||
		p["device"] != "Abs Android · Pixel" || p["url"] != "https://abs.example/audiobookshelf/item/li-dune" {
		t.Fatalf("listening started %+v", got)
	}
	if p := got["audiobookshelf.item_finished"]; p == nil || p["user_email"] != "aleks@b.c" || p["title"] != "Book Two" || p["media_type"] != "book" {
		t.Fatalf("finished %+v", got)
	}
	if p := got["audiobookshelf.item_added"]; p == nil || p["title"] != "Dune" || p["library"] != "Audiobooks" || p["series"] != "Dune #1" || p["user_email"] != nil {
		t.Fatalf("item added %+v", got)
	}
	if p := got["audiobookshelf.episode_added"]; p == nil || p["podcast"] != "Show" || p["title"] != "Episode 1" {
		t.Fatalf("episode added %+v", got)
	}
	if p := got["audiobookshelf.user_created"]; p == nil || p["audiobookshelf_user"] != "guest" {
		t.Fatalf("user created %+v", got)
	}
	if got["audiobookshelf.listening_ended"] != nil {
		t.Fatalf("a session synced a minute ago is not over")
	}
	// Nobody synced it for 20 minutes: it ended, with how long they listened.
	abs.mu.Lock()
	abs.sessions[0]["updatedAt"] = ms(-20 * time.Minute)
	abs.sessions[0]["timeListening"] = 1500.0
	abs.sessions[0]["currentTime"] = 1800.0
	abs.mu.Unlock()
	call("POST", base+"/poll", nil, &pr)
	if pr["events"] != float64(1) {
		t.Fatalf("third poll %+v", pr)
	}
	if p := topics()["audiobookshelf.listening_ended"]; p == nil || p["user_email"] != "aleks@b.c" || p["listened_seconds"] != float64(1500) || p["progress"] != 0.5 {
		t.Fatalf("listening ended %+v", p)
	}
	call("POST", base+"/poll", nil, &pr)
	if pr["events"] != float64(0) {
		t.Fatalf("nothing new: %+v", pr)
	}
}
