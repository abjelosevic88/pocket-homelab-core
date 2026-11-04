package installer

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Before Update starts a new version it stops the app and copies its data
// folders (the stack's relative bind mounts, e.g. ./data) next to the
// archive. A new version migrates its database on start; if it then fails,
// the copy goes back before the old version starts again, so the old
// version never meets a database it doesn't know. Host folders mounted by
// absolute path (media, documents) are not copied: apps don't migrate those.

// keepUpdateBackups is how many backups per app are kept after updates.
const keepUpdateBackups = 3

// dataDirs lists the relative bind-mount folders of every service in the
// compose files (relative to wd), parents only, and notes the named
// volumes it can't copy.
func dataDirs(files []string, wd string) (dirs, notes []string) {
	seen := map[string]bool{}
	for _, f := range files {
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
		base := filepath.Dir(f)
		for i := 0; i+1 < len(svcs.Content); i += 2 {
			vols := mapGet(svcs.Content[i+1], "volumes")
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
				case strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || src == ".":
					rel, err := filepath.Rel(wd, filepath.Join(base, src))
					if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
						continue // outside the app's folder
					}
					if !seen[rel] {
						seen[rel] = true
						dirs = append(dirs, rel)
					}
				case src != "" && !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "$") && !strings.HasPrefix(src, "~") && !strings.Contains(src, "docker.sock"):
					notes = append(notes, "named volume "+src+" is not copied")
				}
			}
		}
	}
	sort.Strings(dirs)
	var out []string
	for _, d := range dirs { // a parent covers its children
		if len(out) > 0 && strings.HasPrefix(d, out[len(out)-1]+string(filepath.Separator)) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(wd, d)); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out, notes
}

// treeSize is the bytes under path.
func treeSize(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func freeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// copyTree copies src to dst (which must not exist): folders, files and
// symlinks with their modes and times, and their owners when run as root.
func copyTree(src, dst string) error {
	root := os.Geteuid() == 0
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.IsDir():
			if err := os.Mkdir(target, fi.Mode().Perm()); err != nil {
				return err
			}
		case fi.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			if err := copyFile(p, target, fi.Mode().Perm()); err != nil {
				return err
			}
		default:
			return nil // sockets, fifos: nothing to keep
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && root {
			_ = os.Lchown(target, int(st.Uid), int(st.Gid))
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			_ = os.Chmod(target, fi.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))
			_ = os.Chtimes(target, fi.ModTime(), fi.ModTime())
		}
		return nil
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// backupData copies each data folder of wd into dest.
func backupData(wd string, dirs []string, dest string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dest, d)), 0o755); err != nil {
			return err
		}
		if err := copyTree(filepath.Join(wd, d), filepath.Join(dest, d)); err != nil {
			return fmt.Errorf("copying %s: %w", d, err)
		}
	}
	return nil
}

// restoreData puts the backed-up data folders back. What the failed version
// left is moved to failed (kept for reading), never deleted; the backup
// itself stays as it was.
func restoreData(wd string, dirs []string, backup, failed string) error {
	var errs []error
	for _, d := range dirs {
		cur := filepath.Join(wd, d)
		if _, err := os.Lstat(cur); err == nil {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(failed, d)), 0o755); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := os.Rename(cur, filepath.Join(failed, d)); err != nil {
				errs = append(errs, fmt.Errorf("moving %s aside: %w", d, err))
				continue
			}
		}
		if err := copyTree(filepath.Join(backup, d), cur); err != nil {
			errs = append(errs, fmt.Errorf("restoring %s: %w", d, err))
		}
	}
	return errors.Join(errs...)
}

// pruneBackups keeps the newest `keep` backups in dir (names sort by time).
func pruneBackups(dir string, keep int) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range es {
		if e.IsDir() && !strings.HasSuffix(e.Name(), "-failed") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i := 0; i < len(names)-keep; i++ {
		_ = os.RemoveAll(filepath.Join(dir, names[i]))
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
