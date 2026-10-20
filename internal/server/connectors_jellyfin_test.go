package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeJellyfin is a small stand-in for a Jellyfin 10.11 server.
type fakeJellyfin struct {
	*httptest.Server
	mu         sync.Mutex
	adminKey   string
	users      []map[string]any
	passwords  map[string]string    // name → password sent at creation
	sessions   map[string]string    // access token → user id
	devices    map[string][2]string // access token → device id, app name
	qc         map[string]*fakeQC   // quick connect secret → request
	qcOff      bool
	pwChanges  int
	ssoPlugin  string // "" = not installed, else its status (Active, Restart)
	providers  map[string]map[string]any
	disclaimer string
	activity   []map[string]any
	items      []map[string]any
	n          int
}

var tokenRe = regexp.MustCompile(`Token="([^"]+)"`)

var (
	deviceRe = regexp.MustCompile(`DeviceId="([^"]+)"`)
	clientRe = regexp.MustCompile(`Client="([^"]+)"`)
)

type fakeQC struct {
	code, device, app, user string
}

// login opens a session from the request's device.
func (f *fakeJellyfin) login(r *http.Request, uid string) string {
	dev, app := "", ""
	if m := deviceRe.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		dev = m[1]
	}
	if m := clientRe.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		app = m[1]
	}
	for tok, d := range f.devices { // Jellyfin replaces the session of the same device
		if d[0] == dev && f.sessions[tok] == uid {
			delete(f.sessions, tok)
			delete(f.devices, tok)
		}
	}
	f.n++
	at := fmt.Sprintf("session-%d", f.n)
	f.sessions[at] = uid
	f.devices[at] = [2]string{dev, app}
	return at
}

func (f *fakeJellyfin) userByID(id string) map[string]any {
	for _, u := range f.users {
		if u["Id"] == id {
			return u
		}
	}
	return nil
}

func newFakeJellyfin(t *testing.T) *fakeJellyfin {
	f := &fakeJellyfin{adminKey: "jf-admin-key", passwords: map[string]string{}, sessions: map[string]string{}, providers: map[string]map[string]any{},
		devices: map[string][2]string{}, qc: map[string]*fakeQC{},
		disclaimer: "Welcome home"}
	f.users = []map[string]any{
		{"Id": "u-aleks", "Name": "aleks", "Policy": map[string]any{"IsAdministrator": false}},
		{"Id": "u-root", "Name": "root", "Policy": map[string]any{"IsAdministrator": true}},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		tok := ""
		if m := tokenRe.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
			tok = m[1]
		}
		admin := tok == f.adminKey
		js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		p := r.URL.Path
		switch {
		case p == "/System/Info/Public":
			js(map[string]any{"Version": "10.11.11", "ServerName": "Test"})
			return
		case p == "/Branding/Configuration" && r.Method == "GET":
			js(map[string]any{"LoginDisclaimer": f.disclaimer, "CustomCss": "", "SplashscreenEnabled": false})
			return
		case p == "/Users/AuthenticateByName":
			var in struct{ Username, Pw string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			for _, u := range f.users {
				if u["Name"] == in.Username && f.passwords[in.Username] == in.Pw && in.Pw != "" {
					js(map[string]any{"AccessToken": f.login(r, u["Id"].(string)), "User": u})
					return
				}
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		case p == "/QuickConnect/Initiate" && r.Method == "POST":
			if f.qcOff {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.n++
			q := &fakeQC{code: fmt.Sprintf("%06d", f.n)}
			if m := deviceRe.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
				q.device = m[1]
			}
			sec := fmt.Sprintf("qc-secret-%d", f.n)
			f.qc[sec] = q
			js(map[string]any{"Secret": sec, "Code": q.code, "DeviceId": q.device})
			return
		case p == "/QuickConnect/Authorize" && r.Method == "POST":
			uid := f.sessions[tok]
			if admin {
				uid = r.URL.Query().Get("userId")
			}
			if uid == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			for _, q := range f.qc {
				if q.code == r.URL.Query().Get("code") {
					q.user = uid
					js(true)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			return
		case p == "/Users/AuthenticateWithQuickConnect":
			var in struct{ Secret string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			q := f.qc[in.Secret]
			if q == nil || q.user == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			delete(f.qc, in.Secret)
			js(map[string]any{"AccessToken": f.login(r, q.user), "User": f.userByID(q.user)})
			return
		}
		if !admin {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case p == "/Devices" && r.Method == "GET":
			out := []map[string]any{}
			for tok, d := range f.devices {
				if uid := f.sessions[tok]; uid == r.URL.Query().Get("userId") {
					out = append(out, map[string]any{"Id": d[0], "AppName": d[1], "LastUserId": uid})
				}
			}
			js(map[string]any{"Items": out})
		case p == "/Devices" && r.Method == "DELETE":
			for tok, d := range f.devices {
				if d[0] == r.URL.Query().Get("id") {
					delete(f.devices, tok)
					delete(f.sessions, tok)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(p, "/Users/") && strings.HasSuffix(p, "/Password"):
			u := f.userByID(strings.TrimSuffix(strings.TrimPrefix(p, "/Users/"), "/Password"))
			if u == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var in struct{ NewPw string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.passwords[u["Name"].(string)] = in.NewPw
			f.pwChanges++
			for tok, uid := range f.sessions { // Jellyfin signs the account out everywhere
				if uid == u["Id"] {
					delete(f.sessions, tok)
					delete(f.devices, tok)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case p == "/System/Info":
			js(map[string]any{"Version": "10.11.11"})
		case p == "/Users" && r.Method == "GET":
			js(f.users)
		case p == "/Users/New":
			var in struct{ Name, Password string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.n++
			u := map[string]any{"Id": fmt.Sprintf("u-new-%d", f.n), "Name": in.Name, "Policy": map[string]any{"IsAdministrator": false}}
			f.users = append(f.users, u)
			f.passwords[in.Name] = in.Password
			js(u)
		case p == "/Plugins":
			list := []map[string]any{{"Name": "TMDb", "Version": "10.11.11.0", "Status": "Active"}}
			if f.ssoPlugin != "" {
				list = append(list, map[string]any{"Name": "SSO-Auth", "Version": "4.0.0.4", "Status": f.ssoPlugin})
			}
			js(list)
		case p == "/sso/OID/Get":
			js(f.providers)
		case strings.HasPrefix(p, "/sso/OID/Add/"):
			var cfg map[string]any
			_ = json.NewDecoder(r.Body).Decode(&cfg)
			f.providers[strings.TrimPrefix(p, "/sso/OID/Add/")] = cfg
		case strings.HasPrefix(p, "/sso/OID/Del/"):
			delete(f.providers, strings.TrimPrefix(p, "/sso/OID/Del/"))
		case p == "/System/Configuration/branding":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.disclaimer = fmt.Sprint(b["LoginDisclaimer"])
		case p == "/System/ActivityLog/Entries":
			out := make([]map[string]any, len(f.activity))
			for i := range f.activity { // newest first, like Jellyfin
				out[i] = f.activity[len(f.activity)-1-i]
			}
			js(map[string]any{"Items": out, "TotalRecordCount": len(out)})
		case p == "/Items":
			if ids := r.URL.Query().Get("Ids"); ids != "" {
				out := []map[string]any{}
				for _, it := range f.items {
					if strings.Contains(ids, it["Id"].(string)) {
						out = append(out, it)
					}
				}
				js(map[string]any{"Items": out})
				return
			}
			out := make([]map[string]any, len(f.items))
			for i := range f.items {
				out[i] = f.items[len(f.items)-1-i]
			}
			js(map[string]any{"Items": out})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestJellyfinConnector(t *testing.T) {
	e := newEnv(t)
	jf := newFakeJellyfin(t)
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
	if c := call("POST", "/api/v1/services", map[string]any{"type": "jellyfin", "url": jf.URL, "public_url": "https://jf.example", "fields": map[string]string{"API_KEY": jf.adminKey}}, &sv); c != 201 {
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
	if !tr.OK || tr.Info.Version != "10.11.11" || tr.Info.Users != 2 || !strings.Contains(tr.Info.Detail, "not installed") {
		t.Fatalf("test %+v", tr)
	}
	call("PUT", base, map[string]any{"enabled": true}, nil)

	// Provision everyone: aleks matches "aleks" by name and is linked with its id only; kid gets a new account and a token.
	var prov struct {
		Results []struct {
			Email      string `json:"email"`
			Status     string `json:"status"`
			RemoteID   string `json:"remote_id"`
			Credential string `json:"credential"`
			Error      string `json:"error"`
			Note       string `json:"note"`
		} `json:"results"`
	}
	call("POST", base+"/provision", map[string]any{"all": true}, &prov)
	if len(prov.Results) != 2 {
		t.Fatalf("provision %+v", prov)
	}
	for _, r := range prov.Results {
		switch r.Email {
		case "aleks@b.c":
			if r.Status != "linked" || r.RemoteID != "u-aleks" || r.Credential != "connector" {
				t.Fatalf("aleks %+v", r)
			}
		case "kid@b.c":
			if r.Status != "ok" || r.Credential != "connector" || r.Error != "" {
				t.Fatalf("kid %+v", r)
			}
		}
	}
	jf.mu.Lock()
	pw := jf.passwords["kid"]
	jf.mu.Unlock()
	if len(pw) < 32 {
		t.Fatalf("kid's account should have a long random password")
	}
	for _, txt := range responses {
		if strings.Contains(txt, pw) {
			t.Fatalf("the throwaway password left Core")
		}
	}
	var kidCreds []map[string]any
	call("GET", "/api/v1/users/"+itoa(kid.ID)+"/credentials", nil, &kidCreds)
	if b, _ := json.Marshal(kidCreds); !strings.Contains(string(b), "u-new-") {
		t.Fatalf("kid's override should carry the new USER_ID: %s", b)
	}

	// The owner really uses "root": link by hand, and provisioning leaves it alone afterwards.
	var remote []map[string]any
	if c := call("GET", base+"/remote-accounts", nil, &remote); c != 200 || len(remote) != 3 {
		t.Fatalf("remote accounts %d %+v", c, remote)
	}
	var link map[string]any
	if c := call("PUT", base+"/accounts/1", map[string]string{"remote_id": "u-root"}, &link); c != 200 || link["status"] != "linked" || link["credential"] != "connector" {
		t.Fatalf("link %d %+v", c, link)
	}
	if c, txt := e.raw("PUT", base+"/accounts/"+itoa(kid.ID), "", map[string]string{"remote_id": "u-root"}); c != 409 {
		t.Fatalf("root is taken: %d %s", c, txt)
	}
	call("POST", base+"/provision", map[string]any{"user_id": 1}, &prov)
	if prov.Results[0].RemoteID != "u-root" || !strings.Contains(prov.Results[0].Note, "by hand") {
		t.Fatalf("manual link must survive provisioning: %+v", prov.Results)
	}
	var rows []map[string]any
	call("GET", base+"/accounts", nil, &rows)
	for _, r := range rows {
		if r["email"] == "aleks@b.c" && (r["remote_id"] != "u-root" || r["manual"] != true) {
			t.Fatalf("owner row %+v", r)
		}
	}

	// Sign-in: refused (with how to fix it) until the SSO plugin is installed.
	if c, txt := e.raw("PUT", base, "", map[string]any{"sso_enabled": true}); c == 200 || !strings.Contains(txt, "SSO Authentication plugin") {
		t.Fatalf("sso without plugin %d %s", c, txt)
	}
	jf.mu.Lock()
	jf.ssoPlugin = "Restart" // installed, Jellyfin not restarted yet
	jf.mu.Unlock()
	if c, txt := e.raw("PUT", base, "", map[string]any{"sso_enabled": true}); c == 200 || !strings.Contains(txt, "restart Jellyfin") {
		t.Fatalf("sso before restart %d %s", c, txt)
	}
	jf.mu.Lock()
	jf.ssoPlugin = "Active"
	// someone the plugin linked on its own before Core knew them
	jf.providers["pocket-core"] = map[string]any{"CanonicalLinks": map[string]any{"guest": "11111111-2222-3333-4444-555555555555"}}
	jf.mu.Unlock()
	if c, txt := e.raw("PUT", base, "", map[string]any{"sso_enabled": true}); c != 200 {
		t.Fatalf("sso on %d %s", c, txt)
	}
	links := func() map[string]any {
		jf.mu.Lock()
		defer jf.mu.Unlock()
		m, _ := jf.providers["pocket-core"]["canonicalLinks"].(map[string]any)
		return m
	}
	jf.mu.Lock()
	cfg := jf.providers["pocket-core"]
	disc := jf.disclaimer
	jf.mu.Unlock()
	if cfg == nil || cfg["enabled"] != true || cfg["enableAuthorization"] != false || cfg["oidEndpoint"] != "http://core.test" || cfg["defaultUsernameClaim"] != "preferred_username" || cfg["schemeOverride"] != "https" {
		t.Fatalf("provider %+v", cfg)
	}
	if l := links(); l["aleks"] != "u-root" || l["guest"] == nil || !strings.HasPrefix(fmt.Sprint(l["kid"]), "u-new-") {
		t.Fatalf("sign-in links: the owner lands in root, the plugin's own link stays: %+v", l)
	}
	// Relinking while sign-in is on updates where the person lands.
	call("PUT", base+"/accounts/1", map[string]string{"remote_id": ""}, nil)
	if l := links(); l["aleks"] != nil || l["guest"] == nil {
		t.Fatalf("after unlink %+v", l)
	}
	call("PUT", base+"/accounts/1", map[string]string{"remote_id": "u-root"}, &link)
	if l := links(); l["aleks"] != "u-root" || !strings.Contains(fmt.Sprint(link["note"]), "sign-in now lands") {
		t.Fatalf("after relink %+v %+v", l, link)
	}
	if !strings.Contains(disc, "https://jf.example/sso/OID/start/pocket-core") || !strings.Contains(disc, "Welcome home") {
		t.Fatalf("login button %q", disc)
	}
	backups, _ := filepath.Glob(filepath.Join(e.dir, "connectors", "jellyfin-sso-*.json"))
	if len(backups) == 0 {
		t.Fatalf("no backup written")
	}
	if b, _ := os.ReadFile(backups[0]); !strings.Contains(string(b), "Welcome home") {
		t.Fatalf("backup content %s", b)
	}
	var redirects struct {
		Redirects []string `json:"redirects"`
	}
	call("GET", base, nil, &redirects)
	if len(redirects.Redirects) != 2 || redirects.Redirects[0] != "https://jf.example/sso/OID/redirect/pocket-core" {
		t.Fatalf("redirects %+v", redirects)
	}
	if c := call("PUT", base, map[string]any{"sso_enabled": false}, nil); c != 200 {
		t.Fatalf("sso off %d", c)
	}
	jf.mu.Lock()
	if jf.providers["pocket-core"] != nil || jf.disclaimer != "Welcome home" {
		t.Fatalf("sso off left %+v %q", jf.providers, jf.disclaimer)
	}
	jf.mu.Unlock()

	// Events: baseline first, then playback by root (= the owner) and a new episode.
	call("PUT", base, map[string]any{"events_enabled": true}, nil)
	jf.mu.Lock()
	jf.activity = []map[string]any{{"Id": 10, "Type": "SessionStarted", "Name": "root is online", "Date": "2026-10-19T10:00:00.0000000Z", "UserId": "u-root"}}
	jf.items = []map[string]any{{"Id": "i-old", "Name": "Old", "Type": "Movie", "DateCreated": "2026-10-01T10:00:00.0000000Z"}}
	jf.mu.Unlock()
	var pr map[string]any
	call("POST", base+"/poll", nil, &pr)
	if pr["baseline"] != true {
		t.Fatalf("first poll %+v", pr)
	}
	jf.mu.Lock()
	jf.activity = append(jf.activity,
		map[string]any{"Id": 11, "Type": "SessionStarted", "Name": "root is online again", "Date": "2026-10-19T11:00:00.0000000Z", "UserId": "u-root"},
		map[string]any{"Id": 12, "Type": "VideoPlayback", "Name": "root is playing Pilot on Living Room TV", "Date": "2026-10-19T11:01:00.0000000Z", "UserId": "u-root", "ItemId": "i-ep"},
		map[string]any{"Id": 13, "Type": "AuthenticationFailed", "Name": "Failed login from 10.0.0.9", "Date": "2026-10-19T11:02:00.0000000Z"})
	one := 1
	jf.items = append(jf.items, map[string]any{"Id": "i-ep", "Name": "Pilot", "Type": "Episode", "SeriesName": "Show", "IndexNumber": one, "ParentIndexNumber": one, "DateCreated": "2026-10-19T09:00:00.0000000Z"})
	jf.mu.Unlock()
	call("POST", base+"/poll", nil, &pr)
	if pr["events"] != float64(3) {
		t.Fatalf("second poll %+v", pr)
	}
	var evs []struct {
		Topic   string         `json:"topic"`
		Subject string         `json:"subject"`
		Payload map[string]any `json:"payload"`
	}
	call("GET", "/api/v1/events?topic=jellyfin.*", nil, &evs)
	got := map[string]map[string]any{}
	for _, ev := range evs {
		got[ev.Topic] = ev.Payload
	}
	if p := got["jellyfin.playback_started"]; p == nil || p["user_email"] != "aleks@b.c" || p["title"] != "Pilot" || p["series"] != "Show" || p["media"] != "video" || p["jellyfin_user"] != "root" {
		t.Fatalf("playback %+v", got)
	}
	if p := got["jellyfin.item_added"]; p == nil || p["item_id"] != "i-ep" || p["url"] != "https://jf.example/web/#/details?id=i-ep" || p["user_email"] != nil {
		t.Fatalf("item added %+v", got)
	}
	if p := got["jellyfin.login_failed"]; p == nil || p["user_email"] != nil {
		t.Fatalf("login failed %+v", got)
	}
	if got["jellyfin.session_started"] != nil {
		t.Fatalf("sessions are not news")
	}

	// Unlink removes the connector's credential and the link.
	if c := call("PUT", base+"/accounts/1", map[string]string{"remote_id": ""}, nil); c != 200 {
		t.Fatalf("unlink %d", c)
	}
	call("GET", base+"/accounts", nil, &rows)
	for _, r := range rows {
		if r["email"] == "aleks@b.c" && (r["status"] != "" || r["credential"] != "") {
			t.Fatalf("after unlink %+v", r)
		}
	}
}
