package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
)

// BuildOptions drive `pocket-core catalog build`.
type BuildOptions struct {
	Owner       string
	RepoPrefix  string
	GitHubToken string
	// SiteMeta is the website's app list (pocket-apps.json: names,
	// categories, summaries, icons), a URL or a file; empty = none.
	SiteMeta string
	SiteBase string // https://pockethomelab.com, for icon and page links
	// Previous is the catalog published so far. An app whose newest tag has
	// no image yet (CI still building) keeps its previous entry.
	Previous *appstore.Catalog
	Log      func(format string, args ...any)
}

// newGitHubHook lets tests point the builder at a fake GitHub.
var newGitHubHook func(*GitHub)

// BuildCatalog reads every app repo and returns the catalog to sign.
func BuildCatalog(ctx context.Context, o BuildOptions) (*appstore.Catalog, error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	gh := NewGitHub(&Config{Owner: o.Owner, RepoPrefix: o.RepoPrefix, GitHubToken: o.GitHubToken})
	if newGitHubHook != nil {
		newGitHubHook(gh)
	}
	rels, err := gh.Catalog(ctx, true)
	if err != nil {
		return nil, err
	}
	meta, err := readSiteMeta(ctx, gh.http, o.SiteMeta)
	if err != nil {
		if o.Previous == nil {
			return nil, err
		}
		// Names and icons are cosmetic: keep the published ones rather than
		// republish a catalog without them.
		logf("%v; using the previous catalog's names and icons", err)
		meta = map[string]siteApp{}
		for _, a := range o.Previous.Apps {
			meta[a.Slug] = siteApp{Slug: a.Slug, Name: a.Name, Category: a.Category, Summary: a.Description, Icon: a.Icon}
		}
	}
	base := strings.TrimRight(o.SiteBase, "/")

	apps := make([]*appstore.App, len(rels))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i := range rels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rel := &rels[i]
			digest, platforms, err := gh.imageInfo(ctx, rel.Image, rel.Version)
			if err != nil {
				if o.Previous != nil {
					if prev := o.Previous.Find(rel.Slug); prev != nil {
						logf("%s %s: %v; keeping %s", rel.Slug, rel.Tag, err, prev.Tag)
						p := *prev
						apps[i] = &p
						return
					}
				}
				logf("%s %s: %v; left out", rel.Slug, rel.Tag, err)
				return
			}
			a := &appstore.App{Slug: rel.Slug, Name: appDisplayName(rel.Slug), Description: rel.Description, Repo: rel.Repo,
				RepoURL: rel.RepoURL, Version: rel.Version, Tag: rel.Tag, Image: rel.Image, Digest: digest, Platforms: platforms,
				Container: rel.Container, Requires: analyzeCompose(rel.compose, rel.Image), Compose: string(rel.compose),
				EnvExample: string(rel.envExample)}
			if m, ok := meta[rel.Slug]; ok {
				a.Name = firstNonEmpty(m.Name, a.Name)
				a.Description = firstNonEmpty(m.Summary, a.Description)
				a.Category = m.Category
				switch {
				case strings.HasPrefix(m.Icon, "https://"):
					a.Icon = m.Icon
				case m.Icon != "" && base != "":
					a.Icon = base + m.Icon
				}
				if base != "" {
					a.Page = base + "/apps/" + rel.Slug + "/"
				}
			}
			apps[i] = a
		}()
	}
	wg.Wait()
	c := &appstore.Catalog{Generated: time.Now().UTC().Truncate(time.Second), Source: "github.com/" + o.Owner}
	for _, a := range apps {
		if a != nil {
			c.Apps = append(c.Apps, *a)
		}
	}
	sort.Slice(c.Apps, func(i, j int) bool { return c.Apps[i].Slug < c.Apps[j].Slug })
	if len(c.Apps) == 0 {
		return nil, fmt.Errorf("no releases found for github.com/%s/%s*", o.Owner, o.RepoPrefix)
	}
	return c, nil
}

func appDisplayName(slug string) string {
	if e, ok := catalog.Get(slug); ok && e.Name != "" {
		return e.Name
	}
	return displayName(slug)
}

type siteApp struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Icon     string `json:"icon"`
}

func readSiteMeta(ctx context.Context, hc *http.Client, src string) (map[string]siteApp, error) {
	out := map[string]siteApp{}
	if src == "" {
		return out, nil
	}
	var b []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		b, err = httpGet(ctx, hc, src, nil)
	} else {
		b, err = os.ReadFile(src)
	}
	if err != nil {
		return nil, fmt.Errorf("site metadata: %w", err)
	}
	var doc struct {
		Apps []siteApp `json:"apps"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("site metadata: %w", err)
	}
	for _, a := range doc.Apps {
		out[a.Slug] = a
	}
	return out, nil
}

func httpGet(ctx context.Context, hc *http.Client, u string, hdr map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pocket-core-catalog")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, res.StatusCode)
	}
	return b, nil
}

const manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// imageInfo asks the registry for the tag's digest and the platforms its
// manifest list covers (anonymous pull token: the images are public).
func (g *GitHub) imageInfo(ctx context.Context, image, tag string) (string, []string, error) {
	repo, ok := strings.CutPrefix(image, "ghcr.io/")
	if !ok {
		return "", nil, fmt.Errorf("%s is not on ghcr.io", image)
	}
	tb, err := httpGet(ctx, g.http, g.registry+"/token?service=ghcr.io&scope="+url.QueryEscape("repository:"+repo+":pull"), nil)
	if err != nil {
		return "", nil, fmt.Errorf("registry token: %w", err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(tb, &tok)
	mb, err := httpGet(ctx, g.http, g.registry+"/v2/"+repo+"/manifests/"+tag, map[string]string{"Accept": manifestTypes, "Authorization": "Bearer " + tok.Token})
	if err != nil {
		return "", nil, fmt.Errorf("no image %s:%s", image, tag)
	}
	sum := sha256.Sum256(mb)
	var m struct {
		Manifests []struct {
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	_ = json.Unmarshal(mb, &m)
	var plats []string
	seen := map[string]bool{}
	for _, x := range m.Manifests {
		p := x.Platform
		if p.OS == "" || p.OS == "unknown" { // build attestations
			continue
		}
		s := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			s += "/" + p.Variant
		}
		if !seen[s] {
			seen[s] = true
			plats = append(plats, s)
		}
	}
	sort.Strings(plats)
	return "sha256:" + hex.EncodeToString(sum[:]), plats, nil
}

// analyzeCompose lists what the stack asks of the host.
func analyzeCompose(compose []byte, image string) appstore.Requires {
	var r appstore.Requires
	var doc yaml.Node
	if yaml.Unmarshal(compose, &doc) != nil || len(doc.Content) == 0 {
		return r
	}
	svcs := mapGet(doc.Content[0], "services")
	if svcs == nil || svcs.Kind != yaml.MappingNode {
		return r
	}
	add := func(list *[]string, v string) {
		for _, x := range *list {
			if x == v {
				return
			}
		}
		*list = append(*list, v)
	}
	for i := 0; i+1 < len(svcs.Content); i += 2 {
		name, svc := svcs.Content[i].Value, svcs.Content[i+1]
		if img := mapGet(svc, "image"); img == nil || imageBase(img.Value) != image {
			label := name
			if img != nil {
				label += " (" + img.Value + ")"
			}
			add(&r.Services, label)
		}
		if nm := mapGet(svc, "network_mode"); nm != nil && nm.Value == "host" {
			r.HostNetwork = true
		}
		if p := mapGet(svc, "privileged"); p != nil && p.Value == "true" {
			r.Privileged = true
		}
		for _, key := range []string{"cap_add", "devices"} {
			n := mapGet(svc, key)
			if n == nil || n.Kind != yaml.SequenceNode {
				continue
			}
			for _, it := range n.Content {
				if key == "cap_add" {
					add(&r.Capabilities, it.Value)
				} else {
					host, _, _ := strings.Cut(it.Value, ":")
					add(&r.Devices, host)
				}
			}
		}
		vols := mapGet(svc, "volumes")
		if vols == nil || vols.Kind != yaml.SequenceNode {
			continue
		}
		for _, it := range vols.Content {
			src := ""
			switch it.Kind {
			case yaml.ScalarNode:
				src = volumeSource(it.Value)
			case yaml.MappingNode:
				if s := mapGet(it, "source"); s != nil {
					src = s.Value
				}
			}
			switch {
			case strings.Contains(src, "docker.sock"):
				r.DockerSocket = true
			case src == "/usr/share/zoneinfo", src == "/etc/localtime", src == "/etc/timezone":
			case strings.HasPrefix(src, "/") || strings.HasPrefix(src, "${") && !strings.Contains(src, ":-./"):
				add(&r.HostFolders, src)
			}
		}
	}
	return r
}

// volumeSource is the host side of a short volume entry; a ${VAR:-default}
// source keeps its colon.
func volumeSource(v string) string {
	if strings.HasPrefix(v, "${") {
		if end := strings.Index(v, "}"); end > 0 {
			rest, _, _ := strings.Cut(v[end+1:], ":")
			return v[:end+1] + rest
		}
	}
	src, _, _ := strings.Cut(v, ":")
	return src
}
