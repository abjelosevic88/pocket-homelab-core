package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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

// RFC 7636's example pair.
const pkceVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
const pkceS256 = "&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256"

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
	code, _ := e.authorize(pkceS256)
	c, tok := e.token(secret, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://rp/cb"}, "code_verifier": {pkceVerifier}})
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
	code, _ := e.authorize(pkceS256)
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
	n, tok := race(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://rp/cb"}, "code_verifier": {pkceVerifier}})
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
	if code, _ := e.authorize(pkceS256); code == "" {
		t.Fatal("S256 PKCE refused")
	}
	// A Pocket app's client must send PKCE at all.
	if _, oerr := e.authorize(""); oerr != "invalid_request" {
		t.Fatalf("Pocket app client without PKCE: error %q", oerr)
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

// No limited credential can trade itself for one without the limit: not a
// personal token, a pairing code, a remote-access pairing, an AI connection or
// a password to sign in with afresh.
func TestLimitedCannotMintCredentials(t *testing.T) {
	e, secret := secEnv(t)
	code, _ := e.authorize(pkceS256)
	_, tok := e.token(secret, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://rp/cb"}, "code_verifier": {pkceVerifier}})
	oidcTok := tok["access_token"].(string)

	var pair struct{ Code string }
	e.do("POST", "/api/v1/pair", "", nil, &pair)
	var prof Profile
	e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": "Hub"}, &prof)
	var ws struct{ Session string }
	e.tokenOnly("POST", "/api/v1/auth/device/web-session", prof.Token, nil, &ws)

	asSession := func(method, path string, body any) int {
		saved := e.cj
		e.cj = []*http.Cookie{{Name: sessionCookie, Value: ws.Session}}
		defer func() { e.cj = saved }()
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(mustJSONString(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", e.srv.URL)
		req.AddCookie(e.cj[0])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	mint := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/tokens", map[string]string{"name": "escape"}},
		{"POST", "/api/v1/pair", map[string]any{}},
		{"POST", "/api/v1/me/remote/pair", map[string]any{}},
		{"POST", "/api/v1/me/ai-connections/token", map[string]any{}},
		{"PUT", "/api/v1/auth/me", map[string]string{"password": "newpassword123", "current_password": "password123"}},
	}
	for _, m := range mint {
		for name, tok := range map[string]string{"oidc": oidcTok, "device": prof.Token} {
			if c := e.tokenOnly(m.method, m.path, tok, m.body, nil); c != 403 {
				t.Errorf("%s %s with a limited %s token: %d, want 403", m.method, m.path, name, c)
			}
		}
		if c := asSession(m.method, m.path, m.body); c != 403 && c != 401 {
			t.Errorf("%s %s with a device's web session: %d, want 403", m.method, m.path, c)
		}
	}
	// The admin's own browser still can.
	if c := e.do("POST", "/api/v1/tokens", "", map[string]string{"name": "mine"}, nil); c != 201 {
		t.Fatalf("admin session creating a token: %d", c)
	}
}

func mustJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// AUTH_MODE=proxy takes the identity header only from TRUSTED_PROXIES.
func TestProxyAuthNeedsTrustedPeer(t *testing.T) {
	s := &Server{cfg: &config.Config{AuthMode: "proxy", AuthProxyHeader: "Remote-User", AuthProxyEmailHeader: "Remote-Email",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	var trusted bool
	h := s.realIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { trusted = fromTrustedPeer(r) }))
	for peer, want := range map[string]bool{"203.0.113.9:1": false, "10.1.2.3:1": true} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = peer
		req.Header.Set("Remote-User", "admin")
		h.ServeHTTP(httptest.NewRecorder(), req)
		if trusted != want {
			t.Fatalf("peer %s trusted=%v", peer, trusted)
		}
	}
}

func TestProxyAuthEndToEnd(t *testing.T) {
	t.Setenv("AUTH_MODE", "proxy")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8") // the test client is 127.0.0.1: not a proxy
	e := newEnv(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Remote-User", "admin")
	req.Header.Set("Remote-Email", "admin@b.c")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("identity header from an untrusted peer: %d", res.StatusCode)
	}
}

// Personal tokens: member-level unless made to manage the box (which asks for
// the password again), and gone once expired.
func TestPersonalTokenLimits(t *testing.T) {
	e, _ := secEnv(t)
	var plain struct{ Secret string }
	if c := e.do("POST", "/api/v1/tokens", "", map[string]any{"name": "raycast", "expires_days": 30}, &plain); c != 201 {
		t.Fatalf("create %d", c)
	}
	if c := e.tokenOnly("GET", "/api/v1/users", plain.Secret, nil, nil); c != 403 {
		t.Fatalf("plain personal token on admin route: %d", c)
	}
	if c := e.tokenOnly("GET", "/api/v1/me/services", plain.Secret, nil, nil); c != 200 {
		t.Fatalf("plain personal token on own services: %d", c)
	}
	if c := e.do("POST", "/api/v1/tokens", "", map[string]any{"name": "ops", "can_manage": true, "current_password": "wrong-one"}, nil); c != 403 {
		t.Fatalf("managing token with a wrong password: %d", c)
	}
	var ops struct{ Secret string }
	if c := e.do("POST", "/api/v1/tokens", "", map[string]any{"name": "ops", "can_manage": true, "current_password": "password123"}, &ops); c != 201 {
		t.Fatalf("managing token: %d", c)
	}
	if c := e.tokenOnly("GET", "/api/v1/users", ops.Secret, nil, nil); c != 200 {
		t.Fatalf("managing token on admin route: %d", c)
	}
	// Expired: unknown.
	u, _ := e.st.GetUserByEmail(context.Background(), "a@b.c")
	_, old, _ := e.st.CreateAPITokenWith(context.Background(), u.ID, "old", true, time.Nanosecond)
	time.Sleep(5 * time.Millisecond)
	if c := e.tokenOnly("GET", "/api/v1/auth/me", old, nil, nil); c != 401 {
		t.Fatalf("expired token: %d", c)
	}
}

// A sign-in token Core gave other software (a hand-made or connector client)
// is good for /oidc/userinfo only: not Core's API, not Pocket apps. A Pocket
// app's sign-in token can't fetch the device profile (service credentials).
func TestForeignOIDCTokenAudience(t *testing.T) {
	e, secret := secEnv(t)
	var made struct {
		Client       store.OIDCClient `json:"client"`
		ClientSecret string           `json:"client_secret"`
	}
	if c := e.do("POST", "/api/v1/oidc/clients", "", map[string]any{"client_id": "jellyfin", "name": "Jellyfin", "redirect_uris": []string{"http://jf/cb"}}, &made); c != 201 {
		t.Fatalf("make client %d", c)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", e.srv.URL+"/oidc/authorize?client_id=jellyfin&redirect_uri=http://jf/cb&response_type=code&scope=openid%20email&state=s", nil)
	for _, c := range e.cj {
		req.AddCookie(c)
	}
	res, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {"http://jf/cb"}}
	treq, _ := http.NewRequest("POST", e.srv.URL+"/oidc/token", strings.NewReader(form.Encode()))
	treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	treq.SetBasicAuth("jellyfin", made.ClientSecret)
	tres, err := http.DefaultClient.Do(treq)
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(tres.Body).Decode(&tok)
	tres.Body.Close()
	jf, _ := tok["access_token"].(string)
	if jf == "" {
		t.Fatalf("token %d %v", tres.StatusCode, tok)
	}
	if c := e.tokenOnly("GET", "/oidc/userinfo", jf, nil, nil); c != 200 {
		t.Fatalf("userinfo: %d", c)
	}
	for _, p := range []string{"/api/v1/auth/me", "/api/v1/pair/profile", "/api/v1/me/services"} {
		if c := e.tokenOnly("GET", p, jf, nil, nil); c != 401 {
			t.Fatalf("%s with Jellyfin's token: %d", p, c)
		}
	}
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var reg struct{ Token string }
	e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "notes", "name": "Pocket Notes", "url": e.srv.URL}, &reg)
	var intro map[string]any
	e.tokenOnly("POST", "/api/v1/auth/introspect", reg.Token, map[string]string{"token": jf}, &intro)
	if intro["active"] != false {
		t.Fatalf("a Pocket app accepted Jellyfin's token: %v", intro)
	}

	// A Pocket app's own sign-in token: Core API yes, credentials profile no.
	code, _ := e.authorize(pkceS256)
	_, ptok := e.token(secret, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://rp/cb"}, "code_verifier": {pkceVerifier}})
	at := ptok["access_token"].(string)
	if c := e.tokenOnly("GET", "/api/v1/pair/profile", at, nil, nil); c != 403 {
		t.Fatalf("profile with an app's sign-in token: %d", c)
	}
	e.tokenOnly("POST", "/api/v1/auth/introspect", reg.Token, map[string]string{"token": at}, &intro)
	if intro["active"] != true {
		t.Fatalf("Pocket app token not accepted by a Pocket app: %v", intro)
	}
}
