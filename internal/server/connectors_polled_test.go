package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type polledEnv struct {
	t *testing.T
	e *env
}

func (p polledEnv) call(method, path string, body, out any) (int, string) {
	p.t.Helper()
	code, txt := p.e.raw(method, path, "", body)
	if out != nil {
		_ = json.Unmarshal([]byte(txt), out)
	}
	return code, txt
}

type pevent struct {
	Topic   string         `json:"topic"`
	Subject string         `json:"subject"`
	Payload map[string]any `json:"payload"`
}

func (p polledEnv) events(topic string) []pevent {
	var out []pevent
	p.call("GET", "/api/v1/events?topic="+topic+"&limit=200", nil, &out)
	return out
}

// service makes the service, turns the connector and its events on and returns the connector's base path.
func (p polledEnv) service(typ, url string, fields map[string]string) string {
	p.t.Helper()
	var sv struct {
		ID int64 `json:"id"`
	}
	if c, txt := p.call("POST", "/api/v1/services", map[string]any{"type": typ, "url": url, "public_url": "https://" + typ + ".example", "fields": fields}, &sv); c != 201 {
		p.t.Fatalf("%s service %d %s", typ, c, txt)
	}
	base := "/api/v1/services/" + itoa(sv.ID) + "/connector"
	if c, txt := p.call("PUT", base, map[string]any{"enabled": true, "events_enabled": true}, nil); c != 200 {
		p.t.Fatalf("%s on %d %s", typ, c, txt)
	}
	return base
}

func (p polledEnv) poll(base string) map[string]any {
	p.t.Helper()
	var pr map[string]any
	if c, txt := p.call("POST", base+"/poll", nil, &pr); c != 200 {
		p.t.Fatalf("poll %s: %d %s", base, c, txt)
	}
	return pr
}

func (p polledEnv) test(base string) (bool, string, string) {
	var tr struct {
		OK   bool   `json:"ok"`
		Err  string `json:"error"`
		Info struct {
			Version string `json:"version"`
			Detail  string `json:"detail"`
		} `json:"info"`
	}
	p.call("POST", base+"/test", nil, &tr)
	return tr.OK, tr.Info.Version, tr.Info.Detail + tr.Err
}

func TestPolledConnectors(t *testing.T) {
	e := newEnv(t)
	p := polledEnv{t, e}
	p.call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)

	t.Run("maloja", func(t *testing.T) {
		var mu sync.Mutex
		now := time.Now().Unix()
		scrobble := func(at int64, artist, title string) map[string]any {
			return map[string]any{"time": at, "track": map[string]any{"artists": []string{artist}, "title": title,
				"album": map[string]any{"artists": []string{artist}, "albumtitle": "Album of " + artist}, "length": 200}, "duration": 180, "origin": "client:navidrome"}
		}
		list := []map[string]any{scrobble(now-600, "Old Artist", "Old Song")}
		plays := map[string]int{"Old Artist": 40}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
			switch r.URL.Path {
			case "/apis/mlj_1/serverinfo":
				js(map[string]any{"name": "Maloja", "versionstring": "3.2.9", "db_status": map[string]any{"healthy": true}})
			case "/apis/mlj_1/numscrobbles":
				js(map[string]any{"status": "ok", "amount": len(list)})
			case "/apis/mlj_1/test":
				if r.URL.Query().Get("key") == "mlj-key" {
					js(map[string]any{"status": "ok"})
				} else {
					w.WriteHeader(403)
					js(map[string]any{"status": "error", "error": "Wrong API key"})
				}
			case "/apis/mlj_1/scrobbles":
				out := make([]map[string]any, len(list))
				for i := range list { // newest first
					out[i] = list[len(list)-1-i]
				}
				js(map[string]any{"status": "ok", "list": out})
			case "/apis/mlj_1/artistinfo":
				js(map[string]any{"artist": r.URL.Query().Get("artist"), "scrobbles": plays[r.URL.Query().Get("artist")]})
			default:
				w.WriteHeader(404)
			}
		}))
		defer srv.Close()
		base := p.service("maloja", srv.URL, map[string]string{"API_KEY": "mlj-key"})
		if ok, v, d := p.test(base); !ok || v != "3.2.9" || !strings.Contains(d, "key is valid") {
			t.Fatalf("test %v %s %s", ok, v, d)
		}
		var remote []map[string]any
		if c, txt := p.call("GET", base+"/remote-accounts", nil, &remote); c != 200 || len(remote) != 1 {
			t.Fatalf("remote %d %s", c, txt)
		}
		if c, txt := p.call("PUT", base+"/accounts/1", map[string]string{"remote_id": "listener"}, nil); c != 200 {
			t.Fatalf("link listener %d %s", c, txt)
		}
		if c, _ := p.call("POST", base+"/provision", map[string]any{"all": true}, nil); c == 200 {
			t.Fatalf("Maloja cannot make accounts")
		}
		if pr := p.poll(base); pr["baseline"] != true {
			t.Fatalf("baseline %+v", pr)
		}
		mu.Lock()
		list = append(list, scrobble(now-60, "Old Artist", "Song A"), scrobble(now-60, "New Artist", "Song B"), scrobble(now-30, "New Artist", "Song C"))
		plays["Old Artist"], plays["New Artist"] = 41, 2
		mu.Unlock()
		if pr := p.poll(base); pr["events"] != float64(4) {
			t.Fatalf("second poll %+v", pr)
		}
		got := p.events("maloja.scrobbled")
		if len(got) != 3 {
			t.Fatalf("scrobbles %+v", got)
		}
		for _, ev := range got {
			if ev.Payload["user_email"] != "aleks@b.c" || ev.Payload["client"] != "navidrome" || ev.Payload["listened_seconds"] != float64(180) {
				t.Fatalf("scrobble payload %+v", ev.Payload)
			}
		}
		if na := p.events("maloja.new_artist"); len(na) != 1 || na[0].Payload["artist"] != "New Artist" || na[0].Payload["track"] != "Song B" {
			t.Fatalf("new artist %+v", na)
		}
		// Another scrobble in the same second as the newest one arrives late: still published, once.
		mu.Lock()
		list = append(list, scrobble(now-30, "Old Artist", "Same Second"))
		mu.Unlock()
		if pr := p.poll(base); pr["events"] != float64(1) {
			t.Fatalf("same-second poll %+v", pr)
		}
		if pr := p.poll(base); pr["events"] != float64(0) {
			t.Fatalf("idle poll %+v", pr)
		}
	})

	t.Run("kuma", func(t *testing.T) {
		var mu sync.Mutex
		status := map[string]int{"Jellyfin": 1, "Immich": 0, "NAS": 1}
		cert := map[string]int{"Jellyfin": 40}
		msg := map[string]string{"Jellyfin": "200 - OK", "Immich": "connect EHOSTUNREACH", "NAS": ""}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch r.URL.Path {
			case "/metrics":
				if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(":uk1_key")) {
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprintln(w, "# HELP monitor_status Monitor Status (1 = UP, 0= DOWN, 2= PENDING, 3= MAINTENANCE)")
				for _, n := range []string{"Jellyfin", "Immich", "NAS"} {
					lab := fmt.Sprintf(`monitor_name="%s",monitor_type="http",monitor_url="https://%s.example",monitor_hostname="null",monitor_port="null"`, n, strings.ToLower(n))
					fmt.Fprintf(w, "monitor_status{%s} %d\n", lab, status[n])
					if d, ok := cert[n]; ok {
						fmt.Fprintf(w, "monitor_cert_days_remaining{%s} %d\n", lab, d)
					}
				}
			case "/api/status-page/home":
				_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"title": "Home"}, "publicGroupList": []any{map[string]any{"name": "Services",
					"monitorList": []any{map[string]any{"id": 1, "name": "Jellyfin", "type": "http"}, map[string]any{"id": 2, "name": "Immich", "type": "http"}}}}})
			case "/api/status-page/heartbeat/home":
				_ = json.NewEncoder(w).Encode(map[string]any{"heartbeatList": map[string]any{
					"1": []any{map[string]any{"status": status["Jellyfin"], "time": "2026-10-19 21:00:00", "msg": msg["Jellyfin"]}},
					"2": []any{map[string]any{"status": status["Immich"], "time": "2026-10-19 21:00:00", "msg": msg["Immich"]}}}})
			default:
				w.WriteHeader(404)
			}
		}))
		defer srv.Close()
		base := p.service("kuma", srv.URL, map[string]string{"API_KEY": "uk1_key", "STATUS_SLUG": "home"})
		if ok, _, d := p.test(base); !ok || !strings.Contains(d, "3 monitors, 1 down") || !strings.Contains(d, "every monitor") {
			t.Fatalf("test %v %s", ok, d)
		}
		if pr := p.poll(base); pr["baseline"] != true {
			t.Fatalf("baseline %+v", pr)
		}
		mu.Lock()
		status["Jellyfin"], msg["Jellyfin"] = 0, "Request failed with status code 502"
		status["Immich"] = 1
		cert["Jellyfin"] = 13
		mu.Unlock()
		if pr := p.poll(base); pr["events"] != float64(3) {
			t.Fatalf("second poll %+v", pr)
		}
		down := p.events("kuma.monitor_down")
		if len(down) != 1 || down[0].Payload["monitor"] != "Jellyfin" || down[0].Payload["message"] != "Request failed with status code 502" ||
			down[0].Payload["target"] != "https://jellyfin.example" || down[0].Payload["user_email"] != nil {
			t.Fatalf("down %+v", down)
		}
		if up := p.events("kuma.monitor_up"); len(up) != 1 || up[0].Payload["monitor"] != "Immich" || up[0].Payload["down_for_seconds"] == nil {
			t.Fatalf("up %+v", up)
		}
		// 13 → 6 days: one more warning (the 7-day step), then nothing until the next step.
		mu.Lock()
		cert["Jellyfin"] = 6
		mu.Unlock()
		p.poll(base)
		mu.Lock()
		cert["Jellyfin"] = 5
		mu.Unlock()
		p.poll(base)
		if c := p.events("kuma.cert_expiring"); len(c) != 2 {
			t.Fatalf("cert warnings %+v", c)
		}
		if got := p.events("kuma.monitor_down"); len(got) != 1 {
			t.Fatalf("still down is not news: %d", len(got))
		}
	})

	t.Run("kuma status page only", func(t *testing.T) {
		p := polledEnv{t, newEnv(t)} // one service per type
		p.call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/status-page/pub":
				_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"title": "Public"}, "publicGroupList": []any{map[string]any{"monitorList": []any{map[string]any{"id": 5, "name": "Web", "type": "http"}}}}})
			case "/api/status-page/heartbeat/pub":
				_ = json.NewEncoder(w).Encode(map[string]any{"heartbeatList": map[string]any{"5": []any{map[string]any{"status": 1, "msg": ""}}}})
			default:
				w.WriteHeader(404)
			}
		}))
		defer srv.Close()
		var sv struct {
			ID int64 `json:"id"`
		}
		if c, txt := p.call("POST", "/api/v1/services", map[string]any{"type": "kuma", "name": "Kuma public", "url": srv.URL, "fields": map[string]string{"STATUS_SLUG": "pub"}}, &sv); c != 201 {
			t.Fatalf("second kuma service %d %s", c, txt)
		}
		if ok, _, d := p.test("/api/v1/services/" + itoa(sv.ID) + "/connector"); !ok || !strings.Contains(d, `status page "Public" only`) {
			t.Fatalf("status-page-only test %v %s", ok, d)
		}
	})

	t.Run("dawarich", func(t *testing.T) {
		var mu sync.Mutex
		last := time.Now().Add(-13 * time.Hour).Unix() // the phone is already quiet: recorded, not news
		visits := []map[string]any{{"id": 1, "name": "Home", "status": "suggested", "started_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339), "duration": 600,
			"place": map[string]any{"latitude": 43.85, "longitude": 18.41, "address": "Secret Street 1"}}}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("X-Dawarich-Version", "1.16.0")
			if r.URL.Path == "/api/v1/health" {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
				return
			}
			if r.Header.Get("Authorization") != "Bearer dw-key" {
				w.WriteHeader(401)
				return
			}
			switch r.URL.Path {
			case "/api/v1/users/me":
				_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": 1, "email": "demo@dawarich.app"}})
			case "/api/v1/points":
				_ = json.NewEncoder(w).Encode([]any{map[string]any{"timestamp": last, "latitude": "43.85", "longitude": "18.41", "city": nil, "country_name": nil}})
			case "/api/v1/visits":
				_ = json.NewEncoder(w).Encode(visits)
			default:
				w.WriteHeader(404)
			}
		}))
		defer srv.Close()
		base := p.service("dawarich", srv.URL, map[string]string{"API_KEY": "dw-key"})
		if ok, v, d := p.test(base); !ok || v != "1.16.0" || !strings.Contains(d, "demo@dawarich.app") {
			t.Fatalf("test %v %s %s", ok, v, d)
		}
		if c, txt := p.call("PUT", base+"/accounts/1", map[string]string{"remote_id": "1"}, nil); c != 200 {
			t.Fatalf("link %d %s", c, txt)
		}
		if pr := p.poll(base); pr["baseline"] != true {
			t.Fatalf("baseline %+v", pr)
		}
		// Tracking resumes, a new visit appears, the old one is confirmed.
		mu.Lock()
		last = time.Now().Add(-time.Minute).Unix()
		visits[0]["status"] = "confirmed"
		visits = append(visits, map[string]any{"id": 2, "name": "Konzum", "status": "suggested", "started_at": time.Now().Add(-14 * time.Hour).Format(time.RFC3339), "duration": 20,
			"place": map[string]any{"latitude": 43.86, "longitude": 18.42, "address": "Secret Street 9"}})
		mu.Unlock()
		if pr := p.poll(base); pr["events"] != float64(3) {
			t.Fatalf("second poll %+v %+v", pr, p.events("dawarich.*"))
		}
		if pr := p.poll(base); pr["events"] != float64(0) {
			t.Fatalf("idle poll %+v", pr)
		}
		for _, topic := range []string{"dawarich.tracking_resumed", "dawarich.visit_detected", "dawarich.visit_confirmed"} {
			got := p.events(topic)
			if len(got) != 1 || got[0].Payload["user_email"] != "aleks@b.c" {
				t.Fatalf("%s %+v", topic, got)
			}
			b, _ := json.Marshal(got[0].Payload)
			if strings.Contains(string(b), "Secret Street") || strings.Contains(string(b), "43.8") || strings.Contains(string(b), "18.4") {
				t.Fatalf("%s leaks a position: %s", topic, b)
			}
		}
		if v := p.events("dawarich.visit_detected"); v[0].Payload["name"] != "Konzum" || v[0].Payload["duration_minutes"] != float64(20) {
			t.Fatalf("visit %+v", v[0].Payload)
		}
	})
}
