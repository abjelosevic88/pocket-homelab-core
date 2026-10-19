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
	"sort"
	"strconv"
	"strings"
)

// Jellyseerr / Seerr (tested against seerr 3.5). Core acts with the
// service's API_KEY (Settings → General), which acts as the admin.
//
//	GET  /api/v1/status                    Test (version)                         read
//	GET  /api/v1/auth/me                   Test (whose key)                       read
//	GET  /api/v1/user                      Test, Provision, linking, Poll         read
//	GET  /api/v1/user/{id}                 linking by hand                        read
//	POST /api/v1/user/import-from-jellyfin Provision: a person with no Jellyseerr user yet  WRITE
//	GET  /api/v1/request, /api/v1/issue    Poll                                   read
//	GET  /api/v1/movie/{tmdb}, /tv/{tmdb}  Poll: titles of what changed           read
//
// Accounts: Jellyseerr users come from Jellyfin, and an imported user's
// "email" is their Jellyfin name. So a person is linked by email, else by
// the Jellyfin account Core's Jellyfin connector linked them to (the
// user's jellyfinUserId), else that Jellyfin user is imported. Jellyseerr
// has no per-person keys: nothing is minted.
//
// Sign-in: none; people sign in with their Jellyfin account.
//
// Events: polled. Jellyseerr has one webhook slot (often used by a Jellyfin
// plugin), so Core leaves it alone and reads requests and issues instead.
type jellyseerr struct{}

func init() { Register(jellyseerr{}) }

func (jellyseerr) Type() string { return "jellyseerr" }

func (jellyseerr) Capabilities() Capabilities {
	return Capabilities{Provision: true, Events: true, Link: true}
}

func (jellyseerr) SSORedirects(string) []string { return nil }

func (jellyseerr) ConfigureSSO(context.Context, Target, SSOConfig) error {
	return errors.New("Jellyseerr has no OpenID sign-in: people sign in with their Jellyfin account")
}

const jsAdminPermission = 2 // Permission.ADMIN

type jsError struct {
	Status int
	Msg    string
}

func (e *jsError) Error() string {
	return fmt.Sprintf("jellyseerr answered HTTP %d: %s", e.Status, e.Msg)
}

func jsKey(t Target) (string, error) {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return "", errors.New("the Jellyseerr service in Core has no API_KEY: copy it from Jellyseerr → Settings → General and paste it under Services → Jellyseerr → Edit")
	}
	return k, nil
}

func jsNeedKey(err error) error {
	var e *jsError
	if errors.As(err, &e) && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden) {
		return fmt.Errorf("Jellyseerr refused the stored API key (HTTP %d): copy it again from Settings → General", e.Status)
	}
	return err
}

func (jellyseerr) call(ctx context.Context, t Target, method, path, key string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	base := strings.TrimRight(t.BaseURL, "/")
	base = strings.TrimSuffix(base, "/api/v1")
	req, err := http.NewRequestWithContext(ctx, method, base+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Api-Key", key)
	res, err := t.HTTP.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), key))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return &jsError{Status: res.StatusCode, Msg: redact(msg, key)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unexpected answer from Jellyseerr %s: %w", path, err)
		}
	}
	return nil
}

type jsUser struct {
	ID               int64  `json:"id"`
	Email            string `json:"email"`
	DisplayName      string `json:"displayName"`
	JellyfinUsername string `json:"jellyfinUsername"`
	JellyfinUserID   string `json:"jellyfinUserId"`
	Permissions      int64  `json:"permissions"`
}

func (u jsUser) admin() bool { return u.Permissions&jsAdminPermission != 0 }

func (u jsUser) remoteID() string { return strconv.FormatInt(u.ID, 10) }

// jfID normalizes a Jellyfin GUID (with or without dashes).
func jfID(s string) string { return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", "")) }

func (c jellyseerr) users(ctx context.Context, t Target, key string) ([]jsUser, error) {
	var out []jsUser
	for skip := 0; skip < 100000; skip += 100 {
		var p struct {
			PageInfo struct {
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []jsUser `json:"results"`
		}
		if err := c.call(ctx, t, http.MethodGet, fmt.Sprintf("/user?take=100&skip=%d&sort=created", skip), key, nil, &p); err != nil {
			return nil, jsNeedKey(err)
		}
		out = append(out, p.Results...)
		if len(p.Results) < 100 || len(out) >= p.PageInfo.Results {
			break
		}
	}
	return out, nil
}

func (c jellyseerr) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	key, err := jsKey(t)
	if err != nil {
		return info, err
	}
	var st struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/status", key, nil, &st); err != nil {
		return info, fmt.Errorf("cannot reach Jellyseerr at %s: %w", t.BaseURL, err)
	}
	info.Version = st.Version
	var me jsUser
	if err := c.call(ctx, t, http.MethodGet, "/auth/me", key, nil, &me); err != nil {
		return info, jsNeedKey(err)
	}
	list, err := c.users(ctx, t, key)
	if err != nil {
		return info, err
	}
	info.Admin, info.Users = me.admin(), len(list)
	if !info.Admin {
		return info, errors.New("the stored API key does not act as a Jellyseerr admin")
	}
	info.Detail = "people sign in with their Jellyfin account; a person without a Jellyseerr user is imported from the Jellyfin account Core's Jellyfin connector linked them to"
	return info, nil
}

func (c jellyseerr) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	key, err := jsKey(t)
	if err != nil {
		return Account{}, err
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	list, err := c.users(ctx, t, key)
	if err != nil {
		return Account{}, err
	}
	const none = "; Jellyseerr has no per-person keys, nothing to mint"
	for _, u := range list {
		if email != "" && strings.EqualFold(strings.TrimSpace(u.Email), email) {
			return Account{RemoteID: u.remoteID(), Status: StatusLinked, Note: "a Jellyseerr user with this email exists; linked" + none}, nil
		}
	}
	jf := jfID(p.Linked["jellyfin"])
	if jf == "" {
		return Account{}, errors.New("no Jellyseerr user with this email, and Core has not linked the person to a Jellyfin account: link them in the Jellyfin connector first (Jellyseerr users come from Jellyfin), or link them here by hand")
	}
	for _, u := range list {
		if jfID(u.JellyfinUserID) == jf {
			return Account{RemoteID: u.remoteID(), Status: StatusLinked, Note: "linked through their Jellyfin account (" + u.JellyfinUsername + ")" + none}, nil
		}
	}
	var created []jsUser
	if err := c.call(ctx, t, http.MethodPost, "/user/import-from-jellyfin", key, map[string]any{"jellyfinUserIds": []string{jf}}, &created); err != nil {
		return Account{}, fmt.Errorf("importing their Jellyfin account into Jellyseerr failed: %w", jsNeedKey(err))
	}
	for _, u := range created {
		if jfID(u.JellyfinUserID) == jf {
			return Account{RemoteID: u.remoteID(), Status: StatusLinked, Created: true, Note: "imported from their Jellyfin account (" + u.JellyfinUsername + ") with Jellyseerr's default permissions" + none}, nil
		}
	}
	return Account{}, errors.New("Jellyseerr imported nothing: is their Jellyfin account still there?")
}

// ---- linking by hand ----

func (c jellyseerr) RemoteAccounts(ctx context.Context, t Target) ([]RemoteAccount, error) {
	key, err := jsKey(t)
	if err != nil {
		return nil, err
	}
	list, err := c.users(ctx, t, key)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteAccount, 0, len(list))
	for _, u := range list {
		name := u.DisplayName
		if name == "" {
			name = u.JellyfinUsername
		}
		if e := strings.TrimSpace(u.Email); e != "" && e != name {
			name += " · " + e
		}
		out = append(out, RemoteAccount{ID: u.remoteID(), Name: name, Admin: u.admin()})
	}
	return out, nil
}

func (c jellyseerr) Link(ctx context.Context, t Target, remoteID string) (Account, error) {
	key, err := jsKey(t)
	if err != nil {
		return Account{}, err
	}
	if _, err := strconv.ParseInt(remoteID, 10, 64); err != nil {
		return Account{}, fmt.Errorf("a Jellyseerr user id is a number, not %q", remoteID)
	}
	var u jsUser
	if err := c.call(ctx, t, http.MethodGet, "/user/"+url.PathEscape(remoteID), key, nil, &u); err != nil {
		var e *jsError
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return Account{}, fmt.Errorf("Jellyseerr has no user %s", remoteID)
		}
		return Account{}, jsNeedKey(err)
	}
	return Account{RemoteID: u.remoteID(), Status: StatusLinked, Note: "linked by hand"}, nil
}

// ---- poll: requests, issues, users ----

type jsRequest struct {
	ID        int64  `json:"id"`
	Status    int    `json:"status"`
	Type      string `json:"type"`
	Is4K      bool   `json:"is4k"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Media     struct {
		TmdbID   int64 `json:"tmdbId"`
		Status   int   `json:"status"`
		Status4K int   `json:"status4k"`
	} `json:"media"`
	RequestedBy jsUser  `json:"requestedBy"`
	ModifiedBy  *jsUser `json:"modifiedBy"`
	Seasons     []any   `json:"seasons"`
	IsAuto      bool    `json:"isAutoRequest"`
}

func (r jsRequest) mediaStatus() int {
	if r.Is4K {
		return r.Media.Status4K
	}
	return r.Media.Status
}

type jsIssue struct {
	ID        int64  `json:"id"`
	IssueType int    `json:"issueType"`
	Status    int    `json:"status"`
	CreatedAt string `json:"createdAt"`
	Media     struct {
		TmdbID    int64  `json:"tmdbId"`
		MediaType string `json:"mediaType"`
	} `json:"media"`
	CreatedBy jsUser `json:"createdBy"`
}

// jsState: what the last poll saw. Requests and issues by id, as
// "status/mediaStatus"; Max* are the highest ids seen, so a request that
// falls off the page and comes back is not taken for a new one.
type jsState struct {
	Req      map[string]string      `json:"req"`
	MaxReq   int64                  `json:"max_req"`
	Issue    map[string]int         `json:"issue"`
	MaxIssue int64                  `json:"max_issue"`
	Users    map[string]ncUserState `json:"users"`
}

var jsRequestStatus = map[int]string{1: "pending", 2: "approved", 3: "declined", 4: "failed", 5: "completed"}
var jsMediaStatus = map[int]string{1: "unknown", 2: "pending", 3: "processing", 4: "partially_available", 5: "available", 6: "blocklisted", 7: "deleted"}
var jsIssueType = map[int]string{1: "video", 2: "audio", 3: "subtitles", 4: "other"}

func (c jellyseerr) title(ctx context.Context, t Target, key, mediaType string, tmdb int64, cache map[string]string) (string, string) {
	k := fmt.Sprintf("%s/%d", mediaType, tmdb)
	if v, ok := cache[k]; ok {
		i := strings.LastIndex(v, "|")
		return v[:i], v[i+1:]
	}
	var m struct {
		Title        string `json:"title"`
		Name         string `json:"name"`
		ReleaseDate  string `json:"releaseDate"`
		FirstAirDate string `json:"firstAirDate"`
	}
	path := "/movie/"
	if mediaType == "tv" {
		path = "/tv/"
	}
	_ = c.call(ctx, t, http.MethodGet, path+strconv.FormatInt(tmdb, 10), key, nil, &m)
	title, date := m.Title, m.ReleaseDate
	if mediaType == "tv" {
		title, date = m.Name, m.FirstAirDate
	}
	year := ""
	if len(date) >= 4 {
		year = date[:4]
	}
	cache[k] = title + "|" + year
	return title, year
}

func (c jellyseerr) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	key, err := jsKey(t)
	if err != nil {
		return nil, state, err
	}
	var prev jsState
	first := len(bytes.TrimSpace(state)) == 0 || json.Unmarshal(state, &prev) != nil || prev.Req == nil
	var reqs struct {
		Results []jsRequest `json:"results"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/request?take=100&skip=0&sort=modified&filter=all", key, nil, &reqs); err != nil {
		return nil, state, jsNeedKey(err)
	}
	var issues struct {
		Results []jsIssue `json:"results"`
	}
	if err := c.call(ctx, t, http.MethodGet, "/issue?take=100&skip=0&sort=modified&filter=all", key, nil, &issues); err != nil {
		return nil, state, jsNeedKey(err)
	}
	users, err := c.users(ctx, t, key)
	if err != nil {
		return nil, state, err
	}
	cur := jsState{Req: map[string]string{}, Issue: map[string]int{}, Users: map[string]ncUserState{}, MaxReq: prev.MaxReq, MaxIssue: prev.MaxIssue}
	for k, v := range prev.Req { // requests off this page keep their last state
		cur.Req[k] = v
	}
	for k, v := range prev.Issue {
		cur.Issue[k] = v
	}
	for _, u := range users {
		cur.Users[u.remoteID()] = ncUserState{Email: strings.ToLower(u.Email), Name: u.DisplayName}
	}
	pub := strings.TrimRight(t.PublicURL, "/")
	titles := map[string]string{}
	var out []Event
	// oldest change first
	sort.Slice(reqs.Results, func(i, j int) bool { return reqs.Results[i].UpdatedAt < reqs.Results[j].UpdatedAt })
	for _, r := range reqs.Results {
		id := strconv.FormatInt(r.ID, 10)
		now := fmt.Sprintf("%d/%d", r.Status, r.mediaStatus())
		before, known := prev.Req[id]
		cur.Req[id] = now
		if r.ID > cur.MaxReq {
			cur.MaxReq = r.ID
		}
		if first || before == now {
			continue
		}
		var names []string
		switch {
		case !known && r.ID > prev.MaxReq:
			names = append(names, "request_created")
		case !known:
			continue // an old request seen for the first time: no "before" to compare with
		default:
			var bs, bm int
			_, _ = fmt.Sscanf(before, "%d/%d", &bs, &bm)
			if bs != r.Status {
				switch r.Status {
				case 2:
					names = append(names, "request_approved")
				case 3:
					names = append(names, "request_declined")
				case 4:
					names = append(names, "request_failed")
				}
			}
			if bm != r.mediaStatus() {
				switch r.mediaStatus() {
				case 5:
					names = append(names, "media_available")
				case 4:
					names = append(names, "media_partially_available")
				}
			}
		}
		if len(names) == 0 {
			continue
		}
		title, year := c.title(ctx, t, key, r.Type, r.Media.TmdbID, titles)
		p := map[string]any{"request_id": r.ID, "media_type": r.Type, "tmdb_id": r.Media.TmdbID, "is_4k": r.Is4K,
			"status": jsRequestStatus[r.Status], "media_status": jsMediaStatus[r.mediaStatus()], "requested_by": r.RequestedBy.DisplayName,
			"auto_request": r.IsAuto, "requested_at": r.CreatedAt, "at": r.UpdatedAt}
		setStr(p, "title", &title)
		setStr(p, "year", &year)
		if r.Type == "tv" && len(r.Seasons) > 0 {
			p["seasons"] = len(r.Seasons)
		}
		if r.ModifiedBy != nil && r.ModifiedBy.ID != r.RequestedBy.ID {
			p["modified_by"] = r.ModifiedBy.DisplayName
		}
		if pub != "" {
			p["url"] = fmt.Sprintf("%s/%s/%d", pub, r.Type, r.Media.TmdbID)
		}
		for _, n := range names {
			out = append(out, Event{Name: n, Subject: "request:" + id, RemoteUserID: r.RequestedBy.remoteID(), Payload: p})
		}
	}
	for _, is := range issues.Results {
		id := strconv.FormatInt(is.ID, 10)
		before, known := prev.Issue[id]
		cur.Issue[id] = is.Status
		if is.ID > cur.MaxIssue {
			cur.MaxIssue = is.ID
		}
		var name string
		switch {
		case first:
		case !known && is.ID > prev.MaxIssue:
			name = "issue_created"
		case known && before != is.Status && is.Status == 2:
			name = "issue_resolved"
		case known && before != is.Status && is.Status == 1:
			name = "issue_reopened"
		}
		if name == "" {
			continue
		}
		title, year := c.title(ctx, t, key, is.Media.MediaType, is.Media.TmdbID, titles)
		p := map[string]any{"issue_id": is.ID, "issue_type": jsIssueType[is.IssueType], "media_type": is.Media.MediaType, "tmdb_id": is.Media.TmdbID,
			"reported_by": is.CreatedBy.DisplayName, "reported_at": is.CreatedAt}
		setStr(p, "title", &title)
		setStr(p, "year", &year)
		if pub != "" {
			p["url"] = fmt.Sprintf("%s/issues/%d", pub, is.ID)
		}
		out = append(out, Event{Name: name, Subject: "issue:" + id, RemoteUserID: is.CreatedBy.remoteID(), Payload: p})
	}
	if !first && prev.Users != nil {
		var ids []string
		for id := range cur.Users {
			if _, known := prev.Users[id]; !known {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			u := cur.Users[id]
			out = append(out, Event{Name: "user_created", Subject: "user:" + id, RemoteUserID: id, Payload: map[string]any{"jellyseerr_user_id": id, "name": u.Name}})
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}
