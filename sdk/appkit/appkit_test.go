package appkit

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// fakeIdP is a tiny OpenID provider: it signs whoever `email` says.
type fakeIdP struct {
	*httptest.Server
	key, forger *rsa.PrivateKey
	email       string
	verified    bool
	forge       bool
	nonces      map[string]string
	challenges  map[string]string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeIdP{key: k, forger: f, verified: true, nonces: map[string]string{}, challenges: map[string]string{}}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	b64 := base64.RawURLEncoding.EncodeToString
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token", "jwks_uri": p.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "kid": "k1", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "app" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad request", 400)
			return
		}
		code := RandomToken(8)
		p.nonces[code], p.challenges[code] = q.Get("nonce"), q.Get("code_challenge")
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		code := r.FormValue("code")
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if id != "app" || secret != "shh" || p.challenges[code] != b64(sum[:]) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
		body, _ := json.Marshal(map[string]any{"iss": p.URL, "sub": strings.ToLower(p.email), "auth_revision": int64(1), "aud": "app", "exp": time.Now().Add(time.Hour).Unix(), "nonce": p.nonces[code], "email": p.email, "email_verified": p.verified, "name": "Aleksandar"})
		signing := b64(head) + "." + b64(body)
		h := sha256.Sum256([]byte(signing))
		signer := p.key
		if p.forge {
			signer = p.forger
		}
		sig, _ := rsa.SignPKCS1v15(rand.Reader, signer, crypto.SHA256, h[:])
		json.NewEncoder(w).Encode(map[string]string{"id_token": signing + "." + b64(sig), "access_token": "at", "token_type": "Bearer"})
	})
	t.Cleanup(p.Close)
	return p
}

func TestRPSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	accounts := map[string]bool{"aleksandar@example.com": true}
	sessions := map[string]string{}
	settings := OIDCSettings{Issuer: idp.URL, ClientID: "app", Secret: "shh", Name: "PocketID"}
	ts := httptest.NewUnstartedServer(nil)
	cookie := SessionCookie{Name: "t_session"}
	rp := &RP{
		Settings:    func() OIDCSettings { return settings },
		CallbackURL: func() string { return "http://" + ts.Listener.Addr().String() + "/api/v1/auth/oidc/callback" },
		Login: func(ctx context.Context, id Identity, autoCreate bool) (string, error) {
			if !accounts[id.Email] && !autoCreate {
				return "", errors.New("no such account")
			}
			if id.Email == "blocked@example.com" {
				return "", &LoginError{Message: "That account is blocked"}
			}
			accounts[id.Email] = true
			s := RandomToken(8)
			sessions[s] = id.Email
			return s, nil
		},
		SetSession: func(w http.ResponseWriter, s string) { cookie.Set(w, s, time.Hour) },
		AppName:    "Pocket Test",
		Cookie:     "t_oidc",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/auth/oidc/start", rp.Start)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", rp.Callback)
	mux.HandleFunc("GET /api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("t_session")
		if err != nil || sessions[c.Value] == "" {
			WriteErr(w, 401, "unauthorized")
			return
		}
		WriteJSON(w, 200, map[string]string{"email": sessions[c.Value]})
	})
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)

	signIn := func(next string) (*url.URL, *http.Client) {
		jar, _ := cookiejar.New(nil)
		var last *url.URL
		cl := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			last = req.URL
			if strings.HasPrefix(req.URL.String(), ts.URL) && !strings.HasPrefix(req.URL.Path, "/api/") {
				return http.ErrUseLastResponse
			}
			return nil
		}}
		res, err := cl.Get(ts.URL + "/api/v1/auth/oidc/start?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return last, cl
	}
	me := func(cl *http.Client) int {
		res, err := cl.Get(ts.URL + "/api/v1/auth/me")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	idp.email = "Aleksandar@Example.com"
	if end, cl := signIn("/lists/1"); end == nil || end.Path != "/lists/1" || me(cl) != 200 {
		t.Fatalf("happy path ended at %v", end)
	}
	if !rp.Enabled() || rp.Name() != "PocketID" {
		t.Errorf("enabled=%v name=%q", rp.Enabled(), rp.Name())
	}
	for _, c := range []struct {
		name, email     string
		verified, forge bool
		want            string
	}{
		{"unknown email", "stranger@example.com", true, false, "No Pocket Test account for stranger@example.com"},
		{"unverified email", "aleksandar@example.com", false, false, "verified email"},
		{"forged signature", "aleksandar@example.com", true, true, "did not check out"},
	} {
		idp.email, idp.verified, idp.forge = c.email, c.verified, c.forge
		end, cl := signIn("/")
		if end == nil || end.Path != "/login" || !strings.Contains(end.Query().Get("error"), c.want) || me(cl) != 401 {
			t.Errorf("%s: ended at %v", c.name, end)
		}
	}
	// a LoginError's sentence is shown as is
	settings.AutoCreate = true
	idp.email, idp.verified, idp.forge = "blocked@example.com", true, false
	if end, _ := signIn("/"); end == nil || end.Query().Get("error") != "That account is blocked" {
		t.Errorf("login error: %v", end)
	}
	// AutoCreate reaches Login
	idp.email = "stranger@example.com"
	if end, cl := signIn("/"); end == nil || end.Path != "/" || me(cl) != 200 {
		t.Errorf("auto-create: ended at %v", end)
	}
	// the return path cannot leave the site
	idp.email = "aleksandar@example.com"
	if end, _ := signIn("//evil.example/x"); end == nil || end.Host != strings.TrimPrefix(ts.URL, "http://") || end.Path != "/" {
		t.Errorf("open redirect: %v", end)
	}
	// a callback without the browser's cookie is refused
	res, _ := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(ts.URL + "/api/v1/auth/oidc/callback?code=x&state=y")
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "/login?error=") {
		t.Errorf("cookieless callback → %q", loc)
	}
	res.Body.Close()
	// switched off: start is a 404
	settings = OIDCSettings{}
	res, _ = http.Get(ts.URL + "/api/v1/auth/oidc/start")
	res.Body.Close()
	if res.StatusCode != 404 || rp.Enabled() {
		t.Errorf("disabled start: %d", res.StatusCode)
	}
}

func TestEnvOrCore(t *testing.T) {
	if s := EnvOrCore(nil, "", "", "", "", false); s.Issuer != "" {
		t.Errorf("nothing configured: %+v", s)
	}
	if s := EnvOrCore(nil, "http://idp", "me", "x", "PocketID", false); s.Issuer != "http://idp" || s.Name != "PocketID" || s.AutoCreate {
		t.Errorf("env: %+v", s)
	}
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/core.token.oidc.json", []byte(`{"issuer":"http://core.invalid/","client_id":"pco_x","client_secret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := core.New("http://core.invalid", core.Options{TokenFile: dir + "/core.token"})
	if s := EnvOrCore(c, "", "", "", "", false); s.ClientID != "pco_x" || s.Issuer != "http://core.invalid" || !s.AutoCreate || s.Name != "Pocket Core" {
		t.Errorf("core client: %+v", s)
	}
	if s := EnvOrCoreAt(c, "", "", "", "", "", false); s.ClientID != "" {
		t.Errorf("core client without a base URL: %+v", s)
	}
	if s := EnvOrCoreAt(c, "http://app", "", "", "", "", false); s.ClientID != "pco_x" {
		t.Errorf("core client with a base URL: %+v", s)
	}
	if s := EnvOrCoreAt(c, "", "http://idp", "me", "", "PocketID", true); s.ClientID != "me" || !s.AutoCreate {
		t.Errorf("env without a base URL: %+v", s)
	}
}

type user struct{ Email string }

func TestAuthOrder(t *testing.T) {
	a := &Auth[*user]{
		SessionCookie: "s",
		APIToken: func(ctx context.Context, tok string) (*user, error) {
			if tok == "good" {
				return &user{"token@x"}, nil
			}
			return nil, errors.New("bad token")
		},
		Session: func(ctx context.Context, id string) (*user, error) {
			if id == "sess" {
				return &user{"session@x"}, nil
			}
			return nil, errors.New("no session")
		},
		Person: func(ctx context.Context, email, name string) (*user, error) { return &user{email}, nil },
	}
	req := func(set func(r *http.Request)) *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/x", nil)
		set(r)
		return r
	}
	cases := []struct {
		name string
		mode string
		r    *http.Request
		want string
	}{
		{"bearer", "local", req(func(r *http.Request) { r.Header.Set("Authorization", "Bearer good") }), "token@x"},
		{"x-api-key", "local", req(func(r *http.Request) { r.Header.Set("X-API-Key", "good") }), "token@x"},
		{"bad bearer beats cookie", "local", req(func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer nope")
			r.AddCookie(&http.Cookie{Name: "s", Value: "sess"})
		}), ""},
		{"session", "local", req(func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "s", Value: "sess"}) }), "session@x"},
		{"nothing", "local", req(func(r *http.Request) {}), ""},
		{"none mode", "none", req(func(r *http.Request) {}), "admin@localhost"},
		{"proxy name only", "proxy", req(func(r *http.Request) { r.Header.Set("Remote-User", "alex") }), "alex@proxy"},
		{"proxy email", "proxy", req(func(r *http.Request) { r.Header.Set("Remote-User", "alex"); r.Header.Set("Remote-Email", "A@X.org") }), "a@x.org"},
		{"proxy without header", "proxy", req(func(r *http.Request) {}), ""},
		{"link token without core is an own token", "local", req(func(r *http.Request) { r.Header.Set("Authorization", "Bearer pcx_abc") }), ""},
	}
	for _, c := range cases {
		mode := c.mode
		a.Mode = func() string { return mode }
		u, _, err := a.Resolve(c.r)
		got := ""
		if err == nil && u != nil {
			got = u.Email
		}
		if got != c.want {
			t.Errorf("%s: got %q (%v), want %q", c.name, got, err, c.want)
		}
	}

	// Fallback runs only without a bearer token
	a.Mode = func() string { return "local" }
	a.Fallback = func(r *http.Request) (*user, bool, error) {
		if _, _, ok := r.BasicAuth(); ok {
			return &user{"basic@x"}, true, nil
		}
		return nil, false, nil
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("u", "p")
	if u, _, _ := a.Resolve(r); u == nil || u.Email != "basic@x" {
		t.Errorf("fallback: %v", u)
	}

	// Require: 401 for nothing, the handler sees the person
	h := a.Require(func(r *http.Request, u *user, via Via) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), ctxUser{}, u))
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 200, r.Context().Value(ctxUser{}).(*user))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 401 {
		t.Errorf("require without credentials: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer good")
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "token@x") {
		t.Errorf("require with token: %d %s", rec.Code, rec.Body)
	}
}

type ctxUser struct{}

// fakeCore answers introspection like Pocket Core does.
func fakeCore(t *testing.T, answers map[string]map[string]any) *core.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if a, ok := answers[in.Token]; ok {
			_ = json.NewEncoder(w).Encode(a)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
	}))
	t.Cleanup(srv.Close)
	return core.New(srv.URL, core.Options{})
}

func TestAuthLinkTokens(t *testing.T) {
	person := map[string]any{"id": 1, "sub": "1", "email": "p@x", "name": "P"}
	c := fakeCore(t, map[string]map[string]any{
		"pcx_read":  {"active": true, "kind": "delegated", "audience": "things", "scope": "things:read", "user": person, "app": map[string]any{"slug": "hub", "name": "Pocket Hub"}},
		"pcx_other": {"active": true, "kind": "delegated", "audience": "money", "scope": "things:read", "user": person, "app": map[string]any{"slug": "hub", "name": "Pocket Hub"}},
		"pcx_noapp": {"active": true, "kind": "delegated", "scope": "things:read", "user": person},
		"pcd_phone": {"active": true, "kind": "device", "user": person},
		"pcd_deleg": {"active": true, "kind": "delegated", "user": person},
	})
	routes := Routes{
		R("GET", `^/api/v1/auth/me$`, ""),
		R("GET", `^/api/v1/things$`, "things:read"),
		R("POST", `^/api/v1/things$`, "things:write"),
	}
	a := &Auth[*user]{
		Core:     c,
		Slug:     "things",
		APIToken: func(ctx context.Context, tok string) (*user, error) { return nil, errors.New("not ours") },
		Session:  func(ctx context.Context, id string) (*user, error) { return nil, errors.New("no") },
		Person:   func(ctx context.Context, e, n string) (*user, error) { return &user{e}, nil },
		Identity: func(ctx context.Context, in *core.Introspection) (*user, error) { return &user{in.User.Email}, nil },
		Allow:    routes.Allow(nil),
	}
	try := func(method, path, tok string) (*user, Via, error) {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.Resolve(r)
	}
	if u, via, err := try("GET", "/api/v1/things", "pcx_read"); err != nil || u.Email != "p@x" || via.Slug != "hub" || via.Name != "Pocket Hub" {
		t.Errorf("in scope: %v %v %v", u, via, err)
	}
	if _, _, err := try("GET", "/api/v1/auth/me", "pcx_read"); err != nil {
		t.Errorf("any-link route: %v", err)
	}
	if _, _, err := try("POST", "/api/v1/things", "pcx_read"); !errors.Is(err, ErrLinkScope) {
		t.Errorf("out of scope: %v", err)
	}
	if _, _, err := try("GET", "/api/v1/other", "pcx_read"); !errors.Is(err, ErrLinkScope) {
		t.Errorf("unlisted route: %v", err)
	}
	if _, _, err := try("GET", "/api/v1/things", "pcx_other"); err == nil {
		t.Errorf("a link for another app was accepted")
	}
	if _, _, err := try("GET", "/api/v1/things", "pcx_noapp"); err == nil {
		t.Errorf("a link without an acting app was accepted")
	}
	if _, _, err := try("GET", "/api/v1/things", "pcx_unknown"); err == nil {
		t.Errorf("inactive link accepted")
	}
	if u, via, err := try("GET", "/api/v1/other", "pcd_phone"); err != nil || u.Email != "p@x" || via.Slug != "" {
		t.Errorf("device token: %v %v %v", u, via, err)
	}
	if _, _, err := try("GET", "/api/v1/things", "pcd_deleg"); err == nil {
		t.Errorf("a delegated answer for a pcd_ token was accepted outside the route check")
	}
	// Require answers 403 for a link outside its scopes
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/things", nil)
	r.Header.Set("Authorization", "Bearer pcx_read")
	a.Require(func(r *http.Request, u *user, v Via) *http.Request { return r })(http.NotFoundHandler()).ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Errorf("out-of-scope link: %d", rec.Code)
	}
}

type allowAll struct{}

func (allowAll) LinkAllows([]string, *http.Request) bool { return true }

func TestRoutes(t *testing.T) {
	rs := Routes{R("PUT DELETE", `^/api/v1/x/\d+$`, "x:write")}
	r := httptest.NewRequest("DELETE", "/api/v1/x/12", nil)
	if !rs.Allows([]string{"x:write"}, r) || rs.Allows([]string{"x:read"}, r) {
		t.Error("scope check")
	}
	if rs.Allows([]string{"x:write"}, httptest.NewRequest("POST", "/api/v1/x/12", nil)) {
		t.Error("method check")
	}
	var nilCaps *core.Capabilities
	if !rs.Allow(nilCaps, allowAll{})(nil, httptest.NewRequest("GET", "/anything", nil)) {
		t.Error("contracts are asked")
	}
	if rs.Allow(nilCaps)(nil, httptest.NewRequest("GET", "/anything", nil)) {
		t.Error("a nil contract allowed something")
	}
}

func TestConnModeAndReasons(t *testing.T) {
	c := core.New("http://core.invalid", core.Options{})
	for _, x := range []struct {
		cl           *core.Client
		mode, secret string
		want         string
	}{
		{c, "off", "tok", "off"}, {c, "", "tok", "direct"}, {c, "", "", "core"}, {nil, "", "", "direct"}, {nil, "core", "", "core"},
	} {
		if got := ConnMode(x.cl, x.mode, x.secret); got != x.want {
			t.Errorf("ConnMode(%v,%q,%q)=%q want %q", x.cl != nil, x.mode, x.secret, got, x.want)
		}
	}
	if !strings.Contains(LinkReason("Still", "Pocket Habits", "off_admin"), "Still → Pocket Habits") || LinkReason("a", "b", "???") != "" {
		t.Error("LinkReason")
	}
	for in, want := range map[string]string{"/x": "/x", "//evil": "/", "/\\evil": "/", "/api/v1/x": "/", "http://evil": "/", "": "/"} {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q)=%q", in, got)
		}
	}
}
