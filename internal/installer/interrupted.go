package installer

// Installs that were cut off, and downloads that didn't get through.
//
// The installer can stop in the middle of an install: Pocket Core and its installer are updated
// together, the machine reboots, Docker restarts. What is left then is the app's folder with its
// compose file and no working app, and until now the next try was refused ("already exists; move it
// away first"): the app could never be installed again without a terminal. Now a folder is marked
// while it is being installed into, and the next install clears a marked folder (or an unmarked one
// that holds nothing but the install's own files and has no container) and starts fresh. A folder with
// anything of the person's in it is never cleared: it is kept under the archive.
//
// Downloads: one "no route to host" to the registry used to fail the app. A home line drops for a few
// seconds now and then, so a download that fails on the network is tried again a few times.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// installingMarker sits in an app's folder from the moment it is written until the app answers.
const installingMarker = ".pocket-installing"

func markInstalling(dir string) {
	_ = os.WriteFile(filepath.Join(dir, installingMarker), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func doneInstalling(dir string) { _ = os.Remove(filepath.Join(dir, installingMarker)) }

// The files an install writes itself; a folder with only these has nothing of the person's.
var installFiles = map[string]bool{"docker-compose.yml": true, ".env": true, appMarker: true, installingMarker: true}

// onlyInstallFiles: the folder holds the install's own files and empty folders, nothing else.
func onlyInstallFiles(dir string) bool {
	clean := true
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			clean = false
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Dir(p) != dir || !installFiles[d.Name()] {
			clean = false
			return filepath.SkipAll
		}
		return nil
	})
	return clean
}

// leftover says whether dir is what a cut-off install of the app left: marked as being installed, or
// (installs from before the mark) nothing but the install's files and no container of that name.
func (a *Agent) leftover(dir, containerName string, cs []container) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, installingMarker)); err == nil {
		return true
	}
	for _, c := range cs {
		if c.Name == containerName {
			return false
		}
	}
	return onlyInstallFiles(dir)
}

// clearLeftover takes the half-made install away: its containers and networks, then the folder
// (deleted when it holds only the install's files, kept under the archive otherwise).
func (a *Agent) clearLeftover(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
		_, _ = a.dk.run(context.WithoutCancel(ctx), dir, "compose", "down", "--remove-orphans")
	}
	if onlyInstallFiles(dir) {
		return os.RemoveAll(dir)
	}
	_, err := a.archive(dir, "interrupted")
	return err
}

// networkTrouble: the download failed on the way (the line, DNS, the registry being busy), not
// because the image doesn't exist or isn't allowed.
func networkTrouble(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	for _, s := range []string{"no route to host", "i/o timeout", "connection reset", "connection refused", "tls handshake timeout",
		"temporary failure in name resolution", "server misbehaving", "no such host", "network is unreachable", "unexpected eof",
		"502 bad gateway", "503 service unavailable", "504 gateway", "toomanyrequests", "request canceled while waiting"} {
		if strings.Contains(e, s) {
			return true
		}
	}
	return false
}

// pull runs a docker pull (or compose pull) and tries again when the network let it down.
func (a *Agent) pull(ctx context.Context, j *Job, dir string, args ...string) error {
	var err error
	for try := 0; ; try++ {
		if _, err = a.dk.run(ctx, dir, args...); err == nil || !networkTrouble(err) || try >= len(a.pullWaits) {
			return err
		}
		wait := a.pullWaits[try]
		a.logf(j, "The download didn't get through (the network dropped); trying again in %s", wait)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}
