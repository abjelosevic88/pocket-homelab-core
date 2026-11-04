package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
)

// Update moves an installed app to the catalog's newest release: it edits
// only the image line of the app's compose file (everything else, comments
// included, stays), pulls, restarts, and waits for /healthz. If the new
// version does not come up, the old line goes back and the old image runs
// again. Apps that follow a moving tag (latest, edge) or are built from
// source on this server are left alone.

func supports(platforms []string) bool {
	a := appstore.App{Platforms: platforms}
	return a.Supports(runtime.GOOS, runtime.GOARCH)
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// semverLess reports a < b for plain X.Y.Z versions.
func semverLess(a, b string) bool {
	x, y := semverRe.FindStringSubmatch(a), semverRe.FindStringSubmatch(b)
	if x == nil || y == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		p, _ := strconv.Atoi(x[i])
		q, _ := strconv.Atoi(y[i])
		if p != q {
			return p < q
		}
	}
	return false
}

type updateCheck struct {
	file    string // compose file holding the image line
	line    string // the line as it is
	current string // its tag
	newer   bool   // the release is newer than current
	err     error  // why Update can't touch it
}

// imageLineRe matches `image: <image>[:tag][@digest]`, quoted or not.
func imageLineRe(image string) *regexp.Regexp {
	return regexp.MustCompile(`(?mi)^(\s*image:\s*["']?)` + regexp.QuoteMeta(image) + `(?::([\w][\w.-]*))?(?:@sha256:[0-9a-f]{64})?(["']?)([ \t]*#.*)?$`)
}

// checkUpdate finds the app's image line and says whether Update can move it.
func (a *Agent) checkUpdate(c *container, rel *Release) updateCheck {
	_, _, files := c.project()
	re := imageLineRe(rel.Image)
	var chk updateCheck
	found := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			found++
			chk.file, chk.line, chk.current = f, m[0], m[2]
		}
		if found > 0 && builtHere(b, rel.Image) {
			chk.err = errors.New("built from source on this server; update it by hand")
			return chk
		}
	}
	switch {
	case len(files) == 0:
		chk.err = errors.New("not started with docker compose")
	case found == 0:
		chk.err = fmt.Errorf("its compose file does not run %s", rel.Image)
	case found > 1:
		chk.err = fmt.Errorf("%s appears more than once in its compose files", rel.Image)
	case chk.current == "":
		chk.err = errors.New("runs the image without a tag; update it by hand")
	case !semverRe.MatchString(chk.current):
		chk.err = fmt.Errorf("follows the %q tag, so something else keeps it current", chk.current)
	default:
		chk.newer = semverLess(chk.current, rel.Version)
	}
	return chk
}

// builtHere: the service running the image also has a build: section.
func builtHere(compose []byte, image string) bool {
	var doc yaml.Node
	if yaml.Unmarshal(compose, &doc) != nil || len(doc.Content) == 0 {
		return false
	}
	svcs := mapGet(doc.Content[0], "services")
	if svcs == nil {
		return false
	}
	for i := 0; i+1 < len(svcs.Content); i += 2 {
		svc := svcs.Content[i+1]
		if img := mapGet(svc, "image"); img != nil && imageBase(img.Value) == image {
			return mapGet(svc, "build") != nil
		}
	}
	return false
}

func (a *Agent) update(ctx context.Context, j *Job, req UpdateRequest) error {
	rel, err := a.src.Find(ctx, req.Slug)
	if err != nil {
		return err
	}
	if !supports(rel.Platforms) {
		return fmt.Errorf("%s %s has no image for %s/%s", rel.Repo, rel.Tag, runtime.GOOS, runtime.GOARCH)
	}
	cs, err := a.dk.containers(ctx)
	if err != nil {
		return err
	}
	loc, err := a.locate(cs, req.Slug, req.URL)
	if err != nil {
		return err
	}
	chk := a.checkUpdate(loc.c, rel)
	if chk.err != nil {
		return fmt.Errorf("%s %s", loc.c.Name, chk.err)
	}
	if !chk.newer {
		return fmt.Errorf("%s already runs %s (the newest is %s)", loc.c.Name, chk.current, rel.Version)
	}
	res := &Result{Dir: loc.wd, Container: loc.c.Name, Port: loc.port, Version: rel.Version}
	a.setResult(j, res)
	a.logf(j, "Updating %s from %s to %s", loc.c.Name, chk.current, rel.Version)

	fi, err := os.Stat(chk.file)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(chk.file)
	if err != nil {
		return err
	}
	re := imageLineRe(rel.Image)
	next := re.ReplaceAllStringFunc(string(old), func(line string) string {
		m := re.FindStringSubmatch(line)
		return m[1] + rel.ref() + m[3] + m[4]
	})
	backup := fmt.Sprintf("%s.bak-update-%s", chk.file, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, old, fi.Mode().Perm()); err != nil {
		return err
	}
	a.cfg.FilesOwner.chown(backup)
	if err := os.WriteFile(chk.file, []byte(next), fi.Mode().Perm()); err != nil {
		return err
	}
	a.logf(j, "Compose file backup: %s", backup)
	args := loc.composeArgs()
	restore := func(cause error) error {
		a.logf(j, "Update failed; going back to %s", chk.current)
		ctx := context.WithoutCancel(ctx)
		_ = os.WriteFile(chk.file, old, fi.Mode().Perm())
		if _, err := a.dk.run(ctx, loc.wd, append(args, "up", "-d")...); err != nil {
			return fmt.Errorf("%w; starting the old version failed too: %v", cause, err)
		}
		return cause
	}
	a.logf(j, "Downloading %s", rel.ref())
	if _, err := a.dk.run(ctx, loc.wd, append(args, "pull", "--quiet")...); err != nil {
		return restore(err)
	}
	a.logf(j, "Restarting %s", loc.c.Name)
	if _, err := a.dk.run(ctx, loc.wd, append(args, "up", "-d")...); err != nil {
		return restore(err)
	}
	if loc.port > 0 {
		if err := a.waitHealthy(ctx, loc.port); err != nil {
			return restore(err)
		}
	}
	a.logf(j, "%s %s is up", loc.c.Name, rel.Version)
	return nil
}
