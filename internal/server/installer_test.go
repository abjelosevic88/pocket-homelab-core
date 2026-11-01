package server

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// fakeInstaller answers like the installer agent; "installing" registers the app
// with Core the way its first start would.
type fakeInstaller struct {
	mu      sync.Mutex
	jobs    map[string]*installer.Job
	removed []installer.UninstallRequest
	gotReq  installer.InstallRequest
}

func startFakeInstaller(t *testing.T, e *env) *fakeInstaller {
	t.Helper()
	dir, err := os.MkdirTemp("", "pci") // unix socket paths must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeInstaller{jobs: map[string]*installer.Job{}}
	const token = "test-installer-token-0123456789"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(installer.Status{Owner: "abjelosevic88", Proxy: true})
	})
	mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"apps": []installer.Release{{Slug: "habits", Version: "0.4.5"}, {Slug: "lookout", Version: "0.2.0"}}})
	})
	mux.HandleFunc("POST /v1/install", func(w http.ResponseWriter, r *http.Request) {
		var req installer.InstallRequest
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.gotReq = req
		j := &installer.Job{ID: "j1", Kind: "install", Slug: req.Slug, State: "done", Log: []string{"Starting pocket-" + req.Slug},
			Result: &installer.Result{Container: "pocket-" + req.Slug, Port: 8300}}
		f.jobs[j.ID] = j
		f.mu.Unlock()
		m := map[string]any{"slug": req.Slug, "name": "Pocket Lookout", "url": "http://host.docker.internal:8300", "public_url": "https://lookout.test",
			"oidc": map[string]any{"redirect_uris": []string{"https://lookout.test/api/v1/auth/oidc/callback"}}}
		if c := e.do("POST", "/api/v1/apps/register", req.RegistrationKey, m, nil); c != 201 {
			t.Errorf("simulated first start: register %d", c)
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(j)
	})
	mux.HandleFunc("POST /v1/uninstall", func(w http.ResponseWriter, r *http.Request) {
		var req installer.UninstallRequest
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.removed = append(f.removed, req)
		j := &installer.Job{ID: "j2", Kind: "uninstall", Slug: req.Slug, State: "done", Log: []string{"Stopping"}, Result: &installer.Result{Archive: "/x"}}
		f.jobs[j.ID] = j
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(j)
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.jobs[r.PathValue("id")])
	})
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
	return f
}

func waitJob(t *testing.T, e *env, id string) map[string]any {
	t.Helper()
	var j map[string]any
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		j = nil
		e.do("GET", "/api/v1/installer/jobs/"+id, "", nil, &j)
		if j["state"] != "running" {
			return j
		}
	}
	t.Fatalf("job %s still running: %v", id, j)
	return nil
}

func TestInstallAndRemove(t *testing.T) {
	e := newEnv(t)
	var st map[string]any
	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["installer"] == true {
		t.Fatal("no agent configured: no installer")
	}
	if c := e.do("GET", "/api/v1/installer", "", nil, nil); c != 401 && c != 403 {
		t.Fatalf("anonymous catalog: %d", c)
	}
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@b.c", "password": "password123", "name": "Owner", "box_name": "Box"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	if c := e.do("GET", "/api/v1/installer", "", nil, nil); c != 404 {
		t.Fatalf("catalog without agent: %d", c)
	}
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	f := startFakeInstaller(t, e)

	e.do("GET", "/api/v1/auth/status", "", nil, &st)
	if st["installer"] != true {
		t.Fatalf("admin with an agent should see the installer: %v", st)
	}
	var cat struct {
		Apps []map[string]any `json:"apps"`
	}
	if c := e.do("GET", "/api/v1/installer", "", nil, &cat); c != 200 || len(cat.Apps) != 2 || cat.Apps[0]["name"] != "Pocket Habits" {
		t.Fatalf("catalog %d %v", c, cat)
	}

	var j map[string]any
	if c := e.do("POST", "/api/v1/installer/install", "", map[string]string{"slug": "lookout"}, &j); c != 202 {
		t.Fatalf("install %d %v", c, j)
	}
	if f.gotReq.RegistrationKey != settings.RegistrationKey || f.gotReq.AdminEmail != "owner@b.c" || f.gotReq.Name == "" {
		t.Fatalf("agent got %+v", f.gotReq)
	}
	j = waitJob(t, e, j["id"].(string))
	log := strings.Join(toStrs(j["log"]), "\n")
	if j["state"] != "done" || !strings.Contains(log, "Registered with Pocket Core") || !strings.Contains(log, "Sign in with Pocket Core is set up") {
		t.Fatalf("install job %v", j)
	}
	if !strings.HasPrefix(j["open_url"].(string), "https://lookout.test") {
		t.Fatalf("open_url %v", j["open_url"])
	}
	if c := e.do("POST", "/api/v1/installer/install", "", map[string]string{"slug": "lookout"}, nil); c != 409 {
		t.Fatalf("second install of a registered app: %d", c)
	}

	if c := e.do("POST", "/api/v1/apps/core/uninstall", "", nil, nil); c != 404 && c != 403 {
		t.Fatalf("core uninstall: %d", c)
	}
	if c := e.do("POST", "/api/v1/apps/lookout/uninstall", "", nil, &j); c != 202 {
		t.Fatalf("uninstall %d %v", c, j)
	}
	j = waitJob(t, e, j["id"].(string))
	if j["state"] != "done" {
		t.Fatalf("uninstall job %v", j)
	}
	if len(f.removed) != 1 || f.removed[0].URL != "http://host.docker.internal:8300" || f.removed[0].PublicURL != "https://lookout.test" {
		t.Fatalf("agent got %+v", f.removed)
	}
	if _, err := e.st.GetAppBySlug(t.Context(), "lookout"); err == nil {
		t.Fatal("the app must be unregistered")
	}
	if _, err := e.st.GetOIDCClient(t.Context(), "pocket-lookout"); err == nil {
		t.Fatal("its sign-in client must go with it")
	}
}

func toStrs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
