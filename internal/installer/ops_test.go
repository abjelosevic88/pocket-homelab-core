package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"512B": 512, "1.5KiB": 1536, "23.5MiB": 24641536, "1GiB": 1 << 30, "2kB": 2000, "1.2GB": 1200000000} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v (want %d)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MiB", "12 parsecs", "--"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestClampAndTrimLogs(t *testing.T) {
	for in, want := range map[string]int{"": 200, "0": 200, "-4": 200, "x": 200, "50": 50, "2000": 2000, "99999": 2000} {
		if got := ClampLines(in); got != want {
			t.Errorf("ClampLines(%q) = %d, want %d", in, got, want)
		}
	}
	if lines, cut := TrimLogs("", 10); len(lines) != 0 || cut {
		t.Fatal("empty output")
	}
	lines, cut := TrimLogs("a\nb\nc\nd\n", 2)
	if strings.Join(lines, ",") != "c,d" || cut {
		t.Fatalf("last 2: %v %v", lines, cut)
	}
	big := strings.Repeat(strings.Repeat("x", 1000)+"\n", 1500) + "newest"
	lines, cut = TrimLogs(big, 2000)
	if !cut || lines[len(lines)-1] != "newest" || len(lines) > MaxLogBytes/1000 {
		t.Fatalf("size cap: %d lines, cut %v", len(lines), cut)
	}
}

func TestAppMemory(t *testing.T) {
	dir := t.TempDir()
	office := filepath.Join(dir, "pocket-office")
	os.MkdirAll(office, 0o755)
	os.WriteFile(filepath.Join(office, appMarker), []byte(`{"slug":"office","port":8300}`), 0o644)
	proj := func(name, wd string) map[string]string {
		return map[string]string{"com.docker.compose.project": name, "com.docker.compose.project.working_dir": wd}
	}
	cs := []container{
		{Name: "pocket-office", Labels: proj("pocket-office", office)},
		{Name: "pocket-office-onlyoffice", Labels: proj("pocket-office", office)},
		{Name: "pocket-notes", Labels: proj("notes", "/elsewhere/notes")},
		{Name: "pocket-core", Labels: proj("pocket", "/home/x/pocket")},
		{Name: "pocket-caddy", Labels: proj("pocket", "/home/x/pocket")},
		{Name: "jellyfin"},
		{Name: "searxng", Labels: proj("search", "/elsewhere/search")},
		{Name: "pocket-search", Labels: proj("search", "/elsewhere/search")},
	}
	stats := strings.Join([]string{
		`{"Name":"pocket-office","MemUsage":"30MiB / 7.6GiB"}`,
		`{"Name":"pocket-office-onlyoffice","MemUsage":"1GiB / 7.6GiB"}`,
		`{"Name":"pocket-notes","MemUsage":"12.5MiB / 7.6GiB"}`,
		`{"Name":"pocket-core","MemUsage":"40MiB / 7.6GiB"}`,
		`{"Name":"jellyfin","MemUsage":"300MiB / 7.6GiB"}`,
		`{"Name":"pocket-caddy","MemUsage":"20MiB / 7.6GiB"}`,
		`{"Name":"searxng","MemUsage":"100MiB / 7.6GiB"}`,
		`{"Name":"pocket-search","MemUsage":"10MiB / 7.6GiB"}`,
		`not json`,
	}, "\n")
	got := map[string]uint64{}
	for _, u := range appMemory(cs, stats) {
		got[u.Slug] = u.Memory
	}
	if got["office"] != 30<<20+1<<30 || got["notes"] != uint64(12.5*(1<<20)) || got["jellyfin"] != 0 {
		t.Fatalf("%v", got)
	}
	// Core's project carries Caddy too: each pocket-<name> container is its own; a sidecar goes with
	// its project's app.
	if got["core"] != 40<<20 || got["caddy"] != 20<<20 || got["search"] != 110<<20 {
		t.Fatalf("core: %v", got)
	}
}
