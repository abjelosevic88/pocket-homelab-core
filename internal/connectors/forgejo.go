package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Forgejo (16+) connector; Gitea 1.22+ and older Forgejo work through the
// fallbacks noted below.
//
// Endpoints used (below /api/v1):
//
//	GET    /version                              Test                                   read
//	GET    /user                                 Test, WhoAmI, RotateKey (whose token)  read
//	GET    /users/{admin}/tokens                 Test (the stored token's scopes)       read
//	GET    /admin/users?page&limit&sort=oldest   Test, Provision, Link, RotateKey, Poll read
//	GET    /admin/emails/search?q=               Provision (secondary emails)           read
//	GET    /users/{name}                         Provision (is a username free)         read
//	POST   /admin/users                          Provision, a missing person            WRITE
//	POST   /admin/users/{name}/tokens            Provision, RotateKey: the person's token  WRITE
//	GET    /admin/users/{name}/tokens            RotateKey                              read
//	DELETE /admin/users/{name}/tokens/{id}       RotateKey: the old "Pocket Core" tokens   WRITE
//	GET    /admin/hooks?page&limit               Test, Subscriptions                    read
//	POST   /admin/hooks                          Subscribe (a system webhook)           WRITE
//	DELETE /admin/hooks/{id}                     Unsubscribe                            WRITE
//
// Only where /admin/users/{name}/tokens does not exist (Forgejo before 16,
// Gitea), tokens are minted the old way, as the account with Basic auth:
//
//	PATCH  /admin/users/{name} {password}        RotateKey: a new random password       WRITE
//	POST   /users/{name}/tokens  (Basic)         Provision, RotateKey                   WRITE
//	GET    /users/{name}/tokens                  RotateKey                              read
//	DELETE /users/{name}/tokens/{id}  (Basic)    RotateKey                              WRITE
//
// The box-wide credential is a site administrator's access token with the
// read:admin, write:admin and read:user scopes (or "all"). Accounts carry
// emails, so a person matches their account by email (primary or any
// verified-or-not secondary address). Forgejo's sign-in sources cannot be
// managed over its API, so "Sign in with Pocket Core" is set up by hand with
// `forgejo admin auth add-oauth` (ConfigureSSO says how).
//
// Events arrive by push: one system webhook per event kind (Site
// Administration → Webhooks → System webhooks), each authenticating with
// "Authorization: Bearer <secret>". A system webhook fires for every
// repository on the box; Core attributes each delivery to the person whose
// account did it (the sender) and drops everyone else's. Account changes are
// polled.
type forgejo struct{}

func init() { Register(forgejo{}) }

const (
	// ForgejoTokenName names every token Core mints (rotated ones get " · <time>").
	ForgejoTokenName = "Pocket Core"
	// ForgejoSSOName is the auth source name the admin adds by hand; the
	// callback URL follows it.
	ForgejoSSOName = "PocketCore"
	fjPageSize     = 50
	fjMaxPages     = 400
)

// ForgejoPersonScopes are what a person's minted token may do: read their
// account, organizations, repositories and packages, open and comment on
// issues and pull requests, read and mark notifications. No admin, no
// writing code or repository settings, no account settings (so no keys or
// tokens), no organization changes.
var ForgejoPersonScopes = []string{"read:user", "read:organization", "read:repository", "read:package", "write:issue", "write:notification"}

// forgejoTriggers: one system webhook each. "issues_only" and
// "pull_request_only" leave out labels, assignees, milestones, comments and
// reviews (Forgejo lists them back as "issues" and "pull_request").
var forgejoTriggers = []PushTrigger{
	{Trigger: "push", Name: "Pocket Core · push"},
	{Trigger: "create", Name: "Pocket Core · branch or tag created"},
	{Trigger: "delete", Name: "Pocket Core · branch or tag deleted"},
	{Trigger: "release", Name: "Pocket Core · release"},
	{Trigger: "issues_only", Name: "Pocket Core · issues"},
	{Trigger: "pull_request_only", Name: "Pocket Core · pull requests"},
	{Trigger: "repository", Name: "Pocket Core · repositories"},
	{Trigger: "fork", Name: "Pocket Core · forks"},
}

// fjListedEvent maps an event as Forgejo lists a hook back to its trigger.
func fjListedEvent(ev string) string {
	switch ev {
	case "issues":
		return "issues_only"
	case "pull_request":
		return "pull_request_only"
	}
	return ev
}

func (forgejo) Type() string { return "forgejo" }

func (forgejo) Capabilities() Capabilities {
	return Capabilities{Provision: true, SSO: false, Events: true, Link: true}
}

// SSORedirects: where Forgejo sends people back for an auth source named
// ForgejoSSOName. Core registers it on its client even though the source
// itself is added by hand.
func (forgejo) SSORedirects(publicURL string) []string {
	p := strings.TrimRight(publicURL, "/")
	if p == "" {
		return nil
	}
	return []string{p + "/user/oauth2/" + ForgejoSSOName + "/callback"}
}

func fjTarget(t Target) Target {
	b := strings.TrimRight(t.BaseURL, "/")
	b = strings.TrimSuffix(b, "/api/v1")
	t.BaseURL = b + "/api/v1"
	return t
}

func fjKey(t Target) (string, error) {
	k := strings.TrimSpace(t.Fields["TOKEN"])
	if k == "" {
		return "", errors.New("the Forgejo service in Core has no TOKEN: create an access token for a site administrator in Forgejo (Settings → Applications, scopes read:admin + write:admin + read:user) and paste it under Services → Forgejo → Edit")
	}
	return k, nil
}

// fjMessage is Forgejo's {"message": …} out of an error, else the error.
func fjMessage(err error) string {
	var ae *appError
	if errors.As(err, &ae) {
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(ae.Msg), &m) == nil && m.Message != "" {
			return m.Message
		}
		return ae.Msg
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// call does one request with the given Authorization value (token or Basic).
func (forgejo) call(ctx context.Context, t Target, method, path, authz string, body, out any, secrets ...string) (http.Header, error) {
	hdr := map[string]string{}
	if authz != "" {
		hdr["Authorization"] = authz
	}
	return doJSON(ctx, fjTarget(t), "Forgejo", method, path, hdr, body, out, secrets...)
}

func fjTokenAuth(tok string) string { return "token " + tok }

func fjBasic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// admin does one request with the box-wide token.
func (c forgejo) admin(ctx context.Context, t Target, method, path string, body, out any, secrets ...string) (http.Header, error) {
	k, err := fjKey(t)
	if err != nil {
		return nil, err
	}
	h, err := c.call(ctx, t, method, path, fjTokenAuth(k), body, out, append(secrets, k)...)
	return h, fjAdminErr(err)
}

// fjAdminErr explains what the admin must fix when Forgejo refuses the box-wide token.
func fjAdminErr(err error) error {
	switch appStatus(err) {
	case http.StatusUnauthorized:
		return errors.New("Forgejo rejected the stored token (HTTP 401): it is wrong or was deleted; create a new one under Settings → Applications")
	case http.StatusForbidden:
		return fmt.Errorf("the stored Forgejo token may not do this (HTTP 403: %s): it must be a site administrator's token with the read:admin, write:admin and read:user scopes", fjMessage(err))
	}
	return err
}

type fjUser struct {
	ID            int64     `json:"id"`
	Login         string    `json:"login"`
	LoginName     string    `json:"login_name"`
	SourceID      int64     `json:"source_id"`
	FullName      string    `json:"full_name"`
	Email         string    `json:"email"`
	HTMLURL       string    `json:"html_url"`
	IsAdmin       bool      `json:"is_admin"`
	Active        bool      `json:"active"`
	ProhibitLogin bool      `json:"prohibit_login"`
	Restricted    bool      `json:"restricted"`
	Created       time.Time `json:"created"`
}

func (u fjUser) id() string { return strconv.FormatInt(u.ID, 10) }

type fjToken struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	SHA1           string   `json:"sha1"`
	TokenLastEight string   `json:"token_last_eight"`
	Scopes         []string `json:"scopes"`
}

// fjAll pages through a list with the box-wide token.
func fjAll[T any](ctx context.Context, c forgejo, t Target, path string) ([]T, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var out []T
	for page := 1; page <= fjMaxPages; page++ {
		var got []T
		h, err := c.admin(ctx, t, http.MethodGet, fmt.Sprintf("%s%spage=%d&limit=%d", path, sep, page, fjPageSize), nil, &got)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		total, terr := strconv.Atoi(h.Get("X-Total-Count"))
		if len(got) == 0 || (terr == nil && len(out) >= total) || (terr != nil && len(got) < fjPageSize) {
			return out, nil
		}
	}
	return nil, fmt.Errorf("Forgejo's %s did not end after %d pages", path, fjMaxPages)
}

// users lists every person account (organizations and bots are not users here).
func (c forgejo) users(ctx context.Context, t Target) ([]fjUser, error) {
	return fjAll[fjUser](ctx, c, t, "/admin/users?sort=oldest")
}

func fjByID(list []fjUser, id string) *fjUser {
	for i := range list {
		if list[i].id() == id {
			return &list[i]
		}
	}
	return nil
}

func (c forgejo) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	k, err := fjKey(t)
	if err != nil {
		return info, err
	}
	var v struct {
		Version string `json:"version"`
	}
	if _, err := c.call(ctx, t, http.MethodGet, "/version", fjTokenAuth(k), nil, &v, k); err != nil {
		if appStatus(err) == 0 {
			return info, fmt.Errorf("cannot reach Forgejo at %s: %w", t.BaseURL, err)
		}
		return info, fjAdminErr(err)
	}
	if v.Version == "" {
		return info, fmt.Errorf("%s does not answer like Forgejo: check the service URL", t.BaseURL)
	}
	info.Version = v.Version
	var me fjUser
	if _, err := c.admin(ctx, t, http.MethodGet, "/user", nil, &me); err != nil {
		return info, err
	}
	if !me.IsAdmin {
		return info, fmt.Errorf("the stored token is %q's, who is not a Forgejo site administrator: make the token as an administrator", me.Login)
	}
	info.Admin = true
	// the token's own scopes, by its last eight characters
	var toks []fjToken
	if _, err := c.admin(ctx, t, http.MethodGet, "/users/"+url.PathEscape(me.Login)+"/tokens?limit=50", nil, &toks); err == nil && len(k) >= 8 {
		for _, tk := range toks {
			if tk.TokenLastEight != "" && strings.HasSuffix(k, tk.TokenLastEight) {
				info.Permissions = tk.Scopes
			}
		}
	}
	if len(info.Permissions) > 0 && !contains(info.Permissions, "all") && !contains(info.Permissions, "write:admin") {
		return info, fmt.Errorf("the stored Forgejo token has the scopes %s: Core needs write:admin (and read:user) to make accounts, tokens and webhooks; make a new token with them", strings.Join(info.Permissions, ", "))
	}
	h, err := c.admin(ctx, t, http.MethodGet, "/admin/users?limit=1&page=1", nil, &[]fjUser{})
	if err != nil {
		return info, err
	}
	if n, err := strconv.Atoi(h.Get("X-Total-Count")); err == nil {
		info.Users = n
	} else if list, err := c.users(ctx, t); err == nil {
		info.Users = len(list)
	}
	notes := []string{}
	if subs, err := c.Subscriptions(ctx, t); err == nil {
		ours := 0
		for _, name := range subs {
			if strings.HasPrefix(name, "Pocket Core · ") {
				ours++
			}
		}
		if ours > 0 {
			notes = append(notes, fmt.Sprintf("%d of Core's system webhooks are under Site Administration → Webhooks", ours))
		} else {
			notes = append(notes, fmt.Sprintf("switching Events on adds %d system webhooks (Site Administration → Webhooks → System webhooks) that report pushes, branches and tags, releases, issues, pull requests, new and deleted repositories and forks of every repository, each attributed to the person who did it", len(forgejoTriggers)))
		}
	}
	notes = append(notes, "Forgejo only calls public addresses unless [webhook] ALLOWED_HOST_LIST lets it reach Core (e.g. FORGEJO__webhook__ALLOWED_HOST_LIST=<Core's host> or private)")
	notes = append(notes, "sign-in sources cannot be set over Forgejo's API: add Pocket Core with `forgejo admin auth add-oauth --name "+ForgejoSSOName+"`")
	info.Detail = strings.Join(notes, "; ")
	return info, nil
}

// ---- accounts ----

func (c forgejo) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		return Account{}, errors.New("person has no email")
	}
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	if u := c.findByEmail(ctx, t, list, email); u != nil {
		note := fmt.Sprintf("the Forgejo account %q has this email; linked with its user id, no token minted (make one under Settings → Applications, or set it in Core by hand)", u.Login)
		if u.IsAdmin {
			note = fmt.Sprintf("the Forgejo administrator %q has this email; linked with its user id and left untouched", u.Login)
		}
		return Account{RemoteID: u.id(), Status: StatusLinked, Note: note}, nil
	}
	password := RandomPassword(40) // nobody knows it: the person signs in with Pocket Core
	taken := map[string]bool{}
	for _, u := range list {
		taken[strings.ToLower(u.Login)] = true
	}
	var created fjUser
	var name string
	for attempt := 0; ; attempt++ {
		name, err = c.freeUsername(ctx, t, p, email, taken)
		if err != nil {
			return Account{}, err
		}
		in := map[string]any{"username": name, "login_name": name, "source_id": 0, "email": email, "full_name": strings.TrimSpace(p.Name),
			"password": password, "must_change_password": false, "send_notify": false, "restricted": false}
		_, err = c.admin(ctx, t, http.MethodPost, "/admin/users", in, &created, password)
		if err == nil {
			break
		}
		msg := strings.ToLower(fjMessage(err))
		if appStatus(err) == http.StatusUnprocessableEntity && attempt < 4 && (strings.Contains(msg, "user already exists") || strings.Contains(msg, "name is reserved") ||
			strings.Contains(msg, "not allowed") || strings.Contains(msg, "username")) {
			taken[strings.ToLower(name)] = true // someone took it meanwhile, or Forgejo refuses it: the next one
			continue
		}
		return Account{}, fmt.Errorf("creating the Forgejo account %q failed: %w", name, err)
	}
	if created.ID == 0 {
		return Account{}, fmt.Errorf("creating the Forgejo account %q returned no user id", name)
	}
	if created.Login != "" {
		name = created.Login
	}
	acc := Account{RemoteID: created.id(), Status: StatusOK, Created: true}
	if p.KeepOwnCredential {
		acc.Note = fmt.Sprintf("account %q created; the person already has their own Forgejo token in Core, so none was minted", name)
		return acc, nil
	}
	tok, _, err := c.mint(ctx, t, name, ForgejoTokenName, password)
	if err != nil {
		return acc, fmt.Errorf("the Forgejo account %q was created but minting its token failed: %w; a retry will link the account by email", name, err)
	}
	acc.Fields = map[string]string{"TOKEN": tok.SHA1}
	acc.Note = fmt.Sprintf("account %q created with a random password nobody knows and its own token (%s): the person signs in with Pocket Core once it is added as a sign-in source, or an admin sets a password in Forgejo", name, strings.Join(ForgejoPersonScopes, ", "))
	return acc, nil
}

// findByEmail: the account whose primary email it is, else whose secondary.
func (c forgejo) findByEmail(ctx context.Context, t Target, list []fjUser, email string) *fjUser {
	for i := range list {
		if strings.EqualFold(strings.TrimSpace(list[i].Email), email) {
			return &list[i]
		}
	}
	var found []struct {
		Email    string `json:"email"`
		UserID   int64  `json:"user_id"`
		UserName string `json:"username"`
	}
	if _, err := c.admin(ctx, t, http.MethodGet, "/admin/emails/search?q="+url.QueryEscape(email)+"&limit=50", nil, &found); err != nil {
		return nil // older Forgejo: primary emails only
	}
	for _, e := range found {
		if strings.EqualFold(strings.TrimSpace(e.Email), email) {
			if u := fjByID(list, strconv.FormatInt(e.UserID, 10)); u != nil {
				return u
			}
		}
	}
	return nil
}

// fjReserved are names Forgejo refuses for accounts (models/user reservedUsernames).
var fjReserved = map[string]bool{
	"api": true, "metrics": true, "v2": true, "assets": true, "attachments": true, "avatar": true, "avatars": true,
	"repo-avatars": true, "captcha": true, "login": true, "org": true, "repo": true, "user": true, "admin": true,
	"explore": true, "issues": true, "pulls": true, "milestones": true, "notifications": true, "report_abuse": true,
	"ssh_info": true, "ghost": true, "gitea-actions": true, "forgejo-actions": true,
}

var fjTranslit = strings.NewReplacer("š", "s", "đ", "dj", "č", "c", "ć", "c", "ž", "z", "ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"á", "a", "à", "a", "â", "a", "å", "a", "é", "e", "è", "e", "ê", "e", "ë", "e", "í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ø", "o", "ú", "u", "ù", "u", "û", "u", "ñ", "n", "ç", "c", "ł", "l", "ý", "y")

// fjUsername makes s a name Forgejo accepts (letters, digits, single dashes,
// starting and ending with a letter or digit, at most 40), "" if nothing is left.
func fjUsername(s string, max int) string {
	s = fjTranslit.Replace(strings.ToLower(strings.TrimSpace(s)))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > max {
		out = strings.Trim(out[:max], "-")
	}
	return out
}

// freeUsername picks the first name nobody has: the email's local part, the
// person's name, then the local part with -2, -3…. Never an existing
// account's or organization's name: an account is never taken over.
func (c forgejo) freeUsername(ctx context.Context, t Target, p Person, email string, taken map[string]bool) (string, error) {
	local := fjUsername(usernameFor(email), 40)
	if local == "" {
		local = "user"
	}
	cands := []string{local}
	if n := fjUsername(p.Name, 40); n != "" && n != local {
		cands = append(cands, n)
	}
	for i := 2; i <= 99; i++ {
		suf := "-" + strconv.Itoa(i)
		cands = append(cands, fjUsername(local, 40-len(suf))+suf)
	}
	for _, n := range cands {
		if n == "" || taken[n] || fjReserved[n] {
			continue
		}
		// organizations and renamed accounts share the namespace
		_, err := c.admin(ctx, t, http.MethodGet, "/users/"+url.PathEscape(n), nil, nil)
		switch {
		case appStatus(err) == http.StatusNotFound:
			return n, nil
		case err != nil:
			return "", fmt.Errorf("checking whether the Forgejo username %q is free failed: %w", n, err)
		}
		taken[n] = true
	}
	return "", fmt.Errorf("no free Forgejo username for %s (tried %s and %s-2…-99)", email, local, local)
}

// mint makes a token for the account: as the admin (Forgejo 16+), else as
// the account with its password. basic reports the second way; it needs a
// password, and "" means the admin endpoint is required.
func (c forgejo) mint(ctx context.Context, t Target, login, name, password string) (fjToken, bool, error) {
	var tok fjToken
	body := map[string]any{"name": name, "scopes": ForgejoPersonScopes}
	_, err := c.admin(ctx, t, http.MethodPost, "/admin/users/"+url.PathEscape(login)+"/tokens", body, &tok)
	if s := appStatus(err); (s == http.StatusNotFound || s == http.StatusMethodNotAllowed) && password != "" {
		// no admin token endpoint (before Forgejo 16): as the account itself
		tok = fjToken{}
		_, err = c.call(ctx, t, http.MethodPost, "/users/"+url.PathEscape(login)+"/tokens", fjBasic(login, password), body, &tok, password)
		if err != nil {
			if s := appStatus(err); s == http.StatusUnauthorized || s == http.StatusForbidden {
				err = fmt.Errorf("signing in as the account with its new password was refused (is Basic auth off, [service] ENABLE_BASIC_AUTHENTICATION?): %s", fjMessage(err))
			}
			return tok, true, err
		}
		if tok.SHA1 == "" {
			return tok, true, errors.New("Forgejo minted a token but sent no secret")
		}
		return tok, true, nil
	}
	if err != nil {
		return tok, false, err
	}
	if tok.SHA1 == "" {
		return tok, false, errors.New("Forgejo minted a token but sent no secret")
	}
	return tok, false, nil
}

// RemoteAccounts lists Forgejo accounts for linking by hand.
func (c forgejo) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(list))
	for _, u := range list {
		n := u.Login
		if u.Email != "" {
			n += " · " + u.Email
		}
		out = append(out, RemoteAccount{ID: u.id(), Name: n, Admin: u.IsAdmin, Disabled: !u.Active || u.ProhibitLogin})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Link ties a person to an existing Forgejo account chosen by an admin.
func (c forgejo) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	list, err := c.users(ctx, t)
	if err != nil {
		return Account{}, err
	}
	if u := fjByID(list, remoteID); u != nil {
		return Account{RemoteID: u.id(), Status: StatusLinked, Note: fmt.Sprintf("linked by hand to the Forgejo account %q", u.Login)}, nil
	}
	return Account{}, fmt.Errorf("Forgejo has no account %s", remoteID)
}

// ---- replacing a person's token ----

func fjOurs(name string) bool {
	return name == ForgejoTokenName || strings.HasPrefix(name, ForgejoTokenName+" · ")
}

// RotateKey mints a new token for the account as the admin and deletes the
// account's older "Pocket Core" tokens (and the stored one). Where Forgejo
// has no admin token endpoints (before 16) it sets a new random password on
// the account first (Core made it; nobody knows the old one) and acts as it.
func (c forgejo) RotateKey(ctx context.Context, t Target, a AccountRef) (Rotated, error) {
	var out Rotated
	list, err := c.users(ctx, t)
	if err != nil {
		return out, err
	}
	user := fjByID(list, a.RemoteID)
	if user == nil {
		return out, fmt.Errorf("Forgejo has no account %s any more", a.RemoteID)
	}
	if user.IsAdmin {
		return out, fmt.Errorf("the Forgejo account %q is a site administrator: Core leaves its tokens alone", user.Login)
	}
	old := strings.TrimSpace(a.Fields["TOKEN"])
	if old != "" {
		var me fjUser
		_, err := c.call(ctx, t, http.MethodGet, "/user", fjTokenAuth(old), nil, &me, old)
		switch {
		case err == nil && me.ID != user.ID:
			return out, fmt.Errorf("the stored token belongs to another Forgejo account (%q, not %q): Core leaves it alone", me.Login, user.Login)
		case err != nil && appStatus(err) != http.StatusUnauthorized && appStatus(err) != http.StatusForbidden:
			return out, err
		}
	}
	name := ForgejoTokenName + " · " + t.Clock().UTC().Format("2006-01-02 15:04:05")
	tok, basic, err := c.mint(ctx, t, user.Login, name, "")
	password := ""
	if s := appStatus(err); err != nil && !basic && (s == http.StatusNotFound || s == http.StatusMethodNotAllowed) {
		// before Forgejo 16: set a new random password and act as the account
		password = RandomPassword(40)
		in := map[string]any{"password": password, "must_change_password": false, "login_name": user.LoginName, "source_id": user.SourceID}
		if in["login_name"] == "" {
			in["login_name"] = user.Login
		}
		if _, err := c.admin(ctx, t, http.MethodPatch, "/admin/users/"+url.PathEscape(user.Login), in, nil, password); err != nil {
			return out, fmt.Errorf("this Forgejo has no admin token endpoints and setting a new password on the account failed: %w", err)
		}
		out.Note = "this Forgejo (before 16) only lets an account mint its own tokens, so Core set a new random password on the account to act as it"
		tok, basic, err = c.mint(ctx, t, user.Login, name, password)
	}
	if err != nil {
		return out, fmt.Errorf("minting the new Forgejo token failed, the old one still works: %w", err)
	}
	out.Fields = map[string]string{"TOKEN": tok.SHA1}
	listPath := "/admin/users/" + url.PathEscape(user.Login) + "/tokens"
	if basic {
		listPath = "/users/" + url.PathEscape(user.Login) + "/tokens" // listing needs no Basic auth, only admin
	}
	toks, err := fjAll[fjToken](ctx, c, t, listPath)
	if err != nil {
		return out, fmt.Errorf("the new token is saved but listing the old ones failed, so they still work: %w", err)
	}
	var failed []string
	for _, k := range toks {
		stored := old != "" && k.TokenLastEight != "" && strings.HasSuffix(old, k.TokenLastEight)
		if k.ID == tok.ID || !(fjOurs(k.Name) || stored) {
			continue
		}
		id := strconv.FormatInt(k.ID, 10)
		if basic {
			_, err = c.call(ctx, t, http.MethodDelete, "/users/"+url.PathEscape(user.Login)+"/tokens/"+id, fjBasic(user.Login, password), nil, nil, password)
		} else {
			_, err = c.admin(ctx, t, http.MethodDelete, "/admin/users/"+url.PathEscape(user.Login)+"/tokens/"+id, nil, nil)
		}
		if err != nil && appStatus(err) != http.StatusNotFound {
			failed = append(failed, k.Name+": "+fjMessage(err))
		}
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("the new token is saved but deleting the old one(s) failed, so they still work: %s", strings.Join(failed, "; "))
	}
	if old != "" && old != tok.SHA1 {
		if _, err := c.call(ctx, t, http.MethodGet, "/user", fjTokenAuth(old), nil, nil, old); err == nil {
			return out, errors.New(OldStillWorks)
		}
	}
	return out, nil
}

// ---- Sign in with Pocket Core: by hand ----

func (c forgejo) ConfigureSSO(_ context.Context, t Target, sc SSOConfig) error {
	if !sc.Enabled {
		return nil // Core never added anything to take away
	}
	iss := strings.TrimRight(sc.Issuer, "/")
	cb := "<Forgejo's public URL>/user/oauth2/" + ForgejoSSOName + "/callback"
	if r := (forgejo{}).SSORedirects(t.PublicURL); len(r) > 0 {
		cb = r[0]
	}
	return fmt.Errorf("Forgejo cannot change its sign-in sources over its API; add Pocket Core on the Forgejo host instead: "+
		"forgejo admin auth add-oauth --name %s --provider openidConnect --key %s --secret <the client secret> --auto-discover-url %s/.well-known/openid-configuration --scopes \"openid email profile\" "+
		"(in Docker: docker exec -u git forgejo forgejo admin auth add-oauth …), then restart Forgejo. Its callback is %s. "+
		"Accounts Core made have passwords nobody knows: to land people in them set [oauth2_client] ENABLE_AUTO_REGISTRATION=true and ACCOUNT_LINKING=auto (Forgejo then links by username first, then by email)",
		ForgejoSSOName, sc.ClientID, iss, cb)
}

// ---- events by push: system webhooks ----

func (forgejo) PushTriggers() []PushTrigger { return append([]PushTrigger(nil), forgejoTriggers...) }

// PushPerUser: system webhooks are box-wide; each delivery names its sender.
func (forgejo) PushPerUser() bool { return false }

func (c forgejo) WhoAmI(ctx context.Context, t Target) (RemoteUser, error) {
	var me fjUser
	if _, err := c.admin(ctx, t, http.MethodGet, "/user", nil, &me); err != nil {
		return RemoteUser{}, err
	}
	return RemoteUser{ID: me.id(), Email: strings.ToLower(strings.TrimSpace(me.Email))}, nil
}

type fjHook struct {
	ID     int64             `json:"id"`
	Type   string            `json:"type"`
	URL    string            `json:"url"`
	Config map[string]string `json:"config"`
	Events []string          `json:"events"`
	Active bool              `json:"active"`
	// authorization_header comes back in clear text: never read or logged
}

func (h fjHook) url() string {
	if h.URL != "" {
		return h.URL
	}
	return h.Config["url"]
}

// hooks lists the system webhooks (Forgejo leaves default/template ones out).
func (c forgejo) hooks(ctx context.Context, t Target) ([]fjHook, error) {
	return fjAll[fjHook](ctx, c, t, "/admin/hooks")
}

func (c forgejo) Subscribe(ctx context.Context, t Target, s Subscription) (string, error) {
	if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("Core's hook URL %q is not usable: set BASE_URL to the address Forgejo reaches Core at", s.URL)
	}
	known := false
	for _, tr := range forgejoTriggers {
		known = known || tr.Trigger == s.Trigger
	}
	if !known {
		return "", fmt.Errorf("unknown Forgejo hook trigger %q", s.Trigger)
	}
	body := map[string]any{
		"type":   "forgejo",
		"active": true,
		"events": []string{s.Trigger},
		// is_system_webhook: without it Forgejo makes a default (template)
		// hook that is only copied into repositories created later
		"config":               map[string]string{"url": s.URL, "content_type": "json", "is_system_webhook": "true"},
		"authorization_header": "Bearer " + s.HeaderValue,
	}
	var made fjHook
	_, err := c.admin(ctx, t, http.MethodPost, "/admin/hooks", body, &made, s.HeaderValue)
	if appStatus(err) == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(fjMessage(err)), "invalid hook type") {
		body["type"] = "gitea" // Gitea: same JSON payloads
		_, err = c.admin(ctx, t, http.MethodPost, "/admin/hooks", body, &made, s.HeaderValue)
	}
	if err != nil {
		return "", fmt.Errorf("adding the Forgejo system webhook %q failed: %w", s.Name, err)
	}
	if made.ID == 0 {
		return "", errors.New("Forgejo created the webhook but returned no id")
	}
	id := strconv.FormatInt(made.ID, 10)
	// A build that ignores is_system_webhook made a template hook instead:
	// it would never fire for existing repositories.
	list, err := c.hooks(ctx, t)
	if err != nil {
		return id, nil // the next check heals it if it is not there
	}
	for _, h := range list {
		if h.ID == made.ID {
			return id, nil
		}
	}
	_, _ = c.admin(ctx, t, http.MethodDelete, "/admin/hooks/"+id, nil, nil)
	return "", errors.New("this Forgejo made a default (template) webhook instead of a system webhook, which would never fire for existing repositories; Core needs Forgejo 7+ (or Gitea 1.22+)")
}

func (c forgejo) Subscriptions(ctx context.Context, t Target) (map[string]string, error) {
	list, err := c.hooks(ctx, t)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, tr := range forgejoTriggers {
		names[tr.Trigger] = tr.Name
	}
	out := make(map[string]string, len(list))
	for _, h := range list {
		name := "system webhook " + strconv.FormatInt(h.ID, 10)
		if len(h.Events) == 1 && strings.Contains(h.url(), ncHookPathPart) {
			if n, ok := names[fjListedEvent(h.Events[0])]; ok {
				name = n
			}
		}
		out[strconv.FormatInt(h.ID, 10)] = name
	}
	return out, nil
}

func (c forgejo) Unsubscribe(ctx context.Context, t Target, remoteID string) error {
	if remoteID == "" {
		return nil
	}
	_, err := c.admin(ctx, t, http.MethodDelete, "/admin/hooks/"+url.PathEscape(remoteID), nil, nil)
	if err == nil || appStatus(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// ---- decoding deliveries ----

type fjSender struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type fjRepo struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	FullName string    `json:"full_name"`
	HTMLURL  string    `json:"html_url"`
	Private  bool      `json:"private"`
	Fork     bool      `json:"fork"`
	Mirror   bool      `json:"mirror"`
	Created  time.Time `json:"created_at"`
}

type fjCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	URL     string `json:"url"`
	Author  *struct {
		Name     string `json:"name"`
		UserName string `json:"username"`
	} `json:"author"`
}

type fjIssue struct {
	ID        int64      `json:"id"`
	Number    int64      `json:"number"`
	Title     string     `json:"title"`
	HTMLURL   string     `json:"html_url"`
	State     string     `json:"state"`
	Merged    bool       `json:"merged"`
	MergedAt  *time.Time `json:"merged_at"`
	Draft     bool       `json:"draft"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	Base      *struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head *struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// fjDelivery is the union of the payloads Core subscribes to.
type fjDelivery struct {
	Action string    `json:"action"`
	Sender *fjSender `json:"sender"`
	Repo   *fjRepo   `json:"repository"`
	// push
	Ref          string     `json:"ref"`
	Before       string     `json:"before"`
	After        string     `json:"after"`
	CompareURL   string     `json:"compare_url"`
	Commits      []fjCommit `json:"commits"`
	TotalCommits int        `json:"total_commits"`
	// create / delete
	RefType string `json:"ref_type"`
	SHA     string `json:"sha"`
	// issues / pull requests
	Number      int64    `json:"number"`
	Issue       *fjIssue `json:"issue"`
	PullRequest *fjIssue `json:"pull_request"`
	// release
	Release *struct {
		ID          int64     `json:"id"`
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		HTMLURL     string    `json:"html_url"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		CreatedAt   time.Time `json:"created_at"`
	} `json:"release"`
	// fork: forkee is the repository forked FROM, repository the new fork
	Forkee *fjRepo `json:"forkee"`
}

// fjBot: Forgejo's ghost (-1) and actions (-2) users, and bot-named accounts.
func fjBot(s *fjSender) bool {
	if s == nil || s.ID <= 0 {
		return true
	}
	l := strings.ToLower(s.Login)
	return l == "ghost" || l == "forgejo-actions" || l == "gitea-actions" || strings.HasSuffix(l, "[bot]")
}

func fjZeroSHA(s string) bool { return s != "" && strings.Trim(s, "0") == "" }

func fjFirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// fjRefURL is the page of a branch or tag.
func fjRefURL(repoURL, kind, name string) string {
	if repoURL == "" || name == "" {
		return repoURL
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.TrimRight(repoURL, "/") + "/src/" + kind + "/" + strings.Join(parts, "/")
}

func fjTime(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

func (forgejo) Decode(t Target, trigger string, body []byte) (Delivery, error) {
	var in fjDelivery
	if err := json.Unmarshal(body, &in); err != nil {
		return Delivery{}, fmt.Errorf("not a Forgejo webhook delivery: %w", err)
	}
	if in.Repo == nil || in.Repo.ID == 0 {
		return Delivery{}, errors.New("not a Forgejo webhook delivery: no repository")
	}
	sum := sha256.Sum256(body)
	d := Delivery{DedupeKey: trigger + ":" + hex.EncodeToString(sum[:12])}
	if in.Sender != nil {
		d.OwnerID = strconv.FormatInt(in.Sender.ID, 10)
	}
	if fjBot(in.Sender) {
		login := "nobody"
		if in.Sender != nil {
			login = in.Sender.Login
		}
		d.Drop = "done by " + login + " (Forgejo's ghost, actions or a bot), not a person"
		return d, nil
	}
	r := in.Repo
	p := map[string]any{"repo": r.FullName, "repo_url": r.HTMLURL, "private": r.Private, "forgejo_user_id": in.Sender.ID, "forgejo_user": in.Sender.Login}
	if r.HTMLURL == "" {
		if pub := strings.TrimRight(t.PublicURL, "/"); pub != "" && r.FullName != "" {
			p["repo_url"] = pub + "/" + r.FullName
		}
	}
	repoURL, _ := p["repo_url"].(string)
	subject := "repo:" + strconv.FormatInt(r.ID, 10)
	name, title, link := "", "", repoURL
	mirrorSync := r.Mirror && (trigger == "push" || trigger == "create" || trigger == "delete" || trigger == "release")
	if mirrorSync {
		d.Drop = "pull-mirror sync of " + r.FullName + ", not someone's push"
		return d, nil
	}

	switch trigger {
	case "push":
		switch {
		case strings.HasPrefix(in.Ref, "refs/tags/"):
			d.Drop = "tag push (tag_created covers it)"
			return d, nil
		case fjZeroSHA(in.After):
			d.Drop = "branch deletion (branch_deleted covers it)"
			return d, nil
		case in.TotalCommits == 0 && len(in.Commits) == 0:
			d.Drop = "push without new commits (branch_created covers a new branch)"
			return d, nil
		}
		branch := strings.TrimPrefix(in.Ref, "refs/heads/")
		n := in.TotalCommits
		if n == 0 {
			n = len(in.Commits)
		}
		msgs := []string{}
		for _, cm := range in.Commits {
			if len(msgs) == 5 {
				break
			}
			if l := fjFirstLine(cm.Message); l != "" {
				msgs = append(msgs, l)
			}
		}
		p["branch"], p["ref"], p["commits"], p["before"], p["after"] = branch, in.Ref, n, in.Before, in.After
		if len(msgs) > 0 {
			p["commit_messages"] = msgs
		}
		setNonEmpty(p, "compare_url", in.CompareURL)
		name = "pushed"
		noun := "commits"
		if n == 1 {
			noun = "commit"
		}
		title = fmt.Sprintf("Pushed %d %s to %s (%s)", n, noun, r.FullName, branch)
		link = in.CompareURL
		if link == "" || fjZeroSHA(in.Before) {
			link = fjRefURL(repoURL, "branch", branch)
		}
	case "create", "delete":
		kind := in.RefType
		if kind != "branch" && kind != "tag" {
			d.Drop = "unknown ref type " + kind
			return d, nil
		}
		short := strings.TrimPrefix(strings.TrimPrefix(in.Ref, "refs/heads/"), "refs/tags/")
		p["ref"], p["ref_type"] = short, kind
		p[kind] = short
		setNonEmpty(p, "sha", in.SHA)
		verb := "created"
		if trigger == "delete" {
			verb = "deleted"
		} else {
			link = fjRefURL(repoURL, kind, short)
		}
		name = kind + "_" + verb
		title = fmt.Sprintf("%s %s %s in %s", strings.ToUpper(kind[:1])+kind[1:], short, verb, r.FullName)
	case "release":
		rel := in.Release
		if rel == nil {
			return Delivery{}, errors.New("not a Forgejo release delivery: no release")
		}
		if in.Action != "published" {
			d.Drop = "release " + in.Action + " (only publications are events)"
			return d, nil
		}
		if rel.Draft {
			d.Drop = "draft release"
			return d, nil
		}
		p["release_id"], p["tag"], p["prerelease"] = rel.ID, rel.TagName, rel.Prerelease
		setNonEmpty(p, "release_name", rel.Name)
		name = "release_published"
		title = r.FullName + " " + strings.TrimSpace(firstNonEmpty(rel.Name, rel.TagName))
		link = firstNonEmpty(rel.HTMLURL, link)
		subject = "release:" + strconv.FormatInt(rel.ID, 10)
		d.At = firstTime(rel.PublishedAt, rel.CreatedAt)
	case "issues_only", "pull_request_only":
		it, kind := in.Issue, "issue"
		if trigger == "pull_request_only" {
			it, kind = in.PullRequest, "pull_request"
		}
		if it == nil {
			return Delivery{}, fmt.Errorf("not a Forgejo %s delivery: no %s", kind, kind)
		}
		action := in.Action
		switch action {
		case "opened":
			d.At = fjTime(it.CreatedAt)
		case "reopened":
			d.At = fjTime(it.UpdatedAt)
		case "closed":
			d.At = fjTime(it.ClosedAt)
			if kind == "pull_request" && it.Merged {
				action = "merged"
				d.At = firstTime(fjTime(it.MergedAt), d.At)
			}
		default:
			d.Drop = kind + " " + action + " (only opened, closed, merged and reopened are events)"
			return d, nil
		}
		num := it.Number
		if num == 0 {
			num = in.Number
		}
		p["number"], p["state"] = num, it.State
		p["title"] = it.Title
		if kind == "pull_request" {
			p["merged"], p["draft"] = it.Merged, it.Draft
			if it.Base != nil {
				p["base"] = it.Base.Ref
			}
			if it.Head != nil {
				p["head"] = it.Head.Ref
			}
		}
		name = kind + "_" + action
		title = it.Title
		link = firstNonEmpty(it.HTMLURL, link)
		short := "issue"
		if kind == "pull_request" {
			short = "pull"
		}
		subject = fmt.Sprintf("%s:%d#%d", short, r.ID, num)
	case "repository":
		switch in.Action {
		case "created":
			if r.Fork {
				d.Drop = "fork created (repo_forked covers it)"
				return d, nil
			}
			d.At = r.Created
		case "deleted":
		default:
			d.Drop = "repository " + in.Action
			return d, nil
		}
		p["mirror"], p["fork"] = r.Mirror, r.Fork
		name = "repo_" + in.Action
		title = r.FullName
		if in.Action == "deleted" {
			link = ""
		}
	case "fork":
		name = "repo_forked"
		title = r.FullName
		if f := in.Forkee; f != nil {
			p["forked_from"], p["forked_from_url"] = f.FullName, f.HTMLURL
			title = r.FullName + " forked from " + f.FullName
		}
	default:
		d.Drop = "unknown trigger " + trigger
		return d, nil
	}
	if title != "" {
		p["title"] = title
	}
	if link != "" {
		p["url"] = link
	}
	d.Event = Event{Name: name, Subject: subject, RemoteUserID: d.OwnerID, Personal: true, Payload: p}
	return d, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func firstTime(v ...time.Time) time.Time {
	for _, x := range v {
		if !x.IsZero() && x.Year() > 1970 {
			return x
		}
	}
	return time.Time{}
}

// ---- events by poll: accounts ----

type fjState struct {
	Init  bool    `json:"init"`
	Users []int64 `json:"users"`
}

func (c forgejo) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	users, err := c.users(ctx, t)
	if err != nil {
		return nil, state, err
	}
	cur := fjState{Init: true, Users: make([]int64, 0, len(users))}
	for _, u := range users {
		cur.Users = append(cur.Users, u.ID)
	}
	sort.Slice(cur.Users, func(i, j int) bool { return cur.Users[i] < cur.Users[j] })
	next, _ := json.Marshal(cur)
	var prev fjState
	if len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &prev) != nil || !prev.Init {
		return nil, next, nil // first poll: baseline only
	}
	had := map[int64]bool{}
	for _, id := range prev.Users {
		had[id] = true
	}
	var out []Event
	for _, u := range users {
		if had[u.ID] {
			delete(had, u.ID)
			continue
		}
		pl := map[string]any{"forgejo_user_id": u.ID, "forgejo_user": u.Login, "admin": u.IsAdmin, "title": u.Login}
		setNonEmpty(pl, "full_name", u.FullName)
		setNonEmpty(pl, "url", u.HTMLURL)
		out = append(out, Event{Name: "user_created", Subject: "forgejo-user:" + u.id(), RemoteUserID: u.id(), Payload: pl})
	}
	gone := make([]int64, 0, len(had))
	for id := range had {
		gone = append(gone, id)
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
	for _, id := range gone {
		sid := strconv.FormatInt(id, 10)
		out = append(out, Event{Name: "user_deleted", Subject: "forgejo-user:" + sid, RemoteUserID: sid,
			Payload: map[string]any{"forgejo_user_id": id}})
	}
	return out, next, nil
}
