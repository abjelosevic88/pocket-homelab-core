package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeAgent answers like the Pocket Box agent and records what it was asked.
type fakeAgent struct {
	mu       sync.Mutex
	done     bool
	timezone string
	calls    []string
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	body, _ := io.ReadAll(r.Body)
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/wizard":
		_ = json.NewEncoder(w).Encode(map[string]bool{"wizard_done": f.done})
	case "POST /v1/wizard":
		f.done = true
		_ = json.NewEncoder(w).Encode(map[string]bool{"wizard_done": true})
	case "GET /v1/status":
		_ = json.NewEncoder(w).Encode(map[string]any{"hostname": "pocket", "timezone": f.timezone})
	case "PUT /v1/timezone":
		if r.ContentLength <= 0 {
			http.Error(w, `{"error":"no Content-Length"}`, http.StatusLengthRequired)
			return
		}
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		if in["timezone"] == "Mars/Olympus" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown time zone"})
			return
		}
		f.timezone = in["timezone"]
		_ = json.NewEncoder(w).Encode(map[string]string{"timezone": f.timezone})
	default:
		http.NotFound(w, r)
	}
}

func startFakeAgent(t *testing.T) (*fakeAgent, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAgent{timezone: "UTC"}
	srv := &http.Server{Handler: f}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f, sock
}

func TestBoxSystemThroughAgent(t *testing.T) {
	agent, sock := startFakeAgent(t)
	t.Setenv("BOX_AGENT", sock)
	e := newEnv(t)

	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_system"] != true || st["box_wizard"] != false {
		t.Fatalf("before setup: %v / %v", st["box_system"], st["box_wizard"])
	}
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_wizard"] != true {
		t.Fatalf("after setup the wizard should be pending: %v", st["box_wizard"])
	}

	var status map[string]any
	if c := e.do("GET", "/api/v1/box/system/status", "", nil, &status); c != 200 || status["hostname"] != "pocket" {
		t.Fatalf("status %d %v", c, status)
	}
	var tz map[string]string
	if c := e.do("PUT", "/api/v1/box/system/timezone", "", map[string]string{"timezone": "Europe/Sarajevo"}, &tz); c != 200 || tz["timezone"] != "Europe/Sarajevo" {
		t.Fatalf("timezone %d %v", c, tz)
	}
	var bad map[string]string
	if c := e.do("PUT", "/api/v1/box/system/timezone", "", map[string]string{"timezone": "Mars/Olympus"}, &bad); c != 400 || bad["error"] != "unknown time zone" {
		t.Fatalf("the agent's 400 must come through: %d %v", c, bad)
	}
	if c := e.do("GET", "/api/v1/box/system/nonsense", "", nil, nil); c != 404 {
		t.Fatalf("unknown route %d", c)
	}

	if c := e.do("POST", "/api/v1/box/system/wizard", "", nil, nil); c != 200 {
		t.Fatalf("wizard done %d", c)
	}
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_wizard"] != false {
		t.Fatalf("after the wizard: %v", st["box_wizard"])
	}

	// A member may not touch the box.
	var member map[string]any
	if c := e.do("POST", "/api/v1/users", "", map[string]any{"email": "kid@example.com", "name": "Kid", "password": "password123", "role": "member"}, &member); c != 201 && c != 200 {
		t.Fatalf("create member %d", c)
	}
	e.cj = nil
	if c := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": "kid@example.com", "password": "password123"}, nil); c != 200 {
		t.Fatalf("member login %d", c)
	}
	agent.mu.Lock()
	before := len(agent.calls)
	agent.mu.Unlock()
	if c := e.do("PUT", "/api/v1/box/system/timezone", "", map[string]string{"timezone": "UTC"}, nil); c != 403 {
		t.Fatalf("member changed the time zone: %d", c)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.calls) != before || agent.timezone != "Europe/Sarajevo" {
		t.Fatalf("the agent was called for a member: %v", agent.calls[before:])
	}
}

func TestBoxSystemOffABox(t *testing.T) {
	e := newEnv(t)
	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_system"] != false || st["box_wizard"] != false {
		t.Fatalf("status %v / %v", st["box_system"], st["box_wizard"])
	}
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
	if c := e.do("GET", "/api/v1/box/system/status", "", nil, nil); c != 404 {
		t.Fatalf("no agent: %d", c)
	}
}

func TestBoxWizardIgnoresDeadAgent(t *testing.T) {
	t.Setenv("BOX_AGENT", filepath.Join(t.TempDir(), "nobody.sock"))
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_wizard"] != false {
		t.Fatal("an agent that does not answer must not trap the owner in the wizard")
	}
	if c := e.do("GET", "/api/v1/box/system/status", "", nil, nil); c != 502 {
		t.Fatalf("dead agent: %d", c)
	}
}

func TestBoxAddressThroughService(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "address.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/address":
			_ = json.NewEncoder(w).Encode(map[string]any{"mode": "pocket", "can_choose": true})
		case "PUT /v1/address":
			if r.ContentLength <= 0 {
				http.Error(w, `{"error":"no Content-Length"}`, http.StatusLengthRequired)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"mode": "own-ca"})
		case "POST /v1/address/cloudflare/zones":
			w.WriteHeader(422)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Cloudflare didn't accept this sign-in."})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("BOX_ADDRESS", sock)
	e := newEnv(t)
	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_address"] != true {
		t.Fatalf("box_address %v", st["box_address"])
	}
	if c := e.do("GET", "/api/v1/box/address", "", nil, nil); c != 401 {
		t.Fatalf("signed out: %d", c)
	}
	var setup map[string]any
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, &setup); c != 200 {
		t.Fatalf("setup %d %v", c, setup)
	}
	var got map[string]any
	if c := e.do("GET", "/api/v1/box/address", "", nil, &got); c != 200 || got["mode"] != "pocket" {
		t.Fatalf("get %d %v", c, got)
	}
	if c := e.do("PUT", "/api/v1/box/address", "", map[string]string{"mode": "own-ca"}, &got); c != 200 || got["mode"] != "own-ca" {
		t.Fatalf("put %d %v", c, got)
	}
	var bad map[string]string
	if c := e.do("POST", "/api/v1/box/address/cloudflare/zones", "", map[string]string{"token": "x"}, &bad); c != 422 || bad["error"] == "" {
		t.Fatalf("the service's 422 must come through: %d %v", c, bad)
	}
	if c := e.do("DELETE", "/api/v1/box/address", "", nil, nil); c != 404 {
		t.Fatalf("unknown route %d", c)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("calls %v", calls)
	}
}

func TestBoxDownloadsThroughService(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "downloads.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method != http.MethodGet && r.Method != http.MethodDelete && r.ContentLength <= 0 {
			http.Error(w, `{"error":"no Content-Length"}`, http.StatusLengthRequired)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/downloads", "DELETE /v1/downloads/usenet", "DELETE /v1/downloads/indexers/3", "PUT /v1/downloads/subtitles":
			_ = json.NewEncoder(w).Encode(map[string]any{"installed": true, "ready": false})
		case "PUT /v1/downloads/usenet":
			// A failed test is still a 200, with the reason.
			_ = json.NewEncoder(w).Encode(map[string]any{"installed": true, "usenet": map[string]any{"status": "error", "message": "The provider turned down the username or password."}})
		case "POST /v1/downloads/indexers":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Paste the indexer's API key."})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("BOX_DOWNLOADS", sock)
	e := newEnv(t)
	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_downloads"] != true {
		t.Fatalf("box_downloads %v", st["box_downloads"])
	}
	if c := e.do("GET", "/api/v1/box/downloads", "", nil, nil); c != 401 {
		t.Fatalf("signed out: %d", c)
	}
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var got map[string]any
	if c := e.do("GET", "/api/v1/box/downloads", "", nil, &got); c != 200 || got["installed"] != true {
		t.Fatalf("get %d %v", c, got)
	}
	if c := e.do("PUT", "/api/v1/box/downloads/usenet", "", map[string]any{"host": "news.eweka.nl", "port": 563, "ssl": true, "username": "u", "password": "p", "connections": 20}, &got); c != 200 || got["usenet"].(map[string]any)["status"] != "error" {
		t.Fatalf("usenet %d %v", c, got)
	}
	var bad map[string]string
	if c := e.do("POST", "/api/v1/box/downloads/indexers", "", map[string]string{"preset": "nzbgeek"}, &bad); c != 400 || bad["error"] == "" {
		t.Fatalf("the service's 400 must come through: %d %v", c, bad)
	}
	if c := e.do("DELETE", "/api/v1/box/downloads/indexers/3", "", nil, nil); c != 200 {
		t.Fatalf("remove indexer %d", c)
	}
	if c := e.do("DELETE", "/api/v1/box/downloads/usenet", "", nil, nil); c != 200 {
		t.Fatalf("remove usenet %d", c)
	}
	if c := e.do("PUT", "/api/v1/box/downloads/subtitles", "", map[string]any{"languages": []string{"en", "sr"}}, nil); c != 200 {
		t.Fatalf("subtitles %d", c)
	}
	// Not on the list: never reaches the service.
	for _, p := range []string{"DELETE /api/v1/box/downloads/indexers/x", "DELETE /api/v1/box/downloads/indexers/3/..", "GET /api/v1/box/downloads/indexers/3", "DELETE /api/v1/box/downloads", "GET /api/v1/box/downloads/nonsense"} {
		m, path, _ := strings.Cut(p, " ")
		if c := e.do(m, path, "", nil, nil); c != 404 {
			t.Fatalf("%s: %d", p, c)
		}
	}

	// A member may not touch it.
	if c := e.do("POST", "/api/v1/users", "", map[string]any{"email": "kid@example.com", "name": "Kid", "password": "password123", "role": "member"}, nil); c != 201 && c != 200 {
		t.Fatalf("create member %d", c)
	}
	e.cj = nil
	if c := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": "kid@example.com", "password": "password123"}, nil); c != 200 {
		t.Fatalf("member login %d", c)
	}
	if c := e.do("GET", "/api/v1/box/downloads", "", nil, nil); c != 403 {
		t.Fatalf("member read downloads: %d", c)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 6 {
		t.Fatalf("calls %v", calls)
	}
}

func TestBoxDownloadsOffABox(t *testing.T) {
	e := newEnv(t)
	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["box_downloads"] != false {
		t.Fatalf("box_downloads %v", st["box_downloads"])
	}
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
	if c := e.do("GET", "/api/v1/box/downloads", "", nil, nil); c != 404 {
		t.Fatalf("no service: %d", c)
	}
}
