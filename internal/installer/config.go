// Package installer installs and removes Pocket apps on a Docker host.
//
// It has two halves. The agent (`pocket-core installer`) runs on the host as
// the user who owns the app folders, with Docker access; it pulls an app's
// release from GitHub, writes its folder (docker-compose.yml + .env), starts
// it, adds its HTTPS site to Caddy, and does the reverse on removal. It
// answers JSON on a unix socket that only Core can reach. The Client is what
// Core uses to talk to it. Core itself stays unprivileged: it never sees the
// Docker socket.
package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
)

// Config is the agent's configuration, read from INSTALLER_* variables.
type Config struct {
	Socket string // unix socket Core connects to
	Token  string // bearer token Core must send

	Owner       string // GitHub account the apps are released from
	RepoPrefix  string // only repos named like this are offered
	GitHubToken string // optional; raises the API limit for the repo list

	// CatalogURLs are the signed app catalogs tried in order; nil = read
	// GitHub directly (INSTALLER_CATALOG_URL=github).
	CatalogURLs []string
	CatalogKeys []string // trusted "<id>:<base64>" keys: the official ones plus INSTALLER_CATALOG_KEYS
	DockerGID   string   // the docker group, when /etc/group can't tell (the agent in a container)

	// Ownership when the agent runs as root (the installer container):
	// apps' data folders go to AppOwner (the uid Pocket images run as), the
	// folder, compose file, .env and the socket to FilesOwner (the person).
	AppOwner   owner
	FilesOwner owner

	AppsDir    string // each app gets AppsDir/pocket-<slug>
	ArchiveDir string // removed apps' folders are moved here
	PublicURL  string // template: https://{slug}.example.com, or http://192.168.1.20:{port} without a domain
	CoreURL    string // how app containers reach Core
	PortFrom   int    // host ports handed to new apps
	PortTo     int
	TZ         string

	Caddyfile      string   // host path; empty = leave the reverse proxy alone
	CaddyContainer string   // container that serves it
	CaddyPath      string   // the same file inside that container
	CaddyImports   []string // snippets every new site imports (tls settings…)
	CaddyUpstream  string   // how Caddy reaches a published port

	Leftovers []string // files searched for the removed app's address afterwards

	// The address choice of an install.sh install (address.go); off without Address.Socket.
	Address   AddressConfig
	Protected []string // slugs that are never removed
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func list(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LoadConfig reads the agent's settings.
func LoadConfig() (*Config, error) {
	home, _ := os.UserHomeDir()
	apps := env("INSTALLER_APPS_DIR", home)
	c := &Config{
		Socket:         env("INSTALLER_SOCKET", ""),
		Token:          env("INSTALLER_TOKEN", ""),
		Owner:          env("INSTALLER_GITHUB_OWNER", "abjelosevic88"),
		RepoPrefix:     env("INSTALLER_REPO_PREFIX", "pocket-homelab-"),
		GitHubToken:    env("INSTALLER_GITHUB_TOKEN", ""),
		CatalogKeys:    append(slices.Clone(appstore.OfficialKeys), list(env("INSTALLER_CATALOG_KEYS", ""))...),
		DockerGID:      env("INSTALLER_DOCKER_GID", ""),
		AppsDir:        apps,
		ArchiveDir:     env("INSTALLER_ARCHIVE_DIR", filepath.Join(apps, "pocket-archive")),
		PublicURL:      env("INSTALLER_PUBLIC_URL", ""),
		CoreURL:        strings.TrimRight(env("INSTALLER_CORE_URL", "http://host.docker.internal:8179"), "/"),
		TZ:             env("INSTALLER_TZ", env("TZ", localZone())),
		Caddyfile:      env("INSTALLER_CADDYFILE", ""),
		CaddyContainer: env("INSTALLER_CADDY_CONTAINER", "caddy"),
		CaddyPath:      env("INSTALLER_CADDY_PATH", "/etc/caddy/Caddyfile"),
		CaddyImports:   list(env("INSTALLER_CADDY_IMPORTS", "")),
		CaddyUpstream:  env("INSTALLER_CADDY_UPSTREAM", "host.docker.internal"),
		Leftovers:      list(env("INSTALLER_LEFTOVER_FILES", "")),
		Protected:      append([]string{"core"}, list(env("INSTALLER_PROTECTED", ""))...),
		Address: AddressConfig{
			Socket:    env("INSTALLER_ADDRESS_SOCKET", ""),
			Dir:       env("INSTALLER_POCKET_DIR", ""),
			NamesURL:  env("INSTALLER_NAMES_URL", "https://names.pockethomelab.com"),
			ACME:      env("INSTALLER_ACME_CA", "https://acme-v02.api.letsencrypt.org/directory"),
			Tailscale: env("INSTALLER_TAILSCALE_CONTAINER", "pocket-tailscale"),
		},
	}
	if c.Address.Socket != "" && c.Address.Dir == "" {
		return nil, fmt.Errorf("INSTALLER_ADDRESS_SOCKET needs INSTALLER_POCKET_DIR (install.sh's folder)")
	}
	var err error
	if c.AppOwner, err = parseOwner(env("INSTALLER_APP_OWNER", "1000:1000")); err != nil {
		return nil, fmt.Errorf("INSTALLER_APP_OWNER: %w", err)
	}
	if c.FilesOwner, err = parseOwner(env("INSTALLER_FILES_OWNER", "")); err != nil {
		return nil, fmt.Errorf("INSTALLER_FILES_OWNER: %w", err)
	}
	if u := env("INSTALLER_CATALOG_URL", appstore.DefaultURL); u != "github" {
		c.CatalogURLs = list(u)
	}
	if _, err := appstore.ParseKeys(c.CatalogKeys); err != nil {
		return nil, fmt.Errorf("INSTALLER_CATALOG_KEYS: %w", err)
	}
	if f := env("INSTALLER_TOKEN_FILE", ""); f != "" && c.Token == "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("INSTALLER_TOKEN_FILE: %w", err)
		}
		c.Token = strings.TrimSpace(string(b))
	}
	from, to, ok := strings.Cut(env("INSTALLER_PORTS", "8300-8399"), "-")
	c.PortFrom, _ = strconv.Atoi(strings.TrimSpace(from))
	c.PortTo, _ = strconv.Atoi(strings.TrimSpace(to))
	switch {
	case c.Socket == "":
		return nil, fmt.Errorf("INSTALLER_SOCKET is required")
	case len(c.Token) < 24:
		return nil, fmt.Errorf("INSTALLER_TOKEN (or INSTALLER_TOKEN_FILE) must be at least 24 characters")
	case c.PublicURL == "" || !strings.Contains(c.PublicURL, "{slug}") && !strings.Contains(c.PublicURL, "{port}"):
		return nil, fmt.Errorf("INSTALLER_PUBLIC_URL must contain {slug} or {port}, e.g. https://{slug}.example.com or http://192.168.1.20:{port}")
	case !ok || c.PortFrom < 1024 || c.PortTo < c.PortFrom || c.PortTo > 65535:
		return nil, fmt.Errorf("INSTALLER_PORTS must look like 8300-8399")
	case !filepath.IsAbs(c.AppsDir) || !filepath.IsAbs(c.ArchiveDir):
		return nil, fmt.Errorf("INSTALLER_APPS_DIR and INSTALLER_ARCHIVE_DIR must be absolute")
	}
	return c, nil
}

// publicURL is where people open the app; {port} is filled in once the
// app has its host port.
func (c *Config) publicURL(slug string) string {
	return strings.TrimRight(strings.ReplaceAll(c.PublicURL, "{slug}", slug), "/")
}

// byPort: apps are opened on their own port, so there is no proxy site to add.
func (c *Config) byPort() bool { return strings.Contains(c.PublicURL, "{port}") }

func (c *Config) protected(slug string) bool {
	for _, p := range c.Protected {
		if p == slug {
			return true
		}
	}
	return false
}

// owner is a uid:gid; set=false leaves files as the agent made them.
type owner struct {
	uid, gid int
	set      bool
}

func parseOwner(v string) (owner, error) {
	if v == "" {
		return owner{}, nil
	}
	u, g, ok := strings.Cut(v, ":")
	uid, err1 := strconv.Atoi(u)
	gid, err2 := strconv.Atoi(g)
	if !ok || err1 != nil || err2 != nil || uid < 0 || gid < 0 {
		return owner{}, fmt.Errorf("want uid:gid, got %q", v)
	}
	return owner{uid, gid, true}, nil
}

// chown hands a path to o, only when the agent runs as root (otherwise it
// can't, and the files already belong to the agent's own user).
func (o owner) chown(path string) {
	if o.set && os.Geteuid() == 0 {
		_ = os.Lchown(path, o.uid, o.gid)
	}
}

func localZone() string {
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if z := strings.TrimSpace(string(b)); z != "" {
			return z
		}
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		if _, z, ok := strings.Cut(l, "zoneinfo/"); ok {
			return z
		}
	}
	return "UTC"
}
