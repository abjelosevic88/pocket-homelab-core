package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	pccrypto "github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// fakeUpstream is a tiny OpenID provider standing in for PocketID.
type fakeUpstream struct {
	*httptest.Server
	key, forger *rsa.PrivateKey
	email       string
	forge       bool
	nonces      map[string]string
	challenges  map[string]string
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeUpstream{key: k, forger: f, nonces: map[string]string{}, challenges: map[string]string{}}
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
		code := pccrypto.Token("", 8)
		p.nonces[code], p.challenges[code] = q.Get("nonce"), q.Get("code_challenge")
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		code := r.FormValue("code")
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if id != "core" || secret != "shh" || p.challenges[code] != b64(sum[:]) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
		body, _ := json.Marshal(map[string]any{"iss": p.URL, "sub": "u1", "aud": "core", "exp": time.Now().Add(time.Hour).Unix(), "nonce": p.nonces[code], "email": p.email, "email_verified": true})
		signing := b64(head) + "." + b64(body)
		h := sha256.Sum256([]byte(signing))
		signer := p.key
		if p.forge {
			signer = p.forger
		}
		sig, _ := rsa.SignPKCS1v15(rand.Reader, signer, crypto.SHA256, h[:])
		json.NewEncoder(w).Encode(map[string]string{"id_token": signing + "." + b64(sig), "access_token": "at"})
	})
	t.Cleanup(p.Close)
	return p
}

func TestSSOThroughUpstream(t *testing.T) {
	up := newFakeUpstream(t)
	dir := t.TempDir()
	ts := httptest.NewUnstartedServer(nil)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("BASE_URL", "http://"+ts.Listener.Addr().String())
	t.Setenv("SSO_ISSUER", up.URL)
	t.Setenv("SSO_CLIENT_ID", "core")
	t.Setenv("SSO_CLIENT_SECRET", "shh")
	t.Setenv("SSO_NAME", "PocketID")
	t.Setenv("PASSWORD_LOGIN", "false")
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

	// first admin (setup is not a password sign-in) and an app with an OIDC client
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123", "name": "A", "box_name": "Box"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var reg struct {
		OIDC struct {
			ClientID string `json:"client_id"`
		} `json:"oidc"`
	}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "money", "name": "Pocket Money", "url": ts.URL, "oidc": map[string]any{"redirect_uris": []string{"http://rp/cb"}}}, &reg); c != 201 {
		t.Fatalf("register %d", c)
	}
	off := &store.User{Email: "off@b.c", Name: "Off", Role: "member"}
	_ = st.CreateUser(ctx, off)
	off.Disabled = true // disabling is an update, like Users → Disable
	if err := st.UpdateUser(ctx, off); err != nil {
		t.Fatal(err)
	}

	var status map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &status)
	if status["sso"] != true || status["password_login"] != false || status["sso_name"] != "PocketID" {
		t.Errorf("status %v", status)
	}
	if c := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil); c != 403 {
		t.Errorf("password login with PASSWORD_LOGIN=false: %d", c)
	}

	// An app sends a browser with no Core session to /oidc/authorize. Core
	// asks it to sign in; the PocketID button goes upstream and the round-trip
	// ends with the app's code.
	run := func(email string, forge bool) *url.URL {
		up.email, up.forge = email, forge
		jar, _ := cookiejar.New(nil)
		var last *url.URL
		cl := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			last = req.URL
			if req.URL.Host == "rp" || req.URL.Path == "/login" {
				return http.ErrUseLastResponse
			}
			return nil
		}}
		authz := ts.URL + "/oidc/authorize?client_id=" + reg.OIDC.ClientID + "&redirect_uri=http://rp/cb&response_type=code&scope=openid%20email&state=s1&nonce=n1"
		res, err := cl.Get(authz)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if last == nil || last.Path != "/login" {
			t.Fatalf("authorize without session went to %v", last)
		}
		next := last.Query().Get("next") // what the login page's button passes on
		res, err = cl.Get(ts.URL + "/api/v1/auth/sso/start?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return last
	}
	end := run("A@B.c", false)
	if end.Host != "rp" || end.Query().Get("code") == "" || end.Query().Get("state") != "s1" {
		t.Fatalf("chain ended at %v", end)
	}
	for _, c := range []struct {
		name, email string
		forge       bool
		want        string
	}{
		{"unknown email", "stranger@b.c", false, "No account on this box"},
		{"forged signature", "a@b.c", true, "did not check out"},
		{"disabled account", "off@b.c", false, "disabled"},
	} {
		end := run(c.email, c.forge)
		if end.Path != "/login" || !strings.Contains(end.Query().Get("error"), c.want) {
			t.Errorf("%s: ended at %v", c.name, end)
		}
	}
}
