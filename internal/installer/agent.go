package installer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// InstallRequest asks the agent to put one app on the server.
type InstallRequest struct {
	Slug            string `json:"slug"`
	Name            string `json:"name,omitempty"`
	RegistrationKey string `json:"registration_key"`
	AdminEmail      string `json:"admin_email"`
	AdminName       string `json:"admin_name,omitempty"`
	Reserved        []int  `json:"reserved_ports,omitempty"` // ports Core knows are taken
}

// UninstallRequest asks the agent to take one app off the server. URL is
// the address the app registered with Core; its port finds the container.
type UninstallRequest struct {
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	PublicURL string `json:"public_url,omitempty"`
}

// Job is one install or removal and its running log.
type Job struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"` // install | uninstall
	Slug    string     `json:"slug"`
	State   string     `json:"state"` // running | done | failed
	Log     []string   `json:"log"`
	Error   string     `json:"error,omitempty"`
	Result  *Result    `json:"result,omitempty"`
	Started time.Time  `json:"started_at"`
	Ended   *time.Time `json:"ended_at,omitempty"`
}

// Result is what a finished job left behind.
type Result struct {
	Dir       string   `json:"dir,omitempty"`
	Archive   string   `json:"archive,omitempty"`
	Container string   `json:"container,omitempty"`
	AppURL    string   `json:"app_url,omitempty"`
	PublicURL string   `json:"public_url,omitempty"`
	Port      int      `json:"port,omitempty"`
	Version   string   `json:"version,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Status describes the agent's setup.
type Status struct {
	Owner      string `json:"owner"`
	AppsDir    string `json:"apps_dir"`
	ArchiveDir string `json:"archive_dir"`
	PublicURL  string `json:"public_url"`
	Proxy      bool   `json:"proxy"` // edits the Caddyfile
}

// Agent does the host-side work.
type Agent struct {
	cfg *Config
	gh  *GitHub
	dk  *Docker
	log *slog.Logger

	mu   sync.Mutex
	jobs map[string]*Job
	work sync.Mutex // one install or removal at a time

	healthWait time.Duration
	httpc      *http.Client
}

func NewAgent(cfg *Config, log *slog.Logger) *Agent {
	return &Agent{cfg: cfg, gh: NewGitHub(cfg), dk: NewDocker(), log: log, jobs: map[string]*Job{},
		healthWait: 3 * time.Minute, httpc: &http.Client{Timeout: 5 * time.Second}}
}

// Serve answers on the unix socket until ctx ends.
func (a *Agent) Serve(ctx context.Context) error {
	if fi, err := os.Lstat(a.cfg.Socket); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("%s exists and is not a socket", a.cfg.Socket)
		}
		os.Remove(a.cfg.Socket)
	}
	if err := os.MkdirAll(filepath.Dir(a.cfg.Socket), 0o700); err != nil {
		return err
	}
	l, err := net.Listen("unix", a.cfg.Socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(a.cfg.Socket, 0o600); err != nil {
		l.Close()
		return err
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	a.log.Info("installer listening", "socket", a.cfg.Socket, "apps", a.cfg.AppsDir, "owner", a.cfg.Owner)
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler is the agent's API. Every call needs the bearer token.
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, Status{Owner: a.cfg.Owner, AppsDir: a.cfg.AppsDir, ArchiveDir: a.cfg.ArchiveDir, PublicURL: a.cfg.PublicURL, Proxy: a.cfg.Caddyfile != ""})
	})
	mux.HandleFunc("GET /v1/catalog", a.handleCatalog)
	mux.HandleFunc("POST /v1/install", func(w http.ResponseWriter, r *http.Request) {
		var req InstallRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || !slugOK(req.Slug) || req.RegistrationKey == "" || req.AdminEmail == "" {
			writeErr(w, http.StatusBadRequest, "slug, registration_key and admin_email are required")
			return
		}
		a.start(w, "install", req.Slug, func(ctx context.Context, j *Job) error { return a.install(ctx, j, req) })
	})
	mux.HandleFunc("POST /v1/uninstall", func(w http.ResponseWriter, r *http.Request) {
		var req UninstallRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || !slugOK(req.Slug) {
			writeErr(w, http.StatusBadRequest, "slug is required")
			return
		}
		if a.cfg.protected(req.Slug) {
			writeErr(w, http.StatusForbidden, req.Slug+" cannot be removed from here")
			return
		}
		a.start(w, "uninstall", req.Slug, func(ctx context.Context, j *Job) error { return a.uninstall(ctx, j, req) })
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		j, ok := a.jobs[r.PathValue("id")]
		var snap Job
		if ok {
			snap = *j
			snap.Log = slices.Clone(j.Log)
		}
		a.mu.Unlock()
		if !ok {
			writeErr(w, http.StatusNotFound, "no such job")
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(a.cfg.Token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "token rejected")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		mux.ServeHTTP(w, r)
	})
}

func (a *Agent) handleCatalog(w http.ResponseWriter, r *http.Request) {
	rels, err := a.gh.Catalog(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	names := map[string]bool{}
	if cs, err := a.dk.containers(r.Context()); err == nil {
		for _, c := range cs {
			names[c.Name] = true
		}
	}
	out := make([]Release, len(rels))
	for i, rel := range rels {
		out[i] = rel
		out[i].Present = names[rel.Container]
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (a *Agent) start(w http.ResponseWriter, kind, slug string, fn func(context.Context, *Job) error) {
	if !a.work.TryLock() {
		writeErr(w, http.StatusConflict, "another install or removal is still running")
		return
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	j := &Job{ID: hex.EncodeToString(id), Kind: kind, Slug: slug, State: "running", Started: time.Now().UTC(), Log: []string{}}
	a.mu.Lock()
	for k, old := range a.jobs { // keep a day of history
		if old.Ended != nil && time.Since(*old.Ended) > 24*time.Hour {
			delete(a.jobs, k)
		}
	}
	a.jobs[j.ID] = j
	snap := *j
	a.mu.Unlock()
	go func() {
		defer a.work.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		err := fn(ctx, j)
		a.mu.Lock()
		now := time.Now().UTC()
		j.Ended = &now
		if err != nil {
			j.State, j.Error = "failed", err.Error()
		} else {
			j.State = "done"
		}
		a.mu.Unlock()
		a.log.Info("installer job finished", "kind", kind, "slug", slug, "state", j.State, "err", err)
	}()
	writeJSON(w, http.StatusAccepted, snap)
}

func (a *Agent) logf(j *Job, format string, args ...any) {
	a.mu.Lock()
	j.Log = append(j.Log, fmt.Sprintf(format, args...))
	a.mu.Unlock()
}

func (a *Agent) setResult(j *Job, r *Result) {
	a.mu.Lock()
	j.Result = r
	a.mu.Unlock()
}

// ---- install ----

func (a *Agent) install(ctx context.Context, j *Job, req InstallRequest) error {
	a.logf(j, "Looking up the newest release on GitHub")
	rel, err := a.gh.Find(ctx, req.Slug)
	if err != nil {
		return err
	}
	a.logf(j, "Found %s %s", rel.Repo, rel.Tag)
	cs, err := a.dk.containers(ctx)
	if err != nil {
		return err
	}
	used := map[int]bool{}
	for _, p := range req.Reserved {
		used[p] = true
	}
	for _, c := range cs {
		if c.Name == rel.Container {
			return fmt.Errorf("a container named %s already exists on this server", rel.Container)
		}
		for _, p := range c.Ports {
			used[p] = true
		}
		if c.Host {
			if p, err := strconv.Atoi(c.Env["PORT"]); err == nil {
				used[p] = true
			}
		}
	}
	dir := filepath.Join(a.cfg.AppsDir, rel.Container)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists; move it away first", dir)
	}
	next := func() (int, error) {
		for p := a.cfg.PortFrom; p <= a.cfg.PortTo; p++ {
			if !used[p] && portFree(p) {
				used[p] = true
				return p, nil
			}
		}
		return 0, fmt.Errorf("no free port left in %d-%d", a.cfg.PortFrom, a.cfg.PortTo)
	}
	_, zerr := os.Stat("/usr/share/zoneinfo")
	public := a.cfg.publicURL(rel.Slug)
	plan, err := BuildPlan(PlanInput{Release: rel, Name: req.Name, PublicURL: public, CoreURL: a.cfg.CoreURL,
		RegistrationKey: req.RegistrationKey, AdminEmail: req.AdminEmail, AdminName: req.AdminName, TZ: a.cfg.TZ,
		Zoneinfo: zerr == nil, NextPort: next, DockerGID: dockerGID(), Now: time.Now()})
	if err != nil {
		return err
	}
	if err := writePlan(dir, plan); err != nil {
		return err
	}
	res := &Result{Dir: dir, Container: plan.Container, AppURL: plan.AppURL, PublicURL: public, Port: plan.Port, Version: rel.Version}
	for _, n := range plan.Notes {
		res.Warnings = append(res.Warnings, "The app "+n+".")
	}
	a.setResult(j, res)
	a.logf(j, "Wrote %s (host port %d)", dir, plan.Port)

	a.logf(j, "Downloading %s:%s", rel.Image, rel.Version)
	if _, err := a.dk.run(ctx, dir, "compose", "pull", "--quiet"); err != nil {
		return a.rollback(ctx, j, dir, err)
	}
	a.logf(j, "Starting %s", plan.Container)
	if _, err := a.dk.run(ctx, dir, "compose", "up", "-d"); err != nil {
		return a.rollback(ctx, j, dir, err)
	}
	if err := a.waitHealthy(ctx, plan.Port); err != nil {
		logs, _ := a.dk.run(ctx, dir, "compose", "logs", "--no-color", "--tail", "15")
		return a.rollback(ctx, j, dir, fmt.Errorf("%w\n%s", err, logs))
	}
	a.logf(j, "%s is up", plan.Container)

	if a.cfg.Caddyfile != "" {
		host := siteHost(public)
		upstream := fmt.Sprintf("%s:%d", a.cfg.CaddyUpstream, plan.Port)
		comment := fmt.Sprintf("%s — installed by Pocket Core %s (%s, :%d).", firstNonEmpty(req.Name, displayName(rel.Slug)), time.Now().Format("2006-01-02"), dir, plan.Port)
		msg, err := a.editCaddy(ctx, j, rel.Slug, func(cf string) (string, string, error) {
			if HasSite(cf, host) {
				return cf, "Caddy already serves " + host + "; left that site as it is", nil
			}
			addr := host
			if strings.HasPrefix(public, "http://") {
				addr = "http://" + host // plain http: no certificate to fetch
			}
			return AddSite(cf, comment, addr, a.cfg.CaddyImports, upstream), "Added the site " + public, nil
		})
		switch {
		case err != nil:
			res.Warnings = append(res.Warnings, "The app runs, but its HTTPS site was not added: "+err.Error())
		case strings.HasPrefix(msg, "Caddy already"):
			res.Warnings = append(res.Warnings, msg+" — check it points at port "+strconv.Itoa(plan.Port)+".")
		default:
			a.logf(j, "%s", msg)
		}
	}
	a.setResult(j, res)
	return nil
}

// rollback stops what was started and archives the half-made folder, so a
// failed install leaves the server as it was (the folder stays for reading).
func (a *Agent) rollback(ctx context.Context, j *Job, dir string, cause error) error {
	a.logf(j, "Install failed; undoing it")
	ctx = context.WithoutCancel(ctx)
	_, _ = a.dk.run(ctx, dir, "compose", "down", "--remove-orphans")
	if dest, err := a.archive(dir, "failed"); err == nil {
		a.logf(j, "Kept the folder for reading at %s", dest)
	}
	return cause
}

func (a *Agent) waitHealthy(ctx context.Context, port int) error {
	deadline := time.Now().Add(a.healthWait)
	u := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	var last string
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		res, err := a.httpc.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode < 500 {
				return nil
			}
			last = res.Status
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("the app did not answer on port %d within %s (%s)", port, a.healthWait, last)
}

// ---- uninstall ----

func (a *Agent) uninstall(ctx context.Context, j *Job, req UninstallRequest) error {
	cs, err := a.dk.containers(ctx)
	if err != nil {
		return err
	}
	port := 0
	if u, err := url.Parse(req.URL); err == nil {
		port, _ = strconv.Atoi(u.Port())
	}
	var target *container
	for i := range cs { // the container publishing the port Core reaches the app on
		if port > 0 && (slices.Contains(cs[i].Ports, port) || cs[i].Host && cs[i].Env["PORT"] == strconv.Itoa(port)) {
			target = &cs[i]
			break
		}
	}
	if target == nil {
		for i := range cs {
			if cs[i].Name == "pocket-"+req.Slug {
				target = &cs[i]
			}
		}
	}
	if target == nil {
		return fmt.Errorf("no container on this server serves %s (looked for port %d and pocket-%s); it may run outside Docker", req.Slug, port, req.Slug)
	}
	proj, wd, files := target.project()
	if proj == "" || wd == "" {
		return fmt.Errorf("%s was not started with docker compose; remove it by hand", target.Name)
	}
	wd = filepath.Clean(wd)
	if rel, err := filepath.Rel(a.cfg.AppsDir, wd); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s lives in %s, outside %s; remove it by hand", target.Name, wd, a.cfg.AppsDir)
	}
	// The folder must belong to this app alone: no other app's containers in
	// its compose project, and no other project started from the same folder.
	for _, c := range cs {
		p, d, _ := c.project()
		if p == proj && c.Name != target.Name && !strings.HasPrefix(c.Name, target.Name+"-") {
			return fmt.Errorf("%s's compose project also runs %s; remove it by hand", target.Name, c.Name)
		}
		if p != proj && filepath.Clean(d) == wd {
			return fmt.Errorf("%s is shared with %s; remove it by hand", wd, c.Name)
		}
	}
	res := &Result{Dir: wd, Container: target.Name, Port: port, PublicURL: req.PublicURL}
	a.setResult(j, res)

	a.logf(j, "Stopping %s", target.Name)
	args := []string{"compose", "-p", proj, "--project-directory", wd}
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			args = append(args, "-f", f)
		}
	}
	if _, err := a.dk.run(ctx, wd, append(args, "down", "--remove-orphans")...); err != nil {
		return err
	}
	a.logf(j, "Removed the containers")

	dest, err := a.archive(wd, "")
	if err != nil {
		res.Warnings = append(res.Warnings, "The folder stays at "+wd+": "+err.Error())
	} else {
		res.Archive = dest
		a.logf(j, "Archived %s to %s", wd, dest)
	}

	host := ""
	if req.PublicURL != "" {
		host = siteHost(req.PublicURL)
	}
	if a.cfg.Caddyfile != "" && host != "" {
		msg, err := a.editCaddy(ctx, j, req.Slug, func(cf string) (string, string, error) { return RemoveSite(cf, host) })
		switch {
		case err != nil:
			res.Warnings = append(res.Warnings, "Its HTTPS site was not removed: "+err.Error())
		case msg != "":
			a.logf(j, "Caddy: %s", msg)
		}
		if b, err := os.ReadFile(a.cfg.Caddyfile); err == nil && port > 0 && strings.Contains(string(b), fmt.Sprintf("%s:%d", a.cfg.CaddyUpstream, port)) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("The Caddyfile still proxies another site to port %d.", port))
		}
	}
	for _, f := range a.cfg.Leftovers {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if host != "" && strings.Contains(string(b), host) || port > 0 && strings.Contains(string(b), ":"+strconv.Itoa(port)) {
			res.Warnings = append(res.Warnings, f+" still mentions this app.")
		}
	}
	a.setResult(j, res)
	return nil
}

// archive moves a folder into the archive dir, stamped with the time.
func (a *Agent) archive(dir, tag string) (string, error) {
	if err := os.MkdirAll(a.cfg.ArchiveDir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(dir) + "-" + time.Now().Format("20060102-150405")
	if tag != "" {
		name += "-" + tag
	}
	dest := filepath.Join(a.cfg.ArchiveDir, name)
	if err := os.Rename(dir, dest); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return "", fmt.Errorf("the archive folder is on another disk")
		}
		return "", err
	}
	return dest, nil
}

// ---- Caddy ----

// editCaddy changes the Caddyfile in place (the same inode: Caddy's
// container bind-mounts the single file), validates, reloads, and puts the
// old file back if Caddy refuses the new one.
func (a *Agent) editCaddy(ctx context.Context, j *Job, slug string, change func(string) (string, string, error)) (string, error) {
	path := a.cfg.Caddyfile
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	next, msg, err := change(string(old))
	if err != nil || next == string(old) {
		return msg, err
	}
	backup := fmt.Sprintf("%s.bak-installer-%s-%s", path, slug, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, old, fi.Mode().Perm()); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(next), fi.Mode().Perm()); err != nil {
		return "", err
	}
	restore := func(cause error) (string, error) {
		_ = os.WriteFile(path, old, fi.Mode().Perm())
		_, _ = a.dk.run(ctx, "", "exec", a.cfg.CaddyContainer, "caddy", "reload", "--config", a.cfg.CaddyPath, "--adapter", "caddyfile")
		return "", cause
	}
	if _, err := a.dk.run(ctx, "", "exec", a.cfg.CaddyContainer, "caddy", "validate", "--config", a.cfg.CaddyPath, "--adapter", "caddyfile"); err != nil {
		return restore(fmt.Errorf("Caddy refused the change, the old Caddyfile is back: %w", err))
	}
	if _, err := a.dk.run(ctx, "", "exec", a.cfg.CaddyContainer, "caddy", "reload", "--config", a.cfg.CaddyPath, "--adapter", "caddyfile"); err != nil {
		return restore(fmt.Errorf("Caddy reload failed, the old Caddyfile is back: %w", err))
	}
	// A file bind mount pins the inode. If the container still sees the old
	// file (the Caddyfile was replaced by an editor at some point), restart it.
	out, _ := a.dk.run(ctx, "", "exec", a.cfg.CaddyContainer, "sha256sum", a.cfg.CaddyPath)
	sum := sha256.Sum256([]byte(next))
	if !strings.HasPrefix(strings.TrimSpace(out), hex.EncodeToString(sum[:])) {
		a.logf(j, "Caddy still had the old file; restarting it")
		if _, err := a.dk.run(ctx, "", "restart", a.cfg.CaddyContainer); err != nil {
			return "", err
		}
	}
	a.logf(j, "Caddyfile backup: %s", backup)
	return msg, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
