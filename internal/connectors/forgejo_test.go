package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeForgejo mimics Forgejo 16's API below /api/v1: "Authorization: token
// <sha1>" for tokens, Basic auth for the per-user token endpoints, site
// admins for /admin/*, X-Total-Count paging, {"message": …} errors, and
// system vs default webhooks.
type fakeForgejo struct {
	mu          sync.Mutex
	users       []*ffUser
	orgs        map[string]bool
	emails      []map[string]any // secondary addresses
	hooks       []*ffHook
	nextID      int64
	adminTokens bool            // Forgejo 16: /admin/users/{u}/tokens exists
	noSystem    bool            // a build that ignores is_system_webhook
	racing      map[string]bool // names someone takes between Core's check and its create
	reqs        []string
}

type ffUser struct {
	ID       int64
	Login    string
	Email    string
	Admin    bool
	Password string
	Tokens   []*ffToken
	MustCh   bool
}

type ffToken struct {
	ID     int64
	Name   string
	SHA1   string
	Scopes []string
}

type ffHook struct {
	ID     int64
	Type   string
	URL    string
	Events []string
	Authz  string
	System bool
}

func newFakeForgejo() *fakeForgejo {
	f := &fakeForgejo{orgs: map[string]bool{}, nextID: 100, adminTokens: true}
	adm := f.addUser("root", "owner@home.lan", true)
	f.addToken(adm, "box", "admintoken000000000000000000000000000001", []string{"write:admin", "read:user"})
	return f
}

func (f *fakeForgejo) id() int64 { f.nextID++; return f.nextID }

func (f *fakeForgejo) addUser(login, email string, admin bool) *ffUser {
	u := &ffUser{ID: f.id(), Login: login, Email: email, Admin: admin, Password: "unknown"}
	f.users = append(f.users, u)
	return u
}

func (f *fakeForgejo) addToken(u *ffUser, name, sha string, scopes []string) *ffToken {
	if sha == "" {
		sha = fmt.Sprintf("%040d", f.id()*7919)
	}
	tk := &ffToken{ID: f.id(), Name: name, SHA1: sha, Scopes: scopes}
	u.Tokens = append(u.Tokens, tk)
	return tk
}

func (f *fakeForgejo) byLogin(l string) *ffUser {
	for _, u := range f.users {
		if strings.EqualFold(u.Login, l) {
			return u
		}
	}
	return nil
}

func (u *ffUser) json() map[string]any {
	return map[string]any{"id": u.ID, "login": u.Login, "username": u.Login, "login_name": "", "source_id": 0, "email": u.Email, "full_name": "",
		"is_admin": u.Admin, "active": true, "prohibit_login": false, "html_url": "https://git.example/" + u.Login, "created": "2026-10-01T10:00:00Z"}
}

func (t *ffToken) json(withSecret bool) map[string]any {
	m := map[string]any{"id": t.ID, "name": t.Name, "token_last_eight": t.SHA1[len(t.SHA1)-8:], "scopes": t.Scopes}
	if withSecret {
		m["sha1"] = t.SHA1
	}
	return m
}

func ffErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg, "url": "https://git.example/api/swagger"})
}

func ffJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// who authenticates the request: a token, or Basic (basic=true).
func (f *fakeForgejo) who(r *http.Request) (u *ffUser, basic bool) {
	a := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(a, "token "); ok {
		for _, x := range f.users {
			for _, t := range x.Tokens {
				if t.SHA1 == tok {
					return x, false
				}
			}
		}
		return nil, false
	}
	if b, ok := strings.CutPrefix(a, "Basic "); ok {
		raw, _ := base64.StdEncoding.DecodeString(b)
		l, p, _ := strings.Cut(string(raw), ":")
		if x := f.byLogin(l); x != nil && x.Password == p {
			return x, true
		}
	}
	return nil, false
}

func paged[T any](w http.ResponseWriter, r *http.Request, all []T) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 30
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(all)))
	from := min((page-1)*limit, len(all))
	to := min(from+limit, len(all))
	ffJSON(w, 200, all[from:to])
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	p, ok := strings.CutPrefix(r.URL.Path, "/api/v1")
	if !ok {
		ffErr(w, 404, "not found")
		return
	}
	me, basic := f.who(r)
	if r.Header.Get("Authorization") != "" && me == nil {
		ffErr(w, 401, "user does not exist")
		return
	}
	if me == nil {
		ffErr(w, 403, "Only signed in user is allowed to call APIs.")
		return
	}
	seg := strings.Split(strings.Trim(p, "/"), "/")
	var in map[string]any
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPatch) {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	switch {
	case p == "/version":
		ffJSON(w, 200, map[string]string{"version": "16.0.6+gitea-1.22.0"})
	case p == "/user":
		ffJSON(w, 200, me.json())
	case len(seg) >= 3 && seg[0] == "users" && seg[2] == "tokens":
		u := f.byLogin(seg[1])
		if u == nil {
			ffErr(w, 404, "user does not exist")
			return
		}
		if u != me && !me.Admin {
			ffErr(w, 403, "only self or admin")
			return
		}
		f.tokens(w, r, u, seg[3:], in, true, basic && me == u)
	case len(seg) == 2 && seg[0] == "users" && r.Method == http.MethodGet:
		if f.byLogin(seg[1]) != nil || f.orgs[strings.ToLower(seg[1])] {
			ffJSON(w, 200, map[string]any{"login": seg[1]})
			return
		}
		ffErr(w, 404, "user does not exist")
	case seg[0] == "admin":
		if !me.Admin {
			ffErr(w, 403, "reqSiteAdmin: you must be a site administrator")
			return
		}
		f.admin(w, r, seg[1:], in)
	default:
		ffErr(w, 404, "not found")
	}
}

func (f *fakeForgejo) tokens(w http.ResponseWriter, r *http.Request, u *ffUser, rest []string, in map[string]any, user, basicOK bool) {
	switch {
	case r.Method == http.MethodGet && len(rest) == 0:
		out := []map[string]any{}
		for _, t := range u.Tokens {
			out = append(out, t.json(false))
		}
		paged(w, r, out)
	case r.Method == http.MethodPost && len(rest) == 0:
		if user && !basicOK {
			ffErr(w, 401, "auth required")
			return
		}
		name, _ := in["name"].(string)
		for _, t := range u.Tokens {
			if t.Name == name {
				ffErr(w, 400, "access token name has been used already")
				return
			}
		}
		var scopes []string
		for _, s := range in["scopes"].([]any) {
			scopes = append(scopes, s.(string))
		}
		if len(scopes) == 0 {
			ffErr(w, 400, "access token must have a scope")
			return
		}
		ffJSON(w, 201, f.addToken(u, name, "", scopes).json(true))
	case r.Method == http.MethodDelete && len(rest) == 1:
		if user && !basicOK {
			ffErr(w, 401, "auth required")
			return
		}
		id, _ := strconv.ParseInt(rest[0], 10, 64)
		for i, t := range u.Tokens {
			if t.ID == id {
				u.Tokens = append(u.Tokens[:i], u.Tokens[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		ffErr(w, 404, "not found")
	default:
		ffErr(w, 405, "method not allowed")
	}
}

func (f *fakeForgejo) admin(w http.ResponseWriter, r *http.Request, seg []string, in map[string]any) {
	switch {
	case len(seg) == 1 && seg[0] == "users" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, u := range f.users {
			out = append(out, u.json())
		}
		paged(w, r, out)
	case len(seg) == 1 && seg[0] == "users" && r.Method == http.MethodPost:
		name, _ := in["username"].(string)
		email, _ := in["email"].(string)
		if f.byLogin(name) != nil || f.orgs[strings.ToLower(name)] || f.racing[name] {
			ffErr(w, 422, "user already exists [name: "+name+"]")
			return
		}
		if name == "admin" {
			ffErr(w, 422, "name is reserved [name: admin]")
			return
		}
		for _, u := range f.users {
			if strings.EqualFold(u.Email, email) {
				ffErr(w, 422, "e-mail already in use [email: "+email+"]")
				return
			}
		}
		u := f.addUser(name, email, false)
		u.Password, _ = in["password"].(string)
		u.MustCh, _ = in["must_change_password"].(bool)
		ffJSON(w, 201, u.json())
	case len(seg) == 2 && seg[0] == "users" && r.Method == http.MethodPatch:
		u := f.byLogin(seg[1])
		if u == nil {
			ffErr(w, 404, "not found")
			return
		}
		if pw, _ := in["password"].(string); pw != "" {
			u.Password = pw
		}
		ffJSON(w, 200, u.json())
	case len(seg) >= 3 && seg[0] == "users" && seg[2] == "tokens":
		if !f.adminTokens {
			ffErr(w, 404, "not found")
			return
		}
		u := f.byLogin(seg[1])
		if u == nil {
			ffErr(w, 404, "user does not exist")
			return
		}
		f.tokens(w, r, u, seg[3:], in, false, false)
	case len(seg) == 2 && seg[0] == "emails" && seg[1] == "search":
		q := strings.ToLower(r.URL.Query().Get("q"))
		out := []map[string]any{}
		for _, e := range f.emails {
			if strings.Contains(strings.ToLower(e["email"].(string)), q) {
				out = append(out, e)
			}
		}
		paged(w, r, out)
	case len(seg) == 1 && seg[0] == "hooks" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, h := range f.hooks {
			if h.System {
				out = append(out, map[string]any{"id": h.ID, "type": h.Type, "url": h.URL, "config": map[string]string{"url": h.URL, "content_type": "json"},
					"events": h.Events, "active": true, "authorization_header": h.Authz})
			}
		}
		paged(w, r, out)
	case len(seg) == 1 && seg[0] == "hooks" && r.Method == http.MethodPost:
		typ, _ := in["type"].(string)
		if typ != "forgejo" && typ != "gitea" {
			ffErr(w, 422, "Invalid hook type: "+typ)
			return
		}
		cfg, _ := in["config"].(map[string]any)
		if cfg == nil || cfg["url"] == nil || cfg["content_type"] == nil {
			ffErr(w, 422, "Missing config option: url")
			return
		}
		var evs []string
		for _, e := range in["events"].([]any) {
			ev := e.(string)
			ev = strings.TrimSuffix(ev, "_only") // listed back as issues / pull_request
			evs = append(evs, ev)
		}
		authz, _ := in["authorization_header"].(string)
		h := &ffHook{ID: f.id(), Type: typ, URL: cfg["url"].(string), Events: evs, Authz: authz, System: cfg["is_system_webhook"] == "true" && !f.noSystem}
		f.hooks = append(f.hooks, h)
		ffJSON(w, 201, map[string]any{"id": h.ID, "type": h.Type, "url": h.URL, "events": h.Events, "active": true, "authorization_header": h.Authz})
	case len(seg) == 2 && seg[0] == "hooks" && r.Method == http.MethodDelete:
		id, _ := strconv.ParseInt(seg[1], 10, 64)
		for i, h := range f.hooks {
			if h.ID == id {
				f.hooks = append(f.hooks[:i], f.hooks[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		ffErr(w, 404, "not found")
	default:
		ffErr(w, 404, "not found")
	}
}

func ffTarget(t *testing.T, f *fakeForgejo) Target {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return Target{BaseURL: srv.URL, PublicURL: "https://git.example", HTTP: srv.Client(), DataDir: t.TempDir(),
		Fields: map[string]string{"TOKEN": "admintoken000000000000000000000000000001"},
		Now:    func() time.Time { return time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC) }}
}

func TestForgejoTest(t *testing.T) {
	f := newFakeForgejo()
	f.addUser("sani", "sani@home.lan", false)
	tg := ffTarget(t, f)
	info, err := forgejo{}.Test(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "16.0.6+gitea-1.22.0" || !info.Admin || info.Users != 2 || strings.Join(info.Permissions, ",") != "write:admin,read:user" {
		t.Fatalf("info %+v", info)
	}
	if !strings.Contains(info.Detail, "adds 8 system webhooks") || !strings.Contains(info.Detail, "ALLOWED_HOST_LIST") {
		t.Fatalf("detail %q", info.Detail)
	}
	// a token without write:admin
	f.users[0].Tokens[0].Scopes = []string{"read:admin", "read:user"}
	if _, err := (forgejo{}).Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "write:admin") {
		t.Fatalf("scopes: %v", err)
	}
	// a non-admin's token
	u := f.byLogin("sani")
	f.addToken(u, "x", "usertoken0000000000000000000000000000002", []string{"all"})
	tg.Fields = map[string]string{"TOKEN": "usertoken0000000000000000000000000000002"}
	if _, err := (forgejo{}).Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "not a Forgejo site administrator") {
		t.Fatalf("non-admin: %v", err)
	}
	// a wrong token: never echoed
	tg.Fields = map[string]string{"TOKEN": "wrongwrongwrong"}
	if _, err := (forgejo{}).Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "wrongwrong") {
		t.Fatalf("401: %v", err)
	}
	tg.Fields = map[string]string{}
	if _, err := (forgejo{}).Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "no TOKEN") {
		t.Fatalf("missing: %v", err)
	}
}

func TestForgejoProvisionLinks(t *testing.T) {
	f := newFakeForgejo()
	// more than one page of users: the match is on page 2
	for i := 0; i < 60; i++ {
		f.addUser(fmt.Sprintf("u%d", i), fmt.Sprintf("u%d@home.lan", i), false)
	}
	sani := f.addUser("sani", "Sani@Home.lan", false)
	mira := f.addUser("mira", "mira@work.example", false)
	f.emails = append(f.emails, map[string]any{"email": "mira@home.lan", "verified": true, "primary": false, "user_id": mira.ID, "username": "mira"})
	tg := ffTarget(t, f)
	c := forgejo{}
	acc, err := c.Provision(context.Background(), tg, Person{Email: "sani@home.lan", Name: "Sani"})
	if err != nil || acc.Status != StatusLinked || acc.RemoteID != strconv.FormatInt(sani.ID, 10) || acc.Fields["TOKEN"] != "" || acc.Created {
		t.Fatalf("primary: %+v %v", acc, err)
	}
	acc, err = c.Provision(context.Background(), tg, Person{Email: "mira@home.lan"})
	if err != nil || acc.Status != StatusLinked || acc.RemoteID != strconv.FormatInt(mira.ID, 10) {
		t.Fatalf("secondary: %+v %v", acc, err)
	}
	acc, err = c.Provision(context.Background(), tg, Person{Email: "owner@home.lan"})
	if err != nil || acc.Status != StatusLinked || !strings.Contains(acc.Note, "administrator") {
		t.Fatalf("admin: %+v %v", acc, err)
	}
	for _, r := range f.reqs {
		if strings.HasPrefix(r, "POST") || strings.HasPrefix(r, "PATCH") || strings.HasPrefix(r, "DELETE") {
			t.Fatalf("linking wrote: %s", r)
		}
	}
	// linking by hand
	list, err := c.RemoteAccounts(context.Background(), tg)
	if err != nil || len(list) != 63 {
		t.Fatalf("remote accounts %d %v", len(list), err)
	}
	acc, err = c.Link(context.Background(), tg, strconv.FormatInt(mira.ID, 10))
	if err != nil || acc.Status != StatusLinked || !strings.Contains(acc.Note, `"mira"`) {
		t.Fatalf("link %+v %v", acc, err)
	}
	if _, err := c.Link(context.Background(), tg, "999999"); err == nil {
		t.Fatal("link to nobody")
	}
}

func TestForgejoProvisionCreates(t *testing.T) {
	f := newFakeForgejo()
	f.addUser("sani", "someone-else@home.lan", false) // the name is taken by another person
	f.orgs["sani-bjelosevic"] = true                  // and the full name by an organization
	tg := ffTarget(t, f)
	c := forgejo{}
	acc, err := c.Provision(context.Background(), tg, Person{Email: "sani@home.lan", Name: "Sani Bjelošević"})
	if err != nil {
		t.Fatal(err)
	}
	u := f.byLogin("sani-2")
	if u == nil || acc.Status != StatusOK || !acc.Created || acc.RemoteID != strconv.FormatInt(u.ID, 10) || u.Email != "sani@home.lan" || u.MustCh {
		t.Fatalf("created %+v %+v", acc, u)
	}
	if f.byLogin("sani").Email != "someone-else@home.lan" || len(u.Password) != 40 {
		t.Fatal("took over the existing account, or no random password")
	}
	tok := acc.Fields["TOKEN"]
	if tok == "" || len(u.Tokens) != 1 || u.Tokens[0].SHA1 != tok || u.Tokens[0].Name != ForgejoTokenName ||
		strings.Join(u.Tokens[0].Scopes, ",") != strings.Join(ForgejoPersonScopes, ",") {
		t.Fatalf("token %+v %+v", acc.Fields, u.Tokens)
	}
	if strings.Contains(acc.Note, u.Password) {
		t.Fatal("password in the note")
	}
	// the person's own token in Core: nothing minted
	acc, err = c.Provision(context.Background(), tg, Person{Email: "mira@home.lan", Name: "Mira", KeepOwnCredential: true})
	if err != nil || acc.Status != StatusOK || acc.Fields["TOKEN"] != "" || len(f.byLogin("mira").Tokens) != 0 {
		t.Fatalf("keep own %+v %v", acc, err)
	}
	// a reserved name is skipped
	acc, err = c.Provision(context.Background(), tg, Person{Email: "admin@home.lan"})
	if err != nil || f.byLogin("admin-2") == nil || acc.Fields["TOKEN"] == "" {
		t.Fatalf("reserved %+v %v", acc, err)
	}
	// a race: the free-looking name is refused at creation, the next one is used
	f.racing = map[string]bool{"late": true}
	acc, err = c.Provision(context.Background(), tg, Person{Email: "late@home.lan"})
	if err != nil || f.byLogin("late-2") == nil || acc.Fields["TOKEN"] == "" {
		t.Fatalf("race %+v %v", acc, err)
	}
}

func TestForgejoProvisionBasicFallback(t *testing.T) {
	f := newFakeForgejo()
	f.adminTokens = false // before Forgejo 16
	tg := ffTarget(t, f)
	acc, err := forgejo{}.Provision(context.Background(), tg, Person{Email: "kid@home.lan"})
	if err != nil || acc.Fields["TOKEN"] == "" {
		t.Fatalf("%+v %v", acc, err)
	}
	if u := f.byLogin("kid"); len(u.Tokens) != 1 || u.Tokens[0].SHA1 != acc.Fields["TOKEN"] {
		t.Fatalf("tokens %+v", u.Tokens)
	}
}

func TestForgejoUsername(t *testing.T) {
	for in, want := range map[string]string{
		"Sani":                  "sani",
		"Đorđe Šćekić":          "djordje-scekic",
		"john.doe+git":          "john-doe-git",
		"__x__":                 "x",
		"...":                   "",
		strings.Repeat("a", 50): strings.Repeat("a", 40),
	} {
		if got := fjUsername(in, 40); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	if got := fjUsername("abcdefgh-ijk", 9); got != "abcdefgh" {
		t.Errorf("trailing dash after cut: %q", got)
	}
}

func TestForgejoRotateKey(t *testing.T) {
	for _, adminTokens := range []bool{true, false} {
		t.Run(fmt.Sprint("adminTokens=", adminTokens), func(t *testing.T) {
			f := newFakeForgejo()
			f.adminTokens = adminTokens
			tg := ffTarget(t, f)
			c := forgejo{}
			acc, err := c.Provision(context.Background(), tg, Person{Email: "sani@home.lan"})
			if err != nil {
				t.Fatal(err)
			}
			u := f.byLogin("sani")
			own := f.addToken(u, "laptop", "", []string{"all"}) // the person's own: kept
			old := acc.Fields["TOKEN"]
			rot, err := c.RotateKey(context.Background(), tg, AccountRef{RemoteID: acc.RemoteID, Email: "sani@home.lan", Fields: acc.Fields})
			if err != nil {
				t.Fatal(err)
			}
			nw := rot.Fields["TOKEN"]
			if nw == "" || nw == old {
				t.Fatalf("rotated %+v", rot)
			}
			names := []string{}
			for _, k := range u.Tokens {
				names = append(names, k.Name)
				if k.SHA1 == old {
					t.Fatal("old token survived")
				}
			}
			sort.Strings(names)
			if strings.Join(names, "|") != "Pocket Core · 2026-10-20 08:00:00|laptop" || own.SHA1 == "" {
				t.Fatalf("tokens left %v", names)
			}
			if adminTokens != (rot.Note == "") {
				t.Fatalf("note %q", rot.Note)
			}
			if f.users[0].Tokens[0].SHA1 != tg.Fields["TOKEN"] {
				t.Fatal("box token touched")
			}
		})
	}
	f := newFakeForgejo()
	tg := ffTarget(t, f)
	if _, err := (forgejo{}).RotateKey(context.Background(), tg, AccountRef{RemoteID: strconv.FormatInt(f.users[0].ID, 10)}); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("admin: %v", err)
	}
	// a stored token of another account
	a := f.addUser("a", "a@x", false)
	b := f.addUser("b", "b@x", false)
	tb := f.addToken(b, ForgejoTokenName, "", []string{"read:user"})
	_, err := forgejo{}.RotateKey(context.Background(), tg, AccountRef{RemoteID: strconv.FormatInt(a.ID, 10), Fields: map[string]string{"TOKEN": tb.SHA1}})
	if err == nil || !strings.Contains(err.Error(), "another Forgejo account") || len(b.Tokens) != 1 {
		t.Fatalf("foreign: %v", err)
	}
}

func TestForgejoSubscriptions(t *testing.T) {
	f := newFakeForgejo()
	tg := ffTarget(t, f)
	c := forgejo{}
	// someone's own system webhook: not Core's
	f.hooks = append(f.hooks, &ffHook{ID: 7, Type: "matrix", URL: "https://chat.example/hook", Events: []string{"push"}, System: true})
	ids := map[string]string{}
	for _, tr := range c.PushTriggers() {
		id, err := c.Subscribe(context.Background(), tg, Subscription{Trigger: tr.Trigger, Name: tr.Name, URL: "https://core.example/api/v1/connectors/hooks/h-" + tr.Trigger,
			HeaderName: PushHeader, HeaderValue: "s3cret-" + tr.Trigger})
		if err != nil {
			t.Fatal(err)
		}
		ids[id] = tr.Name
	}
	for _, h := range f.hooks[1:] {
		if !h.System || h.Type != "forgejo" || len(h.Events) != 1 || !strings.HasPrefix(h.Authz, "Bearer s3cret-") {
			t.Fatalf("hook %+v", h)
		}
	}
	subs, err := c.Subscriptions(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 9 || subs["7"] != "system webhook 7" {
		t.Fatalf("subs %v", subs)
	}
	for id, name := range ids {
		if subs[id] != name {
			t.Fatalf("%s: %q want %q", id, subs[id], name)
		}
	}
	for id := range ids {
		if err := c.Unsubscribe(context.Background(), tg, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Unsubscribe(context.Background(), tg, "424242"); err != nil {
		t.Fatalf("gone already: %v", err)
	}
	if len(f.hooks) != 1 || f.hooks[0].ID != 7 {
		t.Fatalf("left %+v", f.hooks)
	}
	if _, err := c.Subscribe(context.Background(), tg, Subscription{Trigger: "push", URL: "not a url"}); err == nil {
		t.Fatal("bad URL accepted")
	}
	// a build that makes a template hook instead: refused and cleaned up
	f.noSystem = true
	if _, err := c.Subscribe(context.Background(), tg, Subscription{Trigger: "push", URL: "https://core.example/api/v1/connectors/hooks/x", HeaderValue: "s"}); err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("template: %v", err)
	}
	if len(f.hooks) != 1 {
		t.Fatalf("template hook left behind: %+v", f.hooks)
	}
	who, err := c.WhoAmI(context.Background(), tg)
	if err != nil || who.ID != strconv.FormatInt(f.users[0].ID, 10) {
		t.Fatalf("whoami %+v %v", who, err)
	}
}

// Payloads as Forgejo 16 sends them (services/webhook/notifier.go), trimmed.
const (
	ffRepo   = `"repository":{"id":12,"name":"dotfiles","full_name":"sani/dotfiles","html_url":"https://git.example/sani/dotfiles","private":true,"fork":false,"mirror":false,"created_at":"2026-10-20T07:00:00Z"}`
	ffSender = `"sender":{"id":105,"login":"sani","username":"sani","email":"sani@home.lan"}`
)

func TestForgejoDecode(t *testing.T) {
	tg := Target{PublicURL: "https://git.example", Now: func() time.Time { return time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC) }}
	c := forgejo{}
	type want struct {
		name, subject, title, url string
		drop                      string
		at                        string
		keys                      map[string]any
	}
	cases := []struct {
		trigger, body string
		want          want
	}{
		{"push", `{"ref":"refs/heads/main","before":"aaa","after":"bbb","compare_url":"https://git.example/sani/dotfiles/compare/aaa...bbb","total_commits":2,
			"commits":[{"id":"bbb","message":"Fix zsh prompt\n\nlong body","author":{"name":"Sani"}},{"id":"ccc","message":"Add vimrc"}],
			"pusher":{"id":105,"login":"sani"},` + ffRepo + `,` + ffSender + `}`,
			want{name: "pushed", subject: "repo:12", title: "Pushed 2 commits to sani/dotfiles (main)", url: "https://git.example/sani/dotfiles/compare/aaa...bbb",
				keys: map[string]any{"branch": "main", "commits": 2, "repo": "sani/dotfiles", "repo_url": "https://git.example/sani/dotfiles", "commit_messages": []string{"Fix zsh prompt", "Add vimrc"}}}},
		{"push", `{"ref":"refs/tags/v1","after":"bbb","total_commits":1,"commits":[{"id":"b"}],` + ffRepo + `,` + ffSender + `}`, want{drop: "tag push"}},
		{"push", `{"ref":"refs/heads/old","after":"0000000000000000000000000000000000000000",` + ffRepo + `,` + ffSender + `}`, want{drop: "branch deletion"}},
		{"push", `{"ref":"refs/heads/new","before":"0000000000000000000000000000000000000000","after":"bbb","total_commits":0,"commits":[],` + ffRepo + `,` + ffSender + `}`, want{drop: "without new commits"}},
		{"push", `{"ref":"refs/heads/main","after":"bbb","total_commits":3,"commits":[{"id":"b"}],"repository":{"id":13,"full_name":"sani/linux","mirror":true},` + ffSender + `}`, want{drop: "pull-mirror"}},
		{"push", `{"ref":"refs/heads/main","after":"bbb","total_commits":1,"commits":[{"id":"b"}],` + ffRepo + `,"sender":{"id":-2,"login":"forgejo-actions"}}`, want{drop: "forgejo-actions"}},
		{"push", `{"ref":"refs/heads/main","after":"bbb","total_commits":1,"commits":[{"id":"b"}],` + ffRepo + `,"sender":{"id":-1,"login":"Ghost"}}`, want{drop: "Ghost"}},
		{"push", `{"ref":"refs/heads/main","after":"bbb","total_commits":1,"commits":[{"id":"b"}],` + ffRepo + `,"sender":{"id":77,"login":"renovate[bot]"}}`, want{drop: "bot"}},
		{"create", `{"sha":"bbb","ref":"feature/x","ref_type":"branch",` + ffRepo + `,` + ffSender + `}`,
			want{name: "branch_created", subject: "repo:12", title: "Branch feature/x created in sani/dotfiles", url: "https://git.example/sani/dotfiles/src/branch/feature/x", keys: map[string]any{"branch": "feature/x", "ref_type": "branch"}}},
		{"create", `{"sha":"bbb","ref":"v1.0","ref_type":"tag",` + ffRepo + `,` + ffSender + `}`,
			want{name: "tag_created", title: "Tag v1.0 created in sani/dotfiles", url: "https://git.example/sani/dotfiles/src/tag/v1.0", keys: map[string]any{"tag": "v1.0"}}},
		{"delete", `{"ref":"feature/x","ref_type":"branch","pusher_type":"user",` + ffRepo + `,` + ffSender + `}`,
			want{name: "branch_deleted", title: "Branch feature/x deleted in sani/dotfiles", url: "https://git.example/sani/dotfiles"}},
		{"release", `{"action":"published","release":{"id":5,"tag_name":"v1.0","name":"First","html_url":"https://git.example/sani/dotfiles/releases/tag/v1.0","draft":false,"prerelease":true,"published_at":"2026-10-20T07:30:00Z"},` + ffRepo + `,` + ffSender + `}`,
			want{name: "release_published", subject: "release:5", title: "sani/dotfiles First", url: "https://git.example/sani/dotfiles/releases/tag/v1.0", at: "2026-10-20T07:30:00Z", keys: map[string]any{"tag": "v1.0", "prerelease": true}}},
		{"release", `{"action":"updated","release":{"id":5,"tag_name":"v1.0"},` + ffRepo + `,` + ffSender + `}`, want{drop: "release updated"}},
		{"release", `{"action":"published","release":{"id":6,"tag_name":"v2","draft":true},` + ffRepo + `,` + ffSender + `}`, want{drop: "draft"}},
		{"issues_only", `{"action":"opened","number":3,"issue":{"id":40,"number":3,"title":"Prompt is slow","html_url":"https://git.example/sani/dotfiles/issues/3","state":"open","created_at":"2026-10-20T07:40:00Z"},` + ffRepo + `,` + ffSender + `}`,
			want{name: "issue_opened", subject: "issue:12#3", title: "Prompt is slow", url: "https://git.example/sani/dotfiles/issues/3", at: "2026-10-20T07:40:00Z", keys: map[string]any{"number": int64(3), "state": "open"}}},
		{"issues_only", `{"action":"closed","number":3,"issue":{"id":40,"number":3,"title":"Prompt is slow","state":"closed","closed_at":"2026-10-20T07:50:00Z"},` + ffRepo + `,` + ffSender + `}`,
			want{name: "issue_closed", at: "2026-10-20T07:50:00Z"}},
		{"issues_only", `{"action":"edited","number":3,"issue":{"id":40,"number":3,"title":"x"},` + ffRepo + `,` + ffSender + `}`, want{drop: "issue edited"}},
		{"pull_request_only", `{"action":"opened","number":4,"pull_request":{"id":41,"number":4,"title":"Add tmux","html_url":"https://git.example/sani/dotfiles/pulls/4","state":"open","merged":false,"base":{"ref":"main"},"head":{"ref":"tmux"},"created_at":"2026-10-20T07:41:00Z"},` + ffRepo + `,` + ffSender + `}`,
			want{name: "pull_request_opened", subject: "pull:12#4", title: "Add tmux", url: "https://git.example/sani/dotfiles/pulls/4", keys: map[string]any{"base": "main", "head": "tmux", "merged": false}}},
		{"pull_request_only", `{"action":"closed","number":4,"pull_request":{"id":41,"number":4,"title":"Add tmux","state":"closed","merged":true,"merged_at":"2026-10-20T07:55:00Z","closed_at":"2026-10-20T07:55:00Z"},` + ffRepo + `,` + ffSender + `}`,
			want{name: "pull_request_merged", at: "2026-10-20T07:55:00Z"}},
		{"pull_request_only", `{"action":"closed","number":5,"pull_request":{"id":42,"number":5,"title":"Nope","state":"closed","merged":false,"closed_at":"2026-10-20T07:56:00Z"},` + ffRepo + `,` + ffSender + `}`,
			want{name: "pull_request_closed"}},
		{"pull_request_only", `{"action":"synchronized","number":5,"pull_request":{"id":42,"number":5},` + ffRepo + `,` + ffSender + `}`, want{drop: "synchronized"}},
		{"repository", `{"action":"created",` + ffRepo + `,"organization":{"id":105,"login":"sani"},` + ffSender + `}`,
			want{name: "repo_created", subject: "repo:12", title: "sani/dotfiles", url: "https://git.example/sani/dotfiles", at: "2026-10-20T07:00:00Z", keys: map[string]any{"private": true}}},
		{"repository", `{"action":"deleted",` + ffRepo + `,` + ffSender + `}`, want{name: "repo_deleted", title: "sani/dotfiles"}},
		{"repository", `{"action":"created","repository":{"id":20,"full_name":"team/dotfiles","fork":true},` + ffSender + `}`, want{drop: "fork created"}},
		{"fork", `{"forkee":{"id":9,"full_name":"mira/dotfiles","html_url":"https://git.example/mira/dotfiles"},"repository":{"id":21,"full_name":"sani/dotfiles-fork","html_url":"https://git.example/sani/dotfiles-fork","fork":true},` + ffSender + `}`,
			want{name: "repo_forked", subject: "repo:21", title: "sani/dotfiles-fork forked from mira/dotfiles", url: "https://git.example/sani/dotfiles-fork", keys: map[string]any{"forked_from": "mira/dotfiles"}}},
		{"wiki", `{"action":"created",` + ffRepo + `,` + ffSender + `}`, want{drop: "unknown trigger"}},
	}
	for i, tc := range cases {
		d, err := c.Decode(tg, tc.trigger, []byte(tc.body))
		if err != nil {
			t.Fatalf("%d %s: %v", i, tc.trigger, err)
		}
		if tc.want.drop != "" {
			if !strings.Contains(d.Drop, tc.want.drop) {
				t.Fatalf("%d %s: drop %q, want %q", i, tc.trigger, d.Drop, tc.want.drop)
			}
			continue
		}
		if d.Drop != "" || d.Shared || d.OwnerID != "105" || d.DedupeKey == "" || !d.Event.Personal {
			t.Fatalf("%d %s: %+v", i, tc.trigger, d)
		}
		ev := d.Event
		p := ev.Payload
		if ev.Name != tc.want.name || (tc.want.subject != "" && ev.Subject != tc.want.subject) || (tc.want.title != "" && p["title"] != tc.want.title) || (tc.want.url != "" && p["url"] != tc.want.url) {
			t.Fatalf("%d %s: %s %s %v %v", i, tc.trigger, ev.Name, ev.Subject, p["title"], p["url"])
		}
		if tc.want.at != "" && d.At.UTC().Format(time.RFC3339) != tc.want.at {
			t.Fatalf("%d %s: at %v", i, tc.trigger, d.At)
		}
		if p["forgejo_user"] != "sani" || p["forgejo_user_id"] != int64(105) {
			t.Fatalf("%d %s: who %v", i, tc.trigger, p)
		}
		for k, v := range tc.want.keys {
			if fmt.Sprint(p[k]) != fmt.Sprint(v) {
				t.Fatalf("%d %s: %s = %v, want %v", i, tc.trigger, k, p[k], v)
			}
		}
	}
	// the same body twice is the same delivery; another body is not
	b := []byte(cases[0].body)
	d1, _ := c.Decode(tg, "push", b)
	d2, _ := c.Decode(tg, "push", b)
	d3, _ := c.Decode(tg, "push", []byte(strings.Replace(cases[0].body, `"after":"bbb"`, `"after":"ddd"`, 1)))
	if d1.DedupeKey != d2.DedupeKey || d1.DedupeKey == d3.DedupeKey {
		t.Fatal("dedupe keys")
	}
	if _, err := c.Decode(tg, "push", []byte(`{"zen":"hi"}`)); err == nil {
		t.Fatal("not a delivery accepted")
	}
}

func TestForgejoPoll(t *testing.T) {
	f := newFakeForgejo()
	gone := f.addUser("old", "old@x", false)
	tg := ffTarget(t, f)
	c := forgejo{}
	evs, st, err := c.Poll(context.Background(), tg, nil)
	if err != nil || len(evs) != 0 {
		t.Fatalf("baseline %v %v", evs, err)
	}
	evs, st, err = c.Poll(context.Background(), tg, st)
	if err != nil || len(evs) != 0 {
		t.Fatalf("nothing new %v %v", evs, err)
	}
	nu := f.addUser("newbie", "newbie@x", false)
	f.users = append(f.users[:1], f.users[2:]...) // "old" deleted
	evs, st, err = c.Poll(context.Background(), tg, st)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events %+v %v", evs, err)
	}
	if evs[0].Name != "user_created" || evs[0].RemoteUserID != strconv.FormatInt(nu.ID, 10) || evs[0].Payload["forgejo_user"] != "newbie" || evs[0].Payload["title"] != "newbie" {
		t.Fatalf("created %+v", evs[0])
	}
	if evs[1].Name != "user_deleted" || evs[1].RemoteUserID != strconv.FormatInt(gone.ID, 10) {
		t.Fatalf("deleted %+v", evs[1])
	}
	if len(st) > 80 {
		t.Fatalf("state not compact: %s", st)
	}
}

func TestForgejoSSOByHand(t *testing.T) {
	c := forgejo{}
	if r := c.SSORedirects("https://git.example/"); len(r) != 1 || r[0] != "https://git.example/user/oauth2/PocketCore/callback" {
		t.Fatalf("redirects %v", r)
	}
	err := c.ConfigureSSO(context.Background(), Target{PublicURL: "https://git.example"}, SSOConfig{Enabled: true, Issuer: "https://core.example/", ClientID: "cid", ClientSecret: "topsecret"})
	if err == nil || strings.Contains(err.Error(), "topsecret") || !strings.Contains(err.Error(), "https://core.example/.well-known/openid-configuration") ||
		!strings.Contains(err.Error(), "https://git.example/user/oauth2/PocketCore/callback") || !strings.Contains(err.Error(), "--key cid") {
		t.Fatalf("sso: %v", err)
	}
	if err := c.ConfigureSSO(context.Background(), Target{}, SSOConfig{}); err != nil {
		t.Fatalf("off: %v", err)
	}
	if c.Capabilities().SSO {
		t.Fatal("SSO capability")
	}
}
