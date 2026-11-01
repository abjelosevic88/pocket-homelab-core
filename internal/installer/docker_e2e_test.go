package installer

// End-to-end against the real Docker engine: a real release image, a
// throwaway Caddy container, a fake GitHub and a fake Core. Nothing of the
// server's own apps, Caddy or Core is touched. Run with
// POCKET_INSTALLER_DOCKER_TEST=1 (and `env -i PATH=$PATH HOME=$HOME`).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDockerEndToEnd(t *testing.T) {
	if os.Getenv("POCKET_INSTALLER_DOCKER_TEST") != "1" {
		t.Skip("set POCKET_INSTALLER_DOCKER_TEST=1 to run against Docker")
	}
	const (
		caddyName = "pci-e2e-caddy"
		ctrName   = "pocket-e2e-habits"
		caddyImg  = "ghcr.io/caddybuilds/caddy-cloudflare:latest"
	)
	dir, _ := os.MkdirTemp("", "pcie2e")
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", caddyName, ctrName).Run()
		os.RemoveAll(dir)
	})

	// Fake GitHub: one repo whose compose names a container nobody uses.
	compose := strings.ReplaceAll(string(fixture(t, "habits.yml")), "container_name: pocket-habits", "container_name: "+ctrName)
	gh := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/users/"):
			json.NewEncoder(w).Encode([]repo{{Name: "pocket-homelab-habit-tracker"}})
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			io.WriteString(w, "003faaaa refs/tags/v0.4.5\n0000")
		case strings.HasSuffix(r.URL.Path, "/docker-compose.yml"):
			io.WriteString(w, compose)
		default:
			http.NotFound(w, r)
		}
	})}
	ghL, _ := net.Listen("tcp", "127.0.0.1:0")
	go gh.Serve(ghL)
	t.Cleanup(func() { gh.Close() })
	ghURL := "http://" + ghL.Addr().String()

	// Fake Core on the docker bridge, so the container can register with it.
	var mu sync.Mutex
	var regs []map[string]any
	core := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/apps/register" {
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			mu.Lock()
			regs = append(regs, m)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"slug": m["slug"]}, "token": "pca_e2e", "core": map[string]any{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})}
	coreL, err := net.Listen("tcp", "172.17.0.1:0")
	if err != nil {
		t.Skip("no docker0 bridge address: ", err)
	}
	go core.Serve(coreL)
	t.Cleanup(func() { core.Close() })
	corePort := coreL.Addr().(*net.TCPAddr).Port

	// Throwaway Caddy with a single-file bind mount, like the real one.
	caddyfile := filepath.Join(dir, "Caddyfile")
	os.WriteFile(caddyfile, []byte("{\n\tauto_https off\n\tadmin localhost:2019\n}\n\n:80 {\n\trespond \"fallback\"\n}\n"), 0o644)
	if out, err := exec.Command("docker", "run", "-d", "--name", caddyName, "--add-host", "host.docker.internal:host-gateway",
		"-v", caddyfile+":/etc/caddy/Caddyfile:ro", "-p", "127.0.0.1:18480:80", caddyImg).CombinedOutput(); err != nil {
		t.Fatalf("caddy: %s", out)
	}

	cfg := &Config{Token: strings.Repeat("t", 32), Owner: "abjelosevic88", RepoPrefix: "pocket-homelab-",
		AppsDir: filepath.Join(dir, "apps"), ArchiveDir: filepath.Join(dir, "archive"), PublicURL: "http://{slug}.e2e.test",
		CoreURL: "http://host.docker.internal:" + strconv.Itoa(corePort), PortFrom: 18300, PortTo: 18320, TZ: "Europe/Sarajevo",
		Caddyfile: caddyfile, CaddyContainer: caddyName, CaddyPath: "/etc/caddy/Caddyfile", CaddyUpstream: "host.docker.internal",
		Protected: []string{"core"}}
	os.MkdirAll(cfg.AppsDir, 0o755)
	a := NewAgent(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.gh.api, a.gh.git, a.gh.raw = ghURL, ghURL, ghURL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	j := &Job{}
	if err := a.install(ctx, j, InstallRequest{Slug: "e2e-habits", Name: "Pocket Habits (e2e)", RegistrationKey: "e2e-key", AdminEmail: "e2e@example.test", AdminName: "E2E"}); err != nil {
		t.Fatalf("install: %v\n%s", err, strings.Join(j.Log, "\n"))
	}
	t.Logf("install log:\n%s", strings.Join(j.Log, "\n"))
	res := j.Result
	if res.Port < 18300 || res.Container != ctrName {
		t.Fatalf("result %+v", res)
	}
	envf, _ := os.ReadFile(filepath.Join(res.Dir, ".env"))
	if !strings.Contains(string(envf), "CORE_REGISTRATION_KEY=e2e-key") {
		t.Fatalf(".env:\n%s", envf)
	}

	// The app registered itself with "Core" on its first start.
	var reg map[string]any
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline) && reg == nil; time.Sleep(time.Second) {
		mu.Lock()
		if len(regs) > 0 {
			reg = regs[0]
		}
		mu.Unlock()
	}
	if reg == nil || reg["slug"] != "habits" || reg["url"] != res.AppURL || reg["public_url"] != "http://e2e-habits.e2e.test" {
		t.Fatalf("registration %v (want url %s)", reg, res.AppURL)
	}

	// Caddy serves the new site.
	get := func() (int, string) {
		req, _ := http.NewRequest("GET", "http://127.0.0.1:18480/healthz", nil)
		req.Host = "e2e-habits.e2e.test"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := get(); code != 200 || strings.Contains(body, "fallback") {
		t.Fatalf("through caddy: %d %s", code, body)
	}

	// Remove: containers gone, folder archived, site gone.
	j2 := &Job{}
	if err := a.uninstall(ctx, j2, UninstallRequest{Slug: "e2e-habits", URL: res.AppURL, PublicURL: "http://e2e-habits.e2e.test"}); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, strings.Join(j2.Log, "\n"))
	}
	t.Logf("uninstall log:\n%s\nwarnings: %v", strings.Join(j2.Log, "\n"), j2.Result.Warnings)
	if exec.Command("docker", "container", "inspect", ctrName).Run() == nil {
		t.Fatal("container still exists")
	}
	if _, err := os.Stat(res.Dir); err == nil {
		t.Fatal("folder still in place")
	}
	if _, err := os.Stat(filepath.Join(j2.Result.Archive, "data")); err != nil {
		t.Fatalf("archive lacks the data: %v", err)
	}
	if b, _ := os.ReadFile(caddyfile); strings.Contains(string(b), "e2e-habits") {
		t.Fatalf("site left in the Caddyfile:\n%s", b)
	}
	if code, body := get(); code == 200 && !strings.Contains(body, "fallback") {
		t.Fatalf("caddy still proxies the removed app: %d %s", code, body)
	}
}
