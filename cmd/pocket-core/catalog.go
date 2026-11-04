package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
)

// runCatalog builds, signs and checks the app catalog installers read (see
// docs/installer.md). CI runs `catalog build` on a schedule.
func runCatalog(args []string) {
	if len(args) == 0 {
		args = []string{"help"}
	}
	switch args[0] {
	case "build":
		catalogBuild(args[1:])
	case "verify":
		catalogVerify(args[1:])
	case "keygen":
		id := "pocket"
		if len(args) > 1 {
			id = args[1]
		}
		priv, pub, err := appstore.GenerateKey(id)
		if err != nil {
			fail(err)
		}
		fmt.Printf("private (keep secret, CATALOG_SIGNING_KEY): %s\npublic  (INSTALLER_CATALOG_KEYS):          %s\n", priv, pub)
	default:
		fmt.Println(`pocket-core catalog — the signed list of installable apps

  catalog build [flags]     read every app repo, sign the catalog with $CATALOG_SIGNING_KEY
      -owner abjelosevic88  -prefix pocket-homelab-
      -site-meta <url|file> website app list (names, categories, icons)
      -site-base https://pockethomelab.com
      -previous <url|file>  the published catalog: apps without an image yet keep their entry
      -out apps.json        -skip-unchanged: write nothing when no release changed
      GITHUB_TOKEN raises GitHub's API limit
  catalog verify <url|file> check the signature and list the apps
  catalog keygen [id]       make a signing key pair`)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "catalog:", err)
	os.Exit(1)
}

func readSource(ctx context.Context, src string) ([]byte, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", src, res.Status)
		}
		return io.ReadAll(io.LimitReader(res.Body, 16<<20))
	}
	return os.ReadFile(src)
}

func catalogBuild(args []string) {
	fs := flag.NewFlagSet("catalog build", flag.ExitOnError)
	owner := fs.String("owner", "abjelosevic88", "GitHub account")
	prefix := fs.String("prefix", "pocket-homelab-", "repo name prefix")
	siteMeta := fs.String("site-meta", "", "website app list (URL or file)")
	siteBase := fs.String("site-base", "https://pockethomelab.com", "website address for icons and pages")
	previous := fs.String("previous", "", "the published catalog (URL or file)")
	out := fs.String("out", "apps.json", "output file")
	skip := fs.Bool("skip-unchanged", false, "write nothing when no release changed")
	fs.Parse(args)
	key := strings.TrimSpace(os.Getenv("CATALOG_SIGNING_KEY"))
	if f := os.Getenv("CATALOG_SIGNING_KEY_FILE"); key == "" && f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			fail(err)
		}
		key = strings.TrimSpace(string(b))
	}
	pub, err := appstore.PublicKey(key)
	if err != nil {
		fail(fmt.Errorf("CATALOG_SIGNING_KEY: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var prev *appstore.Catalog
	if *previous != "" {
		keys, _ := appstore.ParseKeys(append(append([]string{}, appstore.OfficialKeys...), pub))
		if b, err := readSource(ctx, *previous); err != nil {
			fmt.Fprintln(os.Stderr, "no previous catalog:", err)
		} else if prev, _, err = appstore.Verify(b, keys); err != nil {
			fail(fmt.Errorf("previous catalog: %w", err))
		}
	}
	c, err := installer.BuildCatalog(ctx, installer.BuildOptions{Owner: *owner, RepoPrefix: *prefix, GitHubToken: os.Getenv("GITHUB_TOKEN"),
		SiteMeta: *siteMeta, SiteBase: *siteBase, Previous: prev, Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }})
	if err != nil {
		fail(err)
	}
	if *skip && appstore.SameApps(prev, c) {
		fmt.Printf("unchanged: %d apps\n", len(c.Apps))
		return
	}
	b, err := appstore.Sign(c, key)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s: %d apps\n", *out, len(c.Apps))
	if prev != nil {
		was := map[string]string{}
		for _, a := range prev.Apps {
			was[a.Slug] = a.Version
		}
		for _, a := range c.Apps {
			if v, ok := was[a.Slug]; !ok {
				fmt.Printf("  new  %s %s\n", a.Slug, a.Version)
			} else if v != a.Version {
				fmt.Printf("  %s %s → %s\n", a.Slug, v, a.Version)
			}
		}
	}
}

func catalogVerify(args []string) {
	if len(args) == 0 {
		args = []string{appstore.DefaultURL}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b, err := readSource(ctx, args[0])
	if err != nil {
		fail(err)
	}
	specs := append([]string{}, appstore.OfficialKeys...)
	if extra := os.Getenv("INSTALLER_CATALOG_KEYS"); extra != "" {
		specs = append(specs, strings.Split(extra, ",")...)
	}
	keys, err := appstore.ParseKeys(specs)
	if err != nil {
		fail(err)
	}
	c, id, err := appstore.Verify(b, keys)
	if err != nil {
		fail(err)
	}
	fmt.Printf("signed by %s, generated %s, %d apps\n", id, c.Generated.Format(time.RFC3339), len(c.Apps))
	for _, a := range c.Apps {
		fmt.Printf("  %-12s %-8s %s\n", a.Slug, a.Version, strings.Join(a.Platforms, " "))
	}
}
