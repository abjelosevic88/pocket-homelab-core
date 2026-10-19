package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Paperless-ngx (tested against 3.3, API version 10). Core acts with the
// service's TOKEN, which must belong to a superuser.
//
//	GET  /api/ui_settings/                  Test (who the token is)            read
//	GET  /api/users/                        Test, Provision, linking, Poll     read
//	GET  /api/groups/?name__iexact=…        Provision (the members' group)     read
//	POST /api/groups/                       Provision, first new account       WRITE
//	POST /api/users/                        Provision, missing user            WRITE
//	POST /api/token/                        Provision, as the new user: their own token
//	GET|POST|DELETE /api/workflows/         events                             WRITE
//
// Accounts: linked by email; a missing person gets an account named like
// the email's local part in the "Pocket Core" group (everything a person
// needs for their own documents, nothing that administers Paperless) and
// their own API token. An account by that name with another email is never
// taken over: link it by hand.
//
// Sign-in cannot be switched on over the API: Paperless reads its OpenID
// providers from PAPERLESS_APPS / PAPERLESS_SOCIALACCOUNT_PROVIDERS at start
// (see docs/connectors.md). So no SSO capability.
//
// Events: two workflows (Document Added, Document Updated) whose webhook
// action posts JSON to Core with the per-hook secret header. Workflows run
// for every account; each delivery names the document's owner.
type paperless struct{}

func init() { Register(paperless{}) }

// PaperlessGroup holds the permissions of accounts Core creates.
const PaperlessGroup = "Pocket Core"

// paperlessMemberModels: a member may view, add, change and delete these
// (their own, or what is shared with them: Paperless checks object owners).
var paperlessMemberModels = []string{"document", "note", "tag", "correspondent", "documenttype", "storagepath",
	"customfield", "savedview", "sharelink", "sharelinkbundle", "mailaccount", "mailrule", "processedmail", "uisettings", "paperlesstask"}

const (
	plAdded   = "DocumentAdded"
	plUpdated = "DocumentUpdated"
)

var paperlessTriggers = []PushTrigger{
	{Trigger: plAdded, Name: "Pocket Core · document added"},
	{Trigger: plUpdated, Name: "Pocket Core · document updated"},
}

func (paperless) Type() string { return "paperless" }

func (paperless) Capabilities() Capabilities {
	return Capabilities{Provision: true, Events: true, Link: true}
}

func (paperless) SSORedirects(publicURL string) []string {
	p := strings.TrimRight(publicURL, "/")
	return []string{p + "/accounts/oidc/pocket-core/login/callback/"}
}

func (paperless) ConfigureSSO(ctx context.Context, t Target, sc SSOConfig) error {
	return errors.New("Paperless reads its sign-in providers from its environment at start; set PAPERLESS_APPS and PAPERLESS_SOCIALACCOUNT_PROVIDERS as in Core's connector docs")
}

type plError struct {
	Status int
	Msg    string
}

func (e *plError) Error() string {
	return fmt.Sprintf("paperless answered HTTP %d: %s", e.Status, e.Msg)
}

func plStatus(err error) int {
	var e *plError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func plToken(t Target) (string, error) {
	k := strings.TrimSpace(t.Fields["TOKEN"])
	if k == "" {
		return "", errors.New("the Paperless service in Core has no TOKEN: paste a superuser's API token (Paperless → My Profile → API Auth Token) under Services → Paperless-ngx → Edit")
	}
	return k, nil
}

func plNeedAdmin(err error) error {
	switch plStatus(err) {
	case http.StatusUnauthorized:
		return errors.New("Paperless rejected the stored TOKEN (HTTP 401): it is wrong or was regenerated")
	case http.StatusForbidden:
		return fmt.Errorf("the stored Paperless token may not do this (HTTP 403): Core needs a superuser's token (%v)", err)
	}
	return err
}

// call does one JSON request with the token (token "" = no auth).
func (paperless) call(ctx context.Context, t Target, method, path, token string, body, out any, secrets ...string) error {
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
	req.Header.Set("Accept", "application/json; version=10")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Token "+token)
	}
	secrets = append(secrets, token)
	res, err := t.HTTP.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), secrets...))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Detail != "" {
			msg = e.Detail
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if strings.Contains(msg, "<html") || strings.Contains(msg, "<!DOCTYPE") {
			msg = "an HTML page (wrong URL?)"
		}
		return &plError{Status: res.StatusCode, Msg: redact(msg, secrets...)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unexpected answer from Paperless %s: %w", path, err)
		}
	}
	return nil
}

type plUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	IsActive    bool   `json:"is_active"`
	IsSuperuser bool   `json:"is_superuser"`
}

func (u plUser) email() string { return strings.ToLower(strings.TrimSpace(u.Email)) }

func (u plUser) name() string { return strings.TrimSpace(u.FirstName + " " + u.LastName) }

// list reads every page of a list endpoint.
func list[T any](ctx context.Context, c paperless, t Target, path, token string) ([]T, error) {
	var out []T
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page < 1000; page++ {
		var p struct {
			Next    *string `json:"next"`
			Results []T     `json:"results"`
		}
		if err := c.call(ctx, t, http.MethodGet, fmt.Sprintf("%s%spage_size=100&page=%d", path, sep, page), token, nil, &p); err != nil {
			return nil, plNeedAdmin(err)
		}
		out = append(out, p.Results...)
		if p.Next == nil || len(p.Results) == 0 {
			break
		}
	}
	return out, nil
}

func (c paperless) users(ctx context.Context, t Target, token string) ([]plUser, error) {
	return list[plUser](ctx, c, t, "/api/users/", token)
}

func (c paperless) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	tok, err := plToken(t)
	if err != nil {
		return info, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.BaseURL, "/")+"/api/ui_settings/", nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/json; version=10")
	req.Header.Set("Authorization", "Token "+tok)
	res, err := t.HTTP.Do(req)
	if err != nil {
		return info, fmt.Errorf("cannot reach Paperless at %s: %s", t.BaseURL, redact(err.Error(), tok))
	}
	info.Version = res.Header.Get("X-Version")
	var ui struct {
		User struct {
			Username    string `json:"username"`
			IsSuperuser bool   `json:"is_superuser"`
		} `json:"user"`
	}
	derr := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&ui)
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return info, plNeedAdmin(&plError{Status: res.StatusCode, Msg: "token refused"})
	case res.StatusCode != http.StatusOK || derr != nil || ui.User.Username == "":
		return info, fmt.Errorf("%s/api/ui_settings/ is not a working Paperless-ngx (HTTP %d)", strings.TrimRight(t.BaseURL, "/"), res.StatusCode)
	}
	if !ui.User.IsSuperuser {
		return info, fmt.Errorf("the stored token belongs to %s, who is not a Paperless superuser: Core needs a superuser's token", ui.User.Username)
	}
	list, err := c.users(ctx, t, tok)
	if err != nil {
		return info, err
	}
	n := 0
	for _, u := range list {
		if !plSystemUser(u.Username) {
			n++
		}
	}
	info.Admin, info.Users = true, n
	info.Detail = "sign-in is set in Paperless's environment, not by Core: see the connector docs"
	return info, nil
}

// plSystemUser: accounts Paperless keeps for itself.
func plSystemUser(name string) bool { return name == "consumer" || name == "AnonymousUser" }

var plUIDBad = regexp.MustCompile(`[^a-z0-9_.@+\-]+`)

func plUsername(email string) string {
	local := strings.SplitN(strings.ToLower(email), "@", 2)[0]
	return strings.Trim(plUIDBad.ReplaceAllString(local, "-"), "-.")
}

// memberGroup finds or creates the group new accounts go in.
func (c paperless) memberGroup(ctx context.Context, t Target, tok string) (int64, error) {
	type group struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	gs, err := list[group](ctx, c, t, "/api/groups/?name__iexact="+url.QueryEscape(PaperlessGroup), tok)
	if err != nil {
		return 0, err
	}
	for _, g := range gs {
		if strings.EqualFold(g.Name, PaperlessGroup) {
			return g.ID, nil
		}
	}
	var perms []string
	for _, m := range paperlessMemberModels {
		for _, a := range []string{"view", "add", "change", "delete"} {
			perms = append(perms, a+"_"+m)
		}
	}
	var g group
	if err := c.call(ctx, t, http.MethodPost, "/api/groups/", tok, map[string]any{"name": PaperlessGroup, "permissions": perms}, &g); err != nil {
		return 0, fmt.Errorf("creating the %q group: %w", PaperlessGroup, plNeedAdmin(err))
	}
	return g.ID, nil
}

func (c paperless) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	tok, err := plToken(t)
	if err != nil {
		return Account{}, err
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		return Account{}, errors.New("person has no email")
	}
	all, err := c.users(ctx, t, tok)
	if err != nil {
		return Account{}, err
	}
	for _, u := range all {
		if u.email() == email && !plSystemUser(u.Username) {
			note := "a Paperless account with this email already exists; linked without minting a token (the person can add their own in Core)"
			if u.IsSuperuser {
				note = "a Paperless superuser with this email already exists; linked and left untouched (no token minted)"
			}
			return Account{RemoteID: u.Username, Status: StatusLinked, Note: note}, nil
		}
	}
	name := plUsername(email)
	if name == "" {
		return Account{}, fmt.Errorf("cannot make a Paperless user name from %s", email)
	}
	for _, u := range all {
		if strings.EqualFold(u.Username, name) {
			return Account{}, fmt.Errorf("a Paperless account named %q already exists with another email (%q); link the person to it by hand, or give it their email in Paperless", u.Username, u.email())
		}
	}
	gid, err := c.memberGroup(ctx, t, tok)
	if err != nil {
		return Account{}, err
	}
	first, last := strings.TrimSpace(p.Name), ""
	if i := strings.LastIndex(first, " "); i > 0 {
		first, last = first[:i], first[i+1:]
	}
	password := RandomPassword(40) // used once to mint the token, never stored, logged or returned
	body := map[string]any{"username": name, "email": email, "password": password, "first_name": first, "last_name": last,
		"groups": []int64{gid}, "is_active": true, "is_staff": false, "is_superuser": false}
	if err := c.call(ctx, t, http.MethodPost, "/api/users/", tok, body, nil, password); err != nil {
		return Account{}, plNeedAdmin(err)
	}
	acc := Account{RemoteID: name, Status: StatusOK, Created: true}
	if p.KeepOwnCredential {
		acc.Note = "account created; the person already has their own token in Core, so none was minted"
		return acc, nil
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.call(ctx, t, http.MethodPost, "/api/token/", "", map[string]string{"username": name, "password": password}, &out, password); err != nil || out.Token == "" {
		if err == nil {
			err = errors.New("no token in the answer")
		}
		return acc, fmt.Errorf("the Paperless account %s was created but minting its token failed: %w; a retry will link the account without one", name, err)
	}
	acc.Fields = map[string]string{"TOKEN": out.Token}
	return acc, nil
}

// ---- linking by hand ----

func (c paperless) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	tok, err := plToken(t)
	if err != nil {
		return nil, err
	}
	all, err := c.users(ctx, t, tok)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(all))
	for _, u := range all {
		if plSystemUser(u.Username) {
			continue
		}
		label := u.Username
		if n := u.name(); n != "" {
			label = n + " (" + u.Username + ")"
		}
		if e := u.email(); e != "" {
			label += " · " + e
		}
		out = append(out, RemoteAccount{ID: u.Username, Name: label, Admin: u.IsSuperuser, Disabled: !u.IsActive})
	}
	return out, nil
}

func (c paperless) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	tok, err := plToken(t)
	if err != nil {
		return Account{}, err
	}
	found, err := list[plUser](ctx, c, t, "/api/users/?username__iexact="+url.QueryEscape(remoteID), tok)
	if err != nil {
		return Account{}, err
	}
	for _, u := range found {
		if strings.EqualFold(u.Username, remoteID) && !plSystemUser(u.Username) {
			return Account{RemoteID: u.Username, Status: StatusLinked, Note: "linked by hand; no token minted (the person can add their own in Core)"}, nil
		}
	}
	return Account{}, fmt.Errorf("Paperless has no account %q", remoteID)
}

// ---- events: workflows with a webhook action ----

func (paperless) PushTriggers() []PushTrigger {
	return append([]PushTrigger(nil), paperlessTriggers...)
}

// PushPerUser: workflows run for every account; each delivery names the owner.
func (paperless) PushPerUser() bool { return false }

func (c paperless) WhoAmI(ctx context.Context, t Target) (RemoteUser, error) {
	tok, err := plToken(t)
	if err != nil {
		return RemoteUser{}, err
	}
	var ui struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/api/ui_settings/", tok, nil, &ui); err != nil {
		return RemoteUser{}, plNeedAdmin(err)
	}
	return RemoteUser{ID: ui.User.Username}, nil
}

// plParams are rendered by Paperless (Jinja placeholders) and sent as JSON.
var plParams = map[string]string{
	"doc_id": "{{ doc_id }}", "title": "{{ doc_title }}", "owner": "{{ owner_username }}",
	"correspondent": "{{ correspondent }}", "document_type": "{{ document_type }}",
	"created": "{{ created }}", "added": "{{ added }}", "original_filename": "{{ original_filename }}",
}

func (c paperless) Subscribe(ctx context.Context, t Target, s Subscription) (string, error) {
	tok, err := plToken(t)
	if err != nil {
		return "", err
	}
	if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("Core's hook URL %q is not usable: set BASE_URL to the address Paperless reaches Core at", s.URL)
	}
	typ := 2 // Document Added
	if s.Trigger == plUpdated {
		typ = 3
	}
	params := map[string]string{"event": s.Trigger}
	for k, v := range plParams {
		params[k] = v
	}
	trigger := map[string]any{"type": typ}
	if typ == 2 {
		trigger["sources"] = []int{1, 2, 3, 4} // consume folder, API, mail, web upload
	}
	body := map[string]any{
		"name": s.Name, "order": 1000, "enabled": true,
		"triggers": []any{trigger},
		"actions": []any{map[string]any{"type": 4, "webhook": map[string]any{
			"url": s.URL, "use_params": true, "as_json": true, "params": params,
			"headers": map[string]string{s.HeaderName: s.HeaderValue}, "include_document": false,
		}}},
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.call(ctx, t, http.MethodPost, "/api/workflows/", tok, body, &out, s.HeaderValue); err != nil {
		return "", plNeedAdmin(err)
	}
	if out.ID == 0 {
		return "", errors.New("Paperless created the workflow but returned no id")
	}
	return fmt.Sprint(out.ID), nil
}

func (c paperless) Subscriptions(ctx context.Context, t Target) (map[string]string, error) {
	tok, err := plToken(t)
	if err != nil {
		return nil, err
	}
	type wf struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	all, err := list[wf](ctx, c, t, "/api/workflows/", tok)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all))
	for _, w := range all {
		out[fmt.Sprint(w.ID)] = w.Name
	}
	return out, nil
}

func (c paperless) Unsubscribe(ctx context.Context, t Target, remoteID string) error {
	tok, err := plToken(t)
	if err != nil {
		return err
	}
	if remoteID == "" {
		return nil
	}
	err = c.call(ctx, t, http.MethodDelete, "/api/workflows/"+url.PathEscape(remoteID)+"/", tok, nil, nil)
	if err == nil || plStatus(err) == http.StatusNotFound {
		return nil
	}
	return plNeedAdmin(err)
}

type plDelivery struct {
	Event            string `json:"event"`
	DocID            string `json:"doc_id"`
	Title            string `json:"title"`
	Owner            string `json:"owner"`
	Correspondent    string `json:"correspondent"`
	DocumentType     string `json:"document_type"`
	Created          string `json:"created"`
	Added            string `json:"added"`
	OriginalFilename string `json:"original_filename"`
}

func (paperless) Decode(t Target, trigger string, body []byte) (Delivery, error) {
	var in plDelivery
	if err := json.Unmarshal(body, &in); err != nil {
		return Delivery{}, fmt.Errorf("not a Paperless workflow delivery: %w", err)
	}
	if in.DocID == "" || in.Event == "" {
		return Delivery{}, errors.New("not a Paperless workflow delivery: no doc_id or event")
	}
	sum := sha256.Sum256(body)
	d := Delivery{OwnerID: in.Owner, DedupeKey: trigger + "|" + in.DocID + "|" + hex.EncodeToString(sum[:8])}
	if in.Event != trigger {
		d.Drop = "delivery for " + in.Event + " on the " + trigger + " hook"
		return d, nil
	}
	if strings.TrimSpace(in.Owner) == "" {
		d.Drop = "document has no owner"
		return d, nil
	}
	name := "document_added"
	if trigger == plAdded {
		if at, err := time.Parse(time.RFC3339Nano, in.Added); err == nil {
			d.At = at // a document added before Core connected the person is not news
		}
	} else if trigger == plUpdated {
		name = "document_updated"
	} else {
		d.Drop = "unknown trigger " + trigger
		return d, nil
	}
	p := map[string]any{"paperless_user": in.Owner, "document_id": in.DocID}
	for k, v := range map[string]string{"title": in.Title, "correspondent": in.Correspondent, "document_type": in.DocumentType,
		"created": in.Created, "added": in.Added, "original_filename": in.OriginalFilename} {
		v := v
		setStr(p, k, &v)
	}
	if pub := strings.TrimRight(t.PublicURL, "/"); pub != "" {
		p["url"] = pub + "/documents/" + url.PathEscape(in.DocID) + "/details"
	}
	d.Event = Event{Name: name, Subject: "document:" + in.DocID, Payload: p}
	return d, nil
}

// ---- poll: accounts ----

func (c paperless) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	tok, err := plToken(t)
	if err != nil {
		return nil, state, err
	}
	all, err := c.users(ctx, t, tok)
	if err != nil {
		return nil, state, err
	}
	cur := ncState{Users: map[string]ncUserState{}}
	for _, u := range all {
		if !plSystemUser(u.Username) {
			cur.Users[u.Username] = ncUserState{Email: u.email(), Name: u.name()}
		}
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
			out = append(out, Event{Name: "user_created", Subject: "user:" + id, UserEmail: u.Email, RemoteUserID: id, Payload: map[string]any{"paperless_user": id, "name": u.Name}})
		}
	}
	var gone []string
	for id := range prev.Users {
		if _, still := cur.Users[id]; !still {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		out = append(out, Event{Name: "user_deleted", Subject: "user:" + id, Payload: map[string]any{"paperless_user": id, "name": prev.Users[id].Name}})
	}
	return out, next, nil
}
