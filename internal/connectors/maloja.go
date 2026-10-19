package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Maloja (3.x) connector: events, polled. Maloja is one person's listening
// history, with no accounts and no OpenID sign-in, so its single listener is
// linked to a person by hand and every scrobble carries their email.
//
//	GET /apis/mlj_1/serverinfo    Test           read (public)
//	GET /apis/mlj_1/test?key=     Test           read, only when an API key is saved
//	GET /apis/mlj_1/scrobbles     Poll           read (public), newest first
//	GET /apis/mlj_1/artistinfo    Poll: is an artist new                read (public)
type maloja struct{}

func init() { Register(maloja{}) }

// MalojaListener is the one account Maloja has.
const MalojaListener = "listener"

const malojaPage = 200

func (maloja) Type() string               { return "maloja" }
func (maloja) Capabilities() Capabilities { return Capabilities{Events: true, Link: true} }
func (maloja) SSORedirects(string) []string {
	return nil
}

func (maloja) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Maloja has one listener and no accounts: link it to its person by hand")
}

func (maloja) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Maloja has no OpenID sign-in")
	}
	return nil
}

func (maloja) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	var si struct {
		Name          string `json:"name"`
		VersionString string `json:"versionstring"`
		DB            struct {
			Healthy bool `json:"healthy"`
		} `json:"db_status"`
	}
	if _, err := doJSON(ctx, t, "Maloja", http.MethodGet, "/apis/mlj_1/serverinfo", nil, nil, &si); err != nil {
		return info, fmt.Errorf("cannot reach Maloja at %s: %w", t.BaseURL, err)
	}
	info.Version = si.VersionString
	var n struct {
		Amount int `json:"amount"`
	}
	_, _ = doJSON(ctx, t, "Maloja", http.MethodGet, "/apis/mlj_1/numscrobbles", nil, nil, &n)
	notes := []string{fmt.Sprintf("%d scrobbles; Core only reads (no key needed); link the listener to its person under People so scrobbles carry their email", n.Amount)}
	if !si.DB.Healthy {
		notes = append(notes, "Maloja reports its database as not healthy")
	}
	if k := strings.TrimSpace(t.Fields["API_KEY"]); k != "" {
		var r struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		_, err := doJSON(ctx, t, "Maloja", http.MethodGet, "/apis/mlj_1/test?"+url.Values{"key": {k}}.Encode(), nil, nil, &r, k)
		if err != nil || r.Status != "ok" {
			notes = append(notes, "the saved API key is not accepted by Maloja (devices that scrobble with it will fail)")
		} else {
			info.Admin = true
			notes = append(notes, "the saved API key is valid")
		}
	}
	info.Users = 1
	info.Detail = strings.Join(notes, "; ")
	return info, nil
}

func (maloja) RemoteAccounts(context.Context, Target) ([]RemoteAccount, error) {
	return []RemoteAccount{{ID: MalojaListener, Name: "Maloja's listener"}}, nil
}

func (maloja) Link(_ context.Context, _ Target, remoteID string) (Account, error) {
	if remoteID != MalojaListener {
		return Account{}, fmt.Errorf("Maloja has one listener (%q)", MalojaListener)
	}
	return Account{RemoteID: MalojaListener, Status: StatusLinked, Note: "linked by hand: this person is Maloja's listener"}, nil
}

type mjScrobble struct {
	Time  int64 `json:"time"`
	Track struct {
		Artists []string `json:"artists"`
		Title   string   `json:"title"`
		Album   *struct {
			Artists    []string `json:"artists"`
			AlbumTitle string   `json:"albumtitle"`
		} `json:"album"`
		Length *int64 `json:"length"`
	} `json:"track"`
	Duration *int64 `json:"duration"`
	Origin   string `json:"origin"`
}

func (s mjScrobble) key() string {
	h := sha256.Sum256([]byte(fmt.Sprint(s.Time, "|", strings.Join(s.Track.Artists, "/"), "|", s.Track.Title)))
	return hex.EncodeToString(h[:8])
}

type mjState struct {
	Init bool     `json:"init"`
	Last int64    `json:"last"`           // newest scrobble time seen
	Seen []string `json:"seen,omitempty"` // keys of the scrobbles at that second
}

func (c maloja) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	var page struct {
		List []mjScrobble `json:"list"`
	}
	q := url.Values{"perpage": {fmt.Sprint(malojaPage)}, "page": {"0"}}
	if _, err := doJSON(ctx, t, "Maloja", http.MethodGet, "/apis/mlj_1/scrobbles?"+q.Encode(), nil, nil, &page); err != nil {
		return nil, state, err
	}
	var prev mjState
	_ = json.Unmarshal(state, &prev)
	cur := mjState{Init: true, Last: prev.Last}
	for _, s := range page.List {
		cur.Last = max(cur.Last, s.Time)
	}
	seenPrev := map[string]bool{}
	for _, k := range prev.Seen {
		seenPrev[k] = true
	}
	for _, s := range page.List {
		if s.Time == cur.Last {
			cur.Seen = append(cur.Seen, s.key())
		}
	}
	if cur.Last == prev.Last {
		for k := range seenPrev {
			if !contains(cur.Seen, k) {
				cur.Seen = append(cur.Seen, k)
			}
		}
	}
	sort.Strings(cur.Seen)
	next, _ := json.Marshal(cur)
	if !prev.Init {
		return nil, next, nil // baseline
	}
	fresh := []mjScrobble{}
	for _, s := range page.List {
		if s.Time > prev.Last || (s.Time == prev.Last && !seenPrev[s.key()]) {
			fresh = append(fresh, s)
		}
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Time < fresh[j].Time })
	pub := strings.TrimRight(t.PublicURL, "/")
	var out []Event
	newArtist := map[string]int{} // artist → plays in this batch
	for _, s := range fresh {
		for _, a := range s.Track.Artists {
			newArtist[a]++
		}
	}
	firsts := map[string]bool{}
	checked := 0
	for a, n := range newArtist {
		if checked >= 25 {
			break
		}
		checked++
		var ai struct {
			Scrobbles int `json:"scrobbles"`
		}
		if _, err := doJSON(ctx, t, "Maloja", http.MethodGet, "/apis/mlj_1/artistinfo?"+url.Values{"artist": {a}}.Encode(), nil, nil, &ai); err == nil && ai.Scrobbles > 0 && ai.Scrobbles <= n {
			firsts[a] = true
		}
	}
	for _, s := range fresh {
		at := time.Unix(s.Time, 0).UTC().Format(time.RFC3339)
		p := map[string]any{"title": s.Track.Title, "artists": s.Track.Artists, "artist": strings.Join(s.Track.Artists, ", "), "at": at}
		if s.Track.Album != nil && s.Track.Album.AlbumTitle != "" {
			p["album"] = s.Track.Album.AlbumTitle
			if len(s.Track.Album.Artists) > 0 {
				p["album_artists"] = s.Track.Album.Artists
			}
		}
		if s.Duration != nil && *s.Duration > 0 {
			p["listened_seconds"] = *s.Duration
		}
		if s.Track.Length != nil && *s.Track.Length > 0 {
			p["length_seconds"] = *s.Track.Length
		}
		if o := strings.TrimPrefix(s.Origin, "client:"); o != "" {
			p["client"] = o
		}
		if pub != "" && len(s.Track.Artists) > 0 {
			v := url.Values{"title": {s.Track.Title}}
			for _, a := range s.Track.Artists {
				v.Add("artist", a)
			}
			p["url"] = pub + "/track?" + v.Encode()
		}
		out = append(out, Event{Name: "scrobbled", Subject: "scrobble:" + s.key(), RemoteUserID: MalojaListener, Payload: p})
		for _, a := range s.Track.Artists {
			if firsts[a] {
				delete(firsts, a) // once, on the first new play
				q := map[string]any{"artist": a, "track": s.Track.Title, "at": at, "title": "First listen: " + a}
				if pub != "" {
					q["url"] = pub + "/artist?" + url.Values{"artist": {a}}.Encode()
				}
				out = append(out, Event{Name: "new_artist", Subject: "artist:" + a, RemoteUserID: MalojaListener, Payload: q})
			}
		}
	}
	return out, next, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
