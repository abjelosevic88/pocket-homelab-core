package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Day-to-day operations on an installed app, for Core's app page and the phone app: restart it, read
// its recent log, and what the apps weigh (memory per app, the apps folder's disk). Unlike Install,
// Update and Remove these change no files, so they don't wait for (or block) the one-at-a-time work
// lock; only one job per app runs at a time.

// RestartRequest asks the agent to restart one app. URL is the address the app registered with Core.
type RestartRequest struct {
	Slug string `json:"slug"`
	URL  string `json:"url"`
}

// Logs is an app's recent log lines, oldest first.
type Logs struct {
	Slug      string   `json:"slug"`
	Source    string   `json:"source"` // where they came from: "docker compose logs (…)", "journalctl -u …"
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated,omitempty"` // cut to MaxLogBytes
}

// Resources is what the apps take on this machine.
type Resources struct {
	Disk *DiskUsage `json:"disk,omitempty"` // the filesystem the apps folder is on
	Apps []AppUsage `json:"apps"`
}

// DiskUsage is one filesystem's size and free space.
type DiskUsage struct {
	Path  string `json:"path"`
	Total uint64 `json:"total_bytes"`
	Free  uint64 `json:"free_bytes"` // available to unprivileged users
}

// AppUsage is one app's memory (all its containers or units together).
type AppUsage struct {
	Slug   string `json:"slug"`
	Memory uint64 `json:"memory_bytes"`
}

const (
	MaxLogLines     = 2000
	DefaultLogLines = 200
	MaxLogBytes     = 1 << 20
)

// ClampLines keeps a requested line count within 1..MaxLogLines (0 or junk = the default).
func ClampLines(raw string) int {
	n, err := strconv.Atoi(raw)
	switch {
	case err != nil || n <= 0:
		return DefaultLogLines
	case n > MaxLogLines:
		return MaxLogLines
	}
	return n
}

// TrimLogs splits output into lines, keeps the last n and at most MaxLogBytes (newest kept).
func TrimLogs(out string, n int) ([]string, bool) {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return []string{}, false
	}
	lines := strings.Split(out, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	size, cut := 0, false
	for i := len(lines) - 1; i >= 0; i-- {
		size += len(lines[i]) + 1
		if size > MaxLogBytes {
			lines, cut = lines[i+1:], true
			break
		}
	}
	return lines, cut
}

func (a *Agent) opsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/restart", func(w http.ResponseWriter, r *http.Request) {
		var req RestartRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || !slugOK(req.Slug) {
			writeErr(w, http.StatusBadRequest, "slug is required")
			return
		}
		if a.cfg.protected(req.Slug) {
			writeErr(w, http.StatusForbidden, req.Slug+" is not restarted from here")
			return
		}
		t, err := a.target(r.Context(), req.Slug, req.URL)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		a.startFree(w, "restart", req.Slug, func(ctx context.Context, j *Job) error { return a.restart(ctx, j, t) })
	})
	mux.HandleFunc("GET /v1/logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		slug := q.Get("slug")
		if !slugOK(slug) {
			writeErr(w, http.StatusBadRequest, "slug is required")
			return
		}
		t, err := a.target(r.Context(), slug, q.Get("url"))
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		out, err := a.logs(r.Context(), t, ClampLines(q.Get("lines")))
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		out.Slug = slug
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.resources(r.Context()))
	})
}

// opsTarget is what restart and logs act on: the app's own compose project when it has one to
// itself, else just its container.
type opsTarget struct {
	loc  *located // nil = the container alone
	name string   // the container
	solo bool     // its project runs nothing else (its log lines need no container prefix)
	port int      // where it answers /healthz on this machine (0 = unknown)
}

func (a *Agent) target(ctx context.Context, slug, appURL string) (*opsTarget, error) {
	cs, err := a.dk.containers(ctx)
	if err != nil {
		return nil, err
	}
	var own map[string]bool
	if rel, err := a.src.Find(ctx, slug); err == nil {
		own = composeServices(rel.compose)
	}
	if loc, err := a.locate(cs, slug, appURL, own); err == nil {
		n := 0
		for _, c := range cs {
			if p, _, _ := c.project(); p == loc.proj {
				n++
			}
		}
		return &opsTarget{loc: loc, name: loc.c.Name, port: loc.port, solo: n == 1}, nil
	} else if strings.HasPrefix(err.Error(), "no container") {
		return nil, fmt.Errorf("no container on this server runs %s; it may run as a program on the machine (restart it and read its log there)", displayName(slug))
	}
	// Started some other way (outside the apps folder, a shared project): the container alone.
	port := 0
	if u, err := url.Parse(appURL); err == nil {
		port, _ = strconv.Atoi(u.Port())
	}
	for i := range cs {
		c := &cs[i]
		if port > 0 && (containsInt(c.Ports, port) || c.Host && c.Env["PORT"] == strconv.Itoa(port)) || port == 0 && c.Name == "pocket-"+slug {
			return &opsTarget{name: c.Name, port: port}, nil
		}
	}
	return nil, fmt.Errorf("no container on this server runs %s", displayName(slug))
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// startFree starts a job without the work lock (see run).
func (a *Agent) startFree(w http.ResponseWriter, kind, slug string, fn func(context.Context, *Job) error) {
	a.run(w, kind, slug, false, fn)
}

func (a *Agent) restart(ctx context.Context, j *Job, t *opsTarget) error {
	a.setResult(j, &Result{Container: t.name, Port: t.port})
	if t.loc != nil {
		a.logf(j, "Restarting %s (docker compose restart)", t.name)
		if _, err := a.dk.run(ctx, t.loc.wd, append(t.loc.composeArgs(), "restart")...); err != nil {
			return err
		}
	} else {
		a.logf(j, "Restarting the container %s", t.name)
		if _, err := a.dk.run(ctx, "", "restart", t.name); err != nil {
			return err
		}
	}
	if t.port == 0 {
		a.logf(j, "Restarted")
		return nil
	}
	a.logf(j, "Waiting for it to answer")
	wait := a.healthWait
	if wait > 2*time.Minute {
		wait = 2 * time.Minute
	}
	if err := a.waitHealthyFor(ctx, t.port, wait); err != nil {
		return err
	}
	a.logf(j, "%s is up again", t.name)
	return nil
}

func (a *Agent) logs(ctx context.Context, t *opsTarget, n int) (*Logs, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out, src string
	var err error
	if t.loc != nil {
		src = "docker compose logs (" + t.loc.proj + ")"
		args := append(t.loc.composeArgs(), "logs", "--no-color", "--timestamps", "--tail", strconv.Itoa(n))
		if t.solo {
			args = append(args, "--no-log-prefix")
		}
		out, err = a.dk.run(ctx, t.loc.wd, args...)
	} else {
		src = "docker logs " + t.name
		out, err = a.dk.run(ctx, "", "logs", "--timestamps", "--tail", strconv.Itoa(n), t.name)
	}
	if err != nil {
		return nil, err
	}
	lines, cut := TrimLogs(out, n)
	return &Logs{Source: src, Lines: lines, Truncated: cut}, nil
}

var resCache struct {
	sync.Mutex
	at  time.Time
	val *Resources
}

// resources: memory per app from `docker stats` (which takes a couple of seconds, so kept 10 s) and
// the apps folder's disk.
func (a *Agent) resources(ctx context.Context) *Resources {
	resCache.Lock()
	defer resCache.Unlock()
	if resCache.val != nil && time.Since(resCache.at) < 10*time.Second {
		return resCache.val
	}
	out := &Resources{Apps: []AppUsage{}}
	if d, err := DiskSpace(a.cfg.AppsDir); err == nil {
		out.Disk = d
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if cs, err := a.dk.containers(ctx); err == nil {
		if stats, err := a.dk.run(ctx, "", "stats", "--no-stream", "--format", "{{json .}}"); err == nil {
			out.Apps = appMemory(cs, stats)
		}
	}
	resCache.val, resCache.at = out, time.Now()
	return out
}

// appMemory adds up `docker stats` memory per app: every container of a compose project whose folder
// holds an app marker belongs to that app; otherwise a container named pocket-<slug> is that app, and
// the rest of its project (sidecars) goes with it. Core keeps only the slugs it knows.
func appMemory(cs []container, stats string) []AppUsage {
	marked := map[string]string{} // compose project → slug, from the marker
	byName := map[string]string{} // compose project → slug, from its first pocket-<slug> container
	for _, c := range cs {
		proj, wd, _ := c.project()
		if proj == "" {
			continue
		}
		var mark appMark
		if wd != "" && readJSON(filepath.Join(wd, appMarker), &mark) && mark.Slug != "" {
			marked[proj] = mark.Slug
		} else if s, ok := strings.CutPrefix(c.Name, "pocket-"); ok && slugOK(s) && byName[proj] == "" {
			byName[proj] = s
		}
	}
	slugOf := map[string]string{} // container → slug
	for _, c := range cs {
		proj, _, _ := c.project()
		if s := marked[proj]; proj != "" && s != "" {
			slugOf[c.Name] = s
		} else if s, ok := strings.CutPrefix(c.Name, "pocket-"); ok && slugOK(s) {
			slugOf[c.Name] = s
		} else if s := byName[proj]; proj != "" && s != "" {
			slugOf[c.Name] = s
		}
	}
	sum := map[string]uint64{}
	var order []string
	for _, line := range strings.Split(stats, "\n") {
		var st struct{ Name, MemUsage string }
		if json.Unmarshal([]byte(line), &st) != nil {
			continue
		}
		s := slugOf[st.Name]
		if s == "" {
			continue
		}
		used, _, _ := strings.Cut(st.MemUsage, "/")
		b, err := parseSize(strings.TrimSpace(used))
		if err != nil {
			continue
		}
		if _, ok := sum[s]; !ok {
			order = append(order, s)
		}
		sum[s] += b
	}
	out := make([]AppUsage, 0, len(order))
	for _, s := range order {
		out = append(out, AppUsage{Slug: s, Memory: sum[s]})
	}
	return out
}

// parseSize reads docker's sizes: "12.5MiB", "1.2GiB", "980KiB", "512B", "1.5kB".
func parseSize(s string) (uint64, error) {
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, errors.New("no number")
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
		"kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}[strings.TrimSpace(s[i:])]
	if mult == 0 {
		return 0, fmt.Errorf("unknown unit in %q", s)
	}
	return uint64(v * mult), nil
}
