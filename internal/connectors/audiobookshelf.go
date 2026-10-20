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
	"sort"
	"strings"
	"time"
)

// Audiobookshelf (2.26+, API keys) connector.
//
// Endpoints used:
//
//	GET   /status                         Test                          read  (no key)
//	GET   /api/users                      Test, Provision, Link, Poll   read
//	GET   /api/users/:id                  Poll (finished items)         read
//	POST  /api/users                      Provision, missing user       WRITE
//	POST  /api/api-keys                   Provision, the person's key   WRITE
//	PATCH /api/users/:id                  ConfigureSSO (linked email)   WRITE
//	PATCH /api/users/:id/openid-unlink    ConfigureSSO                  WRITE
//	GET   /api/auth-settings              Test, ConfigureSSO (backup)   read
//	PATCH /api/auth-settings              ConfigureSSO                  WRITE
//	GET   /api/sessions                   Poll (listening)              read
//	GET   /api/libraries                  Poll                          read
//	GET   /api/libraries/:id/items        Poll (new books, podcasts)    read
//	GET   /api/libraries/:id/recent-episodes  Poll (new episodes)       read
//	GET|POST /api/api-keys, DELETE /api/api-keys/:id  RotateKey         WRITE
//
// Accounts carry emails, so a person matches the account with their email
// and nothing else. The box-wide credential is an administrator's API key
// (Settings → API Keys). Audiobookshelf has one OpenID provider: switching
// sign-in on replaces whatever is configured (PocketID…) and keeps a copy
// that switching off puts back.
type audiobookshelf struct{}

func init() { Register(audiobookshelf{}) }

// absMobileRedirect is the Audiobookshelf app's sign-in return address.
const absMobileRedirect = "audiobookshelf://oauth"

// absEndedAfter: a listening session nobody synced for this long is over.
const absEndedAfter = 15 * time.Minute

func (audiobookshelf) Type() string { return "audiobookshelf" }

func (audiobookshelf) Capabilities() Capabilities {
	return Capabilities{Provision: true, SSO: true, Events: true, Link: true}
}

// absBases: the web app lives at /audiobookshelf (the default base path
// since 2.17) and the redirect URLs follow it; a root install uses /.
func absBases(publicURL string) []string {
	p := strings.TrimRight(publicURL, "/")
	if strings.HasSuffix(p, "/audiobookshelf") {
		return []string{p}
	}
	return []string{p + "/audiobookshelf", p}
}

func (audiobookshelf) SSORedirects(publicURL string) []string {
	out := []string{}
	for _, b := range absBases(publicURL) {
		out = append(out, b+"/auth/openid/callback", b+"/auth/openid/mobile-redirect")
	}
	return out
}

type absError struct {
	Status int
	Msg    string
}

func (e *absError) Error() string {
	return fmt.Sprintf("audiobookshelf answered HTTP %d: %s", e.Status, e.Msg)
}

func absStatus(err error) int {
	var ae *absError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func absKey(t Target) (string, error) {
	k := strings.TrimSpace(t.Fields["TOKEN"])
	if k == "" {
		return "", errors.New("the Audiobookshelf service in Core has no TOKEN: create an API key for an administrator in Audiobookshelf (Settings → API Keys) and paste it under Services → Audiobookshelf → Edit")
	}
	return k, nil
}

func (audiobookshelf) call(ctx context.Context, t Target, method, path, key string, body, out any, secrets ...string) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := t.HTTP.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), append(secrets, key)...))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &absError{Status: res.StatusCode, Msg: redact(msg, append(secrets, key)...)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("audiobookshelf %s %s: unexpected answer: %w", method, path, err)
	}
	return nil
}

// admin does one request with the box-wide API key.
func (c audiobookshelf) admin(ctx context.Context, t Target, method, path string, body, out any, secrets ...string) error {
	k, err := absKey(t)
	if err != nil {
		return err
	}
	err = c.call(ctx, t, method, path, k, body, out, secrets...)
	switch absStatus(err) {
	case http.StatusUnauthorized:
		return errors.New("Audiobookshelf rejected the stored API key (HTTP 401): it is wrong, expired or inactive; create a new one under Settings → API Keys")
	case http.StatusForbidden:
		return fmt.Errorf("the stored Audiobookshelf key is not an administrator's (HTTP 403): make the API key for an admin or the root user (%v)", err)
	}
	return err
}

type absUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	Type          string `json:"type"` // root, admin, user, guest
	IsActive      bool   `json:"isActive"`
	HasOpenIDLink bool   `json:"hasOpenIDLink"`
}

func (u absUser) admin() bool { return u.Type == "root" || u.Type == "admin" }

func (c audiobookshelf) users(ctx context.Context, t Target) ([]absUser, error) {
	var out struct {
		Users []absUser `json:"users"`
	}
	if err := c.admin(ctx, t, http.MethodGet, "/api/users", nil, &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

func absByEmail(list []absUser, email string) *absUser {
	for i := range list {
		if e := strings.ToLower(strings.TrimSpace(list[i].Email)); e != "" && e == email {
			return &list[i]
		}
	}
	return nil
}

func (c audiobookshelf) authSettings(ctx context.Context, t Target) (map[string]any, error) {
	var s map[string]any
	if err := c.admin(ctx, t, http.MethodGet, "/api/auth-settings", nil, &s); err != nil {
		return nil, fmt.Errorf("reading Audiobookshelf's sign-in settings failed: %w", err)
	}
	return s, nil
}

func (c audiobookshelf) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	if _, err := absKey(t); err != nil {
		return info, err
	}
	var st struct {
		App           string   `json:"app"`
		ServerVersion string   `json:"serverVersion"`
		AuthMethods   []string `json:"authMethods"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/status", "", nil, &st); err != nil {
		return info, fmt.Errorf("cannot reach Audiobookshelf at %s: %w", t.BaseURL, err)
	}
	if st.App != "audiobookshelf" {
		return info, fmt.Errorf("%s does not answer like Audiobookshelf", t.BaseURL)
	}
	info.Version = st.ServerVersion
	list, err := c.users(ctx, t)
	if err != nil {
		return info, err
	}
	info.Admin, info.Users = true, len(list)
	notes := []string{}
	if s, err := c.authSettings(ctx, t); err == nil {
		iss := fmt.Sprint(nz(s["authOpenIDIssuerURL"]))
		if hasString(s["authActiveAuthMethods"], "openid") && iss != "" {
			notes = append(notes, "people sign in with OpenID at "+iss+"; switching “Sign in with Pocket Core” on replaces it and keeps a copy that switching off puts back")
		}
		if !hasString(s["authActiveAuthMethods"], "local") {
			notes = append(notes, "username/password sign-in is off")
		}
	}
	info.Detail = strings.Join(notes, "; ")
	return info, nil
}

func hasString(v any, want string) bool {
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if fmt.Sprint(x) == want {
				return true
			}
		}
	}
	return false
}

func strList(v any) []string {
	out := []string{}
	if l, ok := v.([]any); ok {
		for _, x := range l {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func (c audiobookshelf) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		return Account{}, errors.New("person has no email")
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	if u := absByEmail(list, email); u != nil {
		note := fmt.Sprintf("the Audiobookshelf account %q has this email; linked with its user id, no API key minted (make one under Settings → API Keys, or set it in Core by hand)", u.Username)
		if u.admin() {
			note = fmt.Sprintf("the Audiobookshelf %s %q has this email; linked with its user id and left untouched", u.Type, u.Username)
		}
		return Account{RemoteID: u.ID, Status: StatusLinked, Fields: map[string]string{"USER_ID": u.ID}, Note: note}, nil
	}
	// Never take an account over by name: a taken name gets the full email.
	name := usernameFor(email)
	for _, u := range list {
		if strings.EqualFold(u.Username, name) {
			name = email
			break
		}
	}
	password := RandomPassword(40) // nobody knows it: the person signs in with Pocket Core
	var created struct {
		User absUser `json:"user"`
	}
	in := map[string]any{"username": name, "password": password, "email": email, "type": "user", "isActive": true}
	if err := c.admin(ctx, t, http.MethodPost, "/api/users", in, &created, password); err != nil {
		return Account{}, fmt.Errorf("creating the Audiobookshelf account %q failed: %w", name, err)
	}
	if created.User.ID == "" {
		return Account{}, fmt.Errorf("creating the Audiobookshelf account %q returned no user id", name)
	}
	acc := Account{RemoteID: created.User.ID, Status: StatusOK, Created: true, Fields: map[string]string{"USER_ID": created.User.ID}}
	if p.KeepOwnCredential {
		acc.Note = "account created; the person already has their own Audiobookshelf key in Core, so none was minted"
		return acc, nil
	}
	var key struct {
		APIKey struct {
			APIKey string `json:"apiKey"`
		} `json:"apiKey"`
	}
	kin := map[string]any{"name": "Pocket Core · " + email, "userId": created.User.ID, "isActive": true}
	if err := c.admin(ctx, t, http.MethodPost, "/api/api-keys", kin, &key); err != nil || key.APIKey.APIKey == "" {
		if err == nil {
			err = errors.New("no key in the answer")
		}
		return acc, fmt.Errorf("the Audiobookshelf account %q was created but making its API key failed: %w; a retry will link the account by email", name, err)
	}
	acc.Fields["TOKEN"] = key.APIKey.APIKey
	acc.Note = "account created with a random password nobody knows and its own API key: the person signs in with Pocket Core, or an admin sets a password in Audiobookshelf"
	return acc, nil
}

// RemoteAccounts lists Audiobookshelf accounts for linking by hand.
func (c audiobookshelf) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(list))
	for _, u := range list {
		n := u.Username
		if u.Email != "" {
			n += " · " + u.Email
		}
		out = append(out, RemoteAccount{ID: u.ID, Name: n, Admin: u.admin(), Disabled: !u.IsActive})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Link ties a person to an existing Audiobookshelf account chosen by an admin.
func (c audiobookshelf) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	for _, u := range list {
		if u.ID == remoteID {
			return Account{RemoteID: u.ID, Status: StatusLinked, Fields: map[string]string{"USER_ID": u.ID},
				Note: fmt.Sprintf("linked by hand to the Audiobookshelf account %q", u.Username)}, nil
		}
	}
	return Account{}, fmt.Errorf("Audiobookshelf has no account %s", remoteID)
}

// ---- Sign in with Pocket Core (built-in OpenID) ----

// absOIDCKeys are the settings switching sign-in replaces and puts back.
var absOIDCKeys = []string{"authOpenIDIssuerURL", "authOpenIDAuthorizationURL", "authOpenIDTokenURL", "authOpenIDUserInfoURL",
	"authOpenIDJwksURL", "authOpenIDLogoutURL", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDTokenSigningAlgorithm",
	"authOpenIDButtonText", "authOpenIDMatchExistingBy", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim",
	"authOpenIDMobileRedirectURIs", "authOpenIDAutoRegister", "authOpenIDAutoLaunch"}

func (c audiobookshelf) backupPath(t Target, what string) string {
	return filepath.Join(t.DataDir, "audiobookshelf-"+what+".json")
}

func (c audiobookshelf) writeBackup(t Target, name string, v any) error {
	if err := os.MkdirAll(t.DataDir, 0o700); err != nil {
		return fmt.Errorf("backup dir: %w", err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(name, b, 0o600)
}

// apply saves settings and makes Audiobookshelf load them. It only rebuilds
// its OpenID client when the method is switched on, so the first request
// leaves just username/password on and the second sets the wanted methods.
func (c audiobookshelf) apply(ctx context.Context, t Target, settings map[string]any, methods []string, secrets ...string) error {
	first := map[string]any{"authActiveAuthMethods": []string{"local"}}
	for k, v := range settings {
		first[k] = v
	}
	if err := c.admin(ctx, t, http.MethodPatch, "/api/auth-settings", first, nil, secrets...); err != nil {
		return err
	}
	if len(methods) == 1 && methods[0] == "local" {
		return nil
	}
	return c.admin(ctx, t, http.MethodPatch, "/api/auth-settings", map[string]any{"authActiveAuthMethods": methods}, nil, secrets...)
}

// prepare makes each linked person's account match their next OpenID
// sign-in by email: the email is the person's, and no other provider's
// subject is attached (Audiobookshelf refuses a second one).
func (c audiobookshelf) prepare(ctx context.Context, t Target, people map[string]string) error {
	if len(people) == 0 {
		return nil
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return err
	}
	byID := map[string]absUser{}
	for _, u := range list {
		byID[u.ID] = u
	}
	emails := make([]string, 0, len(people))
	for e := range people {
		emails = append(emails, e)
	}
	sort.Strings(emails)
	var problems []string
	for _, email := range emails {
		id := people[email]
		u, ok := byID[id]
		if id == "" || !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(u.Email), email) {
			if other := absByEmail(list, strings.ToLower(email)); other != nil && other.ID != u.ID {
				problems = append(problems, fmt.Sprintf("%s is linked to %q but %q already has that email", email, u.Username, other.Username))
				continue
			}
			if err := c.admin(ctx, t, http.MethodPatch, "/api/users/"+url.PathEscape(u.ID), map[string]any{"email": email}, nil); err != nil {
				if u.Type == "root" {
					err = errors.New("only the root user's own API key may change it: set the email under Settings → Users → root, or give Core root's key")
				}
				problems = append(problems, fmt.Sprintf("setting %q's email to %s failed: %v", u.Username, email, err))
				continue
			}
		}
		if u.HasOpenIDLink {
			if err := c.admin(ctx, t, http.MethodPatch, "/api/users/"+url.PathEscape(u.ID)+"/openid-unlink", nil, nil); err != nil {
				problems = append(problems, fmt.Sprintf("detaching %q from the previous sign-in provider failed: %v", u.Username, err))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (c audiobookshelf) ConfigureSSO(ctx context.Context, t Target, sc SSOConfig) error {
	if sc.Enabled && (sc.Issuer == "" || sc.ClientID == "" || sc.ClientSecret == "") {
		return errors.New("sign-in needs Core's issuer and a client id and secret")
	}
	current, err := c.authSettings(ctx, t)
	if err != nil {
		return err
	}
	stamp := t.Clock().UTC().Format("20060102-150405.000")
	backup := c.backupPath(t, "sso-"+stamp)
	if err := c.writeBackup(t, backup, current); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	methods := strList(current["authActiveAuthMethods"])
	original := c.backupPath(t, "sso-original")

	if !sc.Enabled {
		if b, err := os.ReadFile(original); err == nil {
			// put back what was there before Core took over
			var prev map[string]any
			if err := json.Unmarshal(b, &prev); err != nil {
				return fmt.Errorf("the saved sign-in settings %s are unreadable: %w", original, err)
			}
			settings := map[string]any{}
			for _, k := range absOIDCKeys {
				if v, ok := prev[k]; ok {
					settings[k] = v
				}
			}
			if err := c.prepare(ctx, t, sc.People); err != nil {
				return fmt.Errorf("%w (backup kept at %s)", err, backup)
			}
			want := strList(prev["authActiveAuthMethods"])
			if len(want) == 0 {
				want = []string{"local"}
			}
			if err := c.apply(ctx, t, settings, want, fmt.Sprint(nz(prev["authOpenIDClientSecret"]))); err != nil {
				return fmt.Errorf("putting back the previous sign-in settings failed (backup kept at %s): %w", backup, err)
			}
			_ = os.Rename(original, c.backupPath(t, "sso-original-restored-"+stamp))
			return nil
		}
		if !hasString(current["authActiveAuthMethods"], "openid") {
			return nil // nothing to turn off
		}
		// nothing saved to go back to: OpenID off, username/password on
		want := []string{"local"}
		if err := c.apply(ctx, t, map[string]any{}, want); err != nil {
			return fmt.Errorf("turning OpenID sign-in off failed (backup kept at %s): %w", backup, err)
		}
		return nil
	}

	if iss := fmt.Sprint(nz(current["authOpenIDIssuerURL"])); iss != sc.Issuer {
		// someone else's provider (or none): keep it to put back later
		if err := c.writeBackup(t, original, current); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	}
	if err := c.prepare(ctx, t, sc.People); err != nil {
		return fmt.Errorf("%w (backup kept at %s)", err, backup)
	}
	btn := sc.ButtonText
	if btn == "" {
		btn = "Sign in with Pocket Core"
	}
	redirects := strList(current["authOpenIDMobileRedirectURIs"])
	if !(len(redirects) == 1 && redirects[0] == "*") {
		found := false
		for _, r := range redirects {
			found = found || r == absMobileRedirect
		}
		if !found {
			redirects = append(redirects, absMobileRedirect)
		}
	}
	iss := strings.TrimRight(sc.Issuer, "/")
	// Groups and permission claims off: Core decides who may sign in (the
	// service's audience) and sign-in must never change anyone's type or
	// libraries. Auto-register stays as it was.
	settings := map[string]any{
		"authOpenIDIssuerURL": iss, "authOpenIDAuthorizationURL": iss + "/oidc/authorize", "authOpenIDTokenURL": iss + "/oidc/token",
		"authOpenIDUserInfoURL": iss + "/oidc/userinfo", "authOpenIDJwksURL": iss + "/oidc/jwks", "authOpenIDLogoutURL": iss + "/oidc/logout",
		"authOpenIDClientID": sc.ClientID, "authOpenIDClientSecret": sc.ClientSecret, "authOpenIDTokenSigningAlgorithm": "RS256",
		"authOpenIDButtonText": btn, "authOpenIDMatchExistingBy": "email", "authOpenIDGroupClaim": "", "authOpenIDAdvancedPermsClaim": "",
		"authOpenIDMobileRedirectURIs": redirects,
	}
	want := []string{}
	for _, m := range methods {
		if m != "openid" {
			want = append(want, m)
		}
	}
	want = append(want, "openid")
	if err := c.apply(ctx, t, settings, want, sc.ClientSecret); err != nil {
		// leave people able to sign in the way they could before
		_ = c.admin(ctx, t, http.MethodPatch, "/api/auth-settings", map[string]any{"authActiveAuthMethods": methods}, nil)
		return fmt.Errorf("saving the sign-in settings failed (backup kept at %s): %w", backup, err)
	}
	return nil
}

// ---- events (polled) ----

type absState struct {
	Init         bool             `json:"init"`
	Users        []string         `json:"users"`
	LastSession  int64            `json:"last_session"`  // newest session start seen (ms)
	Open         map[string]int64 `json:"open"`          // sessions reported started, not yet ended
	LastFinished int64            `json:"last_finished"` // newest finishedAt seen (ms)
	LastAdded    int64            `json:"last_added"`    // newest library item addedAt seen (ms)
	LastEpisode  int64            `json:"last_episode"`  // newest podcast episode addedAt seen (ms)
}

type absSession struct {
	ID            string  `json:"id"`
	UserID        string  `json:"userId"`
	LibraryID     string  `json:"libraryId"`
	LibraryItemID string  `json:"libraryItemId"`
	EpisodeID     string  `json:"episodeId"`
	MediaType     string  `json:"mediaType"`
	DisplayTitle  string  `json:"displayTitle"`
	DisplayAuthor string  `json:"displayAuthor"`
	Duration      float64 `json:"duration"`
	PlayMethod    int     `json:"playMethod"`
	MediaPlayer   string  `json:"mediaPlayer"`
	TimeListening float64 `json:"timeListening"`
	CurrentTime   float64 `json:"currentTime"`
	StartedAt     int64   `json:"startedAt"`
	UpdatedAt     int64   `json:"updatedAt"`
	DeviceInfo    struct {
		ClientName string `json:"clientName"`
		DeviceName string `json:"deviceName"`
		OSName     string `json:"osName"`
		Browser    string `json:"browserName"`
	} `json:"deviceInfo"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
}

type absProgress struct {
	LibraryItemID   string  `json:"libraryItemId"`
	EpisodeID       string  `json:"episodeId"`
	IsFinished      bool    `json:"isFinished"`
	FinishedAt      int64   `json:"finishedAt"`
	Duration        float64 `json:"duration"`
	DisplayTitle    string  `json:"displayTitle"`
	DisplaySubtitle string  `json:"displaySubtitle"`
	MediaItemType   string  `json:"mediaItemType"`
}

type absLibrary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
}

type absItem struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	AddedAt   int64  `json:"addedAt"`
	MediaType string `json:"mediaType"`
	Media     struct {
		Duration float64 `json:"duration"`
		Metadata struct {
			Title         string `json:"title"`
			AuthorName    string `json:"authorName"`
			Author        string `json:"author"`
			NarratorName  string `json:"narratorName"`
			SeriesName    string `json:"seriesName"`
			PublishedYear string `json:"publishedYear"`
		} `json:"metadata"`
	} `json:"media"`
}

type absEpisode struct {
	ID            string `json:"id"`
	LibraryItemID string `json:"libraryItemId"`
	Title         string `json:"title"`
	PublishedAt   int64  `json:"publishedAt"`
	AddedAt       int64  `json:"addedAt"`
	Podcast       struct {
		Metadata struct {
			Title  string `json:"title"`
			Author string `json:"author"`
		} `json:"metadata"`
	} `json:"podcast"`
}

func absTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func (c audiobookshelf) itemURL(t Target, id string) string {
	pub := strings.TrimRight(t.PublicURL, "/")
	if pub == "" || id == "" {
		return ""
	}
	return absBases(pub)[0] + "/item/" + id
}

func (c audiobookshelf) sessionPayload(t Target, s absSession) map[string]any {
	p := map[string]any{"session_id": s.ID, "title": s.DisplayTitle, "media_type": s.MediaType, "item_id": s.LibraryItemID,
		"audiobookshelf_user_id": s.UserID, "started_at": absTime(s.StartedAt), "url": c.itemURL(t, s.LibraryItemID)}
	if s.DisplayAuthor != "" {
		p["author"] = s.DisplayAuthor
	}
	if s.EpisodeID != "" {
		p["episode_id"] = s.EpisodeID
	}
	if s.User.Username != "" {
		p["audiobookshelf_user"] = s.User.Username
	}
	dev := strings.TrimSpace(s.DeviceInfo.ClientName)
	if n := strings.TrimSpace(s.DeviceInfo.DeviceName); n != "" {
		dev = strings.TrimSpace(dev + " · " + n)
	} else if b := strings.TrimSpace(s.DeviceInfo.Browser); b != "" {
		dev = strings.TrimSpace(dev + " · " + b)
	}
	if dev != "" {
		p["device"] = dev
	}
	if s.Duration > 0 {
		p["duration_seconds"] = int64(s.Duration)
	}
	return p
}

func (c audiobookshelf) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	users, err := c.users(ctx, t)
	if err != nil {
		return nil, state, err
	}
	var sess struct {
		Sessions []absSession `json:"sessions"`
	}
	if err := c.admin(ctx, t, http.MethodGet, "/api/sessions?sort=updatedAt&desc=1&itemsPerPage=100", nil, &sess); err != nil {
		return nil, state, err
	}
	progress := map[string][]absProgress{}
	for _, u := range users {
		var one struct {
			MediaProgress []absProgress `json:"mediaProgress"`
		}
		if err := c.admin(ctx, t, http.MethodGet, "/api/users/"+url.PathEscape(u.ID), nil, &one); err != nil {
			return nil, state, err
		}
		progress[u.ID] = one.MediaProgress
	}
	var libs struct {
		Libraries []absLibrary `json:"libraries"`
	}
	if err := c.admin(ctx, t, http.MethodGet, "/api/libraries", nil, &libs); err != nil {
		return nil, state, err
	}
	libName := map[string]string{}
	items := []absItem{}
	episodes := []absEpisode{}
	for _, l := range libs.Libraries {
		libName[l.ID] = l.Name
		var got struct {
			Results []absItem `json:"results"`
		}
		q := url.Values{"sort": {"addedAt"}, "desc": {"1"}, "limit": {"50"}, "minified": {"1"}}
		if err := c.admin(ctx, t, http.MethodGet, "/api/libraries/"+url.PathEscape(l.ID)+"/items?"+q.Encode(), nil, &got); err != nil {
			return nil, state, err
		}
		for _, it := range got.Results {
			if it.LibraryID == "" {
				it.LibraryID = l.ID
			}
			items = append(items, it)
		}
		if l.MediaType == "podcast" {
			var eps struct {
				Episodes []absEpisode `json:"episodes"`
			}
			if err := c.admin(ctx, t, http.MethodGet, "/api/libraries/"+url.PathEscape(l.ID)+"/recent-episodes?limit=50", nil, &eps); err != nil {
				return nil, state, err
			}
			episodes = append(episodes, eps.Episodes...)
		}
	}

	cur := absState{Init: true, Open: map[string]int64{}}
	for _, u := range users {
		cur.Users = append(cur.Users, u.ID)
	}
	sort.Strings(cur.Users)
	for _, s := range sess.Sessions {
		cur.LastSession = max(cur.LastSession, s.StartedAt)
	}
	for _, l := range progress {
		for _, mp := range l {
			if mp.IsFinished {
				cur.LastFinished = max(cur.LastFinished, mp.FinishedAt)
			}
		}
	}
	for _, it := range items {
		cur.LastAdded = max(cur.LastAdded, it.AddedAt)
	}
	for _, ep := range episodes {
		cur.LastEpisode = max(cur.LastEpisode, ep.AddedAt)
	}
	var prev absState
	if len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &prev) != nil || !prev.Init {
		next, _ := json.Marshal(cur)
		return nil, next, nil // first poll: baseline only
	}
	cur.LastSession = max(cur.LastSession, prev.LastSession)
	cur.LastFinished = max(cur.LastFinished, prev.LastFinished)
	cur.LastAdded = max(cur.LastAdded, prev.LastAdded)
	cur.LastEpisode = max(cur.LastEpisode, prev.LastEpisode)

	var out []Event
	now := t.Clock()

	// accounts
	had := map[string]bool{}
	for _, id := range prev.Users {
		had[id] = true
	}
	for _, u := range users {
		if !had[u.ID] {
			out = append(out, Event{Name: "user_created", Subject: "audiobookshelf-user:" + u.ID, RemoteUserID: u.ID,
				Payload: map[string]any{"audiobookshelf_user_id": u.ID, "audiobookshelf_user": u.Username, "type": u.Type}})
		}
		delete(had, u.ID)
	}
	gone := make([]string, 0, len(had))
	for id := range had {
		gone = append(gone, id)
	}
	sort.Strings(gone)
	for _, id := range gone {
		out = append(out, Event{Name: "user_deleted", Subject: "audiobookshelf-user:" + id, RemoteUserID: id,
			Payload: map[string]any{"audiobookshelf_user_id": id}})
	}

	// listening, oldest first: started when it appears, ended once nobody synced it for a while
	ss := append([]absSession(nil), sess.Sessions...)
	sort.Slice(ss, func(i, j int) bool { return ss[i].StartedAt < ss[j].StartedAt })
	byID := map[string]absSession{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	for id, at := range prev.Open {
		if _, ok := byID[id]; ok {
			cur.Open[id] = at
		}
	}
	for _, s := range ss {
		if s.StartedAt > prev.LastSession {
			out = append(out, Event{Name: "listening_started", Subject: "session:" + s.ID, RemoteUserID: s.UserID, Payload: c.sessionPayload(t, s)})
			cur.Open[s.ID] = s.StartedAt
		}
	}
	ended := []absSession{}
	for id := range cur.Open {
		s := byID[id]
		if now.Sub(time.UnixMilli(s.UpdatedAt)) >= absEndedAfter {
			ended = append(ended, s)
		}
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].UpdatedAt < ended[j].UpdatedAt })
	for _, s := range ended {
		delete(cur.Open, s.ID)
		p := c.sessionPayload(t, s)
		p["ended_at"] = absTime(s.UpdatedAt)
		p["listened_seconds"] = int64(s.TimeListening)
		p["position_seconds"] = int64(s.CurrentTime)
		if s.Duration > 0 {
			p["progress"] = float64(int(s.CurrentTime/s.Duration*1000)) / 1000
		}
		out = append(out, Event{Name: "listening_ended", Subject: "session:" + s.ID, RemoteUserID: s.UserID, Payload: p})
	}

	// finished books and episodes
	type fin struct {
		user string
		mp   absProgress
	}
	fins := []fin{}
	for uid, l := range progress {
		for _, mp := range l {
			if mp.IsFinished && mp.FinishedAt > prev.LastFinished {
				fins = append(fins, fin{uid, mp})
			}
		}
	}
	sort.Slice(fins, func(i, j int) bool { return fins[i].mp.FinishedAt < fins[j].mp.FinishedAt })
	for _, f := range fins {
		p := map[string]any{"item_id": f.mp.LibraryItemID, "title": f.mp.DisplayTitle, "finished_at": absTime(f.mp.FinishedAt),
			"audiobookshelf_user_id": f.user, "url": c.itemURL(t, f.mp.LibraryItemID)}
		if f.mp.EpisodeID != "" {
			p["episode_id"] = f.mp.EpisodeID
			p["media_type"] = "podcast"
		} else {
			p["media_type"] = "book"
		}
		if f.mp.DisplaySubtitle != "" {
			p["subtitle"] = f.mp.DisplaySubtitle
		}
		if f.mp.Duration > 0 {
			p["duration_seconds"] = int64(f.mp.Duration)
		}
		subj := "finished:" + f.user + ":" + f.mp.LibraryItemID
		if f.mp.EpisodeID != "" {
			subj += ":" + f.mp.EpisodeID
		}
		out = append(out, Event{Name: "item_finished", Subject: subj, RemoteUserID: f.user, Payload: p})
	}

	// new books and podcasts, then new episodes
	sort.Slice(items, func(i, j int) bool { return items[i].AddedAt < items[j].AddedAt })
	for _, it := range items {
		if it.AddedAt <= prev.LastAdded {
			continue
		}
		m := it.Media.Metadata
		p := map[string]any{"item_id": it.ID, "title": m.Title, "media_type": it.MediaType, "library": libName[it.LibraryID],
			"added_at": absTime(it.AddedAt), "url": c.itemURL(t, it.ID)}
		if a := m.AuthorName + m.Author; a != "" {
			p["author"] = a
		}
		if m.NarratorName != "" {
			p["narrator"] = m.NarratorName
		}
		if m.SeriesName != "" {
			p["series"] = m.SeriesName
		}
		if m.PublishedYear != "" {
			p["year"] = m.PublishedYear
		}
		if it.Media.Duration > 0 {
			p["duration_seconds"] = int64(it.Media.Duration)
		}
		out = append(out, Event{Name: "item_added", Subject: "item:" + it.ID, Payload: p})
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].AddedAt < episodes[j].AddedAt })
	for _, ep := range episodes {
		if ep.AddedAt <= prev.LastEpisode {
			continue
		}
		p := map[string]any{"episode_id": ep.ID, "item_id": ep.LibraryItemID, "title": ep.Title, "podcast": ep.Podcast.Metadata.Title,
			"added_at": absTime(ep.AddedAt), "url": c.itemURL(t, ep.LibraryItemID)}
		if ep.PublishedAt > 0 {
			p["published_at"] = absTime(ep.PublishedAt)
		}
		out = append(out, Event{Name: "episode_added", Subject: "episode:" + ep.ID, Payload: p})
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}
