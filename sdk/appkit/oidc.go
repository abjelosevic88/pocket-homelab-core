package appkit

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Sign-in with an OpenID Connect provider (Pocket Core on a box, or any
// provider set through OIDC_*): authorization code flow with PKCE and the
// client secret, the ID token's RS256 signature checked against the
// provider's published keys. People are matched by verified email; API
// tokens are untouched.
//
// Without Core and without OIDC_* nothing here is reachable: Enabled is
// false, Start answers 404 and Callback sends the browser to the login page.
// No request leaves the box.

// OIDCSettings is the relying-party client in use.
type OIDCSettings struct {
	Issuer, ClientID, Secret string
	// Name is what the login button and the error messages call the provider.
	Name string
	// AutoCreate gives an unknown verified email an account.
	AutoCreate bool
}

// EnvOrCore is the usual source of OIDCSettings: explicit OIDC_* values win
// (with OIDC_AUTO_CREATE), else the client Core issued when the app
// registered, which always creates accounts because Core vouched for the
// person.
func EnvOrCore(c *core.Client, issuer, clientID, secret, name string, autoCreate bool) OIDCSettings {
	if issuer != "" && clientID != "" {
		return OIDCSettings{Issuer: issuer, ClientID: clientID, Secret: secret, Name: name, AutoCreate: autoCreate}
	}
	if c.Configured() {
		if o := c.OIDC(); o.Issuer != "" && o.ClientID != "" {
			return OIDCSettings{Issuer: strings.TrimRight(o.Issuer, "/"), ClientID: o.ClientID, Secret: o.ClientSecret, Name: "Pocket Core", AutoCreate: true}
		}
	}
	return OIDCSettings{}
}

// EnvOrCoreAt is EnvOrCore for apps that only use Core's client when they
// know their own public address: Core registered the redirect URI from
// BASE_URL, so without one its client cannot work. Explicit OIDC_* still win.
func EnvOrCoreAt(c *core.Client, baseURL, issuer, clientID, secret, name string, autoCreate bool) OIDCSettings {
	if (issuer == "" || clientID == "") && baseURL == "" {
		return OIDCSettings{}
	}
	return EnvOrCore(c, issuer, clientID, secret, name, autoCreate)
}

// Identity is the person the provider vouched for.
type Identity struct {
	Issuer, Subject string
	// Email is lower-cased and verified.
	Email, Name  string
	AuthRevision int64
}

// LoginError carries the sentence the person sees on the login page.
type LoginError struct {
	Message string
	Err     error
}

func (e *LoginError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}
func (e *LoginError) Unwrap() error { return e.Err }

// RP is an app's relying party. Settings, CallbackURL, Login and SetSession
// are required; the rest have defaults.
type RP struct {
	// Settings answers the client in use; called on every request, so a
	// client Core issues after start is picked up.
	Settings func() OIDCSettings
	// CallbackURL is the absolute redirect URI (BaseURL + CallbackPath).
	CallbackURL func() string
	// Login finds or creates the account for the identity (autoCreate says
	// whether a new one may be made) and starts a session, answering its id.
	// A *LoginError's Message is shown as is.
	Login func(ctx context.Context, id Identity, autoCreate bool) (session string, err error)
	// EndSession removes a session Login started when Core cannot track it.
	EndSession func(ctx context.Context, session string)
	// SetSession sets the app's session cookie.
	SetSession func(w http.ResponseWriter, session string)
	// Core tracks the session for revocation when configured.
	Core *core.Client
	// Cookie holds state, nonce, verifier and the return path for 10 minutes
	// (default "pocket_oidc"); Path is where it is sent (default
	// "/api/v1/auth/oidc").
	Cookie, CookiePath string
	Secure             bool
	// LoginPath receives ?error= (default "/login").
	LoginPath string
	// AppName ("Pocket Still") is used in "No Pocket Still account for …".
	AppName string
	Log     *slog.Logger
	// HTTP is the client for the provider (default 15 s timeout).
	HTTP *http.Client

	mu   sync.Mutex
	prov *provider
}

func (rp *RP) cookie() string     { return firstNonEmpty(rp.Cookie, "pocket_oidc") }
func (rp *RP) cookiePath() string { return firstNonEmpty(rp.CookiePath, "/api/v1/auth/oidc") }
func (rp *RP) loginPath() string  { return firstNonEmpty(rp.LoginPath, "/login") }
func (rp *RP) log() *slog.Logger {
	if rp.Log != nil {
		return rp.Log
	}
	return slog.Default()
}
func (rp *RP) http() *http.Client {
	if rp.HTTP != nil {
		return rp.HTTP
	}
	return defaultHTTP
}

var defaultHTTP = &http.Client{Timeout: 15 * time.Second}

// Enabled reports whether a client is configured.
func (rp *RP) Enabled() bool {
	s := rp.Settings()
	return s.Issuer != "" && s.ClientID != ""
}

// Name is the provider's name for the login button ("" when disabled).
func (rp *RP) Name() string { return rp.Settings().Name }

// provider returns the cached provider for the issuer in use, starting a
// fresh one when the issuer changed (Core's client arrives after start).
func (rp *RP) provider(issuer string) *provider {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if rp.prov == nil || rp.prov.issuer != issuer {
		rp.prov = &provider{issuer: issuer, http: rp.http()}
	}
	return rp.prov
}

// Start sends the browser to the provider (GET …/oidc/start?next=/path).
func (rp *RP) Start(w http.ResponseWriter, r *http.Request) {
	s := rp.Settings()
	if s.Issuer == "" || s.ClientID == "" {
		WriteErr(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	d, err := rp.provider(s.Issuer).discovery(r.Context())
	if err != nil {
		rp.log().Warn("oidc discovery", "err", err)
		http.Redirect(w, r, rp.loginPath()+"?error="+url.QueryEscape("Cannot reach "+s.Name+" right now"), http.StatusFound)
		return
	}
	state, nonce, verifier := RandomToken(16), RandomToken(16), RandomToken(32)
	next := SafeNext(r.URL.Query().Get("next"))
	http.SetCookie(w, &http.Cookie{
		Name: rp.cookie(), Value: strings.Join([]string{state, nonce, verifier, base64.RawURLEncoding.EncodeToString([]byte(next))}, "."),
		Path: rp.cookiePath(), HttpOnly: true, Secure: rp.Secure, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	q := url.Values{
		"response_type": {"code"}, "client_id": {s.ClientID}, "redirect_uri": {rp.CallbackURL()},
		"scope": {"openid email profile"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {PKCEChallenge(verifier)}, "code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(d.Auth, "?") {
		sep = "&"
	}
	http.Redirect(w, r, d.Auth+sep+q.Encode(), http.StatusFound)
}

// Callback finishes the sign-in and starts a normal session.
func (rp *RP) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fail := func(msg string, err error) {
		if err != nil {
			rp.log().Warn("oidc sign-in failed", "msg", msg, "err", err)
		}
		http.Redirect(w, r, rp.loginPath()+"?error="+url.QueryEscape(msg), http.StatusFound)
	}
	s := rp.Settings()
	if s.Issuer == "" || s.ClientID == "" {
		fail("Single sign-on is not configured", nil)
		return
	}
	c, err := r.Cookie(rp.cookie())
	http.SetCookie(w, &http.Cookie{Name: rp.cookie(), Value: "", Path: rp.cookiePath(), MaxAge: -1, HttpOnly: true, Secure: rp.Secure})
	if err != nil {
		fail("The sign-in took too long — try again", nil)
		return
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 4 {
		fail("The sign-in took too long — try again", nil)
		return
	}
	state, nonce, verifier := parts[0], parts[1], parts[2]
	nb, _ := base64.RawURLEncoding.DecodeString(parts[3])
	next := SafeNext(string(nb))
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail(firstNonEmpty(q.Get("error_description"), e), nil)
		return
	}
	if q.Get("state") == "" || q.Get("state") != state {
		fail("The sign-in could not be matched to this browser — try again", nil)
		return
	}
	p := rp.provider(s.Issuer)
	d, err := p.discovery(ctx)
	if err != nil {
		fail("Cannot reach "+s.Name, err)
		return
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {rp.CallbackURL()},
		"client_id": {s.ClientID}, "code_verifier": {verifier},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.Token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if s.Secret != "" {
		req.SetBasicAuth(url.QueryEscape(s.ClientID), url.QueryEscape(s.Secret))
	}
	res, err := p.http.Do(req)
	if err != nil {
		fail("Cannot reach "+s.Name, err)
		return
	}
	var tok struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || tok.IDToken == "" {
		fail(s.Name+" refused the sign-in", fmt.Errorf("token endpoint HTTP %d: %s %s", res.StatusCode, tok.Error, tok.Desc))
		return
	}
	claims, err := verifyIDToken(ctx, p, d, s.ClientID, tok.IDToken, nonce)
	if err != nil {
		fail("The answer from "+s.Name+" did not check out", err)
		return
	}
	// some providers keep the email out of the ID token
	if (claims.Email == "" || claims.EmailVerified == nil) && d.UserInfo != "" && tok.AccessToken != "" {
		var ui IDClaims
		ureq, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.UserInfo, nil)
		ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		if ures, err := p.http.Do(ureq); err == nil {
			if ures.StatusCode == http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(ures.Body, 1<<20)).Decode(&ui)
			}
			ures.Body.Close()
		}
		if ui.Sub == claims.Sub {
			claims.Email, claims.EmailVerified = firstNonEmpty(claims.Email, ui.Email), ui.EmailVerified
			claims.Name = firstNonEmpty(claims.Name, ui.Name)
		}
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" || claims.EmailVerified == nil || !*claims.EmailVerified {
		fail("Your "+s.Name+" account needs a verified email address", nil)
		return
	}
	id := Identity{Issuer: claims.Iss, Subject: claims.Sub, Email: email, Name: claims.Name, AuthRevision: claims.AuthRevision}
	session, err := rp.Login(ctx, id, s.AutoCreate)
	if err != nil {
		var le *LoginError
		if errors.As(err, &le) {
			fail(le.Message, le.Err)
		} else {
			fail("No "+firstNonEmpty(rp.AppName, "account")+" account for "+email+" or identity could not be linked", err)
		}
		return
	}
	if rp.Core.Configured() {
		if err := rp.Core.TrackSession(ctx, session, claims.Iss, claims.Sub, claims.AuthRevision); err != nil {
			if rp.EndSession != nil {
				rp.EndSession(ctx, session)
			}
			fail("Could not start a revocable session", err)
			return
		}
	}
	rp.SetSession(w, session)
	rp.log().Info("signed in", "email", email, "via", "oidc")
	http.Redirect(w, r, next, http.StatusFound)
}

// ---- provider: discovery, keys, ID token ----

type provider struct {
	mu     sync.Mutex
	disc   *Discovery
	discAt time.Time
	keys   map[string]*rsa.PublicKey
	keysAt time.Time
	issuer string
	http   *http.Client
}

// Discovery is the part of the provider's discovery document the RP uses.
type Discovery struct {
	Issuer   string `json:"issuer"`
	Auth     string `json:"authorization_endpoint"`
	Token    string `json:"token_endpoint"`
	UserInfo string `json:"userinfo_endpoint"`
	JWKS     string `json:"jwks_uri"`
}

func (p *provider) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", u, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

// discovery is cached for an hour.
func (p *provider) discovery(ctx context.Context) (*Discovery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disc != nil && time.Since(p.discAt) < time.Hour {
		return p.disc, nil
	}
	var d Discovery
	if err := p.getJSON(ctx, p.issuer+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	if d.Issuer != p.issuer || d.Auth == "" || d.Token == "" || d.JWKS == "" {
		return nil, fmt.Errorf("the provider's discovery document does not match the issuer %q (says %q)", p.issuer, d.Issuer)
	}
	p.disc, p.discAt = &d, time.Now()
	return p.disc, nil
}

// key returns the signing key with that id, refetching the key set when it is
// unknown (the provider rotated) but at most once a minute.
func (p *provider) key(ctx context.Context, jwksURL, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if time.Since(p.keysAt) < time.Minute && p.keys != nil {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, jwksURL, &set); err != nil {
		return nil, err
	}
	p.keys, p.keysAt = map[string]*rsa.PublicKey{}, time.Now()
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		p.keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if kid == "" && len(p.keys) == 1 {
		for _, k := range p.keys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

// IDClaims are the ID token claims the RP reads.
type IDClaims struct {
	Iss           string          `json:"iss"`
	Sub           string          `json:"sub"`
	AuthRevision  int64           `json:"auth_revision"`
	Aud           json.RawMessage `json:"aud"`
	Exp           int64           `json:"exp"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified *bool           `json:"email_verified"`
	Name          string          `json:"name"`
}

func (c *IDClaims) hasAudience(id string) bool {
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return one == id
	}
	var many []string
	_ = json.Unmarshal(c.Aud, &many)
	for _, a := range many {
		if a == id {
			return true
		}
	}
	return false
}

// verifyIDToken checks an RS256 ID token's signature, issuer, audience,
// expiry (60 s leeway) and nonce.
func verifyIDToken(ctx context.Context, p *provider, d *Discovery, clientID, raw, nonce string) (*IDClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed ID token")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("malformed ID token header")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &head) != nil || head.Alg != "RS256" {
		return nil, fmt.Errorf("ID token algorithm %q is not RS256", head.Alg)
	}
	key, err := p.key(ctx, d.JWKS, head.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("malformed ID token signature")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return nil, errors.New("ID token signature does not verify")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed ID token payload")
	}
	var c IDClaims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, errors.New("malformed ID token payload")
	}
	switch {
	case c.Iss != d.Issuer:
		return nil, errors.New("ID token from another issuer")
	case !c.hasAudience(clientID):
		return nil, errors.New("ID token for another client")
	case time.Now().Unix() > c.Exp+60:
		return nil, errors.New("ID token expired")
	case nonce == "" || c.Nonce != nonce:
		return nil, errors.New("ID token nonce mismatch")
	}
	return &c, nil
}

// PKCEChallenge is the S256 code challenge for a verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
