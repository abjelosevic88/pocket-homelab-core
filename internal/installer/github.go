package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
)

// Release is the newest release of one app repo: what Install would put on
// the server.
type Release struct {
	Slug        string `json:"slug"`
	Repo        string `json:"repo"`
	RepoURL     string `json:"repo_url"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"` // 0.4.5
	Tag         string `json:"tag"`     // v0.4.5
	Image       string `json:"image"`   // ghcr.io/owner/repo
	Container   string `json:"container"`
	Present     bool   `json:"present"` // a container of that name exists here already

	// From the signed catalog (empty when read straight from GitHub).
	Name      string            `json:"name,omitempty"`
	Category  string            `json:"category,omitempty"`
	Icon      string            `json:"icon,omitempty"`
	Page      string            `json:"page,omitempty"`
	Digest    string            `json:"digest,omitempty"`
	Platforms []string          `json:"platforms,omitempty"`
	Requires  appstore.Requires `json:"requires"`

	// About this server.
	Available  bool   `json:"available"`             // the image runs on this machine
	Running    string `json:"running,omitempty"`     // image tag the container here runs
	Updatable  bool   `json:"updatable,omitempty"`   // Update can move it to Version
	UpdateNote string `json:"update_note,omitempty"` // why it can't
	// Archive is the newest archived folder of a removed install, whose
	// data Install can bring back.
	Archive *ArchiveInfo `json:"archive,omitempty"`

	compose    []byte
	envExample []byte
}

// ref is the image Install and Update write: the release tag, pinned to
// its digest when the catalog knows it.
func (r *Release) ref() string {
	ref := r.Image + ":" + r.Version
	if r.Digest != "" {
		ref += "@" + r.Digest
	}
	return ref
}

func releaseFrom(a *appstore.App) Release {
	return Release{Slug: a.Slug, Repo: a.Repo, RepoURL: a.RepoURL, Description: a.Description, Version: a.Version, Tag: a.Tag,
		Image: a.Image, Container: a.Container, Name: a.Name, Category: a.Category, Icon: a.Icon, Page: a.Page,
		Digest: a.Digest, Platforms: a.Platforms, Requires: a.Requires, Available: true,
		compose: []byte(a.Compose), envExample: []byte(a.EnvExample)}
}

// source is where the agent reads releases from: the signed catalog, or
// GitHub directly (INSTALLER_CATALOG_URL=github).
type source interface {
	Catalog(ctx context.Context, refresh bool) ([]Release, error)
	Find(ctx context.Context, slug string) (*Release, error)
}

// GitHub reads app releases. The repo list is one API call; tags come from
// git's smart-HTTP ref advertisement and files from raw.githubusercontent.com,
// neither of which counts against the API limit.
type GitHub struct {
	cfg      *Config
	http     *http.Client
	api      string // overridable in tests
	git      string
	raw      string
	registry string

	mu      sync.Mutex
	cached  []Release
	fetched time.Time
}

func NewGitHub(cfg *Config) *GitHub {
	return &GitHub{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second},
		api: "https://api.github.com", git: "https://github.com", raw: "https://raw.githubusercontent.com", registry: "https://ghcr.io"}
}

const catalogTTL = 15 * time.Minute

// Catalog lists every installable app: repos of the owner with the prefix,
// a vX.Y.Z tag, and a docker-compose.yml that runs the repo's own image.
func (g *GitHub) Catalog(ctx context.Context, refresh bool) ([]Release, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !refresh && g.cached != nil && time.Since(g.fetched) < catalogTTL {
		return g.cached, nil
	}
	repos, err := g.repos(ctx)
	if err != nil {
		if g.cached != nil {
			return g.cached, nil // GitHub is down or rate limited: the last list still holds
		}
		return nil, err
	}
	out := make([]Release, len(repos))
	ok := make([]bool, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rel, err := g.release(ctx, r.Name, r.Description)
			if err == nil {
				out[i], ok[i] = *rel, true
			}
		}()
	}
	wg.Wait()
	var rels []Release
	seen := map[string]bool{}
	for i := range out {
		if ok[i] && !seen[out[i].Slug] && !g.cfg.protected(out[i].Slug) {
			seen[out[i].Slug] = true
			rels = append(rels, out[i])
		}
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].Slug < rels[j].Slug })
	g.cached, g.fetched = rels, time.Now()
	return rels, nil
}

// Find returns one app's release, refreshing the list once if it is missing.
func (g *GitHub) Find(ctx context.Context, slug string) (*Release, error) {
	for _, refresh := range []bool{false, true} {
		rels, err := g.Catalog(ctx, refresh)
		if err != nil {
			return nil, err
		}
		for i := range rels {
			if rels[i].Slug == slug {
				// The cached copy may be up to 15 minutes old: re-read its newest tag.
				return g.release(ctx, rels[i].Repo, rels[i].Description)
			}
		}
	}
	return nil, fmt.Errorf("no released app %q in github.com/%s", slug, g.cfg.Owner)
}

type repo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Archived    bool   `json:"archived"`
	Fork        bool   `json:"fork"`
}

func (g *GitHub) repos(ctx context.Context) ([]repo, error) {
	var all []repo
	for page := 1; page <= 5; page++ {
		u := fmt.Sprintf("%s/users/%s/repos?per_page=100&type=owner&page=%d", g.api, g.cfg.Owner, page)
		b, err := g.get(ctx, u, true)
		if err != nil {
			return nil, err
		}
		var rs []repo
		if err := json.Unmarshal(b, &rs); err != nil {
			return nil, fmt.Errorf("github repo list: %w", err)
		}
		for _, r := range rs {
			if strings.HasPrefix(r.Name, g.cfg.RepoPrefix) && !r.Archived && !r.Fork {
				all = append(all, r)
			}
		}
		if len(rs) < 100 {
			break
		}
	}
	return all, nil
}

func (g *GitHub) release(ctx context.Context, name, desc string) (*Release, error) {
	tag, err := g.latestTag(ctx, name)
	if err != nil {
		return nil, err
	}
	compose, err := g.get(ctx, fmt.Sprintf("%s/%s/%s/%s/docker-compose.yml", g.raw, g.cfg.Owner, name, tag), false)
	if err != nil {
		return nil, err
	}
	envEx, _ := g.get(ctx, fmt.Sprintf("%s/%s/%s/%s/.env.example", g.raw, g.cfg.Owner, name, tag), false)
	image := strings.ToLower("ghcr.io/" + g.cfg.Owner + "/" + name)
	_, ctr, err := mainService(compose, image)
	if err != nil {
		return nil, err
	}
	slug := strings.TrimPrefix(ctr, "pocket-")
	if ctr == "" || slug == ctr {
		slug = strings.TrimPrefix(name, g.cfg.RepoPrefix)
	}
	if !slugOK(slug) {
		return nil, fmt.Errorf("%s: no usable slug", name)
	}
	if ctr == "" {
		ctr = "pocket-" + slug
	}
	return &Release{Slug: slug, Repo: name, RepoURL: "https://github.com/" + g.cfg.Owner + "/" + name, Description: desc,
		Version: strings.TrimPrefix(tag, "v"), Tag: tag, Image: image, Container: ctr, Available: true, compose: compose, envExample: envEx}, nil
}

var tagRe = regexp.MustCompile(`refs/tags/(v(\d+)\.(\d+)\.(\d+))[\n\x00^]`)

// latestTag reads the repo's tags from git's ref advertisement and returns
// the highest plain vX.Y.Z (no pre-releases).
func (g *GitHub) latestTag(ctx context.Context, name string) (string, error) {
	b, err := g.get(ctx, fmt.Sprintf("%s/%s/%s.git/info/refs?service=git-upload-pack", g.git, g.cfg.Owner, name), false)
	if err != nil {
		return "", err
	}
	return highestTag(string(b))
}

func highestTag(refs string) (string, error) {
	best, bestV := "", [3]int{-1}
	for _, m := range tagRe.FindAllStringSubmatch(refs, -1) {
		var v [3]int
		for i := range v {
			v[i], _ = strconv.Atoi(m[i+2])
		}
		if v[0] > bestV[0] || v[0] == bestV[0] && (v[1] > bestV[1] || v[1] == bestV[1] && v[2] > bestV[2]) {
			best, bestV = m[1], v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no release tag")
	}
	return best, nil
}

func (g *GitHub) get(ctx context.Context, u string, api bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pocket-core-installer")
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		if g.cfg.GitHubToken != "" {
			req.Header.Set("Authorization", "Bearer "+g.cfg.GitHubToken)
		}
	}
	res, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		if api && res.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("GitHub refused the repo list (rate limit?); set INSTALLER_GITHUB_TOKEN")
		}
		return nil, fmt.Errorf("%s: HTTP %d", u, res.StatusCode)
	}
	return b, nil
}
