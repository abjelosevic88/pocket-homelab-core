package installer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// What a cut-off install leaves is cleared; a folder with the person's data never is.
func TestLeftover(t *testing.T) {
	apps := t.TempDir()
	a := &Agent{cfg: &Config{AppsDir: apps, ArchiveDir: filepath.Join(apps, "pocket-archive")}, dk: &Docker{Bin: "true"}, jobs: map[string]*Job{}}
	none := []container{}

	// 1. Only the install's own files, no container (an install from before the mark): leftover, deleted.
	old := filepath.Join(apps, "pocket-home")
	write(t, filepath.Join(old, "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(old, ".env"), "A=1\n")
	if err := os.MkdirAll(filepath.Join(old, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.leftover(old, "pocket-home", none) {
		t.Fatal("a folder with only the install's files and no container is a leftover")
	}
	if a.leftover(old, "pocket-home", []container{{Name: "pocket-home"}}) {
		t.Fatal("with its container there and no mark it is a running app, not a leftover")
	}
	if err := a.clearLeftover(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover not removed: %v", err)
	}

	// 2. Data inside and no mark: someone's app that is merely stopped. Never a leftover.
	kept := filepath.Join(apps, "pocket-notes")
	write(t, filepath.Join(kept, "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(kept, "data", "notes.db"), "precious")
	if a.leftover(kept, "pocket-notes", none) {
		t.Fatal("a folder with data and no mark must not be cleared")
	}

	// 3. Marked as being installed, with restored data in it: leftover, but kept under the archive.
	cut := filepath.Join(apps, "pocket-photos")
	write(t, filepath.Join(cut, "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(cut, "data", "restored.db"), "from the archive")
	markInstalling(cut)
	if !a.leftover(cut, "pocket-photos", []container{{Name: "pocket-photos"}}) {
		t.Fatal("a marked folder is a leftover even with a container")
	}
	if err := a.clearLeftover(context.Background(), cut); err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.Glob(filepath.Join(apps, "pocket-archive", "pocket-photos-*-interrupted", "data", "restored.db"))
	if len(got) != 1 {
		t.Fatalf("data of the interrupted install not kept under the archive: %v", got)
	}

	// 4. The mark goes once the app answers.
	fresh := filepath.Join(apps, "pocket-tasks")
	write(t, filepath.Join(fresh, "docker-compose.yml"), "services: {}\n")
	markInstalling(fresh)
	doneInstalling(fresh)
	if _, err := os.Stat(filepath.Join(fresh, installingMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("mark still there after the install finished")
	}
}

// A download the network let down is tried again; one that fails for another reason is not.
func TestPullRetries(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	fake := filepath.Join(dir, "docker")
	script := `#!/bin/sh
n=$(cat "` + count + `" 2>/dev/null || echo 0); n=$((n + 1)); echo $n > "` + count + `"
if [ "$MODE" = denied ]; then echo "pull access denied for x, repository does not exist" >&2; exit 1; fi
if [ $n -le 2 ]; then echo 'failed to do request: Head "https://ghcr.io/v2/x/manifests/latest": dial tcp 140.82.121.34:443: connect: no route to host' >&2; exit 1; fi
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tries := func() int { b, _ := os.ReadFile(count); n, _ := strconv.Atoi(strings.TrimSpace(string(b))); return n }
	a := &Agent{cfg: &Config{}, dk: &Docker{Bin: fake, env: []string{"PATH=" + os.Getenv("PATH")}}, jobs: map[string]*Job{},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), pullWaits: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}}
	j := &Job{}
	if err := a.pull(context.Background(), j, dir, "compose", "pull"); err != nil {
		t.Fatalf("pull after two network failures: %v", err)
	}
	if tries() != 3 || len(j.Log) != 2 || !strings.Contains(j.Log[0], "trying again") {
		t.Fatalf("tries %d, log %q", tries(), j.Log)
	}
	_ = os.Remove(count)
	a.dk.env = append(a.dk.env, "MODE=denied")
	if err := a.pull(context.Background(), j, dir, "compose", "pull"); err == nil || tries() != 1 {
		t.Fatalf("an image that doesn't exist must fail at once: err %v, tries %d", err, tries())
	}
}
