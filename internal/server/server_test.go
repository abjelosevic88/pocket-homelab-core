package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	cj  []*http.Cookie
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("BASE_URL", "http://core.test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	box, err := crypto.LoadBox(cfg.SecretPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath, box, log)
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, srv: httptest.NewServer(s.Router())}
	t.Cleanup(e.srv.Close)
	t.Cleanup(func() { st.Close() })
	return e
}

func (e *env) do(method, path, auth string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	for _, c := range e.cj {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	if cs := res.Cookies(); len(cs) > 0 {
		e.cj = cs
	}
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)
	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["needs_setup"] != true {
		t.Fatalf("expected needs_setup, got %v", st)
	}
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123", "name": "A", "box_name": "Box"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	if settings.RegistrationKey == "" || settings.BoxName != "Box" {
		t.Fatalf("settings %+v", settings)
	}

	// Register an app with the key, then again with its token.
	var reg struct {
		App   store.App `json:"app"`
		Token string    `json:"token"`
		OIDC  struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"oidc"`
	}
	m := map[string]any{"slug": "habits", "name": "Pocket Habits", "url": e.srv.URL, "version": "1.0",
		"subscribes": []map[string]string{{"topics": "kitchen.*", "url": "/healthz"}},
		"oidc":       map[string]any{"redirect_uris": []string{"http://rp/cb"}}}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, m, &reg); c != 201 || reg.Token == "" || reg.OIDC.ClientSecret == "" {
		t.Fatalf("register %d %+v", c, reg)
	}
	if c := e.do("POST", "/api/v1/apps/register", "pca_wrong", m, nil); c != 401 {
		t.Fatalf("bad token should 401, got %d", c)
	}
	var reg2 struct {
		Token string `json:"token"`
	}
	if c := e.do("POST", "/api/v1/apps/register", reg.Token, m, &reg2); c != 200 || reg2.Token != "" {
		t.Fatalf("re-register %d token=%q", c, reg2.Token)
	}

	// Events: wrong prefix forbidden, own prefix ok, listed, replayable.
	if c := e.do("POST", "/api/v1/events", reg.Token, map[string]any{"topic": "kitchen.cooked"}, nil); c != 403 {
		t.Fatalf("foreign topic %d", c)
	}
	var ev store.Event
	if c := e.do("POST", "/api/v1/events", reg.Token, map[string]any{"topic": "habits.marked", "subject": "habit:6", "payload": map[string]any{"x": 1}}, &ev); c != 201 || ev.ID == 0 {
		t.Fatalf("publish %d", c)
	}
	var list []store.Event
	e.do("GET", "/api/v1/events?topic=habits.*", reg.Token, nil, &list)
	if len(list) != 1 || list[0].Source != "habits" {
		t.Fatalf("events %+v", list)
	}

	// A third-party service with a secret is masked on read.
	var sv map[string]any
	if c := e.do("POST", "/api/v1/services", "", map[string]any{"type": "jellyfin", "url": "http://jf", "fields": map[string]string{"API_KEY": "jfsecret"}}, &sv); c != 201 {
		t.Fatalf("service %d", c)
	}
	if sv["fields"].(map[string]any)["API_KEY"] == "jfsecret" {
		t.Fatal("secret not masked")
	}

	// Pairing: code → device token → profile with both services.
	var pair struct {
		Code string `json:"code"`
	}
	if c := e.do("POST", "/api/v1/pair", "", nil, &pair); c != 201 {
		t.Fatalf("pair %d", c)
	}
	var prof Profile
	if c := e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": "iPhone", "platform": "ios"}, &prof); c != 201 || prof.Token == "" {
		t.Fatalf("claim %d", c)
	}
	if prof.Values["JELLYFIN_API_KEY"] != "jfsecret" || prof.Values["HABITS_API_KEY"] != prof.Token || prof.Values["HABITS_URL"] != e.srv.URL {
		t.Fatalf("profile values %+v", prof.Values)
	}
	if !strings.Contains(prof.Env, "JELLYFIN_URL=http://jf") {
		t.Fatalf("env text:\n%s", prof.Env)
	}
	if c := e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code}, nil); c != 404 {
		t.Fatalf("second claim %d", c)
	}

	// The app validates the device token through introspection.
	var intro map[string]any
	e.do("POST", "/api/v1/auth/introspect", reg.Token, map[string]string{"token": prof.Token}, &intro)
	if intro["active"] != true || intro["kind"] != "device" {
		t.Fatalf("introspect %+v", intro)
	}
	e.do("POST", "/api/v1/auth/introspect", reg.Token, map[string]string{"token": "pcd_nope"}, &intro)
	if intro["active"] != false {
		t.Fatalf("bogus token active")
	}

	// OIDC: authorize with the browser session, exchange the code, userinfo.
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", e.srv.URL+"/oidc/authorize?client_id=pocket-habits&redirect_uri=http://rp/cb&response_type=code&scope=openid%20email&state=s1&nonce=n1", nil)
	for _, c := range e.cj {
		req.AddCookie(c)
	}
	res, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != 302 || loc.Host != "rp" || loc.Query().Get("code") == "" || loc.Query().Get("state") != "s1" {
		t.Fatalf("authorize %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {"http://rp/cb"}}
	treq, _ := http.NewRequest("POST", e.srv.URL+"/oidc/token", strings.NewReader(form.Encode()))
	treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	treq.SetBasicAuth("pocket-habits", reg.OIDC.ClientSecret)
	tres, err := http.DefaultClient.Do(treq)
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(tres.Body).Decode(&tok)
	tres.Body.Close()
	if tres.StatusCode != 200 || tok["id_token"] == nil || tok["access_token"] == nil {
		t.Fatalf("token %d %+v", tres.StatusCode, tok)
	}
	var ui map[string]any
	if c := e.do("GET", "/oidc/userinfo", tok["access_token"].(string), nil, &ui); c != 200 || ui["email"] != "a@b.c" {
		t.Fatalf("userinfo %d %+v", c, ui)
	}
	// Unregistered redirect must be refused outright.
	req2, _ := http.NewRequest("GET", e.srv.URL+"/oidc/authorize?client_id=pocket-habits&redirect_uri=http://evil/cb&response_type=code&scope=openid", nil)
	res2, _ := cl.Do(req2)
	res2.Body.Close()
	if res2.StatusCode != 400 {
		t.Fatalf("evil redirect %d", res2.StatusCode)
	}

	// Removing the app removes its mirrored service, webhook and client.
	if c := e.do("DELETE", "/api/v1/apps/habits", "", nil, nil); c != 204 {
		t.Fatalf("delete app %d", c)
	}
	var svcs []map[string]any
	e.do("GET", "/api/v1/services", "", nil, &svcs)
	if len(svcs) != 1 {
		t.Fatalf("services after delete: %d", len(svcs))
	}
}
