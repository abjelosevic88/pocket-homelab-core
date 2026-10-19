package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Immich (v3) connector.
//
// Endpoints used (paths below /api):
//
//	GET  /server/version          Test                         read
//	GET  /api-keys/me             Test (key permissions)       read
//	GET  /admin/users             Test, Provision, Poll        read   (adminUser.read)
//	POST /admin/users             Provision, missing user      WRITE  (adminUser.create)
//	POST /auth/login              Provision, as the new user   WRITE  (opens a session)
//	POST /api-keys                Provision, as the new user   WRITE  (the person's own key)
//	POST /auth/logout             Provision, as the new user   WRITE  (closes that session)
//	GET  /system-config           ConfigureSSO                 read   (systemConfig.read)
//	PUT  /system-config           ConfigureSSO                 WRITE  (systemConfig.update)
//	GET  /users/me                Events: whose key is this    read   (as the person)
//	GET  /workflows               Events: heal check           read   (workflow.read, as the person)
//	POST /workflows               Events on                    WRITE  (workflow.create, as the person)
//	DELETE /workflows/{id}        Events off                   WRITE  (workflow.delete, as the person)
//
// Events arrive by push: Immich workflows are per user (a workflow runs only
// for its owner's assets), so Core creates, with each person's own key, an
// AssetCreate and an AssetMetadataExtraction workflow whose single step is
// the built-in immich-plugin-core#webhook. See immich_push.go.
//
// /system-config is deprecated since v3.2 in favour of /admin/config (alpha);
// the connector falls back to /admin/config when /system-config answers 404.
type immich struct{}

func init() { Register(immich{}) }

// ImmichAdminPermissions are what the box-wide (admin) API key must allow.
var ImmichAdminPermissions = []string{"adminUser.read", "adminUser.create", "systemConfig.read", "systemConfig.update"}

// ImmichWorkflowPermissions are what a person's key needs for events.
var ImmichWorkflowPermissions = []string{"user.read", "workflow.read", "workflow.create", "workflow.delete"}

// ImmichKeyName is the name of the key minted for each person.
const ImmichKeyName = "Pocket Core"

func (immich) Type() string { return "immich" }

func (immich) Capabilities() Capabilities {
	return Capabilities{Provision: true, SSO: true, Events: true}
}

func (immich) SSORedirects(publicURL string) []string {
	p := strings.TrimRight(publicURL, "/")
	return []string{p + "/auth/login", p + "/user-settings", "app.immich:///oauth-callback", p + "/api/oauth/mobile-redirect"}
}

// apiError is a non-2xx answer from Immich.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("immich answered HTTP %d: %s", e.Status, e.Msg) }

func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func apiBase(t Target) string {
	b := strings.TrimRight(t.BaseURL, "/")
	if !strings.HasSuffix(b, "/api") {
		b += "/api"
	}
	return b
}

// auth is how a request authenticates: the box-wide API key or a session token.
type auth struct{ apiKey, bearer string }

// call does one JSON request. secrets are scrubbed from any error text.
func (immich) call(ctx context.Context, t Target, method, path string, a auth, body, out any, secrets ...string) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase(t)+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.apiKey != "" {
		req.Header.Set("x-api-key", a.apiKey)
	}
	if a.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	secrets = append(secrets, a.apiKey, a.bearer)
	res, err := t.HTTP.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), secrets...))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Message any `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message != nil {
			switch m := e.Message.(type) {
			case string:
				msg = m
			case []any:
				parts := []string{}
				for _, p := range m {
					parts = append(parts, fmt.Sprint(p))
				}
				msg = strings.Join(parts, "; ")
			}
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &apiError{Status: res.StatusCode, Msg: redact(msg, secrets...)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // config values pass through untouched
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("immich %s %s: unexpected answer: %w", method, path, err)
	}
	return nil
}

func adminKey(t Target) (auth, error) {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return auth{}, errors.New("the Immich service in Core has no API_KEY: paste an Immich admin's API key under Services → Immich → Edit")
	}
	return auth{apiKey: k}, nil
}

func needAdmin(err error) error {
	switch statusOf(err) {
	case http.StatusUnauthorized:
		return fmt.Errorf("Immich rejected the stored API key (HTTP 401): it is wrong or was deleted; create a new one as an Immich admin (Account settings → API keys) with permissions %s, or \"all\"", strings.Join(ImmichAdminPermissions, ", "))
	case http.StatusForbidden:
		return fmt.Errorf("the stored API key is not allowed to do this (HTTP 403): Core needs the key of an Immich ADMIN with permissions %s, or \"all\" (%v)", strings.Join(ImmichAdminPermissions, ", "), err)
	}
	return err
}

type immichUser struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"isAdmin"`
}

func (c immich) users(ctx context.Context, t Target, a auth) ([]immichUser, error) {
	var list []immichUser
	if err := c.call(ctx, t, http.MethodGet, "/admin/users", a, nil, &list); err != nil {
		return nil, needAdmin(err)
	}
	return list, nil
}

func (c immich) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	a, err := adminKey(t)
	if err != nil {
		return info, err
	}
	var v struct {
		Major, Minor, Patch json.Number
	}
	if err := c.call(ctx, t, http.MethodGet, "/server/version", auth{}, nil, &v); err != nil {
		return info, fmt.Errorf("cannot reach Immich at %s: %w", apiBase(t), err)
	}
	info.Version = fmt.Sprintf("%s.%s.%s", v.Major, v.Minor, v.Patch)
	var me struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/api-keys/me", a, nil, &me); err == nil {
		info.Permissions = me.Permissions
	} else if statusOf(err) == http.StatusUnauthorized {
		return info, needAdmin(err)
	}
	list, err := c.users(ctx, t, a)
	if err != nil {
		return info, err
	}
	info.Admin, info.Users = true, len(list)
	if miss := missing(info.Permissions, ImmichAdminPermissions); len(miss) > 0 {
		info.Detail = "the key works for reading users but lacks " + strings.Join(miss, ", ") + " — provisioning or SSO will fail until it has them (or \"all\")"
	} else if miss := missing(info.Permissions, ImmichWorkflowPermissions); len(miss) > 0 {
		info.Detail = "the key lacks " + strings.Join(miss, ", ") + ": events for the key's own Immich account will fail until it has them (or \"all\")"
	}
	return info, nil
}

// missing lists wanted permissions the key does not have; nil when unknown or "all".
func missing(have, want []string) []string {
	if len(have) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, p := range have {
		set[p] = true
	}
	if set["all"] {
		return nil
	}
	var out []string
	for _, w := range want {
		if !set[w] {
			out = append(out, w)
		}
	}
	return out
}

func (c immich) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	a, err := adminKey(t)
	if err != nil {
		return Account{}, err
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		return Account{}, errors.New("person has no email")
	}
	list, err := c.users(ctx, t, a)
	if err != nil {
		return Account{}, err
	}
	for _, u := range list {
		if strings.EqualFold(strings.TrimSpace(u.Email), email) {
			// Someone already has this email: link, never touch it. Its
			// password is unknown and an admin account is not ours to change.
			note := "an Immich account with this email already exists; linked without minting a key (the person can add their own key in Core)"
			if u.IsAdmin {
				note = "an Immich admin account with this email already exists; linked and left untouched (no key minted)"
			}
			return Account{RemoteID: u.ID, Status: StatusLinked, Note: note}, nil
		}
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = strings.SplitN(email, "@", 2)[0]
	}
	password := RandomPassword(40) // used for this one login, never stored, logged or returned
	var created immichUser
	body := map[string]any{"email": email, "name": name, "password": password, "shouldChangePassword": false, "notify": false, "isAdmin": false}
	if err := c.call(ctx, t, http.MethodPost, "/admin/users", a, body, &created, password); err != nil {
		return Account{}, needAdmin(err)
	}
	acc := Account{RemoteID: created.ID, Status: StatusOK, Created: true}
	if p.KeepOwnCredential {
		acc.Note = "account created; the person already has their own key in Core, so none was minted"
		return acc, nil
	}
	// Immich only lets a user mint their own keys: sign in once as the new
	// user, create the key, sign out. The password then goes out of scope.
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	if err := c.call(ctx, t, http.MethodPost, "/auth/login", auth{}, map[string]string{"email": email, "password": password}, &login, password); err != nil || login.AccessToken == "" {
		if err == nil {
			err = errors.New("no access token in the answer")
		}
		return acc, fmt.Errorf("the Immich account %s was created but signing in as it to mint its key failed (is password login turned off in Immich?): %w; a retry will link the account without a key", created.ID, err)
	}
	sess := auth{bearer: login.AccessToken}
	var key struct {
		Secret string `json:"secret"`
	}
	kerr := c.call(ctx, t, http.MethodPost, "/api-keys", sess, map[string]any{"name": ImmichKeyName, "permissions": []string{"all"}}, &key, password)
	_ = c.call(ctx, t, http.MethodPost, "/auth/logout", sess, nil, nil, password)
	if kerr != nil || key.Secret == "" {
		if kerr == nil {
			kerr = errors.New("no secret in the answer")
		}
		return acc, fmt.Errorf("the Immich account %s was created but minting its API key failed: %w; a retry will link the account without a key", created.ID, kerr)
	}
	acc.Fields = map[string]string{"API_KEY": key.Secret}
	return acc, nil
}

// configPaths are the GET/PUT endpoints for the full server configuration.
var configPaths = []string{"/system-config", "/admin/config"}

func (c immich) ConfigureSSO(ctx context.Context, t Target, sc SSOConfig) error {
	a, err := adminKey(t)
	if err != nil {
		return err
	}
	pub := strings.TrimRight(t.PublicURL, "/")
	if sc.Enabled && (pub == "" || sc.Issuer == "" || sc.ClientID == "" || sc.ClientSecret == "") {
		return errors.New("sign-in needs the service's public URL, Core's issuer and a client id and secret")
	}
	var cfg map[string]any
	path := ""
	for _, p := range configPaths {
		cfg = nil
		err = c.call(ctx, t, http.MethodGet, p, a, nil, &cfg)
		if statusOf(err) == http.StatusNotFound {
			continue
		}
		path = p
		break
	}
	if err != nil {
		return needAdmin(err)
	}
	oauth, ok := cfg["oauth"].(map[string]any)
	if !ok {
		return errors.New("the Immich configuration has no oauth block; refusing to write")
	}
	// Back up the whole configuration before changing anything.
	if err := os.MkdirAll(t.DataDir, 0o700); err != nil {
		return fmt.Errorf("backup dir: %w", err)
	}
	backup, _ := json.MarshalIndent(cfg, "", "  ")
	name := filepath.Join(t.DataDir, "immich-system-config-"+t.Clock().UTC().Format("20060102-150405.000")+".json")
	if err := os.WriteFile(name, backup, 0o600); err != nil {
		return fmt.Errorf("write config backup: %w", err)
	}
	if sc.Enabled {
		btn := sc.ButtonText
		if btn == "" {
			btn = "Sign in with Pocket Core"
		}
		oauth["enabled"] = true
		oauth["issuerUrl"] = sc.Issuer
		oauth["clientId"] = sc.ClientID
		oauth["clientSecret"] = sc.ClientSecret
		oauth["scope"] = "openid email profile"
		oauth["buttonText"] = btn
		oauth["autoRegister"] = true
		oauth["autoLaunch"] = false
		oauth["mobileOverrideEnabled"] = true
		oauth["mobileRedirectUri"] = pub + "/api/oauth/mobile-redirect"
	} else {
		oauth["enabled"] = false
	}
	cfg["oauth"] = oauth
	if err := c.call(ctx, t, http.MethodPut, path, a, cfg, nil, sc.ClientSecret); err != nil {
		return fmt.Errorf("saving the Immich configuration failed (backup kept at %s): %w", name, needAdmin(err))
	}
	return nil
}

type immichUserState struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type immichState struct {
	Users map[string]immichUserState `json:"users"`
}

// Poll covers what the workflows do not: new Immich accounts. Assets
// arrive by push (immich_push.go).
func (c immich) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	a, err := adminKey(t)
	if err != nil {
		return nil, state, err
	}
	list, err := c.users(ctx, t, a)
	if err != nil {
		return nil, state, err
	}
	cur := immichState{Users: map[string]immichUserState{}}
	for _, u := range list {
		cur.Users[u.ID] = immichUserState{Email: strings.ToLower(u.Email), Name: u.Name}
	}
	next, _ := json.Marshal(cur)
	var prev immichState
	if len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &prev) != nil || prev.Users == nil {
		return nil, next, nil // first poll: baseline only
	}
	ids := make([]string, 0, len(cur.Users))
	for id := range cur.Users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Event
	for _, id := range ids {
		if _, known := prev.Users[id]; !known {
			u := cur.Users[id]
			out = append(out, Event{Name: "user_created", UserEmail: u.Email, Payload: map[string]any{"immich_user_id": id, "name": u.Name}})
		}
	}
	return out, next, nil
}
