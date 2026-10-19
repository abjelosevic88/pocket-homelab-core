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

// Jellyfin (10.9+) connector.
//
// Endpoints used:
//
//	GET  /System/Info/Public               Test                          read
//	GET  /System/Info                      Test (the key is an admin's)  read
//	GET  /Users                            Test, Provision, Link, Poll   read
//	POST /Users/New                        Provision, missing user       WRITE
//	POST /Users/AuthenticateByName         Provision, as the new user    WRITE (a session = the person's token)
//	GET  /Plugins                          Test, ConfigureSSO            read
//	GET  /sso/OID/Get                      ConfigureSSO (backup)         read   (SSO Authentication plugin)
//	POST /sso/OID/Add/pocket-core          ConfigureSSO on               WRITE  (SSO Authentication plugin)
//	GET  /sso/OID/Del/pocket-core          ConfigureSSO off              WRITE  (SSO Authentication plugin)
//	GET  /Branding/Configuration           ConfigureSSO (login button)   read
//	POST /System/Configuration/branding    ConfigureSSO (login button)   WRITE
//	GET  /System/ActivityLog/Entries       Poll (playback, accounts)     read
//	GET  /Items                            Poll (new items, titles)      read
//
// Jellyfin accounts have no email, so a person matches an account whose
// name is their email or the part before "@" (the same name Core sends as
// preferred_username, which the SSO plugin uses). Anything else is linked by
// hand (Linker). The box-wide credential is a Jellyfin API key (Dashboard →
// API Keys), which acts as an administrator.
type jellyfin struct{}

func init() { Register(jellyfin{}) }

// JellyfinSSOProvider is the provider name in the SSO plugin and in its URLs.
const JellyfinSSOProvider = "pocket-core"

// JellyfinClient names Core's sessions in Jellyfin (Dashboard → Devices).
const JellyfinClient = "Pocket Core"

const (
	ssoMarkStart = "<!-- pocket-core-sso -->"
	ssoMarkEnd   = "<!-- /pocket-core-sso -->"
)

func (jellyfin) Type() string { return "jellyfin" }

func (jellyfin) Capabilities() Capabilities {
	return Capabilities{Provision: true, SSO: true, Events: true, Link: true}
}

// SSORedirects: plugin 3.5+ uses /redirect/, older builds /r/.
func (jellyfin) SSORedirects(publicURL string) []string {
	p := strings.TrimRight(publicURL, "/")
	return []string{p + "/sso/OID/redirect/" + JellyfinSSOProvider, p + "/sso/OID/r/" + JellyfinSSOProvider}
}

type jfError struct {
	Status int
	Msg    string
}

func (e *jfError) Error() string {
	return fmt.Sprintf("jellyfin answered HTTP %d: %s", e.Status, e.Msg)
}

func jfStatus(err error) int {
	var je *jfError
	if errors.As(err, &je) {
		return je.Status
	}
	return 0
}

// jfAuth is the MediaBrowser authorization header; token may be empty.
func jfAuth(token, device, deviceID string) string {
	q := func(s string) string { return strings.ReplaceAll(s, `"`, "") }
	h := fmt.Sprintf(`MediaBrowser Client="%s", Device="%s", DeviceId="%s", Version="1.0"`, JellyfinClient, q(device), q(deviceID))
	if token != "" {
		h += fmt.Sprintf(`, Token="%s"`, token)
	}
	return h
}

func (jellyfin) call(ctx context.Context, t Target, method, path, authz string, body, out any, secrets ...string) error {
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
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	res, err := t.HTTP.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), secrets...))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &e) == nil && (e.Title != "" || e.Detail != "") {
			msg = strings.TrimSpace(e.Title + " " + e.Detail)
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &jfError{Status: res.StatusCode, Msg: redact(msg, secrets...)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("jellyfin %s %s: unexpected answer: %w", method, path, err)
	}
	return nil
}

func jfKey(t Target) (string, error) {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return "", errors.New("the Jellyfin service in Core has no API_KEY: create one in Jellyfin (Dashboard → API Keys) and paste it under Services → Jellyfin → Edit")
	}
	return k, nil
}

// admin does one request with the box-wide API key.
func (c jellyfin) admin(ctx context.Context, t Target, method, path string, body, out any, secrets ...string) error {
	k, err := jfKey(t)
	if err != nil {
		return err
	}
	err = c.call(ctx, t, method, path, jfAuth(k, "Pocket Core", "pocket-core"), body, out, append(secrets, k)...)
	switch jfStatus(err) {
	case http.StatusUnauthorized:
		return errors.New("Jellyfin rejected the stored API key (HTTP 401): it is wrong or was revoked; create a new one under Dashboard → API Keys")
	case http.StatusForbidden:
		return fmt.Errorf("the stored Jellyfin credential is not an administrator's (HTTP 403): use an API key from Dashboard → API Keys (%v)", err)
	}
	return err
}

type jfPolicy struct {
	IsAdministrator  bool `json:"IsAdministrator"`
	IsDisabled       bool `json:"IsDisabled"`
	EnableAllFolders bool `json:"EnableAllFolders"`
}

type jfUser struct {
	ID     string   `json:"Id"`
	Name   string   `json:"Name"`
	Policy jfPolicy `json:"Policy"`
}

func (c jellyfin) users(ctx context.Context, t Target) ([]jfUser, error) {
	var list []jfUser
	if err := c.admin(ctx, t, http.MethodGet, "/Users", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

type jfPlugin struct {
	Name    string `json:"Name"`
	Version string `json:"Version"`
	Status  string `json:"Status"`
}

// ssoPlugin finds the SSO Authentication plugin among the installed ones.
func (c jellyfin) ssoPlugin(ctx context.Context, t Target) (*jfPlugin, error) {
	var list []jfPlugin
	if err := c.admin(ctx, t, http.MethodGet, "/Plugins", nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		n := strings.ToLower(list[i].Name)
		if strings.Contains(n, "sso") && list[i].Status != "Superseded" {
			return &list[i], nil
		}
	}
	return nil, nil
}

func (c jellyfin) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	if _, err := jfKey(t); err != nil {
		return info, err
	}
	var pub struct {
		Version    string `json:"Version"`
		ServerName string `json:"ServerName"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/System/Info/Public", "", nil, &pub); err != nil {
		return info, fmt.Errorf("cannot reach Jellyfin at %s: %w", t.BaseURL, err)
	}
	info.Version = pub.Version
	if err := c.admin(ctx, t, http.MethodGet, "/System/Info", nil, nil); err != nil {
		return info, err
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return info, err
	}
	info.Admin, info.Users = true, len(list)
	notes := []string{}
	if p, err := c.ssoPlugin(ctx, t); err == nil && p != nil {
		notes = append(notes, fmt.Sprintf("SSO Authentication plugin %s (%s) is installed, so “Sign in with Pocket Core” can be switched on", p.Version, p.Status))
	} else if err == nil {
		notes = append(notes, "the SSO Authentication plugin is not installed: “Sign in with Pocket Core” needs it (Dashboard → Plugins → Repositories → https://raw.githubusercontent.com/9p4/jellyfin-plugin-sso/manifest-release/manifest.json, install, restart)")
	}
	info.Detail = strings.Join(notes, "; ")
	return info, nil
}

// usernameFor is the Jellyfin name Core uses for a person: the part of the
// email before "@" (what Core sends as preferred_username).
func usernameFor(email string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return email[:i]
	}
	return email
}

func matchUser(list []jfUser, email string) *jfUser {
	for i := range list {
		n := strings.ToLower(strings.TrimSpace(list[i].Name))
		if n == email || n == strings.ToLower(usernameFor(email)) {
			return &list[i]
		}
	}
	return nil
}

func (c jellyfin) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		return Account{}, errors.New("person has no email")
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	if u := matchUser(list, email); u != nil {
		// Its password is unknown and Jellyfin has no way to mint a token for
		// someone else: link it and hand over the user id only.
		note := fmt.Sprintf("the Jellyfin account %q matches this person; linked with its user id, no token minted (sign in on the phone, or set a token by hand)", u.Name)
		if u.Policy.IsAdministrator {
			note = fmt.Sprintf("the Jellyfin administrator %q matches this person; linked with its user id and left untouched", u.Name)
		}
		return Account{RemoteID: u.ID, Status: StatusLinked, Fields: map[string]string{"USER_ID": u.ID}, Note: note}, nil
	}
	name := usernameFor(email)
	password := RandomPassword(40) // used for this one sign-in, never stored, logged or returned
	var created jfUser
	if err := c.admin(ctx, t, http.MethodPost, "/Users/New", map[string]string{"Name": name, "Password": password}, &created, password); err != nil {
		return Account{}, fmt.Errorf("creating the Jellyfin account %q failed: %w", name, err)
	}
	acc := Account{RemoteID: created.ID, Status: StatusOK, Created: true, Fields: map[string]string{"USER_ID": created.ID}}
	if p.KeepOwnCredential {
		acc.Note = "account created; the person already has their own Jellyfin credential in Core, so no token was minted"
		return acc, nil
	}
	// A token is a session: sign in once as the new account from a device
	// named after the person; it shows under Dashboard → Devices.
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        jfUser `json:"User"`
	}
	authz := jfAuth("", email, "pocket-core-"+created.ID)
	if err := c.call(ctx, t, http.MethodPost, "/Users/AuthenticateByName", authz, map[string]string{"Username": name, "Pw": password}, &login, password); err != nil || login.AccessToken == "" {
		if err == nil {
			err = errors.New("no access token in the answer")
		}
		return acc, fmt.Errorf("the Jellyfin account %q was created but signing in as it to get its token failed: %w; a retry will link the account by name", name, err)
	}
	acc.Fields["API_KEY"] = login.AccessToken
	acc.Note = "account created with a random password nobody knows: the person signs in with Pocket Core (SSO plugin) or Quick Connect, or an admin sets a password in Jellyfin"
	return acc, nil
}

// RemoteAccounts lists Jellyfin accounts for linking by hand.
func (c jellyfin) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(list))
	for _, u := range list {
		out = append(out, RemoteAccount{ID: u.ID, Name: u.Name, Admin: u.Policy.IsAdministrator, Disabled: u.Policy.IsDisabled})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Link ties a person to an existing Jellyfin account chosen by an admin.
func (c jellyfin) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	for _, u := range list {
		if u.ID == remoteID {
			return Account{RemoteID: u.ID, Status: StatusLinked, Fields: map[string]string{"USER_ID": u.ID},
				Note: fmt.Sprintf("linked by hand to the Jellyfin account %q", u.Name)}, nil
		}
	}
	return Account{}, fmt.Errorf("Jellyfin has no account %s", remoteID)
}

// ---- Sign in with Pocket Core (SSO Authentication plugin) ----

func (c jellyfin) backup(t Target, what string, v any) (string, error) {
	if err := os.MkdirAll(t.DataDir, 0o700); err != nil {
		return "", fmt.Errorf("backup dir: %w", err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	name := filepath.Join(t.DataDir, "jellyfin-"+what+"-"+t.Clock().UTC().Format("20060102-150405.000")+".json")
	return name, os.WriteFile(name, b, 0o600)
}

// withButton adds (or removes, html == "") Core's block in the login disclaimer.
func withButton(disclaimer, html string) string {
	if i := strings.Index(disclaimer, ssoMarkStart); i >= 0 {
		if j := strings.Index(disclaimer[i:], ssoMarkEnd); j >= 0 {
			disclaimer = strings.TrimSpace(disclaimer[:i] + disclaimer[i+j+len(ssoMarkEnd):])
		}
	}
	if html == "" {
		return disclaimer
	}
	block := ssoMarkStart + html + ssoMarkEnd
	if disclaimer == "" {
		return block
	}
	return block + "\n" + disclaimer
}

func (c jellyfin) ConfigureSSO(ctx context.Context, t Target, sc SSOConfig) error {
	pub := strings.TrimRight(t.PublicURL, "/")
	if sc.Enabled && (pub == "" || sc.Issuer == "" || sc.ClientID == "" || sc.ClientSecret == "") {
		return errors.New("sign-in needs the service's public URL, Core's issuer and a client id and secret")
	}
	plugin, err := c.ssoPlugin(ctx, t)
	if err != nil {
		return err
	}
	if plugin == nil {
		if !sc.Enabled {
			return nil // nothing to turn off
		}
		return errors.New("the Jellyfin SSO Authentication plugin is not installed: add the repository https://raw.githubusercontent.com/9p4/jellyfin-plugin-sso/manifest-release/manifest.json under Dashboard → Plugins → Repositories, install “SSO Authentication”, restart Jellyfin, then switch this on again")
	}
	var current map[string]any
	if err := c.admin(ctx, t, http.MethodGet, "/sso/OID/Get", nil, &current); err != nil {
		return fmt.Errorf("reading the SSO plugin's providers failed: %w", err)
	}
	var branding map[string]any
	if err := c.call(ctx, t, http.MethodGet, "/Branding/Configuration", "", nil, &branding); err != nil {
		return fmt.Errorf("reading Jellyfin's branding failed: %w", err)
	}
	backup, err := c.backup(t, "sso", map[string]any{"providers": current, "branding": branding})
	if err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	if sc.Enabled {
		btn := sc.ButtonText
		if btn == "" {
			btn = "Sign in with Pocket Core"
		}
		// Authorization off: Core already decides who may sign in (the
		// service's audience) and the plugin must not rewrite anyone's
		// libraries or admin rights on login.
		cfg := map[string]any{
			"oidEndpoint": sc.Issuer, "oidClientId": sc.ClientID, "oidSecret": sc.ClientSecret, "enabled": true,
			"enableAuthorization": false, "enableAllFolders": false, "enabledFolders": []string{}, "adminRoles": []string{}, "roles": []string{},
			"enableFolderRoles": false, "folderRoleMapping": []any{}, "roleClaim": "groups", "oidScopes": []string{"openid", "profile", "email", "groups"},
			"defaultUsernameClaim": "preferred_username", "disablePushedAuthorization": true,
		}
		if err := c.admin(ctx, t, http.MethodPost, "/sso/OID/Add/"+JellyfinSSOProvider, cfg, nil, sc.ClientSecret); err != nil {
			return fmt.Errorf("saving the SSO provider failed (backup kept at %s): %w", backup, err)
		}
		start := pub + "/sso/OID/start/" + JellyfinSSOProvider
		html := fmt.Sprintf(`<form action="%s" method="get" style="margin-top:1em"><button class="raised block emby-button button-submit" type="submit">%s</button></form>`, start, btn)
		branding["LoginDisclaimer"] = withButton(fmt.Sprint(nz(branding["LoginDisclaimer"])), html)
	} else {
		if err := c.admin(ctx, t, http.MethodGet, "/sso/OID/Del/"+JellyfinSSOProvider, nil, nil); err != nil && jfStatus(err) != http.StatusNotFound {
			return fmt.Errorf("removing the SSO provider failed (backup kept at %s): %w", backup, err)
		}
		branding["LoginDisclaimer"] = withButton(fmt.Sprint(nz(branding["LoginDisclaimer"])), "")
	}
	if err := c.admin(ctx, t, http.MethodPost, "/System/Configuration/branding", branding, nil); err != nil {
		return fmt.Errorf("updating the login page failed (backup kept at %s): %w", backup, err)
	}
	return nil
}

func nz(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// ---- events (polled) ----

type jfState struct {
	LastActivity int64  `json:"last_activity"`
	LastCreated  string `json:"last_created"`
}

type jfActivity struct {
	ID            int64  `json:"Id"`
	Name          string `json:"Name"`
	ShortOverview string `json:"ShortOverview"`
	Type          string `json:"Type"`
	ItemID        string `json:"ItemId"`
	Date          string `json:"Date"`
	UserID        string `json:"UserId"`
	Severity      string `json:"Severity"`
}

type jfItem struct {
	ID                string `json:"Id"`
	Name              string `json:"Name"`
	Type              string `json:"Type"`
	SeriesName        string `json:"SeriesName"`
	SeasonName        string `json:"SeasonName"`
	IndexNumber       *int   `json:"IndexNumber"`
	ParentIndexNumber *int   `json:"ParentIndexNumber"`
	ProductionYear    int    `json:"ProductionYear"`
	AlbumArtist       string `json:"AlbumArtist"`
	DateCreated       string `json:"DateCreated"`
}

// activityEvents maps Jellyfin's activity types to event names; anything
// else (sessions coming and going, scheduled tasks…) is not news.
var activityEvents = map[string]string{
	"VideoPlayback": "playback_started", "AudioPlayback": "playback_started",
	"VideoPlaybackStopped": "playback_stopped", "AudioPlaybackStopped": "playback_stopped",
	"UserCreated": "user_created", "UserDeleted": "user_deleted", "UserLockedOut": "user_locked_out",
	"AuthenticationFailed": "login_failed",
}

const jfItemTypes = "Movie,Episode,MusicAlbum,Audiobook,Book"

func (c jellyfin) itemURL(t Target, id string) string {
	pub := strings.TrimRight(t.PublicURL, "/")
	if pub == "" || id == "" {
		return ""
	}
	return pub + "/web/#/details?id=" + id
}

func itemPayload(it jfItem) map[string]any {
	p := map[string]any{"item_id": it.ID, "title": it.Name, "item_type": it.Type}
	if it.SeriesName != "" {
		p["series"] = it.SeriesName
	}
	if it.ParentIndexNumber != nil {
		p["season"] = *it.ParentIndexNumber
	}
	if it.IndexNumber != nil && it.Type == "Episode" {
		p["episode"] = *it.IndexNumber
	}
	if it.ProductionYear > 0 {
		p["year"] = it.ProductionYear
	}
	if it.AlbumArtist != "" {
		p["artist"] = it.AlbumArtist
	}
	return p
}

func (c jellyfin) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	var log struct {
		Items []jfActivity `json:"Items"`
	}
	if err := c.admin(ctx, t, http.MethodGet, "/System/ActivityLog/Entries?startIndex=0&limit=500", nil, &log); err != nil {
		return nil, state, err
	}
	var newest struct {
		Items []jfItem `json:"Items"`
	}
	q := url.Values{"Recursive": {"true"}, "IncludeItemTypes": {jfItemTypes}, "SortBy": {"DateCreated"}, "SortOrder": {"Descending"},
		"Limit": {"200"}, "Fields": {"DateCreated,ProductionYear"}}
	if err := c.admin(ctx, t, http.MethodGet, "/Items?"+q.Encode(), nil, &newest); err != nil {
		return nil, state, err
	}
	cur := jfState{}
	for _, e := range log.Items {
		if e.ID > cur.LastActivity {
			cur.LastActivity = e.ID
		}
	}
	for _, it := range newest.Items {
		if jfLater(it.DateCreated, cur.LastCreated) {
			cur.LastCreated = it.DateCreated
		}
	}
	var prev jfState
	if len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &prev) != nil || (prev.LastActivity == 0 && prev.LastCreated == "") {
		next, _ := json.Marshal(cur)
		return nil, next, nil // first poll: baseline only
	}
	if cur.LastActivity < prev.LastActivity {
		cur.LastActivity = prev.LastActivity
	}
	if jfLater(prev.LastCreated, cur.LastCreated) {
		cur.LastCreated = prev.LastCreated
	}
	next, _ := json.Marshal(cur)

	names := map[string]string{}
	if list, err := c.users(ctx, t); err == nil {
		for _, u := range list {
			names[u.ID] = u.Name
		}
	}
	// activity, oldest first
	acts := []jfActivity{}
	ids := []string{}
	for _, e := range log.Items {
		if e.ID > prev.LastActivity && activityEvents[e.Type] != "" {
			acts = append(acts, e)
			if e.ItemID != "" {
				ids = append(ids, e.ItemID)
			}
		}
	}
	sort.Slice(acts, func(i, j int) bool { return acts[i].ID < acts[j].ID })
	items := map[string]jfItem{}
	if len(ids) > 0 {
		var got struct {
			Items []jfItem `json:"Items"`
		}
		q := url.Values{"Ids": {strings.Join(ids, ",")}, "Fields": {"ProductionYear"}}
		if err := c.admin(ctx, t, http.MethodGet, "/Items?"+q.Encode(), nil, &got); err == nil {
			for _, it := range got.Items {
				items[it.ID] = it
			}
		}
	}
	var out []Event
	for _, e := range acts {
		name := activityEvents[e.Type]
		p := map[string]any{"activity_id": e.ID, "summary": e.Name, "at": e.Date}
		if e.UserID != "" {
			p["jellyfin_user_id"] = e.UserID
			if n := names[e.UserID]; n != "" {
				p["jellyfin_user"] = n
			}
		}
		if strings.HasPrefix(e.Type, "Audio") {
			p["media"] = "audio"
		} else if strings.HasPrefix(e.Type, "Video") {
			p["media"] = "video"
		}
		if e.ShortOverview != "" {
			p["detail"] = e.ShortOverview
		}
		if it, ok := items[e.ItemID]; ok {
			for k, v := range itemPayload(it) {
				p[k] = v
			}
			p["url"] = c.itemURL(t, it.ID)
		} else if e.ItemID != "" {
			p["item_id"] = e.ItemID
		}
		ev := Event{Name: name, Subject: fmt.Sprintf("activity:%d", e.ID), RemoteUserID: e.UserID, Payload: p}
		if name == "user_created" || name == "user_deleted" || name == "user_locked_out" {
			ev.Subject = "jellyfin-user:" + e.UserID
		}
		out = append(out, ev)
	}
	// new library items, oldest first
	added := []jfItem{}
	for _, it := range newest.Items {
		if jfLater(it.DateCreated, prev.LastCreated) {
			added = append(added, it)
		}
	}
	sort.Slice(added, func(i, j int) bool { return jfLater(added[j].DateCreated, added[i].DateCreated) })
	for _, it := range added {
		p := itemPayload(it)
		p["added_at"] = it.DateCreated
		p["url"] = c.itemURL(t, it.ID)
		out = append(out, Event{Name: "item_added", Subject: "item:" + it.ID, Payload: p})
	}
	return out, next, nil
}

// jfLater reports whether timestamp a is after b. Jellyfin writes a varying
// number of fraction digits, so the strings do not sort as text.
func jfLater(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	if errA != nil {
		return false
	}
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errB != nil {
		return true // nothing (or garbage) before: anything is later
	}
	return ta.After(tb)
}
