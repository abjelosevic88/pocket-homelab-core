package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
)

// Update moves an installed app to the catalog's newest release: it pulls
// the new image, stops the app and backs up its data folders (see
// databackup.go), edits only the image line of the compose file (everything
// else, comments included, stays), starts it and waits for /healthz. If the
// new version does not come up, the data from before and the old line go
// back and the old image runs again. Apps that follow a moving tag (latest, edge) or are built from
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
	loc, err := a.locate(cs, req.Slug, req.URL, composeServices(rel.compose))
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
	args := loc.composeArgs()

	// Download first, while the old version still runs.
	a.logf(j, "Downloading %s", rel.ref())
	if _, err := a.dk.run(ctx, "", "pull", "--quiet", rel.ref()); err != nil {
		return err
	}

	// Back up the data with the app stopped (SQLite must not be copied live).
	dirs, notes := dataDirs(loc.files, loc.wd)
	for _, n := range notes {
		res.Warnings = append(res.Warnings, "Backup: "+n+".")
	}
	var size int64
	for _, d := range dirs {
		size += treeSize(filepath.Join(loc.wd, d))
	}
	appBackups := filepath.Join(a.cfg.ArchiveDir, "updates", loc.c.Name)
	if err := os.MkdirAll(appBackups, 0o755); err != nil {
		return err
	}
	a.cfg.FilesOwner.chown(filepath.Join(a.cfg.ArchiveDir, "updates"))
	a.cfg.FilesOwner.chown(appBackups)
	if free, err := freeBytes(appBackups); err == nil && free < size+size/10+64<<20 {
		return fmt.Errorf("not enough disk space to back up %s first (%s needed, %s free)", loc.c.Name, humanBytes(size), humanBytes(free))
	}
	stamp := time.Now().Format("20060102-150405")
	backup := filepath.Join(appBackups, stamp+"-"+chk.current)
	if err := os.Mkdir(backup, 0o755); err != nil {
		return err
	}
	a.cfg.FilesOwner.chown(backup)
	a.logf(j, "Stopping %s to back up its data", loc.c.Name)
	if _, err := a.dk.run(ctx, loc.wd, append(args, "stop")...); err != nil {
		return err
	}
	if err := backupData(loc.wd, dirs, backup); err != nil {
		_, _ = a.dk.run(context.WithoutCancel(ctx), loc.wd, append(args, "up", "-d")...)
		return fmt.Errorf("backup failed, %s is unchanged and running again: %w", loc.c.Name, err)
	}
	if err := os.WriteFile(filepath.Join(backup, filepath.Base(chk.file)), old, fi.Mode().Perm()); err != nil {
		_, _ = a.dk.run(context.WithoutCancel(ctx), loc.wd, append(args, "up", "-d")...)
		return err
	}
	a.cfg.FilesOwner.chown(filepath.Join(backup, filepath.Base(chk.file)))
	if len(dirs) == 0 {
		a.logf(j, "No data folders to back up; kept the compose file in %s", backup)
	} else {
		a.logf(j, "Backed up %s (%s) to %s", strings.Join(dirs, ", "), humanBytes(size), backup)
	}

	restore := func(cause error) error {
		a.logf(j, "Update failed; going back to %s with the data from before", chk.current)
		ctx := context.WithoutCancel(ctx)
		_, _ = a.dk.run(ctx, loc.wd, append(args, "stop")...)
		_ = os.WriteFile(chk.file, old, fi.Mode().Perm())
		failed := backup + "-" + rel.Version + "-failed"
		if err := restoreData(loc.wd, dirs, backup, failed); err != nil {
			return fmt.Errorf("%w; restoring the data failed too, nothing was started (backup: %s): %v", cause, backup, err)
		}
		a.cfg.FilesOwner.chown(failed)
		if len(dirs) > 0 {
			a.logf(j, "Data restored; what %s left is in %s", rel.Version, failed)
		}
		if _, err := a.dk.run(ctx, loc.wd, append(args, "up", "-d")...); err != nil {
			return fmt.Errorf("%w; starting the old version failed too: %v", cause, err)
		}
		return cause
	}
	if err := os.WriteFile(chk.file, []byte(next), fi.Mode().Perm()); err != nil {
		return restore(err)
	}
	a.logf(j, "Starting %s %s", loc.c.Name, rel.Version)
	if _, err := a.dk.run(ctx, loc.wd, append(args, "up", "-d")...); err != nil {
		return restore(err)
	}
	if loc.port > 0 {
		if err := a.waitHealthy(ctx, loc.port); err != nil {
			return restore(err)
		}
	}
	pruneBackups(appBackups, keepUpdateBackups)
	a.logf(j, "%s %s is up", loc.c.Name, rel.Version)
	return nil
}
