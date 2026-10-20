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

// Forgejo through the server: system webhooks carry the secret as a bearer
// token, and each delivery belongs to its sender's linked person.
func TestForgejoPushThroughServer(t *testing.T) {
	e := newEnv(t)
	p := polledEnv{t: t, e: e}
	p.call("POST", "/api/v1/auth/setup", map[string]string{"email": "aleks@b.c", "password": "password123", "name": "Aleks"}, nil)

	var mu sync.Mutex
	hooks := map[int64]map[string]any{}
	auth := map[int64]string{}
	var n int64
	users := []map[string]any{
		{"id": 1, "login": "root", "email": "root@b.c", "is_admin": true},
		{"id": 5, "login": "aleks-dev", "email": "dev@b.c", "is_admin": false},
		{"id": 6, "login": "stranger", "email": "x@y.z", "is_admin": false},
	}
	fj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "token admin-token" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"message":"token is required"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch {
		case path == "/version":
			js(map[string]string{"version": "16.0.6+gitea-1.22.0"})
		case path == "/user":
			js(users[0])
		case path == "/admin/users":
			w.Header().Set("X-Total-Count", fmt.Sprint(len(users)))
			if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
				js([]any{})
				return
			}
			js(users)
		case path == "/admin/hooks" && r.Method == "GET":
			out := []any{}
			for i := int64(1); i <= n; i++ {
				if h, ok := hooks[i]; ok {
					out = append(out, h)
				}
			}
			js(out)
		case path == "/admin/hooks" && r.Method == "POST":
			var in struct {
				Type   string            `json:"type"`
				Config map[string]string `json:"config"`
				Events []string          `json:"events"`
				Active bool              `json:"active"`
				Authz  string            `json:"authorization_header"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Config["is_system_webhook"] != "true" {
				t.Errorf("not a system webhook: %+v", in.Config)
			}
			n++
			ev := in.Events[0]
			ev = strings.TrimSuffix(ev, "_only") // listed as issues / pull_request
			hooks[n] = map[string]any{"id": n, "type": in.Type, "active": in.Active, "events": []string{ev}, "config": map[string]string{"url": in.Config["url"], "content_type": "json"}}
			auth[n] = in.Authz
			w.WriteHeader(201)
			js(hooks[n])
		case strings.HasPrefix(path, "/admin/hooks/") && r.Method == "DELETE":
			var id int64
			fmt.Sscan(strings.TrimPrefix(path, "/admin/hooks/"), &id)
			delete(hooks, id)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer fj.Close()

	var sv struct {
		ID int64 `json:"id"`
	}
	if c, txt := p.call("POST", "/api/v1/services", map[string]any{"type": "forgejo", "url": fj.URL, "public_url": "https://git.example", "fields": map[string]string{"TOKEN": "admin-token"}}, &sv); c != 201 {
		t.Fatalf("service %d %s", c, txt)
	}
	base := "/api/v1/services/" + itoa(sv.ID) + "/connector"
	p.call("PUT", base, map[string]any{"enabled": true}, nil)
	// the owner pushes as aleks-dev: link by hand
	if c, txt := p.call("PUT", base+"/accounts/1", map[string]string{"remote_id": "5"}, nil); c != 200 {
		t.Fatalf("link %d %s", c, txt)
	}
	if c, txt := p.call("PUT", base, map[string]any{"events_enabled": true}, nil); c != 200 {
		t.Fatalf("events on %d %s", c, txt)
	}
	mu.Lock()
	var pushURL, bearer string
	for id, h := range hooks {
		if h["events"].([]string)[0] == "push" {
			pushURL, bearer = h["config"].(map[string]string)["url"], auth[id]
		}
	}
	count := len(hooks)
	mu.Unlock()
	if count != 8 || !strings.HasPrefix(bearer, "Bearer ") || len(bearer) < 40 {
		t.Fatalf("%d hooks, bearer %q", count, bearer)
	}
	path := pushURL[strings.Index(pushURL, "/api/v1/connectors/hooks/"):]

	deliver := func(authz string, body map[string]any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forgejo-Event", "push")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	push := func(sender map[string]any, after string) map[string]any {
		return map[string]any{"ref": "refs/heads/main", "before": "aaa", "after": after, "compare_url": "https://git.example/x/compare",
			"commits":    []any{map[string]any{"id": after, "message": "Fix the thing\n\nlong body", "url": "https://git.example/c"}},
			"repository": map[string]any{"id": 9, "name": "homelab", "full_name": "aleks-dev/homelab", "html_url": "https://git.example/aleks-dev/homelab", "private": true},
			"pusher":     sender, "sender": sender}
	}
	me := users[1]
	if c := deliver("Bearer wrong", push(me, "bbb")); c != 404 {
		t.Fatalf("wrong bearer %d", c)
	}
	if c := deliver("", push(me, "bbb")); c != 404 {
		t.Fatalf("no secret %d", c)
	}
	if c := deliver(bearer, push(me, "bbb")); c/100 != 2 {
		t.Fatalf("push %d", c)
	}
	if c := deliver(bearer, push(users[2], "ccc")); c/100 != 2 { // not linked: accepted, not published
		t.Fatalf("stranger %d", c)
	}
	evs := p.events("forgejo.pushed")
	if len(evs) != 1 || evs[0].Payload["user_email"] != "aleks@b.c" || evs[0].Payload["commits"] != float64(1) || evs[0].Payload["repo"] == nil {
		t.Fatalf("pushed %+v", evs)
	}

	// Events off removes Core's hooks.
	p.call("PUT", base, map[string]any{"events_enabled": false}, nil)
	mu.Lock()
	left := len(hooks)
	mu.Unlock()
	if left != 0 {
		t.Fatalf("%d hooks left", left)
	}
}
