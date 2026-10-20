package connectors

import (
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
	"regexp"
	"sort"
	"strings"
	"time"
)

// Nextcloud (tested against 34). Core acts as the box-wide account in the
// service's USERNAME + APP_PASSWORD, which must be a Nextcloud admin.
//
//	GET  /status.php                                   Test                    read
//	GET  /ocs/v2.php/cloud/user                        Test, WhoAmI            read
//	GET  /ocs/v2.php/cloud/apps?filter=enabled         Test (what is missing)  read
//	GET  /ocs/v2.php/cloud/users/details               Test, Provision, Poll   read
//	GET  /ocs/v2.php/cloud/users/{id}                  Link                    read
//	POST /ocs/v2.php/cloud/users                       Provision, missing user WRITE
//	GET  /ocs/v2.php/core/getapppassword               Provision, as the new user: their own app password
//	GET|POST|PUT|DELETE /ocs/v2.php/apps/user_oidc/api/v1/provider[/{id}]     sign-in (user_oidc app)
//	GET|POST|DELETE /ocs/v2.php/apps/webhook_listeners/api/v1/webhooks[/{id}] events (webhook_listeners app)
//	POST /ocs/v2.php/core/apppassword/rotate           RotateKey, as the person with the stored app password
//
// Accounts: a person is linked to the Nextcloud account with their email;
// with none, an account named like the email's local part is created and
// Core keeps that person's own app password. An account by that name with
// another email is never taken over: link it by hand (Linker) instead.
//
// Sign-in: user_oidc with uid = the pocket_uid claim, which Core only sends
// for people it linked to a Nextcloud account, so nobody lands in an account
// by name and unlinked people are refused. Password login stays.
//
// Events: box-wide webhooks (webhook_listeners runs them for everyone);
// Nextcloud sends them from background jobs, so they arrive with its cron
// (every 5 minutes on a default setup). New and deleted accounts are polled.
type nextcloud struct{}

func init() { Register(nextcloud{}) }

// NextcloudProvider is the user_oidc provider Core owns (also the login button: "Log in with Pocket Core").
const NextcloudProvider = "Pocket Core"

// NextcloudAppPasswordName is how Nextcloud lists the app password minted for each person.
const NextcloudAppPasswordName = "Pocket Core"

const (
	ncFileCreated  = `OCP\Files\Events\Node\NodeCreatedEvent`
	ncFileDeleted  = `OCP\Files\Events\Node\NodeDeletedEvent`
	ncCalCreated   = `OCP\Calendar\Events\CalendarObjectCreatedEvent`
	ncCalUpdated   = `OCP\Calendar\Events\CalendarObjectUpdatedEvent`
	ncCalTrashed   = `OCP\Calendar\Events\CalendarObjectMovedToTrashEvent`
	ncHookPathPart = "/api/v1/connectors/hooks/"
)

var nextcloudTriggers = []PushTrigger{
	{Trigger: ncFileCreated, Name: "Pocket Core · file created"},
	{Trigger: ncFileDeleted, Name: "Pocket Core · file deleted"},
	{Trigger: ncCalCreated, Name: "Pocket Core · calendar created"},
	{Trigger: ncCalUpdated, Name: "Pocket Core · calendar updated"},
	{Trigger: ncCalTrashed, Name: "Pocket Core · calendar deleted"},
}

// ncFilesPath is a path inside a person's own files: /<uid>/files/…
// (not previews, versions, trash or upload chunks).
var ncFilesPath = regexp.MustCompile(`^/([^/]+)/files/(.+)$`)

func (nextcloud) Type() string { return "nextcloud" }

func (nextcloud) Capabilities() Capabilities {
	return Capabilities{Provision: true, SSO: true, Events: true, Link: true}
}

func (nextcloud) SSORedirects(publicURL string) []string {
	p := strings.TrimRight(publicURL, "/")
	return []string{p + "/apps/user_oidc/code", p + "/index.php/apps/user_oidc/code"}
}

// ncError is a non-2xx answer from Nextcloud.
type ncError struct {
	Status int
	Msg    string
}

func (e *ncError) Error() string {
	return fmt.Sprintf("nextcloud answered HTTP %d: %s", e.Status, e.Msg)
}

func ncStatus(err error) int {
	var e *ncError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

type ncAuth struct{ user, pass string }

func ncAdmin(t Target) (ncAuth, error) {
	u, p := strings.TrimSpace(t.Fields["USERNAME"]), strings.TrimSpace(t.Fields["APP_PASSWORD"])
	if p == "" {
		p = strings.TrimSpace(t.Fields["ADMIN_PASSWORD"])
	}
	if u == "" || p == "" {
		return ncAuth{}, errors.New("the Nextcloud service in Core needs USERNAME and APP_PASSWORD of a Nextcloud admin: create an app password under Personal settings → Security → Devices & sessions and paste both under Services → Nextcloud → Edit")
	}
	return ncAuth{user: u, pass: p}, nil
}

// ncAdminWrite is the credential for what Nextcloud guards with password
// confirmation (adding accounts, sign-in providers): it refuses app
// passwords there, so the admin's login password (ADMIN_PASSWORD).
func ncAdminWrite(t Target) (ncAuth, error) {
	a, err := ncAdmin(t)
	if err != nil {
		return a, err
	}
	p := strings.TrimSpace(t.Fields["ADMIN_PASSWORD"])
	if p == "" {
		return ncAuth{}, errors.New(ncNeedLoginPassword)
	}
	return ncAuth{user: a.user, pass: p}, nil
}

const ncNeedLoginPassword = "Nextcloud only adds accounts and changes sign-in when it gets the admin's real login password, not an app password: put it in Services → Nextcloud → Edit → \"Login password (connector only)\". It stays in Core and is never sent to apps or devices"

func ncNeedAdmin(err error) error {
	var e *ncError
	if errors.As(err, &e) && strings.Contains(strings.ToLower(e.Msg), "password confirmation") {
		return errors.New(ncNeedLoginPassword + " (the stored one was not accepted)")
	}
	switch ncStatus(err) {
	case http.StatusUnauthorized:
		return fmt.Errorf("Nextcloud rejected the stored USERNAME and password (HTTP 401): it is wrong or was revoked; a login password does not work for an account with two-factor sign-in")
	case http.StatusForbidden:
		return fmt.Errorf("the stored Nextcloud account is not allowed to do this (HTTP 403): Core needs an account in the admin group (%v)", err)
	}
	return err
}

// ocs does one OCS request and decodes ocs.data into out. secrets are
// scrubbed from any error text.
func (nextcloud) ocs(ctx context.Context, t Target, method, path string, a ncAuth, body, out any, secrets ...string) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := strings.TrimRight(t.BaseURL, "/") + path
	if strings.Contains(u, "?") {
		u += "&format=json"
	} else {
		u += "?format=json"
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("User-Agent", NextcloudAppPasswordName) // also the name of a minted app password
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.user != "" {
		req.SetBasicAuth(a.user, a.pass)
	}
	secrets = append(secrets, a.pass)
	res, err := t.HTTP.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), secrets...))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	var env struct {
		OCS struct {
			Meta struct {
				Status     string `json:"status"`
				StatusCode int    `json:"statuscode"`
				Message    string `json:"message"`
			} `json:"meta"`
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	jerr := json.Unmarshal(raw, &env)
	if res.StatusCode < 200 || res.StatusCode > 299 || jerr != nil {
		msg := strings.TrimSpace(env.OCS.Meta.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200] + "…"
			}
			if strings.Contains(msg, "<html") || strings.Contains(msg, "<!DOCTYPE") {
				msg = "an HTML page (wrong URL, or Nextcloud does not trust this host name: add it to trusted_domains)"
			}
		}
		st := res.StatusCode
		if st >= 200 && st <= 299 {
			st = http.StatusBadGateway
		}
		return &ncError{Status: st, Msg: redact(msg, secrets...)}
	}
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return fmt.Errorf("unexpected answer from Nextcloud %s: %w", path, err)
		}
	}
	return nil
}

type ncUser struct {
	ID          string   `json:"id"`
	Email       *string  `json:"email"`
	DisplayName string   `json:"displayname"`
	Enabled     *bool    `json:"enabled"`
	Groups      []string `json:"groups"`
}

func (u ncUser) email() string {
	if u.Email == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*u.Email))
}

func (u ncUser) admin() bool {
	for _, g := range u.Groups {
		if g == "admin" {
			return true
		}
	}
	return false
}

// users reads every account with its details, 200 at a time.
func (c nextcloud) users(ctx context.Context, t Target, a ncAuth) ([]ncUser, error) {
	var out []ncUser
	const page = 200
	for offset := 0; ; offset += page {
		var data struct {
			Users json.RawMessage `json:"users"`
		}
		if err := c.ocs(ctx, t, http.MethodGet, fmt.Sprintf("/ocs/v2.php/cloud/users/details?limit=%d&offset=%d", page, offset), a, nil, &data); err != nil {
			return nil, ncNeedAdmin(err)
		}
		// a map uid → details, or [] when the page is empty
		var m map[string]ncUser
		if err := json.Unmarshal(data.Users, &m); err != nil {
			break
		}
		ids := make([]string, 0, len(m))
		for id := range m {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			u := m[id]
			if u.ID == "" {
				u.ID = id
			}
			out = append(out, u)
		}
		if len(m) < page {
			break
		}
	}
	return out, nil
}

func (c nextcloud) enabledApps(ctx context.Context, t Target, a ncAuth) (map[string]bool, error) {
	var data struct {
		Apps []string `json:"apps"`
	}
	if err := c.ocs(ctx, t, http.MethodGet, "/ocs/v2.php/cloud/apps?filter=enabled", a, nil, &data); err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, id := range data.Apps {
		m[id] = true
	}
	return m, nil
}

func (c nextcloud) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	a, err := ncAdmin(t)
	if err != nil {
		return info, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.BaseURL, "/")+"/status.php", nil)
	if err != nil {
		return info, err
	}
	res, err := t.HTTP.Do(req)
	if err != nil {
		return info, fmt.Errorf("cannot reach Nextcloud at %s: %w", t.BaseURL, err)
	}
	var st struct {
		Installed     bool   `json:"installed"`
		Maintenance   bool   `json:"maintenance"`
		VersionString string `json:"versionstring"`
	}
	derr := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&st)
	res.Body.Close()
	if derr != nil || !st.Installed {
		return info, fmt.Errorf("%s/status.php is not a working Nextcloud (HTTP %d)", strings.TrimRight(t.BaseURL, "/"), res.StatusCode)
	}
	info.Version = st.VersionString
	if st.Maintenance {
		return info, errors.New("Nextcloud is in maintenance mode")
	}
	var me ncUser
	if err := c.ocs(ctx, t, http.MethodGet, "/ocs/v2.php/cloud/user", a, nil, &me); err != nil {
		return info, ncNeedAdmin(err)
	}
	if !me.admin() {
		return info, fmt.Errorf("%s is not in Nextcloud's admin group: Core needs an admin's app password", me.ID)
	}
	list, err := c.users(ctx, t, a)
	if err != nil {
		return info, err
	}
	info.Admin, info.Users = true, len(list)
	var miss []string
	if strings.TrimSpace(t.Fields["ADMIN_PASSWORD"]) == "" {
		miss = append(miss, "adding accounts and sign-in need the admin's login password (\"Login password (connector only)\"): Nextcloud refuses app passwords there")
	} else if w, _ := ncAdminWrite(t); w.pass != "" {
		if err := c.ocs(ctx, t, http.MethodGet, "/ocs/v2.php/cloud/user", w, nil, nil); err != nil {
			return info, fmt.Errorf("the login password (connector only) does not work for %s: %w", w.user, ncNeedAdmin(err))
		}
	}
	if apps, err := c.enabledApps(ctx, t, a); err == nil {
		if !apps["user_oidc"] {
			miss = append(miss, `sign-in needs the "OpenID Connect user backend" app (user_oidc)`)
		}
		if !apps["webhook_listeners"] {
			miss = append(miss, `events need the "Webhook Listeners" app (webhook_listeners)`)
		}
	}
	info.Detail = strings.Join(miss, "; ")
	return info, nil
}

// ncUID makes a Nextcloud user id from an email's local part.
var ncUIDBad = regexp.MustCompile(`[^a-z0-9_.@\-]+`)

func ncUID(email string) string {
	local := strings.SplitN(strings.ToLower(email), "@", 2)[0]
	return strings.Trim(ncUIDBad.ReplaceAllString(local, "-"), "-.")
}

func (c nextcloud) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	a, err := ncAdmin(t)
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
	uid := ncUID(email)
	for _, u := range list {
		if u.email() == email {
			// Someone already has this email: link, never touch it.
			note := "a Nextcloud account with this email already exists; linked without minting an app password (the person can add their own in Core)"
			if u.admin() {
				note = "a Nextcloud admin account with this email already exists; linked and left untouched (no app password minted)"
			}
			return Account{RemoteID: u.ID, Status: StatusLinked, Note: note}, nil
		}
	}
	for _, u := range list {
		if strings.EqualFold(u.ID, uid) {
			return Account{}, fmt.Errorf("a Nextcloud account named %q already exists with another email (%s); link the person to it by hand, or give it their email in Nextcloud", u.ID, u.email())
		}
	}
	if uid == "" {
		return Account{}, fmt.Errorf("cannot make a Nextcloud user name from %s", email)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = uid
	}
	w, err := ncAdminWrite(t)
	if err != nil {
		return Account{}, err
	}
	password := RandomPassword(40) // used to mint the app password, never stored, logged or returned
	body := map[string]any{"userid": uid, "password": password, "displayName": name, "email": email}
	if err := c.ocs(ctx, t, http.MethodPost, "/ocs/v2.php/cloud/users", w, body, nil, password); err != nil {
		return Account{}, ncNeedAdmin(err)
	}
	acc := Account{RemoteID: uid, Status: StatusOK, Created: true}
	// Open the account's files once now, so Nextcloud copies its welcome
	// files before Core links the person: their events are then not news.
	c.initFiles(ctx, t, ncAuth{user: uid, pass: password})
	if p.KeepOwnCredential {
		acc.Note = "account created; the person already has their own app password in Core, so none was minted"
		return acc, nil
	}
	// Nextcloud lets a user turn their password into an app password; the
	// password then goes out of scope.
	var ap struct {
		AppPassword string `json:"apppassword"`
	}
	req := ncAuth{user: uid, pass: password}
	if err := c.ocs(ctx, t, http.MethodGet, "/ocs/v2.php/core/getapppassword", req, nil, &ap, password); err != nil || ap.AppPassword == "" {
		if err == nil {
			err = errors.New("no app password in the answer")
		}
		return acc, fmt.Errorf("the Nextcloud account %s was created but minting its app password failed: %w; a retry will link the account without one", uid, err)
	}
	acc.Fields = map[string]string{"USERNAME": uid, "APP_PASSWORD": ap.AppPassword}
	return acc, nil
}

// initFiles signs in as the user once over WebDAV (Nextcloud sets up a
// new account's files, welcome files included, at its first sign-in).
// Best effort: a failure only means the welcome files come later.
func (nextcloud) initFiles(ctx context.Context, t Target, a ncAuth) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", strings.TrimRight(t.BaseURL, "/")+"/remote.php/dav/files/"+url.PathEscape(a.user)+"/", nil)
	if err != nil {
		return
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("User-Agent", NextcloudAppPasswordName)
	req.SetBasicAuth(a.user, a.pass)
	if res, err := t.HTTP.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
	}
}

// ---- linking by hand ----

func (c nextcloud) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	a, err := ncAdmin(t)
	if err != nil {
		return nil, err
	}
	list, err := c.users(ctx, t, a)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(list))
	for _, u := range list {
		name := u.ID
		if u.DisplayName != "" && u.DisplayName != u.ID {
			name = u.DisplayName + " (" + u.ID + ")"
		}
		if e := u.email(); e != "" {
			name += " · " + e
		}
		out = append(out, RemoteAccount{ID: u.ID, Name: name, Admin: u.admin(), Disabled: u.Enabled != nil && !*u.Enabled})
	}
	return out, nil
}

func (c nextcloud) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	a, err := ncAdmin(t)
	if err != nil {
		return Account{}, err
	}
	var u ncUser
	if err := c.ocs(ctx, t, http.MethodGet, "/ocs/v2.php/cloud/users/"+url.PathEscape(remoteID), a, nil, &u); err != nil {
		if ncStatus(err) == http.StatusNotFound {
			return Account{}, fmt.Errorf("Nextcloud has no account %q", remoteID)
		}
		return Account{}, ncNeedAdmin(err)
	}
	if u.ID == "" {
		u.ID = remoteID
	}
	return Account{RemoteID: u.ID, Status: StatusLinked, Note: "linked by hand; no app password minted (the person can add their own in Core)"}, nil
}

// ---- sign-in (user_oidc) ----

type ncProvider struct {
	ID         int64          `json:"id"`
	Identifier string         `json:"identifier"`
	ClientID   string         `json:"clientId"`
	Discovery  string         `json:"discoveryEndpoint"`
	Scope      string         `json:"scope"`
	Settings   map[string]any `json:"settings"`
}

const ncProviderPath = "/ocs/v2.php/apps/user_oidc/api/v1/provider"

func (c nextcloud) providers(ctx context.Context, t Target, a ncAuth) ([]ncProvider, error) {
	var list []ncProvider
	if err := c.ocs(ctx, t, http.MethodGet, ncProviderPath, a, nil, &list); err != nil {
		if ncStatus(err) == http.StatusNotFound {
			return nil, errors.New(`Nextcloud has no "OpenID Connect user backend" app (user_oidc): install it under Apps → Security (or occ app:install user_oidc), then switch sign-in on again`)
		}
		return nil, ncNeedAdmin(err)
	}
	return list, nil
}

func (c nextcloud) backup(t Target, what string, v any) (string, error) {
	if err := os.MkdirAll(t.DataDir, 0o700); err != nil {
		return "", fmt.Errorf("backup dir: %w", err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	name := filepath.Join(t.DataDir, "nextcloud-"+what+"-"+t.Clock().UTC().Format("20060102-150405.000")+".json")
	return name, os.WriteFile(name, b, 0o600)
}

func (c nextcloud) ConfigureSSO(ctx context.Context, t Target, sc SSOConfig) error {
	a, err := ncAdmin(t)
	if err != nil {
		return err
	}
	if sc.Enabled && (sc.Issuer == "" || sc.ClientID == "" || sc.ClientSecret == "") {
		return errors.New("sign-in needs Core's issuer and a client id and secret")
	}
	list, err := c.providers(ctx, t, a)
	if err != nil {
		if !sc.Enabled && strings.Contains(err.Error(), "user_oidc") {
			return nil // the app is gone: nothing of ours left to remove
		}
		return err
	}
	var ours *ncProvider
	for i := range list {
		if list[i].Identifier == NextcloudProvider {
			ours = &list[i]
		}
	}
	if !sc.Enabled && ours == nil {
		return nil
	}
	w, err := ncAdminWrite(t)
	if err != nil {
		return err
	}
	backup, err := c.backup(t, "sso", map[string]any{"providers": list})
	if err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	if !sc.Enabled {
		if err := c.ocs(ctx, t, http.MethodDelete, fmt.Sprintf("%s/%d", ncProviderPath, ours.ID), w, nil, nil); err != nil && ncStatus(err) != http.StatusNotFound {
			return fmt.Errorf("removing the Pocket Core provider failed (backup kept at %s): %w", backup, ncNeedAdmin(err))
		}
		return nil
	}
	body := map[string]any{
		"identifier":        NextcloudProvider,
		"clientId":          sc.ClientID,
		"clientSecret":      sc.ClientSecret,
		"discoveryEndpoint": strings.TrimRight(sc.Issuer, "/") + "/.well-known/openid-configuration",
		"scope":             "openid email profile",
		"settings": map[string]any{
			"mappingUid": "pocket_uid", // only sent for people Core linked: nobody lands in an account by name
			// Nextcloud keeps each account's own name and email: Core set them
			// on accounts it made, and a linked account is the person's own.
			// (Claims Core never sends, so user_oidc leaves both alone.)
			"mappingDisplayName": "pocket_keep_name",
			"mappingEmail":       "pocket_keep_email",
			"uniqueUid":          false, // the claim is the Nextcloud user id itself, not a hash
			"providerBasedId":    false,
			"groupProvisioning":  false, // never touch anyone's groups (or admin rights)
			"checkBearer":        false,
			"sendIdTokenHint":    true,
		},
	}
	method, path := http.MethodPost, ncProviderPath
	if ours != nil {
		method, path = http.MethodPut, fmt.Sprintf("%s/%d", ncProviderPath, ours.ID)
	}
	if err := c.ocs(ctx, t, method, path, w, body, nil, sc.ClientSecret); err != nil {
		return fmt.Errorf("saving the Pocket Core provider in Nextcloud failed (backup kept at %s): %w", backup, ncNeedAdmin(err))
	}
	return nil
}

// ---- events: push (webhook_listeners) ----

const ncHooksPath = "/ocs/v2.php/apps/webhook_listeners/api/v1/webhooks"

func (nextcloud) PushTriggers() []PushTrigger {
	return append([]PushTrigger(nil), nextcloudTriggers...)
}

// PushPerUser: webhooks are box-wide; each delivery names the account.
func (nextcloud) PushPerUser() bool { return false }

func ncHooksMissing(err error) error {
	if ncStatus(err) == http.StatusNotFound {
		return errors.New(`Nextcloud has no "Webhook Listeners" app (webhook_listeners): enable it under Apps, then switch events on again`)
	}
	return ncNeedAdmin(err)
}

func (c nextcloud) WhoAmI(ctx context.Context, t Target) (RemoteUser, error) {
	a, err := ncAdmin(t)
	if err != nil {
		return RemoteUser{}, err
	}
	var me ncUser
	if err := c.ocs(ctx, t, http.MethodGet, "/ocs/v2.php/cloud/user", a, nil, &me); err != nil {
		return RemoteUser{}, ncNeedAdmin(err)
	}
	return RemoteUser{ID: me.ID, Email: me.email()}, nil
}

func (c nextcloud) Subscribe(ctx context.Context, t Target, s Subscription) (string, error) {
	a, err := ncAdmin(t)
	if err != nil {
		return "", err
	}
	if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("Core's hook URL %q is not usable: set BASE_URL to the address Nextcloud reaches Core at", s.URL)
	}
	body := map[string]any{
		"httpMethod": "POST", "uri": s.URL, "event": s.Trigger,
		"authMethod": "header", "authData": map[string]string{s.HeaderName: s.HeaderValue},
	}
	if s.Trigger == ncFileCreated || s.Trigger == ncFileDeleted {
		// only a person's own files: not previews, versions, trash or upload chunks
		body["eventFilter"] = map[string]any{"event.node.path": `/^\/[^\/]+\/files\//`}
	}
	var out struct {
		ID json.Number `json:"id"`
	}
	if err := c.ocs(ctx, t, http.MethodPost, ncHooksPath, a, body, &out, s.HeaderValue); err != nil {
		return "", ncHooksMissing(err)
	}
	if out.ID == "" {
		return "", errors.New("Nextcloud created the webhook but returned no id")
	}
	return out.ID.String(), nil
}

// Subscriptions lists the webhooks: Core's own (they call a Core hook URL)
// are named after their trigger, anyone else's get a name Core never uses.
func (c nextcloud) Subscriptions(ctx context.Context, t Target) (map[string]string, error) {
	a, err := ncAdmin(t)
	if err != nil {
		return nil, err
	}
	var list []struct {
		ID    json.Number `json:"id"`
		URI   string      `json:"uri"`
		Event string      `json:"event"`
	}
	if err := c.ocs(ctx, t, http.MethodGet, ncHooksPath, a, nil, &list); err != nil {
		return nil, ncHooksMissing(err)
	}
	names := map[string]string{}
	for _, tr := range nextcloudTriggers {
		names[tr.Trigger] = tr.Name
	}
	out := make(map[string]string, len(list))
	for _, w := range list {
		name := "webhook " + w.ID.String()
		if n, ok := names[w.Event]; ok && strings.Contains(w.URI, ncHookPathPart) {
			name = n
		}
		out[w.ID.String()] = name
	}
	return out, nil
}

func (c nextcloud) Unsubscribe(ctx context.Context, t Target, remoteID string) error {
	a, err := ncAdmin(t)
	if err != nil {
		return err
	}
	if remoteID == "" {
		return nil
	}
	err = c.ocs(ctx, t, http.MethodDelete, ncHooksPath+"/"+url.PathEscape(remoteID), a, nil, nil)
	if err == nil || ncStatus(err) == http.StatusNotFound {
		return nil
	}
	return ncNeedAdmin(err)
}

// ncDelivery is what webhook_listeners POSTs.
type ncDelivery struct {
	User *struct {
		UID         string `json:"uid"`
		DisplayName string `json:"displayName"`
	} `json:"user"`
	Time  int64 `json:"time"`
	Event struct {
		Class string `json:"class"`
		Node  *struct {
			ID   json.Number `json:"id"`
			Path string      `json:"path"`
		} `json:"node"`
		CalendarID   json.Number     `json:"calendarId"`
		CalendarData json.RawMessage `json:"calendarData"`
		ObjectData   *struct {
			ID             json.Number `json:"id"`
			URI            string      `json:"uri"`
			LastModified   json.Number `json:"lastmodified"`
			CalendarData   string      `json:"calendardata"`
			Component      string      `json:"component"`
			Classification json.Number `json:"classification"`
		} `json:"objectData"`
	} `json:"event"`
}

func (nextcloud) Decode(t Target, trigger string, body []byte) (Delivery, error) {
	var in ncDelivery
	if err := json.Unmarshal(body, &in); err != nil {
		return Delivery{}, fmt.Errorf("not a Nextcloud webhook delivery: %w", err)
	}
	if in.Event.Class == "" {
		return Delivery{}, errors.New("not a Nextcloud webhook delivery: no event.class")
	}
	var d Delivery
	if in.Time > 0 {
		d.At = time.Unix(in.Time, 0)
	}
	if in.Event.Class != trigger {
		d.Drop = "delivery for " + in.Event.Class + " on the " + trigger + " hook"
		return d, nil
	}
	pub := strings.TrimRight(t.PublicURL, "/")
	actor := ""
	if in.User != nil {
		actor = in.User.UID
	}
	switch trigger {
	case ncFileCreated, ncFileDeleted:
		n := in.Event.Node
		if n == nil {
			return Delivery{}, errors.New("not a Nextcloud file delivery: no event.node")
		}
		m := ncFilesPath.FindStringSubmatch(n.Path)
		if m == nil {
			d.Drop = "not in a person's files"
			return d, nil
		}
		owner, rel := m[1], m[2]
		base := rel[strings.LastIndex(rel, "/")+1:]
		if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "~$") || strings.HasSuffix(base, ".part") {
			d.Drop = "hidden, lock or partial file"
			return d, nil
		}
		d.OwnerID = owner
		d.DedupeKey = fmt.Sprintf("%s|%s|%s|%d", trigger, owner, rel, in.Time)
		p := map[string]any{"nextcloud_user": owner, "path": rel, "name": base}
		if dir := strings.TrimSuffix(rel, base); dir != "" {
			p["folder"] = strings.TrimSuffix(dir, "/")
		}
		if actor != "" && actor != owner {
			p["by"] = actor
		}
		name := "file_created"
		if trigger == ncFileDeleted {
			name = "file_deleted"
		} else if id := n.ID.String(); id != "" {
			p["file_id"] = id
			if pub != "" {
				p["url"] = pub + "/f/" + id
			}
		}
		d.Event = Event{Name: name, Subject: "file:" + owner + "/" + rel, Payload: p}
		return d, nil
	case ncCalCreated, ncCalUpdated, ncCalTrashed:
		return decodeCalendar(in, trigger, pub, actor)
	}
	d.Drop = "unknown trigger " + trigger
	return d, nil
}

func decodeCalendar(in ncDelivery, trigger, pub, actor string) (Delivery, error) {
	var d Delivery
	if in.Time > 0 {
		d.At = time.Unix(in.Time, 0)
	}
	o := in.Event.ObjectData
	if o == nil {
		return Delivery{}, errors.New("not a Nextcloud calendar delivery: no event.objectData")
	}
	var cal map[string]any
	_ = json.Unmarshal(in.Event.CalendarData, &cal)
	principal, _ := cal["principaluri"].(string)
	calURI, _ := cal["uri"].(string)
	calName, _ := cal["{DAV:}displayname"].(string)
	owner := strings.TrimPrefix(principal, "principals/users/")
	if owner == "" || owner == principal {
		d.Drop = "calendar has no user owner"
		return d, nil
	}
	if calURI == "contact_birthdays" {
		d.Drop = "birthday calendar"
		return d, nil
	}
	ics := parseICS(o.CalendarData)
	kind := strings.ToLower(o.Component)
	if kind == "" {
		kind = ics.kind
	}
	if kind != "vevent" && kind != "vtodo" {
		d.Drop = "calendar object of type " + kind
		return d, nil
	}
	d.OwnerID = owner
	d.DedupeKey = fmt.Sprintf("%s|%s|%s", trigger, o.ID.String(), o.LastModified.String())
	p := map[string]any{"nextcloud_user": owner, "uid": ics.props["UID"], "calendar": calName}
	if p["uid"] == "" {
		p["uid"] = strings.TrimSuffix(o.URI, ".ics")
	}
	if actor != "" && actor != owner {
		p["by"] = actor
	}
	if cls := o.Classification.String(); cls != "" && cls != "0" {
		p["private"] = true // private / confidential: no title or place on the bus
	} else {
		setStr(p, "title", strPtr(ics.props["SUMMARY"]))
		setStr(p, "location", strPtr(ics.props["LOCATION"]))
	}
	if v, allDay := ics.time("DTSTART"); v != "" {
		p["start"], p["all_day"] = v, allDay
	}
	if kind == "vevent" {
		if v, _ := ics.time("DTEND"); v != "" {
			p["end"] = v
		}
		if ics.props["RRULE"] != "" {
			p["recurring"] = true
		}
		if pub != "" {
			p["url"] = pub + "/apps/calendar"
		}
	} else {
		if v, _ := ics.time("DUE"); v != "" {
			p["due"] = v
		}
		if st := ics.props["STATUS"]; st != "" {
			p["status"] = strings.ToLower(st)
		}
		if pub != "" {
			p["url"] = pub + "/apps/tasks"
		}
	}
	var name string
	switch {
	case kind == "vevent" && trigger == ncCalCreated:
		name = "calendar_event_created"
	case kind == "vevent" && trigger == ncCalUpdated:
		name = "calendar_event_updated"
	case kind == "vevent":
		name = "calendar_event_deleted"
	case trigger == ncCalCreated:
		name = "task_created"
	case trigger == ncCalUpdated && strings.EqualFold(ics.props["STATUS"], "COMPLETED"):
		name = "task_completed"
	case trigger == ncCalUpdated:
		name = "task_updated"
	default:
		name = "task_deleted"
	}
	d.Event = Event{Name: name, Subject: "calendar:" + owner + "/" + fmt.Sprint(p["uid"]), Payload: p}
	return d, nil
}

func strPtr(s string) *string { return &s }

// icsObject is the first VEVENT or VTODO of a calendar object (the master
// of a recurring one): its properties (unescaped) and their parameters.
type icsObject struct {
	kind   string
	props  map[string]string
	params map[string]string
}

func parseICS(s string) icsObject {
	o := icsObject{props: map[string]string{}, params: map[string]string{}}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n ", "")
	s = strings.ReplaceAll(s, "\n\t", "")
	depth := 0
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(line, "BEGIN:"):
			comp := strings.ToLower(strings.TrimSpace(line[6:]))
			if o.kind == "" && (comp == "vevent" || comp == "vtodo") {
				o.kind, depth = comp, 1
			} else if depth > 0 {
				depth++ // VALARM inside the object: skip its properties
			}
			continue
		case strings.HasPrefix(line, "END:"):
			if depth > 0 {
				depth--
				if depth == 0 {
					return o
				}
			}
			continue
		}
		if depth != 1 {
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		head, val := line[:i], line[i+1:]
		name, par := head, ""
		if j := strings.Index(head, ";"); j >= 0 {
			name, par = head[:j], head[j+1:]
		}
		name = strings.ToUpper(name)
		if _, seen := o.props[name]; seen {
			continue
		}
		r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
		o.props[name] = strings.TrimSpace(r.Replace(val))
		o.params[name] = par
	}
	return o
}

// time renders a DTSTART-like property: a date for all-day values, local
// time plus zone for TZID values, RFC 3339 for UTC ones.
func (o icsObject) time(name string) (string, bool) {
	v := o.props[name]
	par := strings.ToUpper(o.params[name])
	if v == "" {
		return "", false
	}
	if strings.Contains(par, "VALUE=DATE") && !strings.Contains(par, "VALUE=DATE-TIME") || len(v) == 8 {
		if t, err := time.Parse("20060102", v); err == nil {
			return t.Format("2006-01-02"), true
		}
		return v, true
	}
	if t, err := time.Parse("20060102T150405Z", v); err == nil {
		return t.Format(time.RFC3339), false
	}
	if t, err := time.Parse("20060102T150405", v); err == nil {
		out := t.Format("2006-01-02T15:04:05")
		for _, p := range strings.Split(o.params[name], ";") {
			if strings.HasPrefix(strings.ToUpper(p), "TZID=") {
				tz := strings.Trim(p[5:], `"`)
				if loc, err := time.LoadLocation(tz); err == nil {
					return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc).Format(time.RFC3339), false
				}
				return out + " " + tz, false
			}
		}
		return out, false
	}
	return v, false
}

// ---- poll: accounts ----

type ncState struct {
	Users map[string]ncUserState `json:"users"`
}

type ncUserState struct {
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

func (c nextcloud) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	a, err := ncAdmin(t)
	if err != nil {
		return nil, state, err
	}
	list, err := c.users(ctx, t, a)
	if err != nil {
		return nil, state, err
	}
	cur := ncState{Users: map[string]ncUserState{}}
	for _, u := range list {
		cur.Users[u.ID] = ncUserState{Email: u.email(), Name: u.DisplayName}
	}
	next, _ := json.Marshal(cur)
	var prev ncState
	if len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &prev) != nil || prev.Users == nil {
		return nil, next, nil // first poll: baseline only
	}
	var out []Event
	ids := make([]string, 0, len(cur.Users))
	for id := range cur.Users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, known := prev.Users[id]; !known {
			u := cur.Users[id]
			out = append(out, Event{Name: "user_created", Subject: "user:" + id, UserEmail: u.Email, RemoteUserID: id, Payload: map[string]any{"nextcloud_user": id, "name": u.Name}})
		}
	}
	gone := make([]string, 0)
	for id := range prev.Users {
		if _, still := cur.Users[id]; !still {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		out = append(out, Event{Name: "user_deleted", Subject: "user:" + id, Payload: map[string]any{"nextcloud_user": id, "name": prev.Users[id].Name}})
	}
	return out, next, nil
}
