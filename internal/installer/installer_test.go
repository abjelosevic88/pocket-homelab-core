package installer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ports(from int) func() (int, error) {
	n := from - 1
	return func() (int, error) { n++; return n, nil }
}

func plan(t *testing.T, file, repo, slug string) (*Plan, map[string]any) {
	t.Helper()
	rel := &Release{Slug: slug, Repo: repo, RepoURL: "https://github.com/abjelosevic88/" + repo, Version: "1.2.3", Tag: "v1.2.3",
		Image: "ghcr.io/abjelosevic88/" + repo, Container: "pocket-" + slug, compose: fixture(t, file), envExample: []byte("TZ=Europe/Berlin\nBASE_URL=x\n")}
	p, err := BuildPlan(PlanInput{Release: rel, PublicURL: "https://" + slug + ".example.org", CoreURL: "http://host.docker.internal:8179",
		RegistrationKey: "reg-key", AdminEmail: "me@example.org", AdminName: "Me", TZ: "Europe/Sarajevo", Zoneinfo: true,
		NextPort: ports(8300), DockerGID: "108", Now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(p.Compose, &doc); err != nil {
		t.Fatalf("output is not yaml: %v\n%s", err, p.Compose)
	}
	return p, doc
}

func svc(doc map[string]any, name string) map[string]any {
	return doc["services"].(map[string]any)[name].(map[string]any)
}

func TestPlanBridged(t *testing.T) {
	p, doc := plan(t, "habits.yml", "pocket-homelab-habit-tracker", "habits")
	app := svc(doc, "app")
	if app["image"] != "ghcr.io/abjelosevic88/pocket-homelab-habit-tracker:1.2.3" {
		t.Fatalf("image = %v", app["image"])
	}
	if got := app["ports"].([]any); len(got) != 1 || got[0] != "8300:8080" {
		t.Fatalf("ports = %v", got)
	}
	env := app["environment"].(map[string]any)
	want := map[string]string{"BASE_URL": "https://habits.example.org", "SECURE_COOKIES": "true", "CORE_URL": "http://host.docker.internal:8179",
		"CORE_APP_URL": "http://host.docker.internal:8300", "CORE_REGISTRATION_KEY": "${CORE_REGISTRATION_KEY}", "TZ": "Europe/Sarajevo"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %v, want %s", k, env[k], v)
		}
	}
	if !strings.Contains(strings.Join(toStrings(app["extra_hosts"]), ","), "host.docker.internal:host-gateway") {
		t.Errorf("extra_hosts = %v", app["extra_hosts"])
	}
	if !strings.Contains(strings.Join(toStrings(app["volumes"]), ","), "/usr/share/zoneinfo:/usr/share/zoneinfo:ro") {
		t.Errorf("volumes = %v", app["volumes"])
	}
	if p.Port != 8300 || p.AppURL != "http://host.docker.internal:8300" || len(p.Dirs) != 1 || p.Dirs[0] != "data" {
		t.Errorf("plan = %+v", p)
	}
	envf := string(p.envFile())
	for _, s := range []string{"ADMIN_EMAIL=me@example.org", "CORE_REGISTRATION_KEY=reg-key", "TZ=Europe/Sarajevo"} {
		if !strings.Contains(envf, s+"\n") {
			t.Errorf(".env lacks %s:\n%s", s, envf)
		}
	}
	if strings.Contains(envf, "BASE_URL=x") {
		t.Errorf(".env copied the sample's example value:\n%s", envf)
	}
	if !strings.HasPrefix(string(p.Compose), "# Pocket Habits — installed by Pocket Core on 2026-11-01") {
		t.Errorf("header: %s", strings.SplitN(string(p.Compose), "\n", 2)[0])
	}
}

func TestPlanSidecarSecrets(t *testing.T) {
	p, doc := plan(t, "search.yml", "pocket-homelab-search", "search")
	if svc(doc, "engine")["image"] != "searxng/searxng:latest" {
		t.Fatal("the sidecar's image must stay")
	}
	if svc(doc, "pocket-search")["image"] != "ghcr.io/abjelosevic88/pocket-homelab-search:1.2.3" {
		t.Fatal("main image not pinned")
	}
	if _, ok := doc["configs"]; !ok {
		t.Fatal("top-level configs lost")
	}
	envf := string(p.envFile())
	if !strings.Contains(envf, "ENGINE_SECRET=") || strings.Contains(envf, "pocket-search-private-engine") {
		t.Fatalf("ENGINE_SECRET must get a fresh value:\n%s", envf)
	}
}

func TestPlanDefaultedBindDirs(t *testing.T) {
	// Pocket Watch brings Jellyfin along; its folders are ${VAR:-./default}
	p, doc := plan(t, "watch.yml", "pocket-homelab-watch", "watch")
	if svc(doc, "jellyfin")["image"] != "jellyfin/jellyfin:latest" {
		t.Fatal("the sidecar's image must stay")
	}
	want := []string{"data", "jellyfin/config", "jellyfin/cache", "media/movies", "media/shows"}
	if strings.Join(p.Dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("dirs = %v, want %v (Docker would make the defaulted ones as root)", p.Dirs, want)
	}
	if strings.Contains(string(p.envFile()), "COMPOSE_PROFILES") {
		t.Fatal("the sample .env sets no profiles here")
	}
}

func TestPlanComposeProfiles(t *testing.T) {
	// the sample .env switches the bundled Jellyfin on: an install gets it too
	rel := &Release{Slug: "watch", Repo: "pocket-homelab-watch", RepoURL: "https://github.com/abjelosevic88/pocket-homelab-watch", Version: "1.2.3", Tag: "v1.2.3",
		Image: "ghcr.io/abjelosevic88/pocket-homelab-watch", Container: "pocket-watch", compose: fixture(t, "watch.yml"),
		envExample: []byte("TZ=Europe/Berlin\n# MOVIES_DIR=/srv/media/movies\nCOMPOSE_PROFILES=jellyfin\n")}
	p, err := BuildPlan(PlanInput{Release: rel, PublicURL: "https://watch.example.org", CoreURL: "http://host.docker.internal:8179",
		AdminEmail: "me@example.org", TZ: "Europe/Sarajevo", NextPort: ports(8300), Now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	envf := string(p.envFile())
	if !strings.Contains(envf, "\nCOMPOSE_PROFILES=jellyfin\n") || strings.Contains(envf, "MOVIES_DIR") {
		t.Fatalf(".env:\n%s", envf)
	}
}

func TestPlanTwoPorts(t *testing.T) {
	p, doc := plan(t, "files.yml", "pocket-homelab-files", "files")
	got := toStrings(svc(doc, "pocket-files")["ports"])
	if len(got) != 2 || got[0] != "8300:8080" || got[1] != "8301:8081" || p.Port != 8300 {
		t.Fatalf("ports = %v, port %d", got, p.Port)
	}
	if !strings.Contains(string(p.Compose), "# share and upload pages only") {
		t.Fatal("port comment lost")
	}
}

func TestPlanHostNetwork(t *testing.T) {
	p, doc := plan(t, "database.yml", "pocket-homelab-database", "database")
	app := svc(doc, "pocket-database")
	env := app["environment"].(map[string]any)
	if env["PORT"] != "8300" || env["CORE_URL"] != "http://127.0.0.1:8179" || env["CORE_APP_URL"] != "http://host.docker.internal:8300" {
		t.Fatalf("env = %v", env)
	}
	if _, ok := app["extra_hosts"]; ok {
		t.Fatal("host networking takes no extra_hosts")
	}
	if !strings.Contains(string(p.envFile()), "DOCKER_GID=108\n") {
		t.Fatalf(".env:\n%s", p.envFile())
	}
	if len(p.Notes) == 0 {
		t.Fatal("the /srv and docker.sock mounts should be noted")
	}
}

func TestPlanRejectsForeignCompose(t *testing.T) {
	rel := &Release{Slug: "x", Image: "ghcr.io/abjelosevic88/other", compose: fixture(t, "habits.yml")}
	if _, err := BuildPlan(PlanInput{Release: rel, NextPort: ports(8300)}); err == nil {
		t.Fatal("a compose file that runs another image must be refused")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestHighestTag(t *testing.T) {
	refs := "001e# service=git-upload-pack\n0000" +
		"0050aaaa HEAD\x00multi_ack\n" +
		"003faaaa refs/tags/v0.9.0\n" +
		"003faaaa refs/tags/v0.10.0\n" +
		"0042aaaa refs/tags/v0.10.0^{}\n" +
		"003faaaa refs/tags/v0.10.1-rc1\n" +
		"003faaaa refs/tags/v0.2.11\n0000"
	got, err := highestTag(refs)
	if err != nil || got != "v0.10.0" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := highestTag("0000"); err == nil {
		t.Fatal("no tags must be an error")
	}
}

const caddyfile = `{
	email me@example.org
}

(tls_cf) {
	tls {
		dns cloudflare {env.CF_API_TOKEN}
	}
}

# Pocket Search — a comment
# spanning two lines
search.example.org {
	import tls_cf
	reverse_proxy host.docker.internal:8225 {
		flush_interval -1
	}
}

# Habits
habits.example.org {
	import tls_cf
	@x path /api/*
	handle @x {
		header X-Brace "{not a block"
		reverse_proxy host.docker.internal:8169
	}
	reverse_proxy host.docker.internal:8169
}

old.example.org, requests.example.org {
	reverse_proxy host.docker.internal:8210
}
`

func TestCaddyRemove(t *testing.T) {
	out, msg, err := RemoveSite(caddyfile, "habits.example.org")
	if err != nil || !strings.Contains(msg, "removed the habits") {
		t.Fatalf("%q %v", msg, err)
	}
	if strings.Contains(out, "habits") || strings.Contains(out, "8169") || strings.Contains(out, "# Habits") {
		t.Fatalf("block left behind:\n%s", out)
	}
	if !strings.Contains(out, "search.example.org {") || !strings.Contains(out, "old.example.org, requests.example.org {") || !strings.Contains(out, "(tls_cf) {") {
		t.Fatalf("removed too much:\n%s", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("left a gap:\n%q", out)
	}

	out, _, _ = RemoveSite(caddyfile, "search.example.org")
	if strings.Contains(out, "spanning two lines") || strings.Contains(out, "8225") {
		t.Fatalf("comment or block left:\n%s", out)
	}

	out, msg, _ = RemoveSite(caddyfile, "requests.example.org")
	if !strings.Contains(out, "\nold.example.org {\n") || !strings.Contains(out, "8210") || !strings.Contains(msg, "shared") {
		t.Fatalf("shared site handled wrong (%s):\n%s", msg, out)
	}

	if out, msg, _ := RemoveSite(caddyfile, "nothing.example.org"); out != caddyfile || msg != "" {
		t.Fatal("unknown host must change nothing")
	}
}

func TestCaddyAdd(t *testing.T) {
	if !HasSite(caddyfile, "requests.example.org") || HasSite(caddyfile, "tls_cf") || HasSite(caddyfile, "lookout.example.org") {
		t.Fatal("HasSite")
	}
	out := AddSite(caddyfile, "Pocket Lookout — installed", "lookout.example.org", []string{"tls_cf"}, "host.docker.internal:8300")
	if !strings.HasSuffix(out, "\n\n# Pocket Lookout — installed\nlookout.example.org {\n\timport tls_cf\n\treverse_proxy host.docker.internal:8300 {\n\t\tflush_interval -1\n\t}\n}\n") {
		t.Fatalf("got:\n%s", out)
	}
	back, _, _ := RemoveSite(out, "lookout.example.org")
	if back != caddyfile {
		t.Fatalf("add then remove must round-trip:\n%q\n%q", back, caddyfile)
	}
}

func TestAgentAuthAndCatalog(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/users/"):
			json.NewEncoder(w).Encode([]repo{{Name: "pocket-homelab-habit-tracker", Description: "Habits"}, {Name: "pocket-homelab-core"}, {Name: "pocket-homelab-icons"}, {Name: "other"}})
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			w.Write([]byte("003faaaa refs/tags/v0.4.5\n0000"))
		case r.URL.Path == "/abjelosevic88/pocket-homelab-habit-tracker/v0.4.5/docker-compose.yml":
			w.Write(fixture(t, "habits.yml"))
		case r.URL.Path == "/abjelosevic88/pocket-homelab-core/v0.4.5/docker-compose.yml":
			w.Write([]byte("services:\n  core:\n    image: ghcr.io/abjelosevic88/pocket-homelab-core:latest\n    container_name: pocket-core\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	cfg := &Config{Token: strings.Repeat("t", 32), Owner: "abjelosevic88", RepoPrefix: "pocket-homelab-", Protected: []string{"core"}}
	a := NewAgent(cfg, nil)
	g := a.src.(*GitHub)
	g.api, g.git, g.raw = gh.URL, gh.URL, gh.URL
	a.dk.Bin = "/nonexistent-docker"
	h := a.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/catalog", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/v1/catalog", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out struct{ Apps []Release }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Apps) != 1 || out.Apps[0].Slug != "habits" || out.Apps[0].Version != "0.4.5" || out.Apps[0].Description != "Habits" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest("POST", "/v1/uninstall", strings.NewReader(`{"slug":"core"}`))
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("removing core: %d", rec.Code)
	}
}
