package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
)

// Apps that run as a program on the machine (Pocket AI) have no
// docker-compose.yml; their GitHub release carries one archive per
// platform instead: <program>_<tag>_<os>_<arch>.tar.gz. The catalog lists
// them under "binaries" with each file's address and SHA-256, so the app
// can update itself.

type ghAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>" (GitHub fills it for new uploads)
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// binaryRelease reads the newest tag's release downloads.
func (g *GitHub) binaryRelease(ctx context.Context, name, desc string) (*appstore.Binary, error) {
	tag, err := g.latestTag(ctx, name)
	if err != nil {
		return nil, err
	}
	b, err := g.get(ctx, fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", g.api, g.cfg.Owner, name, tag), true)
	if err != nil {
		return nil, fmt.Errorf("%s: no GitHub release yet (%v)", tag, err)
	}
	var rel struct {
		Draft  bool      `json:"draft"`
		Assets []ghAsset `json:"assets"`
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, fmt.Errorf("%s release: %w", tag, err)
	}
	if rel.Draft {
		return nil, fmt.Errorf("%s release is a draft", tag)
	}
	re := regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)_` + regexp.QuoteMeta(tag) + `_([a-z0-9]+)_([a-z0-9]+)\.tar\.gz$`)
	bin := &appstore.Binary{Repo: name, RepoURL: "https://github.com/" + g.cfg.Owner + "/" + name, Description: desc,
		Version: strings.TrimPrefix(tag, "v"), Tag: tag, Files: map[string]appstore.BinaryFile{}}
	for _, a := range rel.Assets {
		m := re.FindStringSubmatch(a.Name)
		if m == nil {
			continue
		}
		if bin.Program == "" {
			bin.Program = m[1]
		} else if bin.Program != m[1] {
			return nil, fmt.Errorf("%s release: archives of two programs (%s, %s)", tag, bin.Program, m[1])
		}
		sum := strings.TrimPrefix(a.Digest, "sha256:")
		if !hexSHA256.MatchString(sum) {
			if sum, err = g.sha256Of(ctx, a.URL); err != nil {
				return nil, fmt.Errorf("%s: %w", a.Name, err)
			}
		}
		bin.Files[m[2]+"/"+m[3]] = appstore.BinaryFile{URL: a.URL, SHA256: sum, Size: a.Size}
	}
	if len(bin.Files) == 0 {
		return nil, fmt.Errorf("%s: no docker-compose.yml and no <program>_%s_<os>_<arch>.tar.gz downloads", tag, tag)
	}
	bin.Slug = strings.TrimPrefix(bin.Program, "pocket-")
	if !slugOK(bin.Slug) {
		return nil, fmt.Errorf("%s: no usable slug in %q", tag, bin.Program)
	}
	return bin, nil
}

// sha256Of downloads a release file (up to 512 MB) and hashes it.
func (g *GitHub) sha256Of(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "pocket-core-catalog")
	res, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", res.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(res.Body, 512<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// buildBinaries turns the repos Catalog left out for lack of a compose file
// into binary entries; the rest is logged with why it is not in the catalog.
func buildBinaries(ctx context.Context, gh *GitHub, o BuildOptions, meta map[string]siteApp, logf func(string, ...any)) []appstore.Binary {
	prevByRepo := map[string]*appstore.Binary{}
	if o.Previous != nil {
		for i := range o.Previous.Binaries {
			prevByRepo[o.Previous.Binaries[i].Repo] = &o.Previous.Binaries[i]
		}
	}
	base := strings.TrimRight(o.SiteBase, "/")
	var out []appstore.Binary
	seen := map[string]bool{}
	for _, f := range gh.failed {
		if !errors.Is(f.Err, errNoCompose) {
			logf("%s: %v; left out", f.Name, f.Err)
			continue
		}
		bin, err := gh.binaryRelease(ctx, f.Name, f.Description)
		if err != nil {
			if prev := prevByRepo[f.Name]; prev != nil {
				logf("%s: %v; keeping %s", f.Name, err, prev.Tag)
				out = append(out, *prev)
				seen[prev.Slug] = true
			} else {
				logf("%s: %v; left out", f.Name, err)
			}
			continue
		}
		if seen[bin.Slug] || gh.cfg.protected(bin.Slug) {
			continue
		}
		seen[bin.Slug] = true
		bin.Name = appDisplayName(bin.Slug)
		if m, ok := meta[bin.Slug]; ok {
			bin.Name = firstNonEmpty(m.Name, bin.Name)
			bin.Description = firstNonEmpty(m.Summary, bin.Description)
			bin.Category = m.Category
			switch {
			case strings.HasPrefix(m.Icon, "https://"):
				bin.Icon = m.Icon
			case m.Icon != "" && base != "":
				bin.Icon = base + m.Icon
			}
			if base != "" {
				bin.Page = base + "/apps/" + bin.Slug + "/"
			}
		}
		out = append(out, *bin)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}
