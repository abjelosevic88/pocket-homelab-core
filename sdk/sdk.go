// Package sdk is the client Pocket apps use to join a Pocket Box: register a
// manifest on start, resolve sibling apps, publish and stream events, and
// validate device tokens that paired phones present.
//
//	core := sdk.New(os.Getenv("CORE_URL"), sdk.Options{TokenFile: dataDir + "/core.token"})
//	reg, err := core.Register(ctx, os.Getenv("CORE_REGISTRATION_KEY"), sdk.Manifest{
//	    Slug: "kitchen", Name: "Pocket Kitchen", URL: "http://pocket-kitchen:8080", Version: version,
//	    Publishes: []string{"kitchen.cooked"},
//	    Subscribes: []sdk.Subscription{{Topics: "habits.*", URL: "/api/v1/core/events", Secret: hookSecret}},
//	})
//	_ = core.Publish(ctx, "kitchen.cooked", "recipe:12", map[string]any{"recipe": "Pancakes"})
//	habits, _ := core.Resolve(ctx, "habits") // habits.URL
//
// Every call is a no-op error when Core is not configured, so apps keep
// working on a lab that has no Core yet.
package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNotConfigured is returned when the base URL is empty.
var ErrNotConfigured = errors.New("pocket core not configured")

// ErrNotFound is a 404 from Core.
var ErrNotFound = errors.New("not found")

// Manifest describes the app to Core. See docs/manifest.md.
type Manifest struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Icon        string         `json:"icon,omitempty"`
	Version     string         `json:"version,omitempty"`
	URL         string         `json:"url"`
	PublicURL   string         `json:"public_url,omitempty"`
	HealthPath  string         `json:"health_path,omitempty"`
	EnvPrefix   string         `json:"env_prefix,omitempty"`
	Publishes   []string       `json:"publishes,omitempty"`
	Subscribes  []Subscription `json:"subscribes,omitempty"`
	OIDC        *OIDCRequest   `json:"oidc,omitempty"`
	// Offers are scopes other apps may use here for a person (see docs/links.md).
	Offers []Offer `json:"offers,omitempty"`
	// Uses are what this app does in other apps for a person.
	Uses []Use `json:"uses,omitempty"`
	// Services are third-party services (immich, maloja, dawarich…) the app
	// reaches for a person through Core instead of a pasted URL and key.
	Services []ServiceUse `json:"services,omitempty"`
}

// ServiceUse asks Core for people's way into a third-party service, by its
// catalog type; Core shows it as a link the admin and each person can switch off.
type ServiceUse struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

// Offer is one scope an app lets linked apps use for a person.
type Offer struct {
	Scope       string `json:"scope"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Use asks for scopes in another app; Core creates one link per scope.
type Use struct {
	App    string   `json:"app"`
	Scopes []string `json:"scopes"`
	Reason string   `json:"reason,omitempty"`
}

// Subscription asks Core to POST matching events to a URL (relative to the app URL allowed).
type Subscription struct {
	Topics string `json:"topics"`
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

// OIDCRequest asks Core to create (or update) a relying-party client for the app.
type OIDCRequest struct {
	RedirectURIs []string `json:"redirect_uris"`
	Public       bool     `json:"public,omitempty"`
}

// Registration is Core's answer to Register.
type Registration struct {
	App   json.RawMessage `json:"app"`
	Token string          `json:"token,omitempty"`
	Core  struct {
		BaseURL       string `json:"base_url"`
		EventsURL     string `json:"events_url"`
		StreamURL     string `json:"stream_url"`
		IntrospectURL string `json:"introspect_url"`
		Issuer        string `json:"issuer"`
	} `json:"core"`
	OIDC *struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret,omitempty"`
		Issuer       string `json:"issuer"`
		Discovery    string `json:"discovery"`
	} `json:"oidc,omitempty"`
}

// Event is one bus message.
type Event struct {
	ID        int64           `json:"id"`
	Topic     string          `json:"topic"`
	Source    string          `json:"source"`
	Subject   string          `json:"subject"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// Resolved is a sibling app's address.
type Resolved struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	PublicURL string `json:"public_url"`
	Status    string `json:"status"`
	Version   string `json:"version"`
}

// Introspection is Core's verdict on a token.
type Introspection struct {
	Active bool   `json:"active"`
	Kind   string `json:"kind"` // user | device | app | delegated
	Scope  string `json:"scope"`
	// Audience is the app a delegated (pcx_) token is for; App is the app acting.
	Audience string `json:"audience,omitempty"`
	Reason   string `json:"reason,omitempty"`
	User     *struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	} `json:"user,omitempty"`
	Device *struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Platform string `json:"platform"`
	} `json:"device,omitempty"`
	App *struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"app,omitempty"`
}

// Options tune the client.
type Options struct {
	// Token is the app's pca_ token when already known.
	Token string
	// TokenFile persists the token Core mints on first registration.
	TokenFile string
	// HTTP overrides the client (default 15 s timeout).
	HTTP *http.Client
	// UserAgent is sent with every request.
	UserAgent string
	// IntrospectCache is how long a verdict is reused (default 60 s).
	IntrospectCache time.Duration
	// OIDCFile persists the relying-party client Core creates for the app
	// (issuer, client id, secret) so sign-in needs no configuration. Defaults
	// to TokenFile + ".oidc.json" when TokenFile is set.
	OIDCFile string
}

// OIDCClient is the relying-party client Core issued to the app.
type OIDCClient struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// Client talks to one Pocket Core.
type Client struct {
	base  string
	opts  Options
	http  *http.Client
	mu    sync.RWMutex
	token string
	oidc  OIDCClient
	cache map[string]cached
}

type cached struct {
	v   Introspection
	exp time.Time
}

// New creates a client. An empty base URL yields a client whose calls return ErrNotConfigured.
func New(baseURL string, opts Options) *Client {
	c := &Client{base: strings.TrimRight(baseURL, "/"), opts: opts, http: opts.HTTP, token: opts.Token, cache: map[string]cached{}}
	if c.http == nil {
		c.http = &http.Client{Timeout: 15 * time.Second}
	}
	if c.opts.UserAgent == "" {
		c.opts.UserAgent = "PocketSDK/1.0"
	}
	if c.opts.IntrospectCache == 0 {
		c.opts.IntrospectCache = time.Minute
	}
	if c.token == "" && opts.TokenFile != "" {
		if b, err := os.ReadFile(opts.TokenFile); err == nil {
			c.token = strings.TrimSpace(string(b))
		}
	}
	if c.opts.OIDCFile == "" && opts.TokenFile != "" {
		c.opts.OIDCFile = opts.TokenFile + ".oidc.json"
	}
	if c.opts.OIDCFile != "" {
		if b, err := os.ReadFile(c.opts.OIDCFile); err == nil {
			_ = json.Unmarshal(b, &c.oidc)
		}
	}
	return c
}

// OIDC returns the relying-party client Core issued (zero value when none).
// Issuer and client id refresh on every registration; the secret is kept
// from the registration that minted it.
func (c *Client) OIDC() OIDCClient {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.oidc
}

// Configured reports whether a base URL is set.
func (c *Client) Configured() bool { return c != nil && c.base != "" }

// Token returns the app token in use.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// BaseURL returns Core's URL.
func (c *Client) BaseURL() string { return c.base }

func (c *Client) do(ctx context.Context, method, path string, auth string, body any, out any) (int, error) {
	if !c.Configured() {
		return 0, ErrNotConfigured
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return res.StatusCode, ErrNotFound
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4000))
		_ = json.Unmarshal(b, &e)
		if e.Error != "" {
			return res.StatusCode, fmt.Errorf("core: HTTP %d: %s", res.StatusCode, e.Error)
		}
		return res.StatusCode, fmt.Errorf("core: HTTP %d", res.StatusCode)
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		return res.StatusCode, nil
	}
	return res.StatusCode, json.NewDecoder(res.Body).Decode(out)
}

// Register announces the manifest. The first time it uses registrationKey
// and stores the minted app token (in memory and TokenFile); afterwards the
// app token alone is enough and the key may be empty.
func (c *Client) Register(ctx context.Context, registrationKey string, m Manifest) (*Registration, error) {
	auth := c.Token()
	if auth == "" {
		auth = registrationKey
	}
	if auth == "" {
		return nil, errors.New("pocket core: no app token and no registration key")
	}
	var reg Registration
	code, err := c.do(ctx, http.MethodPost, "/api/v1/apps/register", auth, m, &reg)
	if (code == http.StatusUnauthorized || code == http.StatusForbidden) && c.Token() != "" && registrationKey != "" {
		// Our stored token was revoked; fall back to the key once.
		_, err = c.do(ctx, http.MethodPost, "/api/v1/apps/register", registrationKey, m, &reg)
	}
	if err != nil {
		return nil, err
	}
	if reg.Token != "" {
		c.mu.Lock()
		c.token = reg.Token
		c.mu.Unlock()
		if c.opts.TokenFile != "" {
			_ = os.MkdirAll(filepath.Dir(c.opts.TokenFile), 0o755)
			_ = os.WriteFile(c.opts.TokenFile, []byte(reg.Token+"\n"), 0o600)
		}
	}
	if reg.OIDC != nil && reg.OIDC.ClientID != "" {
		c.mu.Lock()
		c.oidc.Issuer, c.oidc.ClientID = reg.OIDC.Issuer, reg.OIDC.ClientID
		if reg.OIDC.ClientSecret != "" {
			c.oidc.ClientSecret = reg.OIDC.ClientSecret
		}
		snapshot := c.oidc
		c.mu.Unlock()
		if c.opts.OIDCFile != "" {
			_ = os.MkdirAll(filepath.Dir(c.opts.OIDCFile), 0o755)
			if b, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
				_ = os.WriteFile(c.opts.OIDCFile, b, 0o600)
			}
		}
	}
	return &reg, nil
}

// Publish sends one event under the app's slug.
func (c *Client) Publish(ctx context.Context, topic, subject string, payload any) error {
	body := map[string]any{"topic": topic, "subject": subject, "payload": payload}
	if payload == nil {
		body["payload"] = map[string]any{}
	}
	_, err := c.do(ctx, http.MethodPost, "/api/v1/events", c.Token(), body, nil)
	return err
}

// Resolve returns where a sibling app lives.
func (c *Client) Resolve(ctx context.Context, slug string) (*Resolved, error) {
	var out Resolved
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/resolve?slug="+url.QueryEscape(slug), c.Token(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Events lists history: topic may end in "*", after is an event id.
func (c *Client) Events(ctx context.Context, topic string, after int64, limit int) ([]Event, error) {
	q := url.Values{}
	if topic != "" {
		q.Set("topic", topic)
	}
	if after > 0 {
		q.Set("after", fmt.Sprint(after))
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	var out []Event
	_, err := c.do(ctx, http.MethodGet, "/api/v1/events?"+q.Encode(), c.Token(), nil, &out)
	return out, err
}

// Stream follows the bus over SSE until ctx ends, calling fn for each event.
// It reconnects with backoff and resumes from the last id it saw.
func (c *Client) Stream(ctx context.Context, topics string, fn func(Event)) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	var last int64
	wait := time.Second
	for {
		err := c.streamOnce(ctx, topics, &last, fn)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			wait = time.Second
		} else if wait < 30*time.Second {
			wait *= 2
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, topics string, last *int64, fn func(Event)) error {
	u := c.base + "/api/v1/events/stream?topics=" + url.QueryEscape(topics)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	if *last > 0 {
		req.Header.Set("Last-Event-ID", fmt.Sprint(*last))
	}
	res, err := (&http.Client{}).Do(req) // no timeout: long-lived
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("core: stream HTTP %d", res.StatusCode)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				var e Event
				if json.Unmarshal([]byte(data.String()), &e) == nil {
					if e.ID > *last {
						*last = e.ID
					}
					fn(e)
				}
				data.Reset()
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return sc.Err()
}

// Introspect asks Core whether a token (device, personal or OIDC access) is
// valid, caching the answer briefly.
func (c *Client) Introspect(ctx context.Context, token string) (*Introspection, error) {
	c.mu.RLock()
	if v, ok := c.cache[token]; ok && time.Now().Before(v.exp) {
		c.mu.RUnlock()
		out := v.v
		return &out, nil
	}
	c.mu.RUnlock()
	var out Introspection
	if _, err := c.do(ctx, http.MethodPost, "/api/v1/auth/introspect", c.Token(), map[string]string{"token": token}, &out); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.cache) > 1000 {
		c.cache = map[string]cached{}
	}
	c.cache[token] = cached{v: out, exp: time.Now().Add(c.opts.IntrospectCache)}
	c.mu.Unlock()
	return &out, nil
}

// IsCoreToken reports whether a bearer token looks like one Core issued, so
// an app can route it to Introspect instead of its own token table.
//
// Link tokens (pcx_) are deliberately not included: they carry a scope that
// the app has to enforce, so an app opts in with IsLinkToken.
func IsCoreToken(tok string) bool {
	return strings.HasPrefix(tok, "pcd_") || strings.HasPrefix(tok, "pcu_") || strings.HasPrefix(tok, "pco_")
}

// IsLinkToken reports whether a bearer token is a pcx_ token another app
// obtained to act for a person here. Only apps that declare Offers get them.
func IsLinkToken(tok string) bool { return strings.HasPrefix(tok, "pcx_") }

// Delegated reports whether the verdict is for a link token.
func (in *Introspection) Delegated() bool { return in != nil && in.Kind == "delegated" }

// HasScope reports whether a delegated verdict covers a scope.
func (in *Introspection) HasScope(scope string) bool {
	if in == nil {
		return false
	}
	for _, s := range strings.Fields(in.Scope) {
		if s == scope {
			return true
		}
	}
	return false
}

// LinkToken is a short-lived token to act for a person in another app.
type LinkToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
	App         string `json:"app"`
	URL         string `json:"url"`
	PublicURL   string `json:"public_url"`
}

// LinkError is Core refusing a link token; Reason is machine-readable
// (off_admin, off_person, not_declared, not_offered, target_missing,
// not_in_audience, unknown_user).
type LinkError struct {
	Reason  string
	Message string
}

func (e *LinkError) Error() string { return "pocket core: " + e.Message }

// ActAs returns a token (and the target's address) to call app `target` as
// the person with this email, for the scopes. Every call asks Core, so a link
// switched off stops the next action at once; use the token for the work at
// hand rather than keeping it.
func (c *Client) ActAs(ctx context.Context, email, target string, scopes ...string) (*LinkToken, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	body := map[string]any{"user": email, "app": target, "scopes": scopes}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/links/token", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		var e struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Reason != "" {
			return nil, &LinkError{Reason: e.Reason, Message: e.Error}
		}
		return nil, fmt.Errorf("core: HTTP %d: %s", res.StatusCode, e.Error)
	}
	var t LinkToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// LinkState is one of the app's links as it stands for a person.
type LinkState struct {
	App    string `json:"app"`
	Scope  string `json:"scope"`
	State  string `json:"state"`
	On     bool   `json:"on"`
	Detail string `json:"detail"`
}

// LinkStatus lists the app's links for a person (optionally one target app)
// without issuing a token, e.g. to decide whether to offer an option.
func (c *Client) LinkStatus(ctx context.Context, email, target string) ([]LinkState, error) {
	q := url.Values{"user": {email}}
	if target != "" {
		q.Set("app", target)
	}
	var out []LinkState
	_, err := c.do(ctx, http.MethodGet, "/api/v1/links/status?"+q.Encode(), c.Token(), nil, &out)
	return out, err
}

// LinkOn reports whether every scope is linked for the person in target.
func (c *Client) LinkOn(ctx context.Context, email, target string, scopes ...string) (bool, string) {
	list, err := c.LinkStatus(ctx, email, target)
	if err != nil {
		return false, err.Error()
	}
	on := map[string]LinkState{}
	for _, l := range list {
		on[l.Scope] = l
	}
	for _, sc := range scopes {
		l, ok := on[sc]
		if !ok {
			return false, "not_declared"
		}
		if !l.On {
			return false, l.State
		}
	}
	return true, ""
}

// ServiceCred is a person's way into a third-party service, from Core: the
// service's address and the person's own credential (Source "personal"), or
// the box-wide one when the app acts for an admin (Source "box"). Key is the
// first secret field (the API key / token); Fields has them all by catalog key.
type ServiceCred struct {
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	PublicURL string            `json:"public_url"`
	Key       string            `json:"key"`
	Fields    map[string]string `json:"fields"`
	Source    string            `json:"source"`
}

// ServiceFor asks Core for the person's way into a service the manifest lists
// under Services. Refusals are *LinkError (off_admin, off_person, no_account,
// not_in_audience, target_missing, not_declared, unknown_user). Every call
// asks Core, so a switch-off applies at once; don't keep the key around.
func (c *Client) ServiceFor(ctx context.Context, email, typ string) (*ServiceCred, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	b, _ := json.Marshal(map[string]any{"user": email, "type": typ})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/services/credential", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		var e struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Reason != "" {
			return nil, &LinkError{Reason: e.Reason, Message: e.Error}
		}
		return nil, fmt.Errorf("core: HTTP %d: %s", res.StatusCode, e.Error)
	}
	var out ServiceCred
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
