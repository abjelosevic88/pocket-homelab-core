package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKavita mimics Kavita 0.9.1.4's API: x-api-key or Bearer auth, the
// invite → confirm-email flow, auth keys, server settings with the OpenID
// secret shown as stars, reading sessions and series.

type kvFakeKey struct {
	ID       int    `json:"id"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Provider int    `json:"provider"`
}

type fkUser struct {
	ID       int
	Username string
	Email    string
	Pending  bool
	Roles    []string
	Libs     []int
	Password string
	Token    string // invite token
	Keys     []kvFakeKey
}

type fakeKavita struct {
	t        *testing.T
	mu       sync.Mutex
	users    []*fkUser
	settings map[string]any
	libs     []map[string]any
	sessions []map[string]any
	series   []map[string]any
	volumes  map[int]map[string]any
	jwts     map[string]int
	next     int
	posts    map[string]int // POST path → count
}

func newFakeKavita(t *testing.T) *fakeKavita {
	f := &fakeKavita{t: t, jwts: map[string]int{}, next: 100, posts: map[string]int{}, volumes: map[int]map[string]any{}}
	f.users = []*fkUser{
		{ID: 1, Username: "owner", Email: "owner@home.lan", Roles: []string{"Admin", "Pleb", "Login"}, Keys: []kvFakeKey{{ID: 1, Key: "AdminKey0123456789abcdefghijklmn", Name: "opds", Provider: 1}}},
		{ID: 2, Username: "ana", Email: "Ana@Home.lan", Roles: []string{"Pleb", "Login"}, Keys: []kvFakeKey{{ID: 2, Key: "AnaKey000123456789abcdefghijklmn", Name: "opds", Provider: 1}}},
		{ID: 3, Username: "sani", Email: "other@else.where", Roles: []string{"Pleb"}},
	}
	f.settings = map[string]any{
		"cacheDirectory": "/kavita/config/cache", "taskScan": "daily", "port": 5000, "baseUrl": "/", "bookmarksDirectory": "/kavita/config/bookmarks",
		"hostName": "", "cacheSize": 75, "totalBackups": 30, "enableFolderWatching": true,
		"smtpConfig": map[string]any{"host": "", "senderAddress": "", "password": "smtp-secret"},
		"oidcConfig": map[string]any{"autoLogin": false, "disablePasswordAuthentication": false, "providerName": "OpenID Connect", "enabled": false,
			"authority": "", "clientId": "kavita", "secret": "", "provisionAccounts": true, "requireVerifiedEmail": true, "syncUserSettings": false,
			"rolesPrefix": "", "rolesClaim": "http://schemas.microsoft.com/ws/2008/06/identity/claims/role", "customScopes": []any{},
			"defaultRoles": []any{"Login"}, "defaultLibraries": []any{1}, "defaultAgeRestriction": 0, "defaultIncludeUnknowns": false},
	}
	f.libs = []map[string]any{{"id": 1, "name": "Books", "type": 2}, {"id": 2, "name": "Manga", "type": 0}}
	return f
}

func (f *fakeKavita) id() int { f.next++; return f.next }

func (f *fakeKavita) byKey(k string) *fkUser {
	for _, u := range f.users {
		for _, x := range u.Keys {
			if x.Key == k {
				return u
			}
		}
	}
	return nil
}

func (f *fakeKavita) byID(id int) *fkUser {
	for _, u := range f.users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func (f *fakeKavita) dto(u *fkUser, token string) map[string]any {
	keys := []kvFakeKey{}
	keys = append(keys, u.Keys...)
	opds := ""
	for _, k := range u.Keys {
		if k.Name == "opds" {
			opds = k.Key
		}
	}
	return map[string]any{"id": u.ID, "username": u.Username, "email": u.Email, "roles": u.Roles, "token": token, "refreshToken": "r",
		"apiKey": opds, "kavitaVersion": "0.9.1.4", "authKeys": keys, "identityProvider": 0}
}

func (f *fakeKavita) jwt(u *fkUser) string {
	tok := fmt.Sprintf("eyJ.jwt-%d-%d", u.ID, f.id())
	f.jwts[tok] = u.ID
	return tok
}

func writeJ(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func stars(s string) string { return strings.Repeat("*", len(s)) }

func (f *fakeKavita) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Query().Has("apiKey") {
		f.t.Errorf("key sent in a URL: %s", r.URL.Path)
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if r.Method == http.MethodPost {
		f.posts[r.URL.Path]++
	}
	// who is asking
	var me *fkUser
	if k := r.Header.Get("x-api-key"); k != "" {
		if me = f.byKey(k); me == nil {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
	} else if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if me = f.byID(f.jwts[b]); me == nil {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
	}
	anon := map[string]bool{"/api/health": true, "/api/Account/confirm-email": true, "/api/Account/login": true}
	if me == nil && !anon[r.URL.Path] {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}
	admin := me != nil && contains(me.Roles, "Admin")
	needAdmin := func() bool {
		if !admin {
			w.WriteHeader(http.StatusForbidden)
			return false
		}
		return true
	}
	q := r.URL.Query()
	switch p := r.URL.Path; {
	case p == "/api/health":
		_, _ = w.Write([]byte("Ok"))
	case p == "/api/Account" && r.Method == http.MethodGet:
		if !contains(me.Roles, "Login") && !admin {
			http.Error(w, "disabled-account", http.StatusUnauthorized)
			return
		}
		d := f.dto(me, "")
		delete(d, "token")
		writeJ(w, d)
	case p == "/api/Users":
		if !needAdmin() {
			return
		}
		out := []map[string]any{}
		for _, u := range f.users {
			if u.Pending && q.Get("includePending") != "true" {
				continue
			}
			out = append(out, map[string]any{"id": u.ID, "username": u.Username, "email": u.Email, "isPending": u.Pending, "roles": u.Roles,
				"libraries": []any{}, "ageRestriction": map[string]any{"ageRating": -1, "includeUnknowns": true}, "identityProvider": 0})
		}
		writeJ(w, out)
	case p == "/api/Library/libraries":
		writeJ(w, f.libs)
	case p == "/api/Settings" && r.Method == http.MethodGet:
		if !needAdmin() {
			return
		}
		b, _ := json.Marshal(f.settings)
		var cp map[string]any
		_ = json.Unmarshal(b, &cp)
		o := cp["oidcConfig"].(map[string]any)
		o["secret"] = stars(o["secret"].(string))
		writeJ(w, cp)
	case p == "/api/Settings" && r.Method == http.MethodPost:
		if !needAdmin() {
			return
		}
		cur := f.settings["oidcConfig"].(map[string]any)
		in := body["oidcConfig"].(map[string]any)
		if in["secret"] == stars(cur["secret"].(string)) {
			in["secret"] = cur["secret"]
		}
		if a, _ := in["authority"].(string); a != "" && a != cur["authority"] && !strings.HasPrefix(a, "https://") {
			http.Error(w, "oidc-invalid-authority", http.StatusBadRequest)
			return
		}
		in["enabled"] = in["authority"] != ""
		body["oidcConfig"] = in
		f.settings = body
		writeJ(w, body)
	case p == "/api/Account/invite":
		if !needAdmin() {
			return
		}
		email := body["email"].(string)
		for _, u := range f.users {
			if strings.EqualFold(u.Email, email) {
				http.Error(w, "user-already-invited", http.StatusBadRequest)
				return
			}
		}
		roles := []string{"Pleb"}
		for _, x := range body["roles"].([]any) {
			roles = append(roles, x.(string))
		}
		libs := []int{}
		for _, x := range body["libraries"].([]any) {
			libs = append(libs, int(x.(float64)))
		}
		u := &fkUser{ID: f.id(), Username: email, Email: email, Pending: true, Roles: roles, Libs: libs, Token: fmt.Sprintf("CfDJ8+tok/%d==", f.next)}
		f.users = append(f.users, u)
		writeJ(w, map[string]any{"emailLink": "http://kavita:5000/registration/confirm-email?token=" + url.QueryEscape(u.Token) + "&email=" + url.QueryEscape(email), "emailSent": false, "invalidEmail": true})
	case p == "/api/Account/invite-url":
		if !needAdmin() {
			return
		}
		id, _ := strconv.Atoi(q.Get("userId"))
		u := f.byID(id)
		if u == nil || !u.Pending {
			http.Error(w, "user-already-confirmed", http.StatusBadRequest)
			return
		}
		writeJ(w, "registration/confirm-email?token="+url.QueryEscape(u.Token)+"&email="+url.QueryEscape(u.Email))
	case p == "/api/Account/confirm-email":
		var u *fkUser
		for _, x := range f.users {
			if strings.EqualFold(x.Email, body["email"].(string)) {
				u = x
			}
		}
		name := body["username"].(string)
		if u == nil || u.Token == "" || u.Token != body["token"] {
			http.Error(w, "invalid-email-confirmation", http.StatusBadRequest)
			return
		}
		if name != body["email"] {
			for _, x := range f.users {
				if x != u && strings.EqualFold(x.Username, name) {
					http.Error(w, "username-taken", http.StatusBadRequest)
					return
				}
			}
		}
		u.Username, u.Password, u.Pending, u.Token = name, body["password"].(string), false, ""
		u.Keys = []kvFakeKey{{ID: f.id(), Key: fmt.Sprintf("Opds%028d", f.next), Name: "opds", Provider: 1}, {ID: f.id(), Key: fmt.Sprintf("Img0%028d", f.next), Name: "image-only", Provider: 1}}
		writeJ(w, f.dto(u, f.jwt(u)))
	case p == "/api/Account/login":
		for _, u := range f.users {
			if strings.EqualFold(u.Username, body["username"].(string)) && u.Password != "" && u.Password == body["password"] {
				writeJ(w, f.dto(u, f.jwt(u)))
				return
			}
		}
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	case p == "/api/Account/reset-password":
		if !needAdmin() {
			return
		}
		for _, u := range f.users {
			if u.Username == body["userName"] {
				u.Password = body["password"].(string)
			}
		}
	case p == "/api/Account/auth-keys":
		writeJ(w, me.Keys)
	case p == "/api/Account/create-auth-key":
		for _, k := range me.Keys {
			if strings.EqualFold(k.Name, body["name"].(string)) {
				http.Error(w, "auth-key-unique", http.StatusBadRequest)
				return
			}
		}
		k := kvFakeKey{ID: f.id(), Key: fmt.Sprintf("New%029d", f.next), Name: body["name"].(string)}
		me.Keys = append(me.Keys, k)
		writeJ(w, k)
	case p == "/api/Account/rotate-auth-key":
		id, _ := strconv.Atoi(q.Get("authKeyId"))
		for i, k := range me.Keys {
			if k.ID == id {
				me.Keys[i].Key = fmt.Sprintf("Rot%029d", f.id())
				writeJ(w, me.Keys[i])
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
	case p == "/api/Activity/current":
		if !needAdmin() {
			return
		}
		writeJ(w, f.sessions)
	case p == "/api/Series/all-v2":
		if !needAdmin() {
			return
		}
		so := body["sortOptions"].(map[string]any)
		if so["sortField"] != float64(2) || so["isAscending"] != false || q.Get("PageSize") != "50" {
			f.t.Errorf("all-v2 asked %v %v", so, q)
		}
		out := []map[string]any{}
		for i := len(f.series) - 1; i >= 0 && len(out) < 50; i-- {
			out = append(out, f.series[i])
		}
		writeJ(w, out)
	case p == "/api/Series/volume":
		id, _ := strconv.Atoi(q.Get("volumeId"))
		if v, ok := f.volumes[id]; ok {
			writeJ(w, v)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasPrefix(p, "/api/Series/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/api/Series/"))
		for _, s := range f.series {
			if s["id"] == id {
				writeJ(w, s)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("unexpected %s %s", r.Method, p)
		w.WriteHeader(http.StatusNotFound)
	}
}

func kvTarget(t *testing.T, f *fakeKavita) (Target, func()) {
	srv := httptest.NewServer(f)
	return Target{BaseURL: srv.URL, PublicURL: "https://kavita.example/", Fields: map[string]string{"API_KEY": "AdminKey0123456789abcdefghijklmn"},
		HTTP: srv.Client(), DataDir: t.TempDir(), Now: func() time.Time { return time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC) }}, srv.Close
}

func TestKavitaTest(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	info, err := kavita{}.Test(context.Background(), tg)
	if err != nil || info.Version != "0.9.1.4" || !info.Admin || info.Users != 3 {
		t.Fatalf("%+v %v", info, err)
	}
	tg.Fields["API_KEY"] = "AnaKey000123456789abcdefghijklmn"
	if _, err := (kavita{}).Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "not an admin") {
		t.Fatalf("non-admin key: %v", err)
	}
	tg.Fields["API_KEY"] = "Wrong0123456789abcdefghijklmnopq"
	if _, err := (kavita{}).Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Wrong0123") {
		t.Fatalf("bad key: %v", err)
	}
	tg.Fields["API_KEY"] = ""
	if _, err := (kavita{}).Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "needs an API_KEY") {
		t.Fatalf("no key: %v", err)
	}
}

func TestKavitaProvisionLinksByEmail(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	acc, err := kavita{}.Provision(context.Background(), tg, Person{Email: "ana@home.lan", Name: "Ana"})
	if err != nil || acc.Status != StatusLinked || acc.RemoteID != "2" || acc.Fields["API_KEY"] != "" || acc.Created {
		t.Fatalf("%+v %v", acc, err)
	}
	if f.posts["/api/Account/invite"] != 0 {
		t.Fatal("linking must not invite")
	}
	acc, err = kavita{}.Provision(context.Background(), tg, Person{Email: "OWNER@home.lan"})
	if err != nil || acc.Status != StatusLinked || !strings.Contains(acc.Note, "admin") {
		t.Fatalf("admin %+v %v", acc, err)
	}
}

func TestKavitaProvisionCreates(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	ctx := context.Background()
	// "sani" is taken by another account: the new one is named by the full email
	acc, err := kavita{}.Provision(ctx, tg, Person{Email: "Sani@Home.lan", Name: "Sani"})
	if err != nil || acc.Status != StatusOK || !acc.Created || acc.Fields["API_KEY"] == "" || acc.Fields["USER_ID"] != acc.RemoteID {
		t.Fatalf("%+v %v", acc, err)
	}
	id, _ := strconv.Atoi(acc.RemoteID)
	u := f.byID(id)
	if u == nil || u.Username != "sani@home.lan" || u.Pending || len(u.Password) != 40 || !contains(u.Roles, "Login") || contains(u.Roles, "Admin") || !reflect.DeepEqual(u.Libs, []int{1, 2}) {
		t.Fatalf("account %+v", u)
	}
	if f.byID(3).Username != "sani" || f.byID(3).Password != "" {
		t.Fatal("the existing account was touched")
	}
	if f.byKey(acc.Fields["API_KEY"]) != u || u.Keys[len(u.Keys)-1].Name != KavitaKeyName {
		t.Fatalf("key is not the person's own %q: %+v", KavitaKeyName, u.Keys)
	}
	if strings.Contains(acc.Note, u.Password) {
		t.Fatal("password in note")
	}
	// a free name is used as is; keep-own mints nothing
	acc, err = kavita{}.Provision(ctx, tg, Person{Email: "mila.k@home.lan", KeepOwnCredential: true})
	if err != nil || acc.Status != StatusOK || acc.Fields["API_KEY"] != "" {
		t.Fatalf("keep own %+v %v", acc, err)
	}
	id, _ = strconv.Atoi(acc.RemoteID)
	if u := f.byID(id); u.Username != "mila.k" || len(u.Keys) != 2 {
		t.Fatalf("keep own account %+v", u)
	}
	// an invitation left half-done is finished, not duplicated
	f.users = append(f.users, &fkUser{ID: 50, Username: "kid@home.lan", Email: "kid@home.lan", Pending: true, Roles: []string{"Pleb", "Login"}, Token: "CfDJ8/half+done=="})
	acc, err = kavita{}.Provision(ctx, tg, Person{Email: "kid@home.lan"})
	if err != nil || acc.RemoteID != "50" || acc.Fields["API_KEY"] == "" || f.byID(50).Pending || f.byID(50).Username != "kid" {
		t.Fatalf("pending %+v %v %+v", acc, err, f.byID(50))
	}
	// a pending invitation whose short name is taken keeps its email spelling
	f.users = append(f.users, &fkUser{ID: 52, Username: "Ana@Elsewhere.lan", Email: "Ana@Elsewhere.lan", Pending: true, Roles: []string{"Pleb", "Login"}, Token: "t52"})
	acc, err = kavita{}.Provision(ctx, tg, Person{Email: "ana@elsewhere.lan"})
	if err != nil || acc.RemoteID != "52" || f.byID(52).Username != "Ana@Elsewhere.lan" {
		t.Fatalf("pending, name taken %+v %v %+v", acc, err, f.byID(52))
	}
	// a pending admin invitation is left alone
	f.users = append(f.users, &fkUser{ID: 51, Username: "boss@home.lan", Email: "boss@home.lan", Pending: true, Roles: []string{"Admin"}, Token: "x"})
	if _, err := (kavita{}).Provision(ctx, tg, Person{Email: "boss@home.lan"}); err == nil || !f.byID(51).Pending {
		t.Fatalf("admin invitation: %v", err)
	}
}

func TestKavitaLink(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	list, err := kavita{}.RemoteAccounts(context.Background(), tg)
	if err != nil || len(list) != 3 || list[0].Name != "ana · Ana@Home.lan" || !list[1].Admin || !list[2].Disabled {
		t.Fatalf("%+v %v", list, err)
	}
	if acc, err := (kavita{}).Link(context.Background(), tg, "2"); err != nil || acc.Status != StatusLinked || acc.Fields["USER_ID"] != "2" {
		t.Fatalf("%+v %v", acc, err)
	}
	if _, err := (kavita{}).Link(context.Background(), tg, "99"); err == nil {
		t.Fatal("missing account linked")
	}
}

func TestKavitaSSORoundTrip(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	ctx := context.Background()
	before, _ := json.Marshal(f.settings)
	sc := SSOConfig{Enabled: true, Issuer: "https://core.example", ClientID: "cl-kavita", ClientSecret: "s3cr3t-value", ButtonText: "Sign in with Pocket Core",
		People: map[string]string{"ana@home.lan": "2", "new@home.lan": ""}}
	if err := (kavita{}).ConfigureSSO(ctx, tg, sc); err != nil {
		t.Fatal(err)
	}
	o := f.settings["oidcConfig"].(map[string]any)
	if o["authority"] != "https://core.example" || o["clientId"] != "cl-kavita" || o["secret"] != "s3cr3t-value" || o["providerName"] != "Sign in with Pocket Core" ||
		o["provisionAccounts"] != false || o["syncUserSettings"] != false || o["disablePasswordAuthentication"] != false || o["autoLogin"] != false {
		t.Fatalf("oidc %+v", o)
	}
	if f.settings["cacheSize"] != float64(75) || f.settings["smtpConfig"].(map[string]any)["password"] != "smtp-secret" {
		t.Fatalf("other settings changed: %+v", f.settings)
	}
	files, _ := filepath.Glob(filepath.Join(tg.DataDir, "kavita-sso-2*.json"))
	if len(files) != 1 {
		t.Fatalf("backup %v", files)
	}
	if b, _ := os.ReadFile(files[0]); strings.Contains(string(b), "smtp-secret") || !strings.Contains(string(b), `"oidcConfig"`) {
		t.Fatalf("backup content %s", b)
	}
	if _, err := os.Stat(filepath.Join(tg.DataDir, "kavita-sso-original.json")); err != nil {
		t.Fatal("no original kept")
	}
	// again (a new secret): the original stays the one from before Core
	sc.ClientSecret = "second-secret"
	if err := (kavita{}).ConfigureSSO(ctx, tg, sc); err != nil || f.settings["oidcConfig"].(map[string]any)["secret"] != "second-secret" {
		t.Fatal(err)
	}
	if err := (kavita{}).ConfigureSSO(ctx, tg, SSOConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	_ = json.Unmarshal(before, &want)
	b, _ := json.Marshal(f.settings)
	_ = json.Unmarshal(b, &got)
	if !reflect.DeepEqual(want["oidcConfig"], got["oidcConfig"]) {
		t.Fatalf("not restored:\nwant %v\ngot  %v", want["oidcConfig"], got["oidcConfig"])
	}
	if _, err := os.Stat(filepath.Join(tg.DataDir, "kavita-sso-original.json")); err == nil {
		t.Fatal("original not marked restored")
	}
	// off again: nothing to do
	n := f.posts["/api/Settings"]
	if err := (kavita{}).ConfigureSSO(ctx, tg, SSOConfig{Enabled: false}); err != nil || f.posts["/api/Settings"] != n {
		t.Fatalf("second off wrote: %v", err)
	}
}

func TestKavitaSSOBlocksMismatchedLinks(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	err := kavita{}.ConfigureSSO(context.Background(), tg, SSOConfig{Enabled: true, Issuer: "https://core.example", ClientID: "c", ClientSecret: "sec",
		People: map[string]string{"sani@home.lan": "3"}})
	if err == nil || !strings.Contains(err.Error(), `"sani"`) || f.posts["/api/Settings"] != 0 {
		t.Fatalf("%v", err)
	}
}

func TestKavitaSSOPreviousProviderSecret(t *testing.T) {
	f := newFakeKavita(t)
	o := f.settings["oidcConfig"].(map[string]any)
	o["authority"], o["clientId"], o["secret"], o["providerName"] = "https://id.example", "kavita", "pocketid-secret", "PocketID"
	tg, done := kvTarget(t, f)
	defer done()
	ctx := context.Background()
	if err := (kavita{}).ConfigureSSO(ctx, tg, SSOConfig{Enabled: true, Issuer: "https://core.example", ClientID: "c", ClientSecret: "sec"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(tg.DataDir, "kavita-sso-original.json"))
	if strings.Contains(string(b), "pocketid-secret") {
		t.Fatal("secret on disk")
	}
	err := kavita{}.ConfigureSSO(ctx, tg, SSOConfig{Enabled: false})
	o = f.settings["oidcConfig"].(map[string]any)
	if err == nil || !strings.Contains(err.Error(), "secret") || o["authority"] != "https://id.example" || o["providerName"] != "PocketID" || o["secret"] != "" {
		t.Fatalf("%v %+v", err, o)
	}
	if err := (kavita{}).ConfigureSSO(ctx, tg, SSOConfig{Enabled: false}); err != nil {
		t.Fatalf("second off: %v", err)
	}
}

func TestKavitaRotateKey(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	ctx := context.Background()
	acc, err := kavita{}.Provision(ctx, tg, Person{Email: "mila@home.lan"})
	if err != nil {
		t.Fatal(err)
	}
	old := acc.Fields["API_KEY"]
	rot, err := kavita{}.RotateKey(ctx, tg, AccountRef{RemoteID: acc.RemoteID, Email: "mila@home.lan", Fields: acc.Fields})
	if err != nil || rot.Fields["API_KEY"] == "" || rot.Fields["API_KEY"] == old || rot.Note != "" {
		t.Fatalf("%+v %v", rot, err)
	}
	if f.byKey(old) != nil || f.byKey(rot.Fields["API_KEY"]) == nil {
		t.Fatal("old key alive or new key dead")
	}
	u := f.byKey(rot.Fields["API_KEY"])
	if len(u.Keys) != 3 {
		t.Fatalf("rotated in place, keys %+v", u.Keys)
	}
	// the stored key is gone: new password, sign in, rotate Core's key
	u.Keys = u.Keys[:2]
	u.Keys = append(u.Keys, kvFakeKey{ID: 999, Key: "Lost0123456789abcdefghijklmnopqr", Name: KavitaKeyName})
	rot2, err := kavita{}.RotateKey(ctx, tg, AccountRef{RemoteID: acc.RemoteID, Fields: map[string]string{"API_KEY": "Gone0123456789abcdefghijklmnopqr"}})
	if err != nil || rot2.Note == "" || f.byKey("Lost0123456789abcdefghijklmnopqr") != nil || f.byKey(rot2.Fields["API_KEY"]) != u {
		t.Fatalf("fallback %+v %v", rot2, err)
	}
	if _, err := (kavita{}).RotateKey(ctx, tg, AccountRef{RemoteID: "1", Fields: map[string]string{"API_KEY": "x"}}); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("admin: %v", err)
	}
	if _, err := (kavita{}).RotateKey(ctx, tg, AccountRef{RemoteID: "2", Fields: map[string]string{"API_KEY": rot2.Fields["API_KEY"]}}); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("someone else's key: %v", err)
	}
}

func kvAct(series, vol, chapter, lib int, name string, end, total, read int) map[string]any {
	return map[string]any{"chapterId": chapter, "volumeId": vol, "seriesId": series, "libraryId": lib, "startPage": 0, "endPage": end,
		"startTimeUtc": "2026-10-20T08:00:00", "endTimeUtc": "2026-10-20T08:40:00.1234567", "pagesRead": read, "wordsRead": 0, "totalPages": total,
		"totalWords": 0, "libraryName": "Books", "seriesName": name, "chapterTitle": "", "clientInfo": map[string]any{"browser": "Safari", "deviceType": "Mobile"}}
}

func TestKavitaPoll(t *testing.T) {
	f := newFakeKavita(t)
	tg, done := kvTarget(t, f)
	defer done()
	ctx := context.Background()
	f.series = []map[string]any{{"id": 10, "name": "Old Series", "libraryId": 2, "libraryName": "Manga", "format": 1, "pages": 400}}
	f.sessions = []map[string]any{{"id": 1, "startTimeUtc": "2026-10-20T07:00:00", "isActive": true, "userId": 2, "username": "ana",
		"activityData": []any{kvAct(10, 100, 1000, 2, "Old Series", 20, 20, 19)}}}
	var state json.RawMessage
	poll := func() []Event {
		evs, st, err := kavita{}.Poll(ctx, tg, state)
		if err != nil {
			t.Fatal(err)
		}
		state = st
		return evs
	}
	if evs := poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	// a new account, a new book, someone starts and finishes it
	f.users = append(f.users, &fkUser{ID: 7, Username: "kid", Email: "kid@home.lan", Roles: []string{"Pleb", "Login"}},
		&fkUser{ID: 8, Username: "invited@x", Email: "invited@x", Pending: true})
	f.series = append(f.series, map[string]any{"id": 11, "name": "Dune", "libraryId": 1, "libraryName": "Books", "format": 3, "pages": 300})
	f.volumes[110] = map[string]any{"id": 110, "name": "", "pages": 300, "chapters": []any{map[string]any{"id": 1100}}}
	f.sessions = append(f.sessions, map[string]any{"id": 2, "startTimeUtc": "2026-10-20T08:00:00", "isActive": true, "userId": 7, "username": "kid",
		"activityData": []any{kvAct(11, 110, 1100, 1, "Dune", 300, 300, 120)}})
	evs := poll()
	names := map[string]Event{}
	for _, e := range evs {
		names[e.Name] = e
	}
	if len(evs) != 4 || names["user_created"].RemoteUserID != "7" || names["series_added"].Payload["title"] != "Dune" {
		t.Fatalf("events %+v", evs)
	}
	st := names["reading_started"]
	if !st.Personal || st.RemoteUserID != "7" || st.Payload["title"] != "Dune" || st.Payload["url"] != "https://kavita.example/library/1/series/11" || st.Payload["device"] != "Mobile · Safari" {
		t.Fatalf("started %+v", st)
	}
	fin := names["book_finished"]
	if !fin.Personal || fin.RemoteUserID != "7" || fin.Payload["title"] != "Dune" || fin.Payload["finished_at"] != "2026-10-20T08:40:00Z" {
		t.Fatalf("finished %+v", fin)
	}
	if names["user_created"].Personal || names["series_added"].Personal || names["series_added"].RemoteUserID != "" {
		t.Fatal("household events marked personal")
	}
	if evs := poll(); len(evs) != 0 {
		t.Fatalf("re-poll repeated %+v", evs)
	}
	// both sessions end: only the one Core saw start is reported
	f.sessions = nil
	evs = poll()
	if len(evs) != 1 || evs[0].Name != "reading_ended" || !evs[0].Personal || evs[0].RemoteUserID != "7" {
		t.Fatalf("ended %+v", evs)
	}
	p := evs[0].Payload
	if p["pages_read"] != 120 || p["chapters_finished"] != 1 || p["minutes"] != 40 || p["title"] != "Read Dune" {
		t.Fatalf("ended payload %+v", p)
	}
	if evs := poll(); len(evs) != 0 {
		t.Fatalf("ended twice %+v", evs)
	}
	// a big import is one event; an account leaving is noticed
	for i := 0; i < 12; i++ {
		f.series = append(f.series, map[string]any{"id": 20 + i, "name": fmt.Sprintf("Import %d", i), "libraryId": 2, "libraryName": "Manga", "format": 1, "pages": 10})
	}
	f.users = f.users[:len(f.users)-2]
	evs = poll()
	if len(evs) != 2 || evs[0].Name != "user_deleted" || evs[0].RemoteUserID != "7" || evs[1].Name != "series_added_bulk" || evs[1].Payload["count"] != 12 {
		t.Fatalf("bulk %+v", evs)
	}
	if len(state) > 400 {
		t.Fatalf("state grew: %s", state)
	}
}

func TestKavitaRedirects(t *testing.T) {
	if got := (kavita{}).SSORedirects("https://kavita.example/"); !reflect.DeepEqual(got, []string{"https://kavita.example/signin-oidc"}) {
		t.Fatal(got)
	}
	if kvToken(`"registration/confirm-email?token=CfDJ8%2bab%2fc%3d%3d&email=a%40b"`) != "CfDJ8+ab/c==" {
		t.Fatal("token")
	}
}
