package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// tokenOnly calls the API with a bearer token and no browser cookie.
func (e *env) tokenOnly(method, path, tok string, body, out any) int {
	e.t.Helper()
	saved := e.cj
	e.cj = nil
	defer func() { e.cj = saved }()
	return e.do(method, path, tok, body, out)
}

// secEnv is an admin (signed in through e.cj) and a registered app with an OIDC client.
func secEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var reg struct {
		OIDC struct {
			ClientSecret string `json:"client_secret"`
		} `json:"oidc"`
	}
	m := map[string]any{"slug": "habits", "name": "Pocket Habits", "url": e.srv.URL, "oidc": map[string]any{"redirect_uris": []string{"http://rp/cb"}}}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, m, &reg); c != 201 {
		t.Fatalf("register %d", c)
	}
	return e, reg.OIDC.ClientSecret
}

// authorize runs /oidc/authorize with the admin's session and returns the code
// (or the error the redirect carried).
func (e *env) authorize(extra string) (code, oerr string) {
	e.t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", e.srv.URL+"/oidc/authorize?client_id=pocket-habits&redirect_uri=http://rp/cb&response_type=code&scope=openid%20email&state=s1"+extra, nil)
	for _, c := range e.cj {
		req.AddCookie(c)
	}
	res, err := cl.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	return loc.Query().Get("code"), loc.Query().Get("error")
}

func (e *env) token(secret string, form url.Values) (int, map[string]any) {
	req, _ := http.NewRequest("POST", e.srv.URL+"/oidc/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("pocket-habits", secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// An access token Core gave an app for signing someone in works for that
// person's own things but never as an admin of Core.
func TestOIDCAccessTokenIsNotAdmin(t *testing.T) {
	e, secret := secEnv(t)
	code, _ := e.authorize("")
	c, tok := e.token(secret, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://rp/cb"}})
	if c != 200 {
		t.Fatalf("token %d %v", c, tok)
	}
	at := tok["access_token"].(string)
	if c := e.tokenOnly("GET", "/api/v1/auth/me", at, nil, nil); c != 200 {
		t.Fatalf("me: %d", c)
	}
	for _, p := range []string{"/api/v1/users", "/api/v1/settings", "/api/v1/oidc/clients", "/api/v1/events", "/api/v1/services"} {
		if c := e.tokenOnly("GET", p, at, nil, nil); c != 403 {
			t.Fatalf("%s with an app's access token: %d, want 403", p, c)
		}
	}
	if c := e.tokenOnly("POST", "/api/v1/users", at, map[string]string{"email": "x@y.z", "password": "password123"}, nil); c != 403 {
		t.Fatalf("create user with access token: %d", c)
	}
	// A refresh token is not an access token.
	if c := e.tokenOnly("GET", "/api/v1/auth/me", tok["refresh_token"].(string), nil, nil); c != 401 {
		t.Fatalf("refresh token as bearer: %d", c)
	}
}

// A code and a refresh token each buy exactly one answer, even when the
// requests arrive at the same moment.
func TestOIDCCodeAndRefreshAreSingleUse(t *testing.T) {
	e, secret := secEnv(t)
	code, _ := e.authorize("")
	race := func(form url.Values) (ok int, last map[string]any) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, out := e.token(secret, form)
				mu.Lock()
				defer mu.Unlock()
				if c == 200 {
					ok++
					last = out
				}
			}()
		}
		wg.Wait()
		return ok, last
	}
	n, tok := race(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://rp/cb"}})
	if n != 1 {
		t.Fatalf("code exchanged %d times", n)
	}
	n, _ = race(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}})
	if n != 1 {
		t.Fatalf("refresh token used %d times", n)
	}
}

func TestPKCEPlainRefused(t *testing.T) {
	e, _ := secEnv(t)
	if _, oerr := e.authorize("&code_challenge=abcdefghijklmnopqrstuvwxyzabcdefghijklmnopq&code_challenge_method=plain"); oerr != "invalid_request" {
		t.Fatalf("plain PKCE: error %q", oerr)
	}
	if code, _ := e.authorize("&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256"); code == "" {
		t.Fatal("S256 PKCE refused")
	}
}

// A paired device manages the box only when an admin asked for that; a
// session it opens carries the same limit.
func TestDeviceManageFlag(t *testing.T) {
	e, _ := secEnv(t)
	claim := func(manage bool) (string, int64) {
		var pair struct {
			Code      string `json:"code"`
			CanManage bool   `json:"can_manage"`
		}
		if c := e.do("POST", "/api/v1/pair", "", map[string]any{"can_manage": manage}, &pair); c != 201 || pair.CanManage != manage {
			t.Fatalf("pair %d %+v", c, pair)
		}
		var prof Profile
		if c := e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": "Phone", "platform": "ios"}, &prof); c != 201 {
			t.Fatalf("claim %d", c)
		}
		var me struct {
			Device store.Device `json:"device"`
		}
		e.tokenOnly("GET", "/api/v1/auth/me", prof.Token, nil, &me)
		return prof.Token, me.Device.ID
	}
	hub, hubID := claim(false)
	coreApp, _ := claim(true)

	if c := e.tokenOnly("GET", "/api/v1/users", hub, nil, nil); c != 403 {
		t.Fatalf("plain device on admin route: %d", c)
	}
	if c := e.tokenOnly("GET", "/api/v1/me/services", hub, nil, nil); c != 200 {
		t.Fatalf("plain device on its own services: %d", c)
	}
	if c := e.tokenOnly("GET", "/api/v1/users", coreApp, nil, nil); c != 200 {
		t.Fatalf("managing device on admin route: %d", c)
	}
	// The plain device can't switch itself on; an admin can.
	if c := e.tokenOnly("PUT", "/api/v1/devices/"+itoa(hubID), hub, map[string]bool{"can_manage": true}, nil); c != 403 {
		t.Fatalf("device raising itself: %d", c)
	}

	// Its web session shows a member's view and is refused admin routes.
	var ws struct{ Session string }
	if c := e.tokenOnly("POST", "/api/v1/auth/device/web-session", hub, nil, &ws); c != 201 {
		t.Fatalf("web session %d", c)
	}
	withSession := func(path string, out any) int {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: ws.Session})
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			_ = json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}
	if c := withSession("/api/v1/users", nil); c != 403 {
		t.Fatalf("limited session on admin route: %d", c)
	}
	var st struct {
		User store.User `json:"user"`
	}
	withSession("/api/v1/auth/status", &st)
	if st.User.Role != "member" {
		t.Fatalf("limited session shows role %q", st.User.Role)
	}

	if c := e.do("PUT", "/api/v1/devices/"+itoa(hubID), "", map[string]bool{"can_manage": true}, nil); c != 200 {
		t.Fatalf("admin switching on: %d", c)
	}
	if c := e.tokenOnly("GET", "/api/v1/users", hub, nil, nil); c != 200 {
		t.Fatalf("after switching on: %d", c)
	}

	// A member can't ask for a managing device.
	if c := e.do("POST", "/api/v1/users", "", map[string]string{"email": "m@b.c", "password": "password123", "role": "member"}, nil); c != 201 {
		t.Fatalf("create member %d", c)
	}
	e.cj = nil
	if c := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": "m@b.c", "password": "password123"}, nil); c != 200 {
		t.Fatalf("member login %d", c)
	}
	if c := e.do("POST", "/api/v1/pair", "", map[string]any{"can_manage": true}, nil); c != 403 {
		t.Fatalf("member asking for a managing device: %d", c)
	}
}

// A pairing code makes one device, however many claims race for it.
func TestPairingClaimSingleUse(t *testing.T) {
	e, _ := secEnv(t)
	var pair struct{ Code string }
	e.do("POST", "/api/v1/pair", "", nil, &pair)
	var mu sync.Mutex
	var wg sync.WaitGroup
	ok := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Post(e.srv.URL+"/api/v1/pair/claim", "application/json", strings.NewReader(`{"code":"`+pair.Code+`","name":"Phone"}`))
			if err != nil {
				return
			}
			res.Body.Close()
			mu.Lock()
			if res.StatusCode == 201 {
				ok++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	var devs []store.Device
	e.do("GET", "/api/v1/devices", "", nil, &devs)
	if ok != 1 || len(devs) != 1 {
		t.Fatalf("claims ok=%d devices=%d", ok, len(devs))
	}
}

// X-Forwarded-For counts only from a trusted proxy, and then only the hops
// the proxies wrote.
func TestRealIPTrustsOnlyProxies(t *testing.T) {
	s := &Server{cfg: &config.Config{TrustProxy: true, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("127.0.0.0/8")}}}
	seen := ""
	https := false
	h := s.realIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen, https = clientIP(r), requestIsHTTPS(r) }))
	try := func(peer, xff string) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = peer
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		req.Header.Set("X-Forwarded-Proto", "https")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	try("203.0.113.9:5555", "1.2.3.4")
	if seen != "203.0.113.9" || https {
		t.Fatalf("direct caller: ip %s https %v", seen, https)
	}
	try("10.0.0.2:5555", "1.2.3.4, 198.51.100.7")
	if seen != "198.51.100.7" || !https {
		t.Fatalf("behind proxy: ip %s https %v", seen, https)
	}
	try("10.0.0.2:5555", "1.2.3.4, 198.51.100.7, 10.0.0.9")
	if seen != "198.51.100.7" {
		t.Fatalf("two proxies: ip %s", seen)
	}
	s.cfg.TrustProxy = false
	try("10.0.0.2:5555", "198.51.100.7")
	if seen != "10.0.0.2" || https {
		t.Fatalf("TRUST_PROXY=false: ip %s https %v", seen, https)
	}
}

func TestSecureCookiesFollowBaseURL(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("BASE_URL", "https://core.example")
	if c, err := config.Load(); err != nil || !c.SecureCookies {
		t.Fatalf("https BASE_URL: secure=%v err=%v", c != nil && c.SecureCookies, err)
	}
	t.Setenv("SECURE_COOKIES", "false")
	if c, _ := config.Load(); c.SecureCookies {
		t.Fatal("SECURE_COOKIES=false ignored")
	}
}
