package server

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/logring"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Day-to-day app operations for admins (the phone app's app page): restart an app, read its recent
// log, and the machine's memory, disk and load in GET /system. The host work is the installer
// agent's (installer/ops.go, or a Pocket Box's installer.py); Core's own log comes from its memory.

// handleRestartApp restarts an app through the installer agent: a job like Update, followed the same
// way (GET /installer/jobs/{id}), with no "app down" event while it comes back.
func (s *Server) handleRestartApp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if chi.URLParam(r, "slug") == "core" {
		writeErr(w, http.StatusForbidden, "Pocket Core can't restart itself from here")
		return
	}
	a, err := s.store.GetAppBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	if s.inst == nil {
		writeErr(w, http.StatusNotFound, "no installer on this server, so apps can't be restarted from here")
		return
	}
	if s.jobRunningFor(a.Slug) {
		writeErr(w, http.StatusConflict, "something else is being done to "+a.Name+" right now")
		return
	}
	s.removing.Store(a.Slug, true)
	aj, err := s.inst.Restart(ctx, installer.RestartRequest{Slug: a.Slug, URL: a.URL})
	if err != nil {
		s.removing.Delete(a.Slug)
		s.opsErr(w, r, a, err, "restart it there")
		return
	}
	j := &installJob{ID: aj.ID, Kind: "restart", Slug: a.Slug, Name: a.Name, State: "running", Started: time.Now().UTC()}
	s.installs.Store(j.ID, j)
	if u := userFrom(ctx); u != nil {
		s.log.Info("app restart started", "slug", a.Slug, "by", u.Email)
	}
	go s.followInstall(j)
	writeJSON(w, http.StatusAccepted, j.snapshot())
}

// opsErr explains an agent's refusal; an app that runs as a program (Pocket AI) gets a plain answer.
func (s *Server) opsErr(w http.ResponseWriter, r *http.Request, a *store.App, err error, there string) {
	var ie *installer.Error
	if errors.As(err, &ie) && ie.Status == http.StatusConflict && strings.HasPrefix(ie.Msg, "no container") && s.findBinaryApp(r.Context(), a.Slug) != nil {
		writeErr(w, http.StatusConflict, a.Name+" runs as a program on the machine, not in a container: "+there)
		return
	}
	s.installerErr(w, err)
}

func (s *Server) jobRunningFor(slug string) bool {
	busy := false
	s.installs.Range(func(_, v any) bool {
		j := v.(*installJob)
		j.mu.Lock()
		busy = j.Slug == slug && j.State == "running"
		j.mu.Unlock()
		return !busy
	})
	return busy
}

// awaitBack waits for a restarted app to answer its health check again, then records it.
func (s *Server) awaitBack(ctx context.Context, j *installJob) {
	a, err := s.store.GetAppBySlug(ctx, j.Slug)
	if err != nil {
		j.finish("done", "")
		return
	}
	j.logf("Waiting for %s to answer Pocket Core", a.Name)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if s.answers(ctx, a) {
			s.checkApp(ctx, a)
			j.logf("%s is back", a.Name)
			j.finish("done", "")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	j.finish("failed", a.Name+" restarted but doesn't answer yet. Check its log.")
}

func (s *Server) answers(ctx context.Context, a *store.App) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL+a.HealthPath, nil)
	if err != nil {
		return false
	}
	res, err := s.http.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode < 400
}

// handleAppLogs is an app's recent log lines, oldest first: ?lines=200 (at most 2000, 1 MB).
func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := installer.ClampLines(r.URL.Query().Get("lines"))
	slug := chi.URLParam(r, "slug")
	if slug == "core" {
		lines, cut := installer.TrimLogs(strings.Join(logring.Default.Last(n), "\n"), n)
		writeJSON(w, http.StatusOK, installer.Logs{Slug: "core", Source: "Pocket Core, since it last started", Lines: lines, Truncated: cut})
		return
	}
	a, err := s.store.GetAppBySlug(ctx, slug)
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	if s.inst == nil {
		writeErr(w, http.StatusNotFound, "no installer on this server, so app logs can't be read from here")
		return
	}
	l, err := s.inst.Logs(ctx, a.Slug, a.URL, n)
	if err != nil {
		s.opsErr(w, r, a, err, "its log is there")
		return
	}
	if l.Lines == nil {
		l.Lines = []string{}
	}
	l.Slug = a.Slug
	writeJSON(w, http.StatusOK, l)
}

// ---- the machine's numbers, for GET /system ----

// hostInfo is the machine's memory, load and disks. Core in a container still reads the host's
// /proc/meminfo and /proc/loadavg (they are not namespaced), so this is the whole machine.
func (s *Server) hostInfo(ctx context.Context) map[string]any {
	out := map[string]any{"cpus": runtime.NumCPU()}
	if m := readMeminfo("/proc/meminfo"); m != nil {
		out["memory"] = m
	}
	if l := readLoadavg("/proc/loadavg"); l != nil {
		out["load"] = l
	}
	disks := []map[string]any{}
	if d, err := installer.DiskSpace(s.cfg.DataDir); err == nil {
		disks = append(disks, map[string]any{"name": "data", "path": d.Path, "total_bytes": d.Total, "free_bytes": d.Free})
	}
	if res := s.agentResources(ctx); res != nil {
		if res.Disk != nil && res.Disk.Total > 0 {
			disks = append(disks, map[string]any{"name": "apps", "path": res.Disk.Path, "total_bytes": res.Disk.Total, "free_bytes": res.Disk.Free})
		}
		known := map[string]bool{"core": true}
		if apps, err := s.store.ListApps(ctx); err == nil {
			for _, a := range apps {
				known[a.Slug] = true
			}
		}
		var mine []installer.AppUsage // only apps registered here (not Caddy, the installer…)
		for _, u := range res.Apps {
			if known[u.Slug] {
				mine = append(mine, u)
			}
		}
		if len(mine) > 0 {
			out["apps"] = mine
		}
	}
	out["disks"] = disks
	return out
}

// readMeminfo: total and available memory and swap, in bytes.
func readMeminfo(path string) map[string]uint64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	kb := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fs := strings.Fields(v)
		if len(fs) == 0 {
			continue
		}
		if n, err := strconv.ParseUint(fs[0], 10, 64); err == nil {
			kb[k] = n
		}
	}
	if kb["MemTotal"] == 0 {
		return nil
	}
	avail, ok := kb["MemAvailable"]
	if !ok { // kernels before 3.14
		avail = kb["MemFree"] + kb["Buffers"] + kb["Cached"]
	}
	return map[string]uint64{"total_bytes": kb["MemTotal"] << 10, "available_bytes": avail << 10,
		"swap_total_bytes": kb["SwapTotal"] << 10, "swap_free_bytes": kb["SwapFree"] << 10}
}

// readLoadavg: the 1, 5 and 15 minute load averages.
func readLoadavg(path string) []float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fs := strings.Fields(string(b))
	if len(fs) < 3 {
		return nil
	}
	out := make([]float64, 3)
	for i := range out {
		v, err := strconv.ParseFloat(fs[i], 64)
		if err != nil {
			return nil
		}
		out[i] = v
	}
	return out
}

// The agent's numbers take a couple of seconds (docker stats), so they are kept for 15 s and
// refreshed in the background after the first read.
type resourcesCache struct {
	mu      sync.Mutex
	at      time.Time
	val     *installer.Resources
	loading bool
}

var agentRes sync.Map // *Server → *resourcesCache

func (s *Server) agentResources(ctx context.Context) *installer.Resources {
	if s.inst == nil {
		return nil
	}
	v, _ := agentRes.LoadOrStore(s, &resourcesCache{})
	c := v.(*resourcesCache)
	c.mu.Lock()
	fresh := time.Since(c.at) < 15*time.Second
	val, first := c.val, c.at.IsZero()
	if fresh || c.loading {
		c.mu.Unlock()
		return val
	}
	c.loading = true
	c.mu.Unlock()
	load := func(ctx context.Context) *installer.Resources {
		ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		res, err := s.inst.Resources(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.loading, c.at = false, time.Now()
		if err != nil {
			c.val = nil // an older agent (404) or none: no app numbers
			return nil
		}
		c.val = res
		return res
	}
	if first {
		return load(ctx)
	}
	go load(context.WithoutCancel(ctx))
	return val
}
