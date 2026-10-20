package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kavita (0.9.1+, auth keys) connector.
//
// Every call that needs a key sends it in the x-api-key header, never in a
// URL (Kavita writes a failed /api/Plugin/authenticate?apiKey=… to its log,
// key included, so Core does not use that route).
//
// Endpoints used:
//
//	GET  /api/health                          Test                              read  (no key)
//	GET  /api/Account                         Test, RotateKey (who a key is)    read
//	GET  /api/Users?includePending=true       Test, Provision, Link, SSO, Poll, RotateKey  read
//	GET  /api/Settings                        Test, ConfigureSSO (backup)       read
//	GET  /api/Library/libraries               Provision (libraries to grant)    read
//	GET  /api/Account/invite-url              Provision, an unfinished invite   read
//	POST /api/Account/invite                  Provision, missing user           WRITE
//	POST /api/Account/confirm-email           Provision (finish the invite)     WRITE (no key: the invite token)
//	POST /api/Account/create-auth-key         Provision, RotateKey              WRITE (as the person)
//	GET  /api/Account/auth-keys               RotateKey                         read  (as the person)
//	POST /api/Account/rotate-auth-key         RotateKey                         WRITE (as the person)
//	POST /api/Account/reset-password          RotateKey, stored key dead        WRITE
//	POST /api/Account/login                   RotateKey, stored key dead        read  (opens a session)
//	POST /api/Settings                        ConfigureSSO                      WRITE
//	GET  /api/Activity/current                Poll (reading sessions)           read
//	GET  /api/Series/volume?volumeId=         Poll (was a whole book finished)  read
//	GET  /api/Series/:id                      Poll (was a whole book finished)  read
//	POST /api/Series/all-v2                   Poll (new series; a search)       read
//
// Accounts carry emails, so a person matches the account with their email.
// The box-wide credential is an administrator's auth key (User Settings →
// Auth Keys). Kavita's OpenID sign-in matches an account by the provider's
// subject, then by email; it reads the provider from appsettings.json at
// start, so a provider change needs a restart of Kavita.
type kavita struct{}

func init() { Register(kavita{}) }

// KavitaKeyName is the auth key Core makes for each person it creates.
const KavitaKeyName = "Pocket Core"

// Kavita's role names (PolicyConstants).
const (
	kvRoleAdmin = "Admin"
	kvRoleLogin = "Login"
)

// kvMaxSeriesEvents: more new series than this in one poll become one event.
const kvMaxSeriesEvents = 10

// kvMaxFinished: book_finished events per poll at most.
const kvMaxFinished = 10

func (kavita) Type() string { return "kavita" }

func (kavita) Capabilities() Capabilities {
	return Capabilities{Provision: true, SSO: true, Events: true, Link: true}
}

// SSORedirects: Kavita's OpenID callback (ASP.NET's CallbackPath), under the
// public URL including any base URL.
func (kavita) SSORedirects(publicURL string) []string {
	return []string{strings.TrimRight(publicURL, "/") + "/signin-oidc"}
}

func kvKey(t Target) (string, error) {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return "", errors.New("the Kavita service in Core needs an API_KEY: in Kavita, signed in as an admin, open User Settings → Auth Keys, create a key and paste it under Services → Kavita → Edit")
	}
	return k, nil
}

// call does one request; key "" = no key, bearer = a session token.
func (kavita) call(ctx context.Context, t Target, method, path, key, bearer string, body, out any, secrets ...string) error {
	hdr := map[string]string{}
	if key != "" {
		hdr["x-api-key"] = key
	}
	if bearer != "" {
		hdr["Authorization"] = "Bearer " + bearer
	}
	_, err := doJSON(ctx, t, "Kavita", method, path, hdr, body, out, append(secrets, key, bearer)...)
	return err
}

// admin does one request with the box-wide key.
func (c kavita) admin(ctx context.Context, t Target, method, path string, body, out any, secrets ...string) error {
	k, err := kvKey(t)
	if err != nil {
		return err
	}
	err = c.call(ctx, t, method, path, k, "", body, out, secrets...)
	switch appStatus(err) {
	case http.StatusUnauthorized:
		return errors.New("Kavita rejected the stored API key (HTTP 401): it is wrong, expired or deleted; create a new one under User Settings → Auth Keys while signed in as an admin")
	case http.StatusForbidden:
		return fmt.Errorf("the stored Kavita key is not an admin's (HTTP 403): create the auth key while signed in as a Kavita admin (%v)", err)
	}
	return err
}

type kvUser struct {
	ID        int      `json:"id"`
	Username  string   `json:"username"`
	Email     string   `json:"email"`
	IsPending bool     `json:"isPending"`
	Roles     []string `json:"roles"`
}

func (u kvUser) admin() bool    { return contains(u.Roles, kvRoleAdmin) }
func (u kvUser) disabled() bool { return !u.admin() && !contains(u.Roles, kvRoleLogin) }
func (u kvUser) sid() string    { return strconv.Itoa(u.ID) }

// users lists every account, invites not yet accepted included (isPending).
func (c kavita) users(ctx context.Context, t Target) ([]kvUser, error) {
	var list []kvUser
	if err := c.admin(ctx, t, http.MethodGet, "/api/Users?includePending=true", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func kvByEmail(list []kvUser, email string) *kvUser {
	for i := range list {
		if e := strings.ToLower(strings.TrimSpace(list[i].Email)); e != "" && e == email {
			return &list[i]
		}
	}
	return nil
}

// kvMe is Kavita's UserDto (GET /api/Account, confirm-email, login).
type kvMe struct {
	ID            int      `json:"id"`
	Username      string   `json:"username"`
	Email         string   `json:"email"`
	Roles         []string `json:"roles"`
	Token         string   `json:"token"`
	KavitaVersion string   `json:"kavitaVersion"`
}

type kvAuthKey struct {
	ID       int    `json:"id"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Provider int    `json:"provider"` // 0 user, 1 system (opds, image-only)
}

// settings reads the server settings, numbers kept as written.
func (c kavita) settings(ctx context.Context, t Target) (map[string]any, error) {
	var raw []byte
	if err := c.admin(ctx, t, http.MethodGet, "/api/Settings", nil, &raw); err != nil {
		return nil, fmt.Errorf("reading Kavita's server settings failed: %w", err)
	}
	m, err := kvDecode(raw)
	if err != nil {
		return nil, fmt.Errorf("Kavita's server settings: %w", err)
	}
	return m, nil
}

func kvDecode(raw []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("empty answer")
	}
	return m, nil
}

func kvStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// kvEmailSetUp mirrors ServerSettingDto.IsEmailSetup: Kavita then mails its own invites.
func kvEmailSetUp(s map[string]any) bool {
	smtp, _ := s["smtpConfig"].(map[string]any)
	return kvStr(smtp["host"]) != "" && kvStr(smtp["senderAddress"]) != "" && kvStr(s["hostName"]) != ""
}

func (c kavita) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	k, err := kvKey(t)
	if err != nil {
		return info, err
	}
	var health []byte
	if err := c.call(ctx, t, http.MethodGet, "/api/health", "", "", nil, &health); err != nil {
		return info, fmt.Errorf("cannot reach Kavita at %s: %w", t.BaseURL, err)
	}
	var me kvMe
	if err := c.call(ctx, t, http.MethodGet, "/api/Account", k, "", nil, &me); err != nil {
		if appStatus(err) == http.StatusUnauthorized {
			return info, errors.New("Kavita rejected the stored API key (HTTP 401): it is wrong, expired or deleted, or its account may not sign in; create a new one under User Settings → Auth Keys while signed in as an admin")
		}
		return info, err
	}
	info.Version = me.KavitaVersion
	if !contains(me.Roles, kvRoleAdmin) {
		return info, fmt.Errorf("the stored Kavita key belongs to %q, who is not an admin: create the auth key while signed in as a Kavita admin", me.Username)
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return info, err
	}
	info.Admin = true
	for _, u := range list {
		if !u.IsPending {
			info.Users++
		}
	}
	notes := []string{}
	if s, err := c.settings(ctx, t); err == nil {
		oidc, _ := s["oidcConfig"].(map[string]any)
		if a := kvStr(oidc["authority"]); a != "" {
			notes = append(notes, "Kavita's OpenID settings point at "+a+" (Kavita applies a provider change only after a restart)")
		}
		if b, _ := oidc["disablePasswordAuthentication"].(bool); b {
			notes = append(notes, "password sign-in is off for non-admins")
		}
		if kvEmailSetUp(s) {
			notes = append(notes, "Kavita has email set up: when Core makes an account, Kavita also mails its own invitation, whose link stops working once Core has finished the setup")
		}
	}
	info.Detail = strings.Join(notes, "; ")
	return info, nil
}

// kvUsername is the name Core gives a new account: the email's local part
// in Kavita's allowed characters, or the full email when that is taken (or
// empty). It never takes an existing account's name. The full email is the
// invitation's own spelling: Kavita skips its name check only when the name
// equals the email exactly (the invited account is already named so).
func kvUsername(email string, list []kvUser, self int) string {
	var b strings.Builder
	for _, r := range usernameFor(strings.ToLower(email)) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-._@+/", r) {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		return email
	}
	for _, u := range list {
		if u.ID != self && strings.EqualFold(u.Username, name) {
			return email
		}
	}
	return name
}

// kvToken reads the invite token from Kavita's confirm link.
func kvToken(link string) string {
	link = strings.TrimSpace(link)
	if strings.HasPrefix(link, `"`) {
		var s string
		if json.Unmarshal([]byte(link), &s) == nil {
			link = s
		}
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Query().Get("token")
}

func (c kavita) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		return Account{}, errors.New("person has no email")
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	u := kvByEmail(list, email)
	if u != nil && !u.IsPending {
		note := fmt.Sprintf("the Kavita account %q has this email; linked with its user id, no key minted (the person makes one under User Settings → Auth Keys, or it is set in Core by hand)", u.Username)
		if u.admin() {
			note = fmt.Sprintf("the Kavita admin %q has this email; linked with its user id and left untouched", u.Username)
		}
		return Account{RemoteID: u.sid(), Status: StatusLinked, Fields: map[string]string{"USER_ID": u.sid()}, Note: note}, nil
	}

	notes := []string{}
	var token, inviteEmail string
	self := 0
	if u != nil {
		// An invitation nobody accepted (Core's own earlier attempt, or an
		// admin's): finish it as this person.
		if u.admin() {
			return Account{}, fmt.Errorf("Kavita has an unaccepted admin invitation for %s: Core does not finish admin invitations; accept or delete it in Kavita (Settings → Users)", email)
		}
		self, inviteEmail = u.ID, u.Email
		var raw []byte
		if err := c.admin(ctx, t, http.MethodGet, "/api/Account/invite-url?userId="+u.sid()+"&withBaseUrl=false", nil, &raw); err != nil {
			return Account{}, fmt.Errorf("Kavita has an unaccepted invitation for %s but reading it failed: %w", email, err)
		}
		token = kvToken(string(raw))
		notes = append(notes, "finished Kavita's pending invitation for this email")
	} else {
		var libs []struct {
			ID int `json:"id"`
		}
		if err := c.admin(ctx, t, http.MethodGet, "/api/Library/libraries", nil, &libs); err != nil {
			return Account{}, fmt.Errorf("listing Kavita's libraries failed: %w", err)
		}
		ids := make([]int, 0, len(libs))
		for _, l := range libs {
			ids = append(ids, l.ID)
		}
		// Login only (Kavita's own default; it adds its base "Pleb" role), every
		// library the admin key sees, no age restriction.
		in := map[string]any{"email": email, "roles": []string{kvRoleLogin}, "libraries": ids,
			"ageRestriction": map[string]any{"ageRating": -1, "includeUnknowns": true}}
		var inv struct {
			EmailLink string `json:"emailLink"`
			EmailSent bool   `json:"emailSent"`
		}
		if err := c.admin(ctx, t, http.MethodPost, "/api/Account/invite", in, &inv); err != nil {
			return Account{}, fmt.Errorf("inviting %s to Kavita failed: %w", email, err)
		}
		token, inviteEmail = kvToken(inv.EmailLink), email
		if inv.EmailSent {
			notes = append(notes, "Kavita also mailed its own invitation (email is set up there); its link no longer works")
		}
	}
	if token == "" {
		return Account{}, fmt.Errorf("Kavita invited %s but its answer carried no invite token; a retry finishes the invitation", email)
	}
	name := kvUsername(inviteEmail, list, self)
	password := RandomPassword(40) // nobody knows it: the person signs in with Pocket Core
	var me kvMe
	in := map[string]any{"email": inviteEmail, "token": token, "password": password, "username": name}
	if err := c.call(ctx, t, http.MethodPost, "/api/Account/confirm-email", "", "", in, &me, password, token); err != nil {
		return Account{}, fmt.Errorf("Kavita invited %s but finishing the account %q failed (a retry finishes it): %w", email, name, err)
	}
	if me.ID == 0 {
		return Account{}, fmt.Errorf("setting up the Kavita account %q returned no user id", name)
	}
	id := strconv.Itoa(me.ID)
	acc := Account{RemoteID: id, Status: StatusOK, Created: true, Fields: map[string]string{"USER_ID": id}}
	if p.KeepOwnCredential {
		acc.Note = strings.Join(append(notes, "account created; the person already has their own Kavita key in Core, so none was minted"), "; ")
		return acc, nil
	}
	var key kvAuthKey
	kin := map[string]any{"keyLength": 32, "name": KavitaKeyName}
	if err := c.call(ctx, t, http.MethodPost, "/api/Account/create-auth-key", "", me.Token, kin, &key); err != nil || key.Key == "" {
		if err == nil {
			err = errors.New("no key in the answer")
		}
		return acc, fmt.Errorf("the Kavita account %q was created but making its auth key failed: %w; a retry will link the account by email", name, err)
	}
	acc.Fields["API_KEY"] = key.Key
	acc.Note = strings.Join(append(notes, fmt.Sprintf("account %q created with a random password nobody knows and its own auth key %q: the person signs in with Pocket Core, or an admin sets a password in Kavita", name, KavitaKeyName)), "; ")
	return acc, nil
}

// RemoteAccounts lists Kavita accounts for linking by hand (accepted ones).
func (c kavita) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(list))
	for _, u := range list {
		if u.IsPending {
			continue
		}
		n := u.Username
		if u.Email != "" && !strings.EqualFold(u.Email, u.Username) {
			n += " · " + u.Email
		}
		out = append(out, RemoteAccount{ID: u.sid(), Name: n, Admin: u.admin(), Disabled: u.disabled()})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Link ties a person to an existing Kavita account chosen by an admin.
func (c kavita) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	for _, u := range list {
		if u.sid() == strings.TrimSpace(remoteID) {
			if u.IsPending {
				return Account{}, fmt.Errorf("the Kavita account %q is an invitation nobody accepted yet", u.Username)
			}
			return Account{RemoteID: u.sid(), Status: StatusLinked, Fields: map[string]string{"USER_ID": u.sid()},
				Note: fmt.Sprintf("linked by hand to the Kavita account %q", u.Username)}, nil
		}
	}
	return Account{}, fmt.Errorf("Kavita has no account %s", remoteID)
}

// ---- replacing a person's key ----

// RotateKey replaces the person's stored auth key in place (Kavita's
// rotate-auth-key keeps the key's id and name and kills the old value),
// acting as the person with that key. When the stored key no longer works,
// Core sets a new random password (nobody knew the old one: Core made it),
// signs in once and rotates or makes the "Pocket Core" key.
func (c kavita) RotateKey(ctx context.Context, t Target, a AccountRef) (Rotated, error) {
	var out Rotated
	list, err := c.users(ctx, t)
	if err != nil {
		return out, err
	}
	var user *kvUser
	for i := range list {
		if list[i].sid() == a.RemoteID {
			user = &list[i]
		}
	}
	if user == nil {
		return out, fmt.Errorf("Kavita has no account %s any more", a.RemoteID)
	}
	if user.admin() {
		return out, fmt.Errorf("the Kavita account %q is an admin: Core leaves its keys alone", user.Username)
	}
	old := strings.TrimSpace(a.Fields["API_KEY"])
	if old != "" && old == strings.TrimSpace(t.Fields["API_KEY"]) {
		return out, errors.New("the stored key is the Kavita service's own admin key: Core never replaces it")
	}
	key, bearer := "", "" // how Core acts as the person
	if old != "" {
		var me kvMe
		if err := c.call(ctx, t, http.MethodGet, "/api/Account", old, "", nil, &me); err == nil {
			if strconv.Itoa(me.ID) != a.RemoteID {
				return out, fmt.Errorf("the stored key belongs to another Kavita account (%d, not %s): Core leaves it alone", me.ID, a.RemoteID)
			}
			key = old
		} else if appStatus(err) != http.StatusUnauthorized {
			return out, err
		}
	}
	if key == "" {
		password := RandomPassword(40)
		if err := c.admin(ctx, t, http.MethodPost, "/api/Account/reset-password", map[string]any{"userName": user.Username, "password": password, "oldPassword": ""}, nil, password); err != nil {
			return out, fmt.Errorf("the stored key no longer works and setting a new password on the Kavita account failed: %w", err)
		}
		var me kvMe
		if err := c.call(ctx, t, http.MethodPost, "/api/Account/login", "", "", map[string]any{"username": user.Username, "password": password}, &me, password); err != nil || me.Token == "" {
			if err == nil {
				err = errors.New("no session token in the answer")
			}
			return out, fmt.Errorf("signing in as the Kavita account to replace its key failed (is password sign-in turned off in Kavita's OpenID settings?): %w", err)
		}
		bearer = me.Token
		out.Note = "the stored key no longer worked, so Core set a new random password on the account to sign in as it"
	}
	var keys []kvAuthKey
	if err := c.call(ctx, t, http.MethodGet, "/api/Account/auth-keys", key, bearer, nil, &keys, old); err != nil {
		return out, fmt.Errorf("listing the account's Kavita auth keys failed: %w", err)
	}
	target := 0
	for _, k := range keys {
		if old != "" && k.Key == old {
			target = k.ID
		}
	}
	if target == 0 && key == "" {
		for _, k := range keys {
			if k.Name == KavitaKeyName && k.Provider == 0 {
				target = k.ID
			}
		}
	}
	var made kvAuthKey
	body := map[string]any{"keyLength": 32, "name": KavitaKeyName}
	if target != 0 {
		err = c.call(ctx, t, http.MethodPost, "/api/Account/rotate-auth-key?authKeyId="+strconv.Itoa(target), key, bearer, body, &made, old)
	} else {
		err = c.call(ctx, t, http.MethodPost, "/api/Account/create-auth-key", key, bearer, body, &made, old)
	}
	if err != nil || made.Key == "" {
		if err == nil {
			err = errors.New("no key in the answer")
		}
		return out, fmt.Errorf("making the new Kavita key failed: %w", err)
	}
	out.Fields = map[string]string{"API_KEY": made.Key, "USER_ID": a.RemoteID}
	if old != "" && c.call(ctx, t, http.MethodGet, "/api/Account", old, "", nil, nil, made.Key) == nil {
		return out, errors.New(OldStillWorks)
	}
	return out, nil
}

// ---- Sign in with Pocket Core (built-in OpenID) ----

func (c kavita) backupPath(t Target, what string) string {
	return filepath.Join(t.DataDir, "kavita-"+what+".json")
}

func (c kavita) writeBackup(name string, v any) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return fmt.Errorf("backup dir: %w", err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(name, b, 0o600)
}

// kvBackupCopy is the settings as Core keeps them on disk: the SMTP password
// left out (Core never needs it; Kavita already hides the OpenID secret).
func kvBackupCopy(s map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		out[k] = v
	}
	if smtp, ok := s["smtpConfig"].(map[string]any); ok {
		cp := map[string]any{}
		for k, v := range smtp {
			cp[k] = v
		}
		if kvStr(cp["password"]) != "" {
			cp["password"] = "[not saved by Core]"
		}
		out["smtpConfig"] = cp
	}
	return out
}

// kvOurs is what Core last wrote, to recognise its own provider.
type kvOurs struct {
	Authority string `json:"authority"`
	ClientID  string `json:"client_id"`
}

// save posts the whole settings back with oidcConfig replaced.
func (c kavita) save(ctx context.Context, t Target, settings, oidc map[string]any, secrets ...string) error {
	body := map[string]any{}
	for k, v := range settings {
		body[k] = v
	}
	body["oidcConfig"] = oidc
	err := c.admin(ctx, t, http.MethodPost, "/api/Settings", body, nil, secrets...)
	if err != nil && appStatus(err) == http.StatusBadRequest && strings.Contains(strings.ToLower(err.Error()), "oidc") {
		return fmt.Errorf("Kavita refused the provider %q: it loads %s/.well-known/openid-configuration itself, needs https and the issuer to match exactly; make sure the Kavita container reaches that address (%v)", kvStr(oidc["authority"]), strings.TrimRight(kvStr(oidc["authority"]), "/"), err)
	}
	return err
}

// mismatched lists linked people whose Kavita account would not match their
// sign-in: Kavita matches by email, and Core does not rewrite accounts
// (Kavita's admin update replaces roles and libraries wholesale).
func (c kavita) mismatched(ctx context.Context, t Target, people map[string]string) ([]string, error) {
	if len(people) == 0 {
		return nil, nil
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return nil, err
	}
	emails := make([]string, 0, len(people))
	for e := range people {
		emails = append(emails, e)
	}
	sort.Strings(emails)
	var out []string
	for _, email := range emails {
		id := people[email]
		if id == "" {
			continue
		}
		for _, u := range list {
			if u.sid() != id || strings.EqualFold(strings.TrimSpace(u.Email), email) {
				continue
			}
			if other := kvByEmail(list, strings.ToLower(email)); other != nil {
				out = append(out, fmt.Sprintf("%s is linked to %q but the Kavita account %q has that email", email, u.Username, other.Username))
			} else {
				out = append(out, fmt.Sprintf("set the email of the Kavita account %q to %s (Settings → Users → Edit)", u.Username, email))
			}
		}
	}
	return out, nil
}

func (c kavita) ConfigureSSO(ctx context.Context, t Target, sc SSOConfig) error {
	if sc.Enabled && (sc.Issuer == "" || sc.ClientID == "" || sc.ClientSecret == "") {
		return errors.New("sign-in needs Core's issuer and a client id and secret")
	}
	settings, err := c.settings(ctx, t)
	if err != nil {
		return err
	}
	current, ok := settings["oidcConfig"].(map[string]any)
	if !ok {
		return errors.New("Kavita's server settings have no OpenID section: it needs Kavita 0.8.7 or newer")
	}
	stamp := t.Clock().UTC().Format("20060102-150405.000")
	backup := c.backupPath(t, "sso-"+stamp)
	if err := c.writeBackup(backup, kvBackupCopy(settings)); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	original := c.backupPath(t, "sso-original")
	oursPath := c.backupPath(t, "sso-core")

	if !sc.Enabled {
		if b, err := os.ReadFile(original); err == nil {
			// put back what was there before Core took over
			prev, err := kvDecode(b)
			if err != nil {
				return fmt.Errorf("the saved sign-in settings %s are unreadable: %w", original, err)
			}
			want := map[string]any{}
			for k, v := range prev {
				want[k] = v
			}
			lostSecret := kvStr(prev["secret"]) != "" // Kavita only ever showed it as stars
			if lostSecret {
				want["secret"] = "" // keeps the button hidden instead of failing at the provider
			}
			if err := c.save(ctx, t, settings, want); err != nil {
				return fmt.Errorf("putting back the previous sign-in settings failed (backup kept at %s): %w", backup, err)
			}
			_ = os.Rename(original, c.backupPath(t, "sso-original-restored-"+stamp))
			_ = os.Remove(oursPath)
			if lostSecret {
				return fmt.Errorf("Kavita's OpenID settings are back to %s (client %q), but Kavita never reveals a client secret, so Core left it empty and the sign-in button is hidden: enter that provider's secret under Settings → OpenID Connect and restart Kavita (switching sign-in off again here finishes)", kvStr(prev["authority"]), kvStr(prev["clientId"]))
			}
			return nil
		}
		// nothing saved to go back to: switch off only Core's own provider
		var ours kvOurs
		if b, err := os.ReadFile(oursPath); err == nil && json.Unmarshal(b, &ours) == nil &&
			ours.Authority != "" && kvStr(current["authority"]) == ours.Authority && kvStr(current["clientId"]) == ours.ClientID {
			want := map[string]any{}
			for k, v := range current {
				want[k] = v
			}
			want["authority"], want["clientId"], want["secret"] = "", "", ""
			if err := c.save(ctx, t, settings, want); err != nil {
				return fmt.Errorf("turning OpenID sign-in off failed (backup kept at %s): %w", backup, err)
			}
			_ = os.Remove(oursPath)
		}
		return nil
	}

	if problems, err := c.mismatched(ctx, t, sc.People); err != nil {
		return err
	} else if len(problems) > 0 {
		return fmt.Errorf("Kavita matches a sign-in to an account by email, and these linked people would not find theirs: %s; then switch sign-in on again", strings.Join(problems, "; "))
	}
	if kvStr(current["authority"]) != sc.Issuer {
		// someone else's provider (or none): keep it to put back later
		if err := c.writeBackup(original, current); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	}
	btn := sc.ButtonText
	if btn == "" {
		btn = "Sign in with Pocket Core"
	}
	want := map[string]any{}
	for k, v := range current {
		want[k] = v
	}
	// Core decides who may sign in (the service's audience): Kavita makes no
	// accounts on sign-in and never changes roles, libraries or age rating
	// from claims. Custom scopes belonged to the previous provider. Password
	// sign-in, auto-login and the verified-email rule stay as they were.
	want["authority"] = sc.Issuer // exactly Core's issuer: Kavita compares it with the discovery document
	want["clientId"] = sc.ClientID
	want["secret"] = sc.ClientSecret
	want["providerName"] = btn
	want["provisionAccounts"] = false
	want["syncUserSettings"] = false
	want["customScopes"] = []string{}
	if err := c.save(ctx, t, settings, want, sc.ClientSecret); err != nil {
		return fmt.Errorf("saving the sign-in settings failed (backup kept at %s): %w", backup, err)
	}
	if err := c.writeBackup(oursPath, kvOurs{Authority: sc.Issuer, ClientID: sc.ClientID}); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

// ---- events (polled) ----

// kvOpen is one reading session Core is watching.
type kvOpen struct {
	User     int      `json:"u"`
	Name     string   `json:"n,omitempty"`
	Start    string   `json:"s,omitempty"`
	Last     string   `json:"l,omitempty"`
	Quiet    bool     `json:"q,omitempty"` // already running at the baseline: no reading_ended
	Series   []int    `json:"sr,omitempty"`
	Titles   []string `json:"t,omitempty"`
	Lib      int      `json:"lib,omitempty"` // first series' library, for the link
	Pages    int      `json:"p,omitempty"`
	Words    int      `json:"w,omitempty"`
	Finished []int    `json:"f,omitempty"` // chapters finished in this session
}

type kvState struct {
	Init       bool              `json:"init"`
	Users      []int             `json:"users"`
	LastSeries int               `json:"last_series"` // highest series id seen
	Open       map[string]kvOpen `json:"open,omitempty"`
}

type kvActivity struct {
	ChapterID    int     `json:"chapterId"`
	VolumeID     int     `json:"volumeId"`
	SeriesID     int     `json:"seriesId"`
	LibraryID    int     `json:"libraryId"`
	EndPage      int     `json:"endPage"`
	StartTimeUtc string  `json:"startTimeUtc"`
	EndTimeUtc   *string `json:"endTimeUtc"`
	PagesRead    int     `json:"pagesRead"`
	WordsRead    int     `json:"wordsRead"`
	TotalPages   int     `json:"totalPages"`
	LibraryName  string  `json:"libraryName"`
	SeriesName   string  `json:"seriesName"`
	ChapterTitle string  `json:"chapterTitle"`
	ClientInfo   *struct {
		Browser    string `json:"browser"`
		DeviceType string `json:"deviceType"`
		AppVersion string `json:"appVersion"`
	} `json:"clientInfo"`
}

// done mirrors Kavita's own rule for a completed chapter (CloseSession).
func (a kvActivity) done() bool { return a.TotalPages > 0 && a.EndPage >= a.TotalPages }

type kvSession struct {
	ID           int          `json:"id"`
	StartTimeUtc string       `json:"startTimeUtc"`
	ActivityData []kvActivity `json:"activityData"`
	UserID       int          `json:"userId"`
	Username     string       `json:"username"`
}

type kvSeries struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	LibraryID   int    `json:"libraryId"`
	LibraryName string `json:"libraryName"`
	Format      int    `json:"format"`
	Pages       int    `json:"pages"`
}

var kvFormats = map[int]string{0: "image", 1: "archive", 3: "epub", 4: "pdf"}

// kvTime reads Kavita's UTC times, written with or without a zone.
func kvTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02T15:04:05.999999999", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func kvRFC(s string) string {
	if t := kvTime(s); !t.IsZero() {
		return t.Format(time.RFC3339)
	}
	return ""
}

func (c kavita) seriesURL(t Target, lib, series int) string {
	pub := strings.TrimRight(t.PublicURL, "/")
	if pub == "" || series == 0 {
		return pub
	}
	return fmt.Sprintf("%s/library/%d/series/%d", pub, lib, series)
}

func kvHasInt(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

type kvVolume struct {
	Name     string `json:"name"`
	Pages    int    `json:"pages"`
	Chapters []struct {
		ID int `json:"id"`
	} `json:"chapters"`
}

// bookTitle says whether finishing this chapter finished a whole book: the
// chapter is its volume's only one (an epub, a pdf, a single-file volume).
// The title is the series, plus the volume when the series has more.
func (c kavita) bookTitle(ctx context.Context, t Target, a kvActivity, vols map[int]*kvVolume, series map[int]kvSeries) (string, string, bool) {
	vol, seen := vols[a.VolumeID]
	if !seen {
		var v kvVolume
		if err := c.admin(ctx, t, http.MethodGet, "/api/Series/volume?volumeId="+strconv.Itoa(a.VolumeID), nil, &v); err == nil {
			vol = &v
		}
		vols[a.VolumeID] = vol
	}
	if vol == nil || len(vol.Chapters) != 1 || vol.Chapters[0].ID != a.ChapterID {
		return "", "", false
	}
	s, ok := series[a.SeriesID]
	if !ok {
		if err := c.admin(ctx, t, http.MethodGet, "/api/Series/"+strconv.Itoa(a.SeriesID), nil, &s); err != nil {
			s = kvSeries{ID: a.SeriesID, Name: a.SeriesName}
		}
		series[a.SeriesID] = s
	}
	title := a.SeriesName
	if title == "" {
		title = s.Name
	}
	volume := ""
	if s.Pages > 0 && vol.Pages > 0 && s.Pages > vol.Pages {
		// one book of several: name the volume (Kavita's loose-leaf names are negative numbers)
		if n := strings.TrimSpace(vol.Name); n != "" && !strings.HasPrefix(n, "-") {
			volume = n
			title += " · " + n
		}
	}
	return title, volume, true
}

func (c kavita) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	users, err := c.users(ctx, t)
	if err != nil {
		return nil, state, err
	}
	var sessions []kvSession
	if err := c.admin(ctx, t, http.MethodGet, "/api/Activity/current", nil, &sessions); err != nil {
		return nil, state, err
	}
	// newest series first, every library the admin sees (a search: writes nothing)
	filter := map[string]any{"statements": []any{}, "combination": 1, "limitTo": 0,
		"sortOptions": map[string]any{"sortField": 2, "isAscending": false}}
	var recent []kvSeries
	if err := c.admin(ctx, t, http.MethodPost, "/api/Series/all-v2?PageNumber=1&PageSize=50", filter, &recent); err != nil {
		return nil, state, err
	}

	var prev kvState
	first := len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &prev) != nil || !prev.Init
	cur := kvState{Init: true, Open: map[string]kvOpen{}}
	byID := map[int]kvUser{}
	for _, u := range users {
		byID[u.ID] = u
		if !u.IsPending {
			cur.Users = append(cur.Users, u.ID)
		}
	}
	sort.Ints(cur.Users)
	for _, s := range recent {
		cur.LastSeries = max(cur.LastSeries, s.ID)
	}
	cur.LastSeries = max(cur.LastSeries, prev.LastSeries)

	var out []Event
	// accounts (accepted invitations only)
	if !first {
		had := map[int]bool{}
		for _, id := range prev.Users {
			had[id] = true
		}
		for _, id := range cur.Users {
			if !had[id] {
				u := byID[id]
				out = append(out, Event{Name: "user_created", Subject: "kavita-user:" + u.sid(), RemoteUserID: u.sid(),
					Payload: map[string]any{"title": "New Kavita account " + u.Username, "kavita_user_id": u.sid(), "kavita_user": u.Username, "admin": u.admin(), "url": strings.TrimRight(t.PublicURL, "/")}})
			}
			delete(had, id)
		}
		gone := make([]int, 0, len(had))
		for id := range had {
			gone = append(gone, id)
		}
		sort.Ints(gone)
		for _, id := range gone {
			sid := strconv.Itoa(id)
			out = append(out, Event{Name: "user_deleted", Subject: "kavita-user:" + sid, RemoteUserID: sid,
				Payload: map[string]any{"title": "Kavita account " + sid + " removed", "kavita_user_id": sid}})
		}
	}

	// reading: oldest session first
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	vols, seriesInfo := map[int]*kvVolume{}, map[int]kvSeries{}
	finishedBudget := kvMaxFinished
	for _, s := range sessions {
		key := strconv.Itoa(s.ID)
		o, known := prev.Open[key]
		if !known {
			o = kvOpen{User: s.UserID, Start: s.StartTimeUtc, Quiet: first}
		}
		o.Name = s.Username
		pages, words := 0, 0
		acts := append([]kvActivity(nil), s.ActivityData...)
		sort.SliceStable(acts, func(i, j int) bool { return kvTime(acts[i].StartTimeUtc).Before(kvTime(acts[j].StartTimeUtc)) })
		for _, a := range acts {
			pages += max(a.PagesRead, 0)
			words += max(a.WordsRead, 0)
			if a.EndTimeUtc != nil && kvTime(*a.EndTimeUtc).After(kvTime(o.Last)) {
				o.Last = *a.EndTimeUtc
			}
			uid := strconv.Itoa(s.UserID)
			if !kvHasInt(o.Series, a.SeriesID) && len(o.Series) < 10 {
				o.Series = append(o.Series, a.SeriesID)
				o.Titles = append(o.Titles, a.SeriesName)
				if o.Lib == 0 {
					o.Lib = a.LibraryID
				}
				if !first {
					p := map[string]any{"title": a.SeriesName, "series": a.SeriesName, "series_id": a.SeriesID, "library": a.LibraryName,
						"chapter_id": a.ChapterID, "started_at": kvRFC(a.StartTimeUtc), "url": c.seriesURL(t, a.LibraryID, a.SeriesID),
						"kavita_user_id": uid, "kavita_user": s.Username}
					setNonEmpty(p, "chapter", strings.TrimSpace(a.ChapterTitle))
					if a.ClientInfo != nil {
						setNonEmpty(p, "device", strings.TrimSpace(strings.Join(nonEmpty(a.ClientInfo.DeviceType, a.ClientInfo.Browser), " · ")))
					}
					out = append(out, Event{Name: "reading_started", Subject: fmt.Sprintf("kavita-reading:%d:%d", s.ID, a.SeriesID),
						RemoteUserID: uid, Personal: true, Payload: p})
				}
			}
			if a.done() && !kvHasInt(o.Finished, a.ChapterID) {
				if len(o.Finished) < 100 {
					o.Finished = append(o.Finished, a.ChapterID)
				}
				if first || finishedBudget == 0 {
					continue
				}
				title, volume, whole := c.bookTitle(ctx, t, a, vols, seriesInfo)
				if !whole {
					continue // a chapter of a longer volume: counted in reading_ended
				}
				finishedBudget--
				at := a.StartTimeUtc
				if a.EndTimeUtc != nil {
					at = *a.EndTimeUtc
				}
				p := map[string]any{"title": title, "series": a.SeriesName, "series_id": a.SeriesID, "volume_id": a.VolumeID,
					"chapter_id": a.ChapterID, "pages": a.TotalPages, "finished_at": kvRFC(at), "library": a.LibraryName,
					"url": c.seriesURL(t, a.LibraryID, a.SeriesID), "kavita_user_id": uid, "kavita_user": s.Username}
				setNonEmpty(p, "volume", volume)
				out = append(out, Event{Name: "book_finished", Subject: fmt.Sprintf("kavita-finished:%d:%d", s.UserID, a.ChapterID),
					RemoteUserID: uid, Personal: true, Payload: p})
			}
		}
		o.Pages, o.Words = max(o.Pages, pages), max(o.Words, words)
		if len(cur.Open) < 50 {
			cur.Open[key] = o
		}
	}

	// sessions Kavita closed since the last poll
	ended := make([]string, 0)
	for key := range prev.Open {
		if _, still := cur.Open[key]; !still {
			ended = append(ended, key)
		}
	}
	sort.Slice(ended, func(i, j int) bool { a, _ := strconv.Atoi(ended[i]); b, _ := strconv.Atoi(ended[j]); return a < b })
	for _, key := range ended {
		o := prev.Open[key]
		if o.Quiet || first {
			continue
		}
		uid := strconv.Itoa(o.User)
		titles := []string{}
		for _, x := range o.Titles {
			if x != "" {
				titles = append(titles, x)
			}
		}
		title := "Read in Kavita"
		if len(titles) > 0 {
			title = "Read " + strings.Join(titles, ", ")
		}
		p := map[string]any{"title": title, "series": titles, "series_ids": o.Series, "pages_read": o.Pages,
			"chapters_finished": len(o.Finished), "started_at": kvRFC(o.Start), "kavita_user_id": uid, "kavita_user": o.Name,
			"url": c.seriesURL(t, o.Lib, firstInt(o.Series))}
		if o.Words > 0 {
			p["words_read"] = o.Words
		}
		if e := kvTime(o.Last); !e.IsZero() {
			p["ended_at"] = e.Format(time.RFC3339)
			if s := kvTime(o.Start); !s.IsZero() && e.After(s) {
				p["minutes"] = int(e.Sub(s).Minutes() + 0.5)
			}
		}
		out = append(out, Event{Name: "reading_ended", Subject: "kavita-session:" + key, RemoteUserID: uid, Personal: true, Payload: p})
	}

	// new series, oldest first; a big import is one event
	if !first {
		var added []kvSeries
		for _, s := range recent {
			if s.ID > prev.LastSeries {
				added = append(added, s)
			}
		}
		sort.Slice(added, func(i, j int) bool { return added[i].ID < added[j].ID })
		if len(added) > kvMaxSeriesEvents {
			names := []string{}
			for _, s := range added[:kvMaxSeriesEvents] {
				names = append(names, s.Name)
			}
			count := strconv.Itoa(len(added))
			if len(added) >= len(recent) && len(recent) >= 50 {
				count += "+"
			}
			out = append(out, Event{Name: "series_added_bulk", Subject: fmt.Sprintf("kavita-series-bulk:%d", added[len(added)-1].ID),
				Payload: map[string]any{"title": count + " new series in Kavita", "count": len(added), "series": names, "url": strings.TrimRight(t.PublicURL, "/")}})
		} else {
			for _, s := range added {
				p := map[string]any{"title": s.Name, "series": s.Name, "series_id": s.ID, "library": s.LibraryName, "library_id": s.LibraryID,
					"pages": s.Pages, "url": c.seriesURL(t, s.LibraryID, s.ID)}
				setNonEmpty(p, "format", kvFormats[s.Format])
				out = append(out, Event{Name: "series_added", Subject: "kavita-series:" + strconv.Itoa(s.ID), Payload: p})
			}
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}

func nonEmpty(v ...string) []string {
	out := []string{}
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstInt(l []int) int {
	if len(l) > 0 {
		return l[0]
	}
	return 0
}
