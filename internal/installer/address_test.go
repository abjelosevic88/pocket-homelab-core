package installer

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAddress(t *testing.T) (*Address, string) {
	t.Helper()
	dir := t.TempDir()
	apps := filepath.Join(dir, "apps")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	env := "POCKET_HOST=192.168.1.20\nCORE_PORT=8179\nCOMPOSE_PROFILES=\nBASE_URL=http://192.168.1.20:8179\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	ag := &Agent{cfg: &Config{AppsDir: apps, CaddyUpstream: "host.docker.internal"}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return newAddress(ag, AddressConfig{Dir: dir}, ag.log), dir
}

const notesCompose = `# Pocket Notes — installed by Pocket Core on 2026-11-13 from https://github.com/abjelosevic88/pocket-homelab-notes (v0.11.1).
# http://192.168.1.20:8300, host port 8300. Data lives next to this file; settings and secrets are in .env.
services:
  pocket-notes:
    image: ghcr.io/abjelosevic88/pocket-homelab-notes:0.11.1@sha256:abc
    environment:
      BASE_URL: http://192.168.1.20:8300
      SECURE_COOKIES: "false"
      CORE_URL: http://host.docker.internal:8179
      CORE_APP_URL: http://host.docker.internal:8300
`

func TestInstalledAppsAndRewrite(t *testing.T) {
	ad, _ := testAddress(t)
	app := filepath.Join(ad.ag.cfg.AppsDir, "pocket-notes")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(app, "docker-compose.yml"), []byte(notesCompose), 0o644)
	os.WriteFile(filepath.Join(app, appMarker), []byte(`{"slug":"notes","port":8300}`), 0o644)
	// not an app: no CORE_APP_URL
	os.MkdirAll(filepath.Join(ad.ag.cfg.AppsDir, "pocket-archive"), 0o755)

	apps, err := ad.installedApps(context.Background())
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps %v %v", apps, err)
	}
	a := apps[0]
	if a.Slug != "notes" || a.Port != 8300 || a.BaseURL != "http://192.168.1.20:8300" {
		t.Fatalf("app %+v", a)
	}
	next := ad.appURL(a, "x7k2m9.box.pockethomelab.net")
	if next != "https://notes.x7k2m9.box.pockethomelab.net" {
		t.Fatal(next)
	}
	got := rewriteCompose(notesCompose, a.BaseURL, next, true)
	for _, want := range []string{"BASE_URL: https://notes.x7k2m9.box.pockethomelab.net\n", `SECURE_COOKIES: "true"`, "# https://notes.x7k2m9.box.pockethomelab.net, host port 8300", "CORE_APP_URL: http://host.docker.internal:8300"} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten compose lacks %q:\n%s", want, got)
		}
	}
	// and back to this network's address
	back := rewriteCompose(got, next, ad.appURL(a, ""), false)
	if back != notesCompose {
		t.Errorf("round trip:\n%s", back)
	}
}

// Some sample compose files say what a line is for, after it.
func TestTrailingComments(t *testing.T) {
	compose := "    environment:\n      BASE_URL: http://192.168.1.20:8305 # used in reminder links\n      SECURE_COOKIES: \"false\" # behind HTTPS\n      CORE_APP_URL: http://host.docker.internal:8305\n"
	if m := reBaseURL.FindStringSubmatch(compose); m == nil || m[1] != "http://192.168.1.20:8305" {
		t.Fatalf("BASE_URL: %v", m)
	}
	got := rewriteCompose(compose, "http://192.168.1.20:8305", "https://habits.example.net", true)
	if !strings.Contains(got, "BASE_URL: https://habits.example.net # used in reminder links\n") || !strings.Contains(got, `SECURE_COOKIES: "true" # behind HTTPS`) {
		t.Fatalf("rewritten:\n%s", got)
	}
}

func TestCaddyfile(t *testing.T) {
	ad, _ := testAddress(t)
	cf := ad.caddyfile("x7k2m9.box.pockethomelab.net", []installedApp{{Slug: "notes", Port: 8300}, {Slug: "tasks", Port: 8301}})
	for _, want := range []string{
		"https://x7k2m9.box.pockethomelab.net, https://*.x7k2m9.box.pockethomelab.net {",
		"tls /etc/caddy/tls/cert.pem /etc/caddy/tls/key.pem",
		"@core host core.x7k2m9.box.pockethomelab.net",
		"reverse_proxy host.docker.internal:8179",
		"@notes host notes.x7k2m9.box.pockethomelab.net",
		"reverse_proxy host.docker.internal:8301",
		"redir https://core.x7k2m9.box.pockethomelab.net{uri}",
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("Caddyfile lacks %q:\n%s", want, cf)
		}
	}
	if strings.Count(cf, "{") != strings.Count(cf, "}") {
		t.Errorf("unbalanced braces:\n%s", cf)
	}
}

func TestEnvSetKeepsTheRest(t *testing.T) {
	ad, dir := testAddress(t)
	if err := ad.envSet(map[string]string{"BASE_URL": "https://core.example.net", "POCKET_ADDRESS": "example.net"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	want := "POCKET_HOST=192.168.1.20\nCORE_PORT=8179\nCOMPOSE_PROFILES=\nBASE_URL=https://core.example.net\nPOCKET_ADDRESS=example.net\n"
	if string(b) != want {
		t.Fatalf(".env:\n%s", b)
	}
	if fi, _ := os.Stat(filepath.Join(dir, ".env")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if ad.envGet("POCKET_ADDRESS") != "example.net" || ad.plainCoreURL() != "http://192.168.1.20:8179" {
		t.Error("envGet")
	}
}

func TestProfiles(t *testing.T) {
	for _, c := range [][4]string{
		{"", "proxy", "", "proxy"},
		{"proxy", "proxy", "", "proxy"},
		{"tailscale,proxy", "", "proxy", "tailscale"},
		{"domain", "tailscale", "", "domain,tailscale"},
	} {
		if got := profiles(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("profiles(%q, +%q, -%q) = %q, want %q", c[0], c[1], c[2], got, c[3])
		}
	}
}

func TestSetChoiceValidates(t *testing.T) {
	ad, _ := testAddress(t)
	ad.cfg.NamesURL = "http://127.0.0.1:1" // nothing there: no OAuth
	if _, err := ad.setChoice(context.Background(), map[string]any{"mode": "own-ca"}); err == nil {
		t.Error("own-ca accepted")
	}
	if _, err := ad.setChoice(context.Background(), map[string]any{"mode": "cloudflare", "sub": "bad name!", "token": "x"}); err == nil {
		t.Error("bad sub accepted")
	}
	v, err := ad.setChoice(context.Background(), map[string]any{"mode": "plain"})
	if err != nil || v["mode"] != "plain" || ad.choice() != "plain" {
		t.Fatalf("plain: %v %v", v, err)
	}
}

func TestCaddyfileTailnet(t *testing.T) {
	ad, dir := testAddress(t)
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	os.WriteFile(filepath.Join(dir, ".env"), append(env, "POCKET_TAILNET=1\n"...), 0o600)
	if !ad.tailnet() {
		t.Fatal("POCKET_TAILNET=1 is tailnet mode")
	}
	apps := []installedApp{{Slug: "notes", Port: 8300}}
	// No name yet: Core and each app on their own port, plain http, nothing else.
	c := ad.caddyfile("", apps)
	for _, want := range []string{"http://:8179 {\n\treverse_proxy host.docker.internal:8179", "http://:8300 {\n\treverse_proxy host.docker.internal:8300"} {
		if !strings.Contains(c, want) {
			t.Fatalf("no %q in\n%s", want, c)
		}
	}
	if strings.Contains(c, "https://") || strings.Contains(c, "tls ") {
		t.Fatalf("no name, no https site:\n%s", c)
	}
	// With a name: the https site and the plain ports as well.
	c = ad.caddyfile("x7k2m9.box.pockethomelab.net", apps)
	for _, want := range []string{"https://x7k2m9.box.pockethomelab.net, https://*.x7k2m9.box.pockethomelab.net {", "@notes host notes.x7k2m9", "http://:8300 {"} {
		if !strings.Contains(c, want) {
			t.Fatalf("no %q in\n%s", want, c)
		}
	}
	// Not tailnet mode: no plain ports.
	os.WriteFile(filepath.Join(dir, ".env"), env, 0o600)
	if c = ad.caddyfile("x7k2m9.box.pockethomelab.net", apps); strings.Contains(c, "http://:") {
		t.Fatalf("plain ports outside tailnet mode:\n%s", c)
	}
}

func TestTailnetIPv4(t *testing.T) {
	if got := tailnetIPv4([]string{"fd7a:115c:a1e0::1", "100.101.102.103"}); got != "100.101.102.103" {
		t.Fatalf("got %q", got)
	}
	if got := tailnetIPv4([]string{"192.168.1.5"}); got != "" {
		t.Fatalf("got %q", got)
	}
}
