package installer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
)

// fakeGitHub serves the repo list, tags, compose files and a ghcr.io
// registry for two apps: habits (multi-arch image) and lookout (no image
// for its newest tag yet).
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	index := `{"schemaVersion":2,"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}},{"platform":{"os":"unknown","architecture":"unknown"}}]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/users/"):
			json.NewEncoder(w).Encode([]repo{{Name: "pocket-homelab-habit-tracker", Description: "Habits"}, {Name: "pocket-homelab-lookout"}, {Name: "pocket-homelab-web"}})
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			w.Write([]byte("003faaaa refs/tags/v0.4.5\n003faaaa refs/tags/v0.4.10\n0000"))
		case strings.HasSuffix(r.URL.Path, "/pocket-homelab-habit-tracker/v0.4.10/docker-compose.yml"):
			w.Write(fixture(t, "habits.yml"))
		case strings.HasSuffix(r.URL.Path, "/pocket-homelab-lookout/v0.4.10/docker-compose.yml"):
			w.Write([]byte("services:\n  app:\n    image: ghcr.io/abjelosevic88/pocket-homelab-lookout:latest\n    container_name: pocket-lookout\n    network_mode: host\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n      - ${MEDIA_DIR:-/srv/media}:/media\n      - ./data:/data\n    devices:\n      - /dev/dri:/dev/dri\n  browser:\n    image: chromedp/headless-shell\n"))
		case r.URL.Path == "/token":
			w.Write([]byte(`{"token":"anon"}`))
		case r.URL.Path == "/v2/abjelosevic88/pocket-homelab-habit-tracker/manifests/0.4.10":
			if r.Header.Get("Authorization") != "Bearer anon" {
				http.Error(w, "no", 401)
				return
			}
			w.Write([]byte(index))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestBuildCatalog(t *testing.T) {
	gh := fakeGitHub(t)
	defer gh.Close()
	site := filepath.Join(t.TempDir(), "pocket-apps.json")
	os.WriteFile(site, []byte(`{"apps":[{"slug":"habits","name":"Pocket Habits","category":"life","summary":"Streaks.","icon":"/apps/icons/habits.svg"}]}`), 0o644)

	build := func(prev *appstore.Catalog) *appstore.Catalog {
		// BuildCatalog makes its own GitHub client; point it at the fake.
		old := newGitHubHook
		newGitHubHook = func(g *GitHub) { g.api, g.git, g.raw, g.registry = gh.URL, gh.URL, gh.URL, gh.URL }
		defer func() { newGitHubHook = old }()
		c, err := BuildCatalog(context.Background(), BuildOptions{Owner: "abjelosevic88", RepoPrefix: "pocket-homelab-",
			SiteMeta: site, SiteBase: "https://pockethomelab.com", Previous: prev})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := build(nil)
	if len(c.Apps) != 1 {
		t.Fatalf("lookout has no image yet and no previous entry: %+v", c.Apps)
	}
	h := c.Apps[0]
	if h.Slug != "habits" || h.Version != "0.4.10" || h.Name != "Pocket Habits" || h.Category != "life" || h.Description != "Streaks." ||
		h.Icon != "https://pockethomelab.com/apps/icons/habits.svg" || !strings.HasPrefix(h.Digest, "sha256:") ||
		strings.Join(h.Platforms, ",") != "linux/amd64,linux/arm64" || h.Compose == "" {
		t.Fatalf("habits entry: %+v", h)
	}

	prev := &appstore.Catalog{Apps: []appstore.App{{Slug: "lookout", Version: "0.4.5", Tag: "v0.4.5", Image: "ghcr.io/abjelosevic88/pocket-homelab-lookout"}}}
	c = build(prev)
	if l := c.Find("lookout"); l == nil || l.Version != "0.4.5" {
		t.Fatalf("lookout should keep its previous entry: %+v", c.Apps)
	}
}

func TestAnalyzeCompose(t *testing.T) {
	r := analyzeCompose([]byte("services:\n  app:\n    image: ghcr.io/me/app:1\n    network_mode: host\n    privileged: true\n    cap_add: [SYS_ADMIN]\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n      - ${MEDIA_DIR:-/srv/media}:/media\n      - ${DATA:-./data}:/data\n      - ./x:/x\n      - /usr/share/zoneinfo:/usr/share/zoneinfo:ro\n    devices:\n      - /dev/dri:/dev/dri\n  db:\n    image: postgres:17\n"), "ghcr.io/me/app")
	if !r.DockerSocket || !r.HostNetwork || !r.Privileged || strings.Join(r.Capabilities, ",") != "SYS_ADMIN" ||
		strings.Join(r.Devices, ",") != "/dev/dri" || strings.Join(r.HostFolders, ",") != "${MEDIA_DIR:-/srv/media}" ||
		strings.Join(r.Services, ",") != "db (postgres:17)" {
		t.Fatalf("%+v", r)
	}
}

func signedCatalog(t *testing.T, c *appstore.Catalog) ([]byte, string) {
	t.Helper()
	priv, pub, _ := appstore.GenerateKey("t")
	b, err := appstore.Sign(c, priv)
	if err != nil {
		t.Fatal(err)
	}
	return b, pub
}

func TestCatalogSource(t *testing.T) {
	cat := &appstore.Catalog{Generated: time.Now(), Apps: []appstore.App{
		{Slug: "habits", Name: "Pocket Habits", Version: "0.5.0", Tag: "v0.5.0", Image: "ghcr.io/abjelosevic88/pocket-homelab-habit-tracker", Digest: "sha256:" + strings.Repeat("a", 64), Container: "pocket-habits", Compose: string(fixture(t, "habits.yml"))},
		{Slug: "core", Version: "1.0.0", Image: "ghcr.io/abjelosevic88/pocket-homelab-core"},
	}}
	body, pub := signedCatalog(t, cat)
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "down", 503)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	keys, _ := appstore.ParseKeys([]string{pub})
	s := newCatalogSource(&Config{CatalogURLs: []string{srv.URL + "/missing-host-first", srv.URL}, Protected: []string{"core"}}, keys)
	rels, err := s.Catalog(context.Background(), false)
	if err != nil || len(rels) != 1 || rels[0].Name != "Pocket Habits" {
		t.Fatalf("%v %+v", err, rels)
	}
	if rels[0].ref() != "ghcr.io/abjelosevic88/pocket-homelab-habit-tracker:0.5.0@sha256:"+strings.Repeat("a", 64) {
		t.Fatal(rels[0].ref())
	}
	fail = true
	if rels, err := s.Catalog(context.Background(), true); err != nil || len(rels) != 1 {
		t.Fatalf("a failed refresh must keep the last good copy: %v", err)
	}

	// A catalog signed by someone else is refused outright.
	other, _ := appstore.ParseKeys([]string{func() string { _, p, _ := appstore.GenerateKey("t"); return p }()})
	fail = false
	if _, err := newCatalogSource(&Config{CatalogURLs: []string{srv.URL}}, other).Catalog(context.Background(), false); err == nil {
		t.Fatal("catalog with a foreign signature accepted")
	}
}

func TestPlanByPortAndDigest(t *testing.T) {
	rel := &Release{Slug: "habits", Repo: "pocket-homelab-habit-tracker", Version: "1.2.3", Tag: "v1.2.3", Digest: "sha256:" + strings.Repeat("b", 64),
		Image: "ghcr.io/abjelosevic88/pocket-homelab-habit-tracker", Container: "pocket-habits", compose: fixture(t, "habits.yml")}
	p, err := BuildPlan(PlanInput{Release: rel, PublicURL: "http://192.168.1.20:{port}", CoreURL: "http://host.docker.internal:8179",
		RegistrationKey: "k", AdminEmail: "me@example.org", TZ: "UTC", NextPort: ports(8300), Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if p.PublicURL != "http://192.168.1.20:8300" || !strings.Contains(string(p.Compose), "BASE_URL: http://192.168.1.20:8300") ||
		!strings.Contains(string(p.Compose), "SECURE_COOKIES: \"false\"") && !strings.Contains(string(p.Compose), "SECURE_COOKIES: false") ||
		!strings.Contains(string(p.Compose), ":1.2.3@sha256:"+strings.Repeat("b", 64)) {
		t.Fatalf("%s\n%s", p.PublicURL, p.Compose)
	}
}

func TestCheckAndRewriteUpdate(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "docker-compose.yml")
	img := "ghcr.io/abjelosevic88/pocket-homelab-habit-tracker"
	write := func(s string) { os.WriteFile(f, []byte(s), 0o644) }
	c := &container{Name: "pocket-habits", Labels: map[string]string{"com.docker.compose.project": "x", "com.docker.compose.project.working_dir": dir, "com.docker.compose.project.config_files": f}}
	rel := &Release{Image: img, Version: "0.10.0", Digest: "sha256:" + strings.Repeat("c", 64)}
	a := &Agent{cfg: &Config{}}

	write("services:\n  app:\n    image: \"" + img + ":0.9.1\" # pinned\n")
	chk := a.checkUpdate(c, rel)
	if chk.err != nil || !chk.newer || chk.current != "0.9.1" {
		t.Fatalf("%+v", chk)
	}
	re := imageLineRe(img)
	b, _ := os.ReadFile(f)
	out := re.ReplaceAllStringFunc(string(b), func(line string) string { m := re.FindStringSubmatch(line); return m[1] + rel.ref() + m[3] + m[4] })
	if out != "services:\n  app:\n    image: \""+img+":0.10.0@sha256:"+strings.Repeat("c", 64)+"\" # pinned\n" {
		t.Fatalf("rewrite: %q", out)
	}

	write("services:\n  app:\n    image: " + img + ":0.10.0@sha256:" + strings.Repeat("d", 64) + "\n")
	if chk := a.checkUpdate(c, rel); chk.err != nil || chk.newer {
		t.Fatalf("same version is not an update: %+v", chk)
	}
	write("services:\n  app:\n    image: " + img + ":latest\n")
	if chk := a.checkUpdate(c, rel); chk.err == nil || !strings.Contains(chk.err.Error(), `"latest"`) {
		t.Fatalf("moving tag: %+v", chk)
	}
	write("services:\n  app:\n    build: .\n    image: " + img + ":0.9.0\n")
	if chk := a.checkUpdate(c, rel); chk.err == nil || !strings.Contains(chk.err.Error(), "built from source") {
		t.Fatalf("built here: %+v", chk)
	}
	if !semverLess("0.9.1", "0.10.0") || semverLess("1.0.0", "0.10.0") || semverLess("latest", "1.0.0") {
		t.Fatal("semverLess")
	}
}
