package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
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
