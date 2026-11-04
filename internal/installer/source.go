package installer

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
)

// catalogSource reads the signed app catalog (INSTALLER_CATALOG_URL). A
// catalog whose signature does not check out is never used; when the
// download fails the last good copy still holds.
type catalogSource struct {
	cfg  *Config
	keys []appstore.Key
	http *http.Client

	mu      sync.Mutex
	cached  []Release
	fetched time.Time
	from    string // URL the cached copy came from
}

func newCatalogSource(cfg *Config, keys []appstore.Key) *catalogSource {
	return &catalogSource{cfg: cfg, keys: keys, http: &http.Client{Timeout: 30 * time.Second}}
}

func (s *catalogSource) Catalog(ctx context.Context, refresh bool) ([]Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !refresh && s.cached != nil && time.Since(s.fetched) < catalogTTL {
		return s.cached, nil
	}
	var errs []error
	for _, u := range s.cfg.CatalogURLs {
		c, err := appstore.Fetch(ctx, s.http, u, s.keys)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rels := make([]Release, 0, len(c.Apps))
		for i := range c.Apps {
			if a := &c.Apps[i]; slugOK(a.Slug) && !s.cfg.protected(a.Slug) {
				rels = append(rels, releaseFrom(a))
			}
		}
		s.cached, s.fetched, s.from = rels, time.Now(), u
		return rels, nil
	}
	if s.cached != nil {
		return s.cached, nil
	}
	if len(errs) == 1 {
		return nil, errs[0]
	}
	return nil, fmt.Errorf("no app catalog could be read: %v", errs)
}

func (s *catalogSource) Find(ctx context.Context, slug string) (*Release, error) {
	for _, refresh := range []bool{false, true} {
		rels, err := s.Catalog(ctx, refresh)
		if err != nil {
			return nil, err
		}
		for i := range rels {
			if rels[i].Slug == slug {
				r := rels[i]
				return &r, nil
			}
		}
	}
	return nil, fmt.Errorf("the app catalog has no %q", slug)
}
