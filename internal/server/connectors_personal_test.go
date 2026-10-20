package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Personal events (what someone reads) reach the bus only for accounts
// linked to a Core person; everyone else's stay out, never published as
// household events. Driven through Kavita's polled reading sessions.
func TestPersonalEventsNeedALinkedPerson(t *testing.T) {
	e := newEnv(t)
	p := polledEnv{t: t, e: e}
	p.call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)

	var mu sync.Mutex
	sessions := []any{}
	kv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/health" {
			_, _ = w.Write([]byte("Ok"))
			return
		}
		if r.Header.Get("x-api-key") != "kv-admin" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/Users":
			js([]any{
				map[string]any{"id": 1, "username": "owner", "email": "owner@home.lan", "roles": []string{"Admin", "Pleb", "Login"}},
				map[string]any{"id": 2, "username": "aleks", "email": "reader@home.lan", "roles": []string{"Pleb", "Login"}},
				map[string]any{"id": 3, "username": "guest", "email": "guest@home.lan", "roles": []string{"Pleb", "Login"}},
			})
		case "/api/Activity/current":
			js(sessions)
		case "/api/Series/all-v2":
			js([]any{})
		default:
			w.WriteHeader(404)
		}
	}))
	defer kv.Close()

	base := p.service("kavita", kv.URL, map[string]string{"API_KEY": "kv-admin"})
	if c, txt := p.call("PUT", base+"/accounts/1", map[string]string{"remote_id": "2"}, nil); c != 200 {
		t.Fatalf("link %d %s", c, txt)
	}
	p.poll(base) // baseline

	start := time.Now().UTC().Add(-5 * time.Minute).Format("2006-01-02T15:04:05.0000000")
	session := func(id, user int, name, series string) map[string]any {
		return map[string]any{"id": id, "startTimeUtc": start, "userId": user, "username": name, "activityData": []any{map[string]any{
			"chapterId": 10 + id, "volumeId": 20 + id, "seriesId": 30 + id, "libraryId": 1, "endPage": 3, "startTimeUtc": start,
			"pagesRead": 3, "totalPages": 300, "libraryName": "Books", "seriesName": series, "chapterTitle": "1"}}}
	}
	mu.Lock()
	sessions = []any{session(1, 2, "aleks", "The Dispossessed"), session(2, 3, "guest", "Someone else's book")}
	mu.Unlock()
	p.poll(base)

	evs := p.events("kavita.reading_started")
	if len(evs) != 1 || evs[0].Payload["user_email"] != "aleks@b.c" || evs[0].Payload["series"] != "The Dispossessed" {
		t.Fatalf("only the linked person's reading is published: %+v", evs)
	}
}
