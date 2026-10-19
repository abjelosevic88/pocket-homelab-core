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
	"strings"
)

// Radarr (5+), Sonarr (4+) and Lidarr (2+) connector: events only. These
// are household tools with no per-person accounts and no OpenID sign-in, so
// Core does one thing: keep a Webhook connection named "Pocket Core" in the
// app (box-wide, with Core's secret header) while Events are on, and turn
// each call into an event about the whole household (no user_email).
//
// Endpoints used (v3 for Radarr and Sonarr, v1 for Lidarr):
//
//	GET    /api/vN/system/status                 Test                read
//	GET    /api/vN/notification                  Test, subscriptions read
//	GET    /api/vN/notification/schema           subscribe           read
//	POST   /api/vN/notification?forceSave=true   subscribe           WRITE
//	DELETE /api/vN/notification/:id              unsubscribe         WRITE
//
// Saving an enabled connection makes the app call it once with a Test
// event first (forceSave only turns warnings off), so Core records the hook
// before subscribing and drops that call.
type arr struct {
	typ  string // radarr | sonarr | lidarr
	name string // Radarr
	api  string // /api/v3
	// on: the Webhook triggers Core switches on (only those the app has
	// are set). Renames, retags and "deleted for upgrade" are left off:
	// they repeat what an import already said.
	on []string
}

func init() {
	common := []string{"onGrab", "onUpgrade", "onHealthIssue", "onHealthRestored", "onApplicationUpdate"}
	Register(arr{typ: "radarr", name: "Radarr", api: "/api/v3", on: append([]string{"onDownload", "onMovieAdded", "onMovieDelete", "onMovieFileDelete", "onManualInteractionRequired"}, common...)})
	Register(arr{typ: "sonarr", name: "Sonarr", api: "/api/v3", on: append([]string{"onDownload", "onSeriesAdd", "onSeriesDelete", "onEpisodeFileDelete", "onManualInteractionRequired"}, common...)})
	Register(arr{typ: "lidarr", name: "Lidarr", api: "/api/v1", on: append([]string{"onReleaseImport", "onArtistAdd", "onArtistDelete", "onAlbumDelete", "onDownloadFailure", "onImportFailure"}, common...)})
}

// ArrHookName is the connection Core keeps in each app.
const ArrHookName = "Pocket Core"

func (a arr) Type() string { return a.typ }

func (a arr) Capabilities() Capabilities { return Capabilities{Events: true} }

func (a arr) SSORedirects(string) []string { return nil }

func (a arr) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, fmt.Errorf("%s has no per-person accounts", a.name)
}

func (a arr) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return fmt.Errorf("%s has no OpenID sign-in", a.name)
	}
	return nil
}

type arrError struct {
	App    string
	Status int
	Msg    string
}

func (e *arrError) Error() string {
	return fmt.Sprintf("%s answered HTTP %d: %s", e.App, e.Status, e.Msg)
}

func (a arr) call(ctx context.Context, t Target, method, path string, body, out any, secrets ...string) error {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return fmt.Errorf("the %s service in Core has no API_KEY: copy it from %s (Settings → General → API Key) under Services → %s → Edit", a.name, a.name, a.name)
	}
	secrets = append(secrets, k)
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.BaseURL, "/")+a.api+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", k)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := t.HTTP.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), secrets...))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s rejected the stored API key (HTTP 401): copy it again from Settings → General", a.name)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		var verr []struct {
			PropertyName string `json:"propertyName"`
			ErrorMessage string `json:"errorMessage"`
		}
		if json.Unmarshal(raw, &verr) == nil && len(verr) > 0 {
			parts := []string{}
			for _, v := range verr {
				parts = append(parts, strings.TrimSpace(v.PropertyName+": "+v.ErrorMessage))
			}
			msg = strings.Join(parts, "; ")
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &arrError{App: a.name, Status: res.StatusCode, Msg: redact(msg, secrets...)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s %s: unexpected answer: %w", a.name, method, path, err)
	}
	return nil
}

type arrNotification struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
}

func (a arr) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	var st struct {
		AppName      string `json:"appName"`
		InstanceName string `json:"instanceName"`
		Version      string `json:"version"`
	}
	if err := a.call(ctx, t, http.MethodGet, "/system/status", nil, &st); err != nil {
		return info, fmt.Errorf("cannot reach %s at %s: %w", a.name, t.BaseURL, err)
	}
	if st.AppName != "" && !strings.EqualFold(st.AppName, a.name) {
		return info, fmt.Errorf("%s answers as %s, not %s: check the service URL", t.BaseURL, st.AppName, a.name)
	}
	info.Version, info.Admin = st.Version, true
	var list []arrNotification
	if err := a.call(ctx, t, http.MethodGet, "/notification", nil, &list); err != nil {
		return info, err
	}
	n := 0
	for _, x := range list {
		if x.Implementation == "Webhook" && x.Name == ArrHookName {
			n++
		}
	}
	if n > 0 {
		info.Detail = fmt.Sprintf("the %q connection is in Settings → Connect", ArrHookName)
	} else {
		info.Detail = fmt.Sprintf("no per-person accounts or sign-in here; switching Events on adds a Webhook connection %q under Settings → Connect (grabs, imports, upgrades, additions, deletions, health, updates)", ArrHookName)
	}
	return info, nil
}

// ---- push ----

func (a arr) PushTriggers() []PushTrigger {
	return []PushTrigger{{Trigger: "webhook", Name: ArrHookName}}
}

func (a arr) PushPerUser() bool { return false }

func (a arr) WhoAmI(context.Context, Target) (RemoteUser, error) { return RemoteUser{ID: a.typ}, nil }

func (a arr) Subscribe(ctx context.Context, t Target, s Subscription) (string, error) {
	var schema []map[string]any
	if err := a.call(ctx, t, http.MethodGet, "/notification/schema", nil, &schema); err != nil {
		return "", err
	}
	var hook map[string]any
	for _, x := range schema {
		if x["implementation"] == "Webhook" {
			hook = x
			break
		}
	}
	if hook == nil {
		return "", fmt.Errorf("%s offers no Webhook connection", a.name)
	}
	hook["name"] = s.Name
	for _, k := range a.on {
		if _, ok := hook[k]; ok {
			hook[k] = true
		}
	}
	if _, ok := hook["includeHealthWarnings"]; ok {
		hook["includeHealthWarnings"] = false // warnings flap with every WAN blip
	}
	fields, _ := hook["fields"].([]any)
	for _, f := range fields {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		switch m["name"] {
		case "url":
			m["value"] = s.URL
		case "method":
			m["value"] = 1 // POST
		case "headers":
			m["value"] = []map[string]string{{"key": s.HeaderName, "value": s.HeaderValue}}
		}
	}
	var made arrNotification
	if err := a.call(ctx, t, http.MethodPost, "/notification?forceSave=true", hook, &made, s.HeaderValue); err != nil {
		return "", fmt.Errorf("adding the %q connection to %s failed: %w", s.Name, a.name, err)
	}
	if made.ID == 0 {
		return "", fmt.Errorf("%s returned no id for the new connection", a.name)
	}
	return fmt.Sprint(made.ID), nil
}

func (a arr) Subscriptions(ctx context.Context, t Target) (map[string]string, error) {
	var list []arrNotification
	if err := a.call(ctx, t, http.MethodGet, "/notification", nil, &list); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, x := range list {
		if x.Implementation == "Webhook" {
			out[fmt.Sprint(x.ID)] = x.Name
		}
	}
	return out, nil
}

func (a arr) Unsubscribe(ctx context.Context, t Target, remoteID string) error {
	err := a.call(ctx, t, http.MethodDelete, "/notification/"+url.PathEscape(remoteID), nil, nil)
	var ae *arrError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// arrEvents maps the apps' eventType to event names.
var arrEvents = map[string]string{
	"Grab": "grabbed", "Download": "imported", "Rename": "renamed",
	"MovieAdded": "added", "SeriesAdd": "added", "ArtistAdd": "added",
	"MovieDelete": "removed", "SeriesDelete": "removed", "ArtistDelete": "removed", "AlbumDelete": "album_removed",
	"MovieFileDelete": "file_deleted", "EpisodeFileDelete": "file_deleted",
	"Health": "health_issue", "HealthRestored": "health_restored", "ApplicationUpdate": "updated",
	"ManualInteractionRequired": "manual_interaction_required", "DownloadFailure": "download_failed", "ImportFailure": "import_failed",
	"Retag": "retagged",
}

// arrBody is the union of the payloads Core reads (camelCase JSON).
type arrBody struct {
	EventType    string `json:"eventType"`
	InstanceName string `json:"instanceName"`
	AppURL       string `json:"applicationUrl"`
	Movie        *struct {
		ID     int64  `json:"id"`
		Title  string `json:"title"`
		Year   int    `json:"year"`
		TmdbID int64  `json:"tmdbId"`
		ImdbID string `json:"imdbId"`
	} `json:"movie"`
	Series *struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		TitleSlug string `json:"titleSlug"`
		Year      int    `json:"year"`
		TvdbID    int64  `json:"tvdbId"`
		ImdbID    string `json:"imdbId"`
	} `json:"series"`
	Episodes []struct {
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
		Title         string `json:"title"`
		AirDate       string `json:"airDate"`
	} `json:"episodes"`
	Artist *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		MBID string `json:"mbId"`
	} `json:"artist"`
	Album *struct {
		Title       string `json:"title"`
		MBID        string `json:"mbId"`
		ReleaseDate string `json:"releaseDate"`
	} `json:"album"`
	Albums []struct {
		Title       string `json:"title"`
		ReleaseDate string `json:"releaseDate"`
	} `json:"albums"`
	Tracks  []json.RawMessage `json:"tracks"`
	Release *struct {
		ReleaseTitle string `json:"releaseTitle"`
		Indexer      string `json:"indexer"`
		Quality      string `json:"quality"`
		ReleaseGroup string `json:"releaseGroup"`
		Size         int64  `json:"size"`
	} `json:"release"`
	MovieFile   *arrFile `json:"movieFile"`
	EpisodeFile *arrFile `json:"episodeFile"`
	IsUpgrade   bool     `json:"isUpgrade"`
	DownloadCl  string   `json:"downloadClient"`
	DownloadID  string   `json:"downloadId"`
	// health, update, failures
	Level           any    `json:"level"`
	Message         string `json:"message"`
	Type            string `json:"type"`
	WikiURL         string `json:"wikiUrl"`
	PreviousVersion string `json:"previousVersion"`
	NewVersion      string `json:"newVersion"`
	Quality         string `json:"quality"`
	ReleaseTitle    string `json:"releaseTitle"`
	DeleteReason    any    `json:"deleteReason"`
	DownloadInfo    *struct {
		Title   string `json:"title"`
		Quality string `json:"quality"`
	} `json:"downloadInfo"`
	DownloadStatusMessages []struct {
		Title    string   `json:"title"`
		Messages []string `json:"messages"`
	} `json:"downloadStatusMessages"`
}

type arrFile struct {
	RelativePath string `json:"relativePath"`
	Quality      string `json:"quality"`
	ReleaseGroup string `json:"releaseGroup"`
	Size         int64  `json:"size"`
}

func (a arr) webURL(t Target, b arrBody, path string) string {
	base := strings.TrimRight(t.PublicURL, "/")
	if base == "" {
		base = strings.TrimRight(b.AppURL, "/")
	}
	if base == "" || path == "" {
		return ""
	}
	return base + path
}

func (a arr) Decode(t Target, trigger string, body []byte) (Delivery, error) {
	var b arrBody
	if err := json.Unmarshal(body, &b); err != nil {
		return Delivery{}, fmt.Errorf("not a %s webhook: %w", a.name, err)
	}
	if b.EventType == "" {
		return Delivery{}, fmt.Errorf("not a %s webhook: no eventType", a.name)
	}
	if b.EventType == "Test" {
		return Delivery{Drop: "test delivery from " + a.name}, nil
	}
	name := arrEvents[b.EventType]
	if name == "" {
		name = snake(b.EventType)
	}
	if name == "imported" && b.IsUpgrade {
		name = "upgraded"
	}
	p := map[string]any{"event": b.EventType}
	if b.InstanceName != "" {
		p["instance"] = b.InstanceName
	}
	subject := ""
	title := ""
	link := ""
	switch {
	case b.Movie != nil:
		m := b.Movie
		p["movie_id"], p["movie"] = m.ID, m.Title
		title = m.Title
		if m.Year > 0 {
			p["year"] = m.Year
			title = fmt.Sprintf("%s (%d)", m.Title, m.Year)
		}
		if m.TmdbID > 0 {
			p["tmdb_id"] = m.TmdbID
			link = fmt.Sprintf("/movie/%d", m.TmdbID)
		}
		if m.ImdbID != "" {
			p["imdb_id"] = m.ImdbID
		}
		subject = fmt.Sprintf("movie:%d", m.ID)
	case b.Series != nil:
		s := b.Series
		p["series_id"], p["series"] = s.ID, s.Title
		title = s.Title
		if s.Year > 0 {
			p["year"] = s.Year
		}
		if s.TvdbID > 0 {
			p["tvdb_id"] = s.TvdbID
		}
		if s.ImdbID != "" {
			p["imdb_id"] = s.ImdbID
		}
		if s.TitleSlug != "" {
			link = "/series/" + s.TitleSlug
		}
		subject = fmt.Sprintf("series:%d", s.ID)
		if len(b.Episodes) > 0 {
			eps := []map[string]any{}
			codes := []string{}
			for _, e := range b.Episodes {
				eps = append(eps, map[string]any{"season": e.SeasonNumber, "episode": e.EpisodeNumber, "title": e.Title, "air_date": e.AirDate})
				codes = append(codes, fmt.Sprintf("S%02dE%02d", e.SeasonNumber, e.EpisodeNumber))
			}
			p["episodes"] = eps
			if len(b.Episodes) == 1 {
				e := b.Episodes[0]
				p["season"], p["episode"], p["episode_title"] = e.SeasonNumber, e.EpisodeNumber, e.Title
			}
			title += " " + strings.Join(codes, "-")
		}
	case b.Artist != nil:
		ar := b.Artist
		p["artist_id"], p["artist"] = ar.ID, ar.Name
		title = ar.Name
		if ar.MBID != "" {
			p["musicbrainz_id"] = ar.MBID
			link = "/artist/" + ar.MBID
		}
		subject = fmt.Sprintf("artist:%d", ar.ID)
		albums := []string{}
		if b.Album != nil {
			albums = append(albums, b.Album.Title)
			if b.Album.ReleaseDate != "" {
				p["release_date"] = b.Album.ReleaseDate
			}
		}
		for _, al := range b.Albums {
			albums = append(albums, al.Title)
		}
		if len(albums) > 0 {
			p["album"] = albums[0]
			if len(albums) > 1 {
				p["albums"] = albums
			}
			title += " – " + strings.Join(albums, ", ")
		}
		if len(b.Tracks) > 0 {
			p["tracks"] = len(b.Tracks)
		}
	}
	if r := b.Release; r != nil {
		setNonEmpty(p, "release_title", r.ReleaseTitle)
		setNonEmpty(p, "indexer", r.Indexer)
		setNonEmpty(p, "quality", r.Quality)
		setNonEmpty(p, "release_group", r.ReleaseGroup)
		if r.Size > 0 {
			p["size"] = r.Size
		}
	}
	for _, f := range []*arrFile{b.MovieFile, b.EpisodeFile} {
		if f != nil {
			setNonEmpty(p, "file", f.RelativePath)
			setNonEmpty(p, "quality", f.Quality)
			setNonEmpty(p, "release_group", f.ReleaseGroup)
			if f.Size > 0 {
				p["size"] = f.Size
			}
		}
	}
	setNonEmpty(p, "download_client", b.DownloadCl)
	setNonEmpty(p, "download_id", b.DownloadID)
	setNonEmpty(p, "quality", b.Quality)
	setNonEmpty(p, "release_title", b.ReleaseTitle)
	if b.DeleteReason != nil {
		p["reason"] = fmt.Sprint(b.DeleteReason)
	}
	if d := b.DownloadInfo; d != nil {
		setNonEmpty(p, "release_title", d.Title)
		setNonEmpty(p, "quality", d.Quality)
	}
	if len(b.DownloadStatusMessages) > 0 {
		msgs := []string{}
		for _, m := range b.DownloadStatusMessages {
			msgs = append(msgs, m.Messages...)
		}
		if len(msgs) > 0 {
			p["messages"] = msgs
		}
	}
	dedupe := ""
	switch b.EventType {
	case "Health", "HealthRestored":
		p["level"] = strings.ToLower(fmt.Sprint(b.Level))
		setNonEmpty(p, "check", b.Type)
		setNonEmpty(p, "wiki_url", b.WikiURL)
		subject = "health:" + b.Type
		title = b.Message
	case "ApplicationUpdate":
		setNonEmpty(p, "previous_version", b.PreviousVersion)
		setNonEmpty(p, "new_version", b.NewVersion)
		subject = "update:" + b.NewVersion
		title = a.name + " " + b.NewVersion
	default:
		// the apps never resend a call: the same body twice is a replay
		sum := sha256.Sum256(body)
		dedupe = trigger + ":" + hex.EncodeToString(sum[:12])
	}
	setNonEmpty(p, "message", b.Message)
	if title != "" {
		p["title"] = title
	}
	if u := a.webURL(t, b, link); u != "" {
		p["url"] = u
	}
	if subject == "" {
		subject = snake(b.EventType)
	}
	return Delivery{Shared: true, DedupeKey: dedupe, Event: Event{Name: name, Subject: subject, Payload: p}}, nil
}

func setNonEmpty(p map[string]any, k, v string) {
	if v = strings.TrimSpace(v); v != "" {
		p[k] = v
	}
}

// snake turns ManualInteractionRequired into manual_interaction_required.
func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Poll: everything arrives by push.
func (a arr) Poll(_ context.Context, _ Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	if len(bytes.TrimSpace(state)) == 0 {
		state = json.RawMessage(`{}`)
	}
	return nil, state, nil
}
