package installer

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Remove archives an app's folder, data included. Install can bring that
// data back: it copies the archived data folders into the new install
// (the archive stays as it is) and reuses the archived secrets, which some
// apps encrypt stored passwords and tokens with. The app's old Core token
// is left out: Core deleted it on removal, and apps never re-enroll on
// their own when it is refused, so the new install registers afresh.

// ArchiveInfo is a removed app's archived folder.
type ArchiveInfo struct {
	Name    string    `json:"name"` // folder name in the archive
	SavedAt time.Time `json:"saved_at"`
}

var archiveRe = regexp.MustCompile(`^(.+)-(\d{8}-\d{6})$`)

// archivesFor lists the archived folders of an app (by its folder names),
// newest first. Failed installs (…-failed) are not offered.
func (a *Agent) archivesFor(names ...string) []ArchiveInfo {
	es, err := os.ReadDir(a.cfg.ArchiveDir)
	if err != nil {
		return nil
	}
	var out []ArchiveInfo
	for _, e := range es {
		m := archiveRe.FindStringSubmatch(e.Name())
		if !e.IsDir() || m == nil || !slices.Contains(names, m[1]) {
			continue
		}
		t, err := time.ParseInLocation("20060102-150405", m[2], time.Local)
		if err != nil {
			continue
		}
		out = append(out, ArchiveInfo{Name: e.Name(), SavedAt: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out
}

// archivedDir resolves a restore_from name to a folder in the archive that
// belongs to the container, refusing anything else.
func (a *Agent) archivedDir(name string, names ...string) (string, error) {
	m := archiveRe.FindStringSubmatch(name)
	if name != filepath.Base(name) || m == nil || !slices.Contains(names, m[1]) {
		return "", fmt.Errorf("%q is not an archived folder of this app", name)
	}
	dir := filepath.Join(a.cfg.ArchiveDir, name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("no archived folder %s", dir)
	}
	return dir, nil
}

// oldSettings reads the values an archived install had: its .env, plus the
// literal environment values of its compose files (hand-written stacks keep
// secrets there).
func oldSettings(dir string) map[string]string {
	vals := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "="); ok {
				vals[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*compose*.y*ml"))
	for _, f := range files {
		if strings.Contains(f, ".bak") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc yaml.Node
		if yaml.Unmarshal(b, &doc) != nil || len(doc.Content) == 0 {
			continue
		}
		svcs := mapGet(doc.Content[0], "services")
		if svcs == nil || svcs.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(svcs.Content); i += 2 {
			envs := mapGet(svcs.Content[i+1], "environment")
			if envs == nil {
				continue
			}
			add := func(k, v string) {
				if _, ok := vals[k]; !ok && v != "" && !strings.Contains(v, "${") {
					vals[k] = v
				}
			}
			switch envs.Kind {
			case yaml.MappingNode:
				for j := 0; j+1 < len(envs.Content); j += 2 {
					add(envs.Content[j].Value, envs.Content[j+1].Value)
				}
			case yaml.SequenceNode:
				for _, it := range envs.Content {
					if k, v, ok := strings.Cut(it.Value, "="); ok {
						add(k, v)
					}
				}
			}
		}
	}
	return vals
}

// keepOldSecrets swaps the plan's freshly generated values for the archived
// ones, so data encrypted with them stays readable. Core's registration key
// is always the current one.
func keepOldSecrets(p *Plan, old map[string]string) []string {
	var kept []string
	for i, e := range p.Env {
		if e.Key == "CORE_REGISTRATION_KEY" || e.Key == "DOCKER_GID" {
			continue
		}
		if v, ok := old[e.Key]; ok && v != "" && v != e.Value {
			p.Env[i].Value = v
			kept = append(kept, e.Key)
		}
	}
	return kept
}

// coreTokenFiles are the files the SDK keeps the app's Core registration in.
var coreTokenFiles = []string{"core.token", "core.token.oidc.json"}

// restoreInto copies the archived data folders the new install uses into
// dir (replacing the empty ones writePlan made) and drops the old Core
// token from the copy. It returns the folders it brought back.
func restoreInto(dir, archived string, dirs []string) ([]string, error) {
	var got []string
	for _, d := range parentsOnly(dirs) {
		src := filepath.Join(archived, d)
		if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
			continue
		}
		dst := filepath.Join(dir, d)
		if err := removeEmptyDirs(dst); err != nil {
			return got, err
		}
		if err := copyTree(src, dst); err != nil {
			return got, fmt.Errorf("bringing back %s: %w", d, err)
		}
		for _, f := range coreTokenFiles {
			_ = os.Remove(filepath.Join(dst, f))
		}
		got = append(got, d)
	}
	return got, nil
}

// parentsOnly drops folders whose parent is in the list too.
func parentsOnly(dirs []string) []string {
	ds := slices.Clone(dirs)
	sort.Strings(ds)
	var out []string
	for _, d := range ds {
		if len(out) > 0 && (d == out[len(out)-1] || strings.HasPrefix(d, out[len(out)-1]+string(filepath.Separator))) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// removeEmptyDirs removes a folder tree that holds no files (what writePlan
// made); anything with a file in it is left alone and reported.
func removeEmptyDirs(dir string) error {
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return fmt.Errorf("%s is not empty", dir)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
