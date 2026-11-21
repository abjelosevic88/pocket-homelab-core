package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/logring"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// An agent that restarts, reads logs and reports resources, like installer/ops.go.
func startOpsAgent(t *testing.T, e *env) (restarts *[]installer.RestartRequest, logLines *atomic.Value) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pco")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	jobs := map[string]*installer.Job{}
	restarts = &[]installer.RestartRequest{}
	logLines = &atomic.Value{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/restart", func(w http.ResponseWriter, r *http.Request) {
		var req installer.RestartRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Slug == "ai" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "no container on this server runs Pocket AI"})
			return
		}
		mu.Lock()
		*restarts = append(*restarts, req)
		j := &installer.Job{ID: "r1", Kind: "restart", Slug: req.Slug, State: "done", Log: []string{"Restarting pocket-" + req.Slug}}
		jobs[j.ID] = j
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(j)
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(jobs[r.PathValue("id")])
	})
	mux.HandleFunc("GET /v1/logs", func(w http.ResponseWriter, r *http.Request) {
		logLines.Store(r.URL.Query().Get("lines"))
		json.NewEncoder(w).Encode(installer.Logs{Source: "docker compose logs (pocket-" + r.URL.Query().Get("slug") + ")",
			Lines: []string{"2026-11-21T10:00:00Z started", "2026-11-21T10:00:01Z ERROR something"}})
	})
	mux.HandleFunc("GET /v1/resources", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(installer.Resources{Disk: &installer.DiskUsage{Path: "/apps", Total: 100 << 30, Free: 40 << 30},
			Apps: []installer.AppUsage{{Slug: "lookout", Memory: 23 << 20}}})
	})
	const token = "test-installer-token-0123456789"
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"error":"token rejected"}`, http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	e.s.inst = installer.NewClient(sock, token)
	return restarts, logLines
}

func TestAppOps(t *testing.T) {
	e := newEnv(t)
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@b.c", "password": "password123", "name": "Owner", "box_name": "Box"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	// Without an agent: no restart, no logs (but Core's own log always).
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) }))
	defer app.Close()
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "lookout", "name": "Pocket Lookout", "url": app.URL}, nil); c != 201 {
		t.Fatalf("register %d", c)
	}
	if c := e.do("POST", "/api/v1/apps/lookout/restart", "", nil, nil); c != 404 {
		t.Fatalf("restart without an agent: %d", c)
	}
	fmt.Fprintln(logring.Default, `level=INFO msg="hello from core"`)
	var logs installer.Logs
	if c := e.do("GET", "/api/v1/apps/core/logs?lines=50", "", nil, &logs); c != 200 || len(logs.Lines) == 0 || !strings.Contains(logs.Lines[len(logs.Lines)-1], "hello from core") {
		t.Fatalf("core logs %d %+v", c, logs)
	}

	restarts, logLines := startOpsAgent(t, e)
	var j map[string]any
	if c := e.do("POST", "/api/v1/apps/lookout/restart", "", nil, &j); c != 202 || j["kind"] != "restart" {
		t.Fatalf("restart %d %v", c, j)
	}
	j = waitJob(t, e, j["id"].(string))
	if j["state"] != "done" || !strings.Contains(strings.Join(toStrs(j["log"]), "\n"), "Pocket Lookout is back") {
		t.Fatalf("restart job %v", j)
	}
	if len(*restarts) != 1 || (*restarts)[0].URL != app.URL {
		t.Fatalf("agent got %+v", *restarts)
	}
	if _, busy := e.s.removing.Load("lookout"); busy {
		t.Fatal("health checks must resume after the restart")
	}
	if c := e.do("POST", "/api/v1/apps/core/restart", "", nil, nil); c != 403 {
		t.Fatalf("core restart: %d", c)
	}

	logs = installer.Logs{}
	if c := e.do("GET", "/api/v1/apps/lookout/logs?lines=99999", "", nil, &logs); c != 200 || len(logs.Lines) != 2 || logs.Slug != "lookout" {
		t.Fatalf("logs %d %+v", c, logs)
	}
	if logLines.Load() != "2000" {
		t.Fatalf("lines must be capped at 2000, agent got %v", logLines.Load())
	}

	var sys struct {
		Version string `json:"version"`
		Host    *struct {
			CPUs   int               `json:"cpus"`
			Memory map[string]uint64 `json:"memory"`
			Load   []float64         `json:"load"`
			Disks  []struct {
				Name  string `json:"name"`
				Total uint64 `json:"total_bytes"`
				Free  uint64 `json:"free_bytes"`
			} `json:"disks"`
			Apps []installer.AppUsage `json:"apps"`
		} `json:"host"`
	}
	if c := e.do("GET", "/api/v1/system", "", nil, &sys); c != 200 || sys.Version == "" || sys.Host == nil {
		t.Fatalf("system %d %+v", c, sys)
	}
	h := sys.Host
	if h.CPUs < 1 || len(h.Disks) != 2 || h.Disks[0].Name != "data" || h.Disks[0].Total == 0 || h.Disks[1].Name != "apps" || h.Disks[1].Free != 40<<30 {
		t.Fatalf("host %+v", h)
	}
	if len(h.Apps) != 1 || h.Apps[0].Memory != 23<<20 {
		t.Fatalf("apps %+v", h.Apps)
	}
	if _, err := os.Stat("/proc/meminfo"); err == nil && (h.Memory["total_bytes"] == 0 || len(h.Load) != 3) {
		t.Fatalf("memory %v load %v", h.Memory, h.Load)
	}
}

func TestReadMeminfo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "meminfo")
	os.WriteFile(p, []byte("MemTotal:        3884096 kB\nMemFree:          200000 kB\nMemAvailable:    1942048 kB\nBuffers: 1 kB\nSwapTotal:       1048572 kB\nSwapFree:         524286 kB\n"), 0o644)
	m := readMeminfo(p)
	if m["total_bytes"] != 3884096*1024 || m["available_bytes"] != 1942048*1024 || m["swap_free_bytes"] != 524286*1024 {
		t.Fatalf("%v", m)
	}
	os.WriteFile(p, []byte("0.52 0.58 0.59 1/389 12345\n"), 0o644)
	if l := readLoadavg(p); len(l) != 3 || l[0] != 0.52 || l[2] != 0.59 {
		t.Fatalf("%v", l)
	}
}
