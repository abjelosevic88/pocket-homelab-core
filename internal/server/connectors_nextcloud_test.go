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

// fakeNextcloud answers the OCS calls the connector makes. Like the real
// one, it refuses app passwords where Nextcloud asks for password
// confirmation (adding users, user_oidc providers).
type fakeNextcloud struct {
	*httptest.Server
	mu        sync.Mutex
	users     map[string]map[string]any // uid → id, email, displayname, groups, enabled
	passwords map[string]string         // uid → login password
	appPass   map[string]string         // app password → uid
	providers map[int]map[string]any
	hooks     map[int]map[string]any
	nextID    int
	propfinds []string
}

func newFakeNextcloud(t *testing.T) *fakeNextcloud {
	f := &fakeNextcloud{
		users: map[string]map[string]any{
			"aleks": {"id": "aleks", "email": "aleks@b.c", "displayname": "Aleks", "groups": []string{"admin"}, "enabled": true},
			"kid":   {"id": "kid", "email": "someone.else@b.c", "displayname": "Kid (NC)", "groups": []string{}, "enabled": true},
		},
		passwords: map[string]string{"aleks": "aleks-login-pw"},
		appPass:   map[string]string{"aleks-app-pw": "aleks"},
		providers: map[int]map[string]any{}, hooks: map[int]map[string]any{}, nextID: 1,
	}
	ocs := func(w http.ResponseWriter, code int, data any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{"meta": map[string]any{"statuscode": code, "message": http.StatusText(code)}, "data": data}})
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/status.php" {
			_, _ = io.WriteString(w, `{"installed":true,"maintenance":false,"versionstring":"34.0.3"}`)
			return
		}
		user, pw, _ := r.BasicAuth()
		who, byPassword := "", false
		if f.passwords[user] != "" && f.passwords[user] == pw {
			who, byPassword = user, true
		} else if f.appPass[pw] == user && user != "" {
			who = user
		}
		if who == "" {
			ocs(w, 401, nil)
			return
		}
		admin := false
		for _, g := range f.users[who]["groups"].([]string) {
			admin = admin || g == "admin"
		}
		confirm := func() bool {
			if !byPassword {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(403)
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":403,"message":"Password confirmation is required"},"data":[]}}`)
				return false
			}
			return true
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p := r.URL.Path
		switch {
		case r.Method == "PROPFIND" && strings.HasPrefix(p, "/remote.php/dav/files/"):
			f.propfinds = append(f.propfinds, who)
			w.WriteHeader(207)
		case p == "/ocs/v2.php/cloud/user":
			ocs(w, 200, f.users[who])
		case p == "/ocs/v2.php/core/getapppassword":
			if !byPassword {
				ocs(w, 403, nil)
				return
			}
			ap := "app-" + who
			f.appPass[ap] = who
			ocs(w, 200, map[string]string{"apppassword": ap})
		case !admin:
			ocs(w, 403, nil)
		case p == "/ocs/v2.php/cloud/apps":
			ocs(w, 200, map[string]any{"apps": []string{"files", "user_oidc", "webhook_listeners"}})
		case p == "/ocs/v2.php/cloud/users/details":
			if r.URL.Query().Get("offset") != "0" {
				ocs(w, 200, map[string]any{"users": []any{}})
				return
			}
			ocs(w, 200, map[string]any{"users": f.users})
		case strings.HasPrefix(p, "/ocs/v2.php/cloud/users/") && r.Method == http.MethodGet:
			if u, ok := f.users[strings.TrimPrefix(p, "/ocs/v2.php/cloud/users/")]; ok {
				ocs(w, 200, u)
			} else {
				ocs(w, 404, nil)
			}
		case p == "/ocs/v2.php/cloud/users" && r.Method == http.MethodPost:
			if !confirm() {
				return
			}
			uid := body["userid"].(string)
			f.users[uid] = map[string]any{"id": uid, "email": body["email"], "displayname": body["displayName"], "groups": []string{}, "enabled": true}
			f.passwords[uid] = body["password"].(string)
			ocs(w, 200, map[string]any{"id": uid})
		case strings.HasPrefix(p, "/ocs/v2.php/apps/user_oidc/api/v1/provider"):
			if r.Method == http.MethodGet {
				list := []any{}
				for _, v := range f.providers {
					list = append(list, v)
				}
				ocs(w, 200, list)
				return
			}
			if !confirm() {
				return
			}
			id := f.nextID
			if r.Method != http.MethodPost {
				_, _ = fmt.Sscanf(strings.TrimPrefix(p, "/ocs/v2.php/apps/user_oidc/api/v1/provider/"), "%d", &id)
			} else {
				f.nextID++
			}
			if r.Method == http.MethodDelete {
				delete(f.providers, id)
			} else {
				body["id"] = id
				f.providers[id] = body
			}
			ocs(w, 200, map[string]any{})
		case strings.HasPrefix(p, "/ocs/v2.php/apps/webhook_listeners/api/v1/webhooks"):
			switch r.Method {
			case http.MethodGet:
				list := []any{}
				for _, v := range f.hooks {
					list = append(list, v)
				}
				ocs(w, 200, list)
			case http.MethodPost:
				id := f.nextID
				f.nextID++
				body["id"] = id
				f.hooks[id] = body
				ocs(w, 200, body)
			case http.MethodDelete:
				var id int
				_, _ = fmt.Sscanf(strings.TrimPrefix(p, "/ocs/v2.php/apps/webhook_listeners/api/v1/webhooks/"), "%d", &id)
				if _, ok := f.hooks[id]; !ok {
					ocs(w, 404, nil)
					return
				}
				delete(f.hooks, id)
				ocs(w, 200, nil)
			}
		default:
			ocs(w, 404, nil)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestNextcloudConnector(t *testing.T) {
	e := newEnv(t)
	nc := newFakeNextcloud(t)
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
	fields := map[string]string{"USERNAME": "aleks", "APP_PASSWORD": "aleks-app-pw"}
	if c := call("POST", "/api/v1/services", map[string]any{"type": "nextcloud", "url": nc.URL, "public_url": "https://cloud.example", "fields": fields}, &sv); c != 201 {
		t.Fatalf("create service %d", c)
	}
	base := "/api/v1/services/" + itoa(sv.ID) + "/connector"
	var tr struct {
		OK   bool   `json:"ok"`
		Err  string `json:"error"`
		Info struct {
			Users  int    `json:"users"`
			Detail string `json:"detail"`
		} `json:"info"`
	}
	call("POST", base+"/test", nil, &tr)
	if !tr.OK || tr.Info.Users != 2 || !strings.Contains(tr.Info.Detail, "login password") {
		t.Fatalf("test without the login password %+v", tr)
	}
	call("PUT", base, map[string]any{"enabled": true}, nil)

	type result struct {
		Email, Status, Error, Note, Credential string
		RemoteID                               string `json:"remote_id"`
	}
	var prov struct{ Results []result }
	// Without the login password, Nextcloud refuses to add newbie; the error says what to do.
	call("POST", base+"/provision", map[string]any{"user_id": newbie.ID}, &prov)
	if len(prov.Results) != 1 || prov.Results[0].Status != "error" || !strings.Contains(prov.Results[0].Error, "Login password (connector only)") {
		t.Fatalf("provision without login password %+v", prov)
	}
	fields["ADMIN_PASSWORD"] = "aleks-login-pw"
	if c := call("PUT", "/api/v1/services/"+itoa(sv.ID), map[string]any{"fields": fields}, nil); c != 200 {
		t.Fatalf("update service %d", c)
	}
	prov.Results = nil
	call("POST", base+"/provision", map[string]any{"all": true}, &prov)
	got := map[string]result{}
	for _, r := range prov.Results {
		got[r.Email] = r
	}
	if r := got["aleks@b.c"]; r.Status != "linked" || r.RemoteID != "aleks" || r.Credential != "none" {
		t.Fatalf("aleks %+v", r)
	}
	if r := got["kid@b.c"]; r.Status != "error" || !strings.Contains(r.Error, `named "kid" already exists with another email`) {
		t.Fatalf("kid must not take over the kid account %+v", r)
	}
	if r := got["newbie@b.c"]; r.Status != "ok" || r.RemoteID != "newbie" || r.Credential != "connector" {
		t.Fatalf("newbie %+v", r)
	}
	nc.mu.Lock()
	if len(nc.propfinds) != 1 || nc.propfinds[0] != "newbie" {
		t.Fatalf("the new account's files were not opened once: %v", nc.propfinds)
	}
	nc.mu.Unlock()
	// newbie's own credential: their user name and an app password, in Core.
	ovs, _ := e.st.ListOverrides(context.Background(), newbie.ID)
	if ov := ovs[sv.ID]; ov.Fields["USERNAME"] != "newbie" || ov.Fields["APP_PASSWORD"] != "app-newbie" {
		t.Fatalf("newbie's credential %+v", ov.Fields)
	}
	// Link kid by hand to the existing account.
	var link result
	if c := call("PUT", base+"/accounts/"+itoa(kid.ID), map[string]string{"remote_id": "kid"}, &link); c != 200 || link.Status != "linked" {
		t.Fatalf("link kid %d %+v", c, link)
	}

	// The login password never leaves Core: not in the profile, not settable per person.
	code, prof := e.raw("GET", "/api/v1/pair/profile", "", nil)
	if code != 200 || strings.Contains(prof, "aleks-login-pw") || strings.Contains(prof, "NEXTCLOUD_ADMIN_PASSWORD") || !strings.Contains(prof, "NEXTCLOUD_APP_PASSWORD") {
		t.Fatalf("profile %d leaks or lacks fields: %s", code, prof)
	}
	call("PUT", "/api/v1/users/"+itoa(kid.ID)+"/credentials/"+itoa(sv.ID), map[string]any{"fields": map[string]string{"USERNAME": "kid", "ADMIN_PASSWORD": "x"}}, nil)
	ovs, _ = e.st.ListOverrides(context.Background(), kid.ID)
	if _, set := ovs[sv.ID].Fields["ADMIN_PASSWORD"]; set {
		t.Fatal("a connector-only field was stored per person")
	}
	call("DELETE", "/api/v1/users/"+itoa(kid.ID)+"/credentials/"+itoa(sv.ID), nil, nil)

	// Sign-in: one provider, uid from pocket_uid, Nextcloud keeps names, emails and groups.
	if c := call("PUT", base, map[string]any{"sso_enabled": true}, nil); c != 200 {
		t.Fatalf("sso on %d", c)
	}
	nc.mu.Lock()
	if len(nc.providers) != 1 {
		t.Fatalf("providers %v", nc.providers)
	}
	for _, p := range nc.providers {
		st := p["settings"].(map[string]any)
		if p["identifier"] != "Pocket Core" || st["mappingUid"] != "pocket_uid" || st["uniqueUid"] != false || st["groupProvisioning"] != false || st["mappingEmail"] == "email" {
			t.Fatalf("provider %v", p)
		}
		if !strings.HasSuffix(p["discoveryEndpoint"].(string), "/.well-known/openid-configuration") {
			t.Fatalf("discovery %v", p["discoveryEndpoint"])
		}
	}
	nc.mu.Unlock()
	ctx := context.Background()
	cl, err := e.st.GetOIDCClientByService(ctx, sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   int64
		want any
	}{{1, "aleks"}, {kid.ID, "kid"}, {newbie.ID, "newbie"}} {
		u, _ := e.st.GetUser(ctx, c.id)
		if got := e.s.clientClaims(ctx, cl, u)["pocket_uid"]; got != c.want {
			t.Fatalf("pocket_uid for %s = %v, want %v", u.Email, got, c.want)
		}
	}
	call("PUT", base+"/accounts/"+itoa(kid.ID), map[string]string{"remote_id": ""}, nil)
	if u, _ := e.st.GetUser(ctx, kid.ID); e.s.clientClaims(ctx, cl, u) != nil {
		t.Fatal("an unlinked person must get no pocket_uid (Nextcloud then refuses the sign-in)")
	}
	call("PUT", base+"/accounts/"+itoa(kid.ID), map[string]string{"remote_id": "kid"}, nil)

	// Events: five box-wide webhooks with Core's secret header.
	if c := call("PUT", base, map[string]any{"events_enabled": true}, nil); c != 200 {
		t.Fatalf("events on %d", c)
	}
	nc.mu.Lock()
	var fileHook map[string]any
	for _, h := range nc.hooks {
		if strings.HasSuffix(h["event"].(string), `\NodeCreatedEvent`) {
			fileHook = h
		}
	}
	n := len(nc.hooks)
	nc.mu.Unlock()
	if n != 5 || fileHook == nil || fileHook["authMethod"] != "header" {
		t.Fatalf("webhooks %d %v", n, fileHook)
	}
	var secret string
	for _, v := range fileHook["authData"].(map[string]any) {
		secret = v.(string)
	}
	deliver := func(body string) (int, string) {
		req, _ := http.NewRequest("POST", fileHook["uri"].(string), strings.NewReader(body))
		req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(e.srv.URL, "http://")
		req.Header.Set("X-Pocket-Hook", secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	now := time.Now().Add(2 * time.Second).Unix()
	file := func(path string, at int64) string {
		return fmt.Sprintf(`{"user":{"uid":"kid"},"time":%d,"event":{"class":"OCP\\Files\\Events\\Node\\NodeCreatedEvent","node":{"id":42,"path":%q}}}`, at, path)
	}
	if c, b := deliver(file("/kid/files/Photos/beach.jpg", now)); c != 202 || !strings.Contains(b, `"published":true`) {
		t.Fatalf("kid's upload %d %s", c, b)
	}
	for _, d := range []struct{ body, why string }{
		{file("/kid/files/Readme.md", now-3600), "before Core connected"},
		{file("/kid/files_versions/a.txt.v1", now), "not in a person's files"},
		{file("/kid/files/.DS_Store", now), "hidden"},
		{file("/stranger/files/x.txt", now), "not a Core user"},
	} {
		if c, b := deliver(d.body); c != 202 || !strings.Contains(b, d.why) {
			t.Fatalf("%s: %d %s", d.why, c, b)
		}
	}
	var evs []struct {
		Topic   string         `json:"topic"`
		Payload map[string]any `json:"payload"`
	}
	call("GET", "/api/v1/events?source=connector:nextcloud", nil, &evs)
	if len(evs) != 1 || evs[0].Topic != "nextcloud.file_created" || evs[0].Payload["user_email"] != "kid@b.c" || evs[0].Payload["url"] != "https://cloud.example/f/42" {
		t.Fatalf("events %+v", evs)
	}

	// Off again: webhooks and the provider are gone.
	call("PUT", base, map[string]any{"events_enabled": false, "sso_enabled": false}, nil)
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if len(nc.hooks) != 0 || len(nc.providers) != 0 {
		t.Fatalf("left behind: %d webhooks, %d providers", len(nc.hooks), len(nc.providers))
	}
}
