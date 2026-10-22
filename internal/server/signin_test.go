package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	pccrypto "github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Sign-in methods set in Settings take over from the environment at once: an admin adds PocketID,
// people sign in with it, passwords can then be switched off (never before a provider works), a
// member cannot touch any of it, and Reset hands control back to SSO_* / PASSWORD_LOGIN.
func TestSigninMethodsFromSettings(t *testing.T) {
	up := newFakeUpstream(t)
	dir := t.TempDir()
	ts := httptest.NewUnstartedServer(nil)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("BASE_URL", "http://"+ts.Listener.Addr().String())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	box, _ := pccrypto.LoadBox(cfg.SecretPath)
	st, err := store.Open(cfg.DBPath, box, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(cfg, st, log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = s.Router()
	ts.Start()
	t.Cleanup(ts.Close)
	e := &env{t: t, srv: ts}
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123", "name": "A", "box_name": "Box"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}

	var v signinView
	e.do("GET", "/api/v1/signin", "", nil, &v)
	if v.Enabled || !v.PasswordLogin || v.Source != "none" || !strings.HasSuffix(v.CallbackURL, "/api/v1/auth/sso/callback") {
		t.Fatalf("defaults %+v", v)
	}
	// passwords cannot go before a provider is on
	if c := e.do("PUT", "/api/v1/signin", "", map[string]any{"password_login": false}, nil); c != 400 {
		t.Fatalf("passwords off without a provider: %d", c)
	}
	// an issuer that does not answer is refused
	if c := e.do("PUT", "/api/v1/signin", "", map[string]any{"enabled": true, "issuer": "http://127.0.0.1:1", "client_id": "core", "password_login": true}, nil); c != 400 {
		t.Fatalf("dead issuer accepted: %d", c)
	}
	if c := e.do("POST", "/api/v1/signin/test", "", map[string]string{"issuer": up.URL + "/.well-known/openid-configuration"}, nil); c != 200 {
		t.Fatalf("test %d", c)
	}
	if c := e.do("PUT", "/api/v1/signin", "", map[string]any{"enabled": true, "issuer": up.URL, "client_id": "core", "client_secret": "shh", "name": "PocketID", "password_login": true}, &v); c != 200 {
		t.Fatalf("save %d", c)
	}
	if !v.Enabled || !v.HasSecret || v.Source != "settings" || v.Name != "PocketID" {
		t.Fatalf("saved %+v", v)
	}
	raw, _ := st.KVGet(ctx, signinKey)
	if strings.Contains(raw, "shh") {
		t.Fatal("client secret stored in the clear")
	}
	var status map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &status)
	if status["sso"] != true || status["sso_name"] != "PocketID" || status["password_login"] != true {
		t.Fatalf("status %v", status)
	}

	// Signing in with the provider set here (no SSO_* in the environment) works end to end.
	up.email = "a@b.c"
	jar, _ := cookiejar.New(nil)
	var last *url.URL
	cl := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		last = req.URL
		if req.URL.Host == ts.Listener.Addr().String() && req.URL.Path != "/api/v1/auth/sso/callback" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	res, err := cl.Get(ts.URL + "/api/v1/auth/sso/start?next=/apps")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if last == nil || last.Path != "/apps" || last.Query().Get("error") != "" {
		t.Fatalf("sign-in ended at %v", last)
	}

	// now passwords can go; a password sign-in is refused, and saving again keeps the secret
	if c := e.do("PUT", "/api/v1/signin", "", map[string]any{"enabled": true, "issuer": up.URL, "client_id": "core", "name": "PocketID", "password_login": false}, &v); c != 200 || !v.HasSecret || v.PasswordLogin {
		t.Fatalf("passwords off: %d %+v", c, v)
	}
	if c := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil); c != 403 {
		t.Fatalf("password login with passwords off: %d", c)
	}
	// …and the provider cannot be switched off while passwords are off
	if c := e.do("PUT", "/api/v1/signin", "", map[string]any{"enabled": false, "password_login": false}, nil); c != 400 {
		t.Fatalf("lock-out accepted: %d", c)
	}

	// a member sees none of it
	member := &store.User{Email: "m@b.c", Name: "M", Role: "member"}
	_ = st.CreateUser(ctx, member)
	_, tok, err := st.CreateAPIToken(ctx, member.ID, "t")
	if err != nil {
		t.Fatal(err)
	}
	saved := e.cj
	e.cj = nil
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		if c := e.do(m, "/api/v1/signin", tok, map[string]any{"password_login": true}, nil); c != 403 {
			t.Errorf("member %s /signin: %d", m, c)
		}
	}
	e.cj = saved

	// Reset: the environment applies again (no provider, passwords on)
	if c := e.do("DELETE", "/api/v1/signin", "", nil, &v); c != 200 || v.Enabled || !v.PasswordLogin || v.Source != "none" {
		t.Fatalf("reset %d %+v", c, v)
	}
}
