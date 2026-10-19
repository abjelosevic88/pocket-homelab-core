package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeArr is a small stand-in for Radarr 6 / Sonarr 4 / Lidarr 3.
type fakeArr struct {
	*httptest.Server
	mu        sync.Mutex
	app, api  string
	key       string
	triggers  []string
	notes     map[int64]map[string]any
	n         int64
	forceSave bool
	coreURL   string // where http://core.test really is
	tested    int    // test calls made before saving
}

func newFakeArr(t *testing.T, app, api string, triggers []string) *fakeArr {
	f := &fakeArr{app: app, api: api, key: app + "-key", triggers: triggers, notes: map[int64]map[string]any{
		1: {"id": 1, "name": "Apprise central router", "implementation": "Apprise"},
	}, n: 1}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != f.key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		p := strings.TrimPrefix(r.URL.Path, f.api)
		switch {
		case p == "/system/status":
			js(map[string]any{"appName": f.app, "instanceName": f.app, "version": "9.9.9"})
		case p == "/notification/schema":
			s := map[string]any{"implementation": "Webhook", "name": "", "tags": []any{}, "includeHealthWarnings": true,
				"fields": []any{map[string]any{"name": "url"}, map[string]any{"name": "method", "value": 1}, map[string]any{"name": "username"},
					map[string]any{"name": "password"}, map[string]any{"name": "headers", "value": []any{}}}}
			for _, k := range f.triggers {
				s[k] = false
			}
			js([]any{map[string]any{"implementation": "Ntfy"}, s})
		case p == "/notification" && r.Method == "GET":
			out := []any{}
			for i := int64(1); i <= f.n; i++ {
				if n, ok := f.notes[i]; ok {
					out = append(out, n)
				}
			}
			js(out)
		case p == "/notification" && r.Method == "POST":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.forceSave = r.URL.Query().Get("forceSave") == "true"
			// Like the real apps: an enabled connection is called once with
			// a Test event before it is saved, forceSave or not.
			if msg := f.testCall(in); msg != "" {
				w.WriteHeader(400)
				js([]map[string]string{{"propertyName": "Url", "errorMessage": msg}})
				return
			}
			f.n++
			in["id"] = f.n
			f.notes[f.n] = in
			js(in)
		case strings.HasPrefix(p, "/notification/") && r.Method == "DELETE":
			var id int64
			fmt.Sscan(strings.TrimPrefix(p, "/notification/"), &id)
			if _, ok := f.notes[id]; !ok {
				w.WriteHeader(404)
				return
			}
			delete(f.notes, id)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// testCall posts {"eventType":"Test"} the way the apps do; "" = it passed.
func (f *fakeArr) testCall(n map[string]any) string {
	enabled := false
	for k, v := range n {
		enabled = enabled || (strings.HasPrefix(k, "on") && v == true)
	}
	if !enabled {
		return ""
	}
	var u string
	hdr := map[string]string{}
	for _, x := range n["fields"].([]any) {
		m := x.(map[string]any)
		switch m["name"] {
		case "url":
			u = fmt.Sprint(m["value"])
		case "headers":
			if l, ok := m["value"].([]any); ok {
				for _, h := range l {
					hm := h.(map[string]any)
					hdr[fmt.Sprint(hm["key"])] = fmt.Sprint(hm["value"])
				}
			}
		}
	}
	req, _ := http.NewRequest("POST", strings.Replace(u, "http://core.test", f.coreURL, 1), strings.NewReader(`{"eventType":"Test","instanceName":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	f.tested++
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "Unable to post to webhook: " + err.Error()
	}
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Sprintf("Unable to post to webhook: HTTP request failed: [%d]", res.StatusCode)
	}
	return ""
}

// ours returns Core's connection, its URL path and secret.
func (f *fakeArr) ours(t *testing.T) (map[string]any, string, string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.notes {
		if n["name"] != "Pocket Core" {
			continue
		}
		var u, secret string
		for _, x := range n["fields"].([]any) {
			m := x.(map[string]any)
			switch m["name"] {
			case "url":
				u = fmt.Sprint(m["value"])
			case "headers":
				h := m["value"].([]any)[0].(map[string]any)
				if h["key"] != "X-Pocket-Hook" {
					t.Fatalf("header %+v", h)
				}
				secret = fmt.Sprint(h["value"])
			}
		}
		return n, strings.TrimPrefix(u, "http://core.test"), secret
	}
	return nil, "", ""
}

func TestArrConnectors(t *testing.T) {
	e := newEnv(t)
	call := func(method, path string, body any, out any) int {
		t.Helper()
		code, txt := e.raw(method, path, "", body)
		if out != nil {
			_ = json.Unmarshal([]byte(txt), out)
		}
		return code
	}
	call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)
	deliver := func(path, secret string, body any) (int, string) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Pocket-Hook", secret)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		return res.StatusCode, buf.String()
	}
	type ev struct {
		Topic   string         `json:"topic"`
		Subject string         `json:"subject"`
		Payload map[string]any `json:"payload"`
	}
	events := func(topic string) []ev {
		var out []ev
		call("GET", "/api/v1/events?topic="+topic, nil, &out)
		return out
	}

	cases := []struct {
		app, api string
		triggers []string
		want     []string // switched on
		off      []string // left off
		import1  map[string]any
		check    func(p map[string]any) string
	}{
		{"radarr", "/api/v3", []string{"onGrab", "onDownload", "onUpgrade", "onRename", "onMovieAdded", "onMovieDelete", "onMovieFileDelete", "onMovieFileDeleteForUpgrade", "onHealthIssue", "onHealthRestored", "onApplicationUpdate", "onManualInteractionRequired"},
			[]string{"onGrab", "onDownload", "onUpgrade", "onMovieAdded", "onHealthIssue"}, []string{"onRename", "onMovieFileDeleteForUpgrade"},
			map[string]any{"eventType": "Download", "instanceName": "Radarr", "isUpgrade": false, "movie": map[string]any{"id": 7, "title": "Dune", "year": 2021, "tmdbId": 438631},
				"movieFile": map[string]any{"relativePath": "Dune (2021).mkv", "quality": "Bluray-2160p", "size": 50}, "release": map[string]any{"releaseTitle": "Dune.2021.2160p", "indexer": "Nyaa"}, "downloadClient": "qBittorrent"},
			func(p map[string]any) string {
				if p["title"] != "Dune (2021)" || p["tmdb_id"] != float64(438631) || p["quality"] != "Bluray-2160p" || p["url"] != "https://radarr.example/movie/438631" || p["file"] != "Dune (2021).mkv" {
					return "radarr payload"
				}
				return ""
			}},
		{"sonarr", "/api/v3", []string{"onGrab", "onDownload", "onUpgrade", "onImportComplete", "onRename", "onSeriesAdd", "onSeriesDelete", "onEpisodeFileDelete", "onEpisodeFileDeleteForUpgrade", "onHealthIssue", "onHealthRestored", "onApplicationUpdate", "onManualInteractionRequired"},
			[]string{"onDownload", "onSeriesAdd", "onEpisodeFileDelete"}, []string{"onImportComplete", "onRename"},
			map[string]any{"eventType": "Download", "isUpgrade": true, "series": map[string]any{"id": 3, "title": "Severance", "titleSlug": "severance", "tvdbId": 371980},
				"episodes": []any{map[string]any{"seasonNumber": 2, "episodeNumber": 1, "title": "Hello, Ms. Cobel"}}, "episodeFile": map[string]any{"relativePath": "S02E01.mkv", "quality": "WEBDL-1080p"}},
			func(p map[string]any) string {
				if p["title"] != "Severance S02E01" || p["season"] != float64(2) || p["episode_title"] != "Hello, Ms. Cobel" || p["url"] != "https://sonarr.example/series/severance" {
					return "sonarr payload"
				}
				return ""
			}},
		{"lidarr", "/api/v1", []string{"onGrab", "onReleaseImport", "onUpgrade", "onRename", "onArtistAdd", "onArtistDelete", "onAlbumDelete", "onHealthIssue", "onHealthRestored", "onDownloadFailure", "onImportFailure", "onTrackRetag", "onApplicationUpdate"},
			[]string{"onReleaseImport", "onDownloadFailure", "onImportFailure"}, []string{"onTrackRetag", "onRename"},
			map[string]any{"eventType": "Download", "artist": map[string]any{"id": 4, "name": "Massive Attack", "mbId": "10adbe5e"}, "album": map[string]any{"title": "Mezzanine", "releaseDate": "1998-04-20"},
				"tracks": []any{map[string]any{"id": 1}, map[string]any{"id": 2}}},
			func(p map[string]any) string {
				if p["title"] != "Massive Attack – Mezzanine" || p["tracks"] != float64(2) || p["url"] != "https://lidarr.example/artist/10adbe5e" {
					return "lidarr payload"
				}
				return ""
			}},
	}
	for _, tc := range cases {
		f := newFakeArr(t, strings.ToUpper(tc.app[:1])+tc.app[1:], tc.api, tc.triggers)
		f.coreURL = e.srv.URL
		var sv struct {
			ID int64 `json:"id"`
		}
		if c := call("POST", "/api/v1/services", map[string]any{"type": tc.app, "url": f.URL, "public_url": "https://" + tc.app + ".example", "fields": map[string]string{"API_KEY": f.key}}, &sv); c != 201 {
			t.Fatalf("%s service %d", tc.app, c)
		}
		base := "/api/v1/services/" + itoa(sv.ID) + "/connector"
		var tr struct {
			OK   bool   `json:"ok"`
			Err  string `json:"error"`
			Info struct {
				Version string `json:"version"`
				Detail  string `json:"detail"`
			} `json:"info"`
		}
		call("POST", base+"/test", nil, &tr)
		if !tr.OK || tr.Info.Version != "9.9.9" || !strings.Contains(tr.Info.Detail, "Settings → Connect") {
			t.Fatalf("%s test %+v", tc.app, tr)
		}
		var view struct {
			Capabilities map[string]bool `json:"capabilities"`
		}
		call("GET", base, nil, &view)
		if !view.Capabilities["events"] || view.Capabilities["provision"] || view.Capabilities["sso"] {
			t.Fatalf("%s caps %+v", tc.app, view)
		}
		if c, txt := e.raw("PUT", base, "", map[string]any{"enabled": true, "sso_enabled": true}); c == 200 && strings.Contains(txt, `"sso_enabled":true`) {
			t.Fatalf("%s: there is no sign-in to switch on: %s", tc.app, txt)
		}
		call("PUT", base, map[string]any{"enabled": true}, nil)
		if c, txt := e.raw("PUT", base, "", map[string]any{"events_enabled": true}); c != 200 {
			t.Fatalf("%s events on %d %s", tc.app, c, txt)
		}
		n, path, secret := f.ours(t)
		if n == nil || len(secret) < 32 || !strings.HasPrefix(path, "/api/v1/connectors/hooks/") {
			t.Fatalf("%s connection %+v %q", tc.app, n, path)
		}
		f.mu.Lock()
		forced := f.forceSave
		f.mu.Unlock()
		if f.tested != 1 {
			t.Fatalf("%s: the app's test call should have run once and passed, ran %d", tc.app, f.tested)
		}
		if !forced || n["includeHealthWarnings"] != false {
			t.Fatalf("%s: forceSave %v, warnings %v", tc.app, forced, n["includeHealthWarnings"])
		}
		for _, k := range tc.want {
			if n[k] != true {
				t.Fatalf("%s: %s should be on", tc.app, k)
			}
		}
		for _, k := range tc.off {
			if n[k] != false {
				t.Fatalf("%s: %s should stay off", tc.app, k)
			}
		}
		// Switching events on again changes nothing.
		call("PUT", base, map[string]any{"events_enabled": true}, nil)
		f.mu.Lock()
		count := len(f.notes)
		f.mu.Unlock()
		if count != 2 {
			t.Fatalf("%s: %d connections", tc.app, count)
		}

		// Deliveries: wrong secret refused, Test dropped, an import becomes a household event.
		if c, _ := deliver(path, "wrong", tc.import1); c != 404 {
			t.Fatalf("%s wrong secret %d", tc.app, c)
		}
		if c, txt := deliver(path, secret, map[string]any{"eventType": "Test"}); c/100 != 2 {
			t.Fatalf("%s test delivery %d %s", tc.app, c, txt)
		}
		if c, txt := deliver(path, secret, tc.import1); c/100 != 2 {
			t.Fatalf("%s import %d %s", tc.app, c, txt)
		}
		deliver(path, secret, tc.import1) // a replay is published once
		deliver(path, secret, map[string]any{"eventType": "Health", "level": "error", "message": "Indexers unavailable", "type": "IndexerStatusCheck", "wikiUrl": "https://wiki"})
		name := tc.app + ".imported"
		if tc.app == "sonarr" {
			name = tc.app + ".upgraded"
		}
		got := events(name)
		if len(got) != 1 {
			t.Fatalf("%s: %d %s events", tc.app, len(got), name)
		}
		if got[0].Payload["user_email"] != nil {
			t.Fatalf("%s: household events carry no person: %+v", tc.app, got[0].Payload)
		}
		if msg := tc.check(got[0].Payload); msg != "" {
			t.Fatalf("%s: %+v", msg, got[0].Payload)
		}
		if h := events(tc.app + ".health_issue"); len(h) != 1 || h[0].Subject != "health:IndexerStatusCheck" || h[0].Payload["level"] != "error" || h[0].Payload["title"] != "Indexers unavailable" {
			t.Fatalf("%s health %+v", tc.app, h)
		}
		if len(events(tc.app+".test")) != 0 {
			t.Fatalf("%s: Test calls are not news", tc.app)
		}

		// Events off removes Core's connection and leaves the others.
		call("PUT", base, map[string]any{"events_enabled": false}, nil)
		if n, _, _ := f.ours(t); n != nil {
			t.Fatalf("%s: connection left behind", tc.app)
		}
		f.mu.Lock()
		_, apprise := f.notes[1]
		f.mu.Unlock()
		if !apprise {
			t.Fatalf("%s: someone else's connection was removed", tc.app)
		}
	}
}
