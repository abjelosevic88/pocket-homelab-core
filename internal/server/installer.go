package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// One-click install and removal of Pocket apps. The work on the host is done
// by the installer agent (INSTALLER_SOCKET); Core decides who may ask, hands
// the agent its registration key, and finishes the job on its side: it waits
// for a new app to register (which creates its sign-in client, service entry
// and app links) and unregisters a removed one.

// installJob mirrors an agent job plus what Core did after it.
type installJob struct {
	mu      sync.Mutex
	ID      string            `json:"id"`
	Kind    string            `json:"kind"` // install | update | uninstall
	Slug    string            `json:"slug"`
	Name    string            `json:"name"`
	State   string            `json:"state"` // running | done | failed
	Log     []string          `json:"log"`
	Error   string            `json:"error,omitempty"`
	Result  *installer.Result `json:"result,omitempty"`
	OpenURL string            `json:"open_url,omitempty"`
	Started time.Time         `json:"started_at"`

	agentLog []string
	coreLog  []string
	ended    time.Time
}

func (j *installJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	log := append(append([]string{}, j.agentLog...), j.coreLog...) // [] not null before the first line
	return map[string]any{"id": j.ID, "kind": j.Kind, "slug": j.Slug, "name": j.Name, "state": j.State,
		"log": log, "error": j.Error, "result": j.Result,
		"open_url": j.OpenURL, "started_at": j.Started}
}

func (j *installJob) logf(format string, args ...any) {
	j.mu.Lock()
	j.coreLog = append(j.coreLog, fmt.Sprintf(format, args...))
	j.mu.Unlock()
}

func (j *installJob) finish(state, errMsg string) {
	j.mu.Lock()
	j.State, j.Error, j.ended = state, errMsg, time.Now()
	j.mu.Unlock()
}

// installerState says whether one-click installs exist here and why not.
// "" = on; otherwise the reason they are off.
func (s *Server) installerState(ctx context.Context) string {
	switch {
	case s.inst == nil:
		return "no installer agent on this server (INSTALLER_SOCKET)"
	case !s.cfg.InstallerEnabled:
		return "switched off by the server (INSTALLER_ENABLED=false)"
	}
	if st, err := s.store.GetSettings(ctx); err == nil && st.InstallsOff {
		return "switched off under Settings → Box"
	}
	return ""
}

// installerOff answers 404/403 when installs are not available.
func (s *Server) installerOff(w http.ResponseWriter, r *http.Request) bool {
	why := s.installerState(r.Context())
	switch {
	case why == "":
		return false
	case s.inst == nil:
		writeErr(w, http.StatusNotFound, "no installer on this server")
	default:
		writeErr(w, http.StatusForbidden, "Install and Remove are "+why)
	}
	return true
}

// appName prefers Core's own catalog, then the app catalog's name (apps
// released after this Core was built are only in the latter).
func appName(slug string, fallback ...string) string {
	if e, ok := catalog.Get(slug); ok && e.Name != "" {
		return e.Name
	}
	for _, f := range fallback {
		if f != "" {
			return f
		}
	}
	return "Pocket " + slug
}

func (s *Server) installerErr(w http.ResponseWriter, err error) {
	var ie *installer.Error
	if errors.As(err, &ie) {
		writeErr(w, ie.Status, ie.Msg)
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error())
}

// handleInstallerCatalog lists the released apps with whether each is
// already registered here and whether Update can move it to the newest.
func (s *Server) handleInstallerCatalog(w http.ResponseWriter, r *http.Request) {
	if s.installerOff(w, r) {
		return
	}
	ctx := r.Context()
	st, err := s.inst.Status(ctx)
	if err != nil {
		s.installerErr(w, err)
		return
	}
	rels, err := s.inst.Catalog(ctx, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		s.installerErr(w, err)
		return
	}
	apps, _ := s.store.ListApps(ctx)
	have := map[string]*store.App{}
	for i := range apps {
		have[apps[i].Slug] = &apps[i]
	}
	out := make([]map[string]any, 0, len(rels))
	for _, rel := range rels {
		item := map[string]any{"slug": rel.Slug, "name": appName(rel.Slug, rel.Name), "description": rel.Description, "repo": rel.Repo,
			"repo_url": rel.RepoURL, "version": rel.Version, "tag": rel.Tag, "container": rel.Container, "present": rel.Present,
			"category": rel.Category, "icon": rel.Icon, "page": rel.Page, "digest": rel.Digest, "platforms": rel.Platforms,
			"requires": rel.Requires, "available": rel.Available, "running": rel.Running, "updatable": rel.Updatable,
			"update_note": rel.UpdateNote, "archive": rel.Archive}
		if a := have[rel.Slug]; a != nil {
			item["installed"], item["name"], item["installed_version"] = true, a.Name, a.Version
		}
		out = append(out, item)
	}
	running := ""
	s.installs.Range(func(_, v any) bool {
		if j := v.(*installJob); j.snapshot()["state"] == "running" {
			running = j.ID
			return false
		}
		return true
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": st, "apps": out, "running_job": running})
}

// handleInstallerStatus is the agent's status alone (no catalog), cheap enough to poll: on a Pocket
// Box it says which apps are still on their way.
func (s *Server) handleInstallerStatus(w http.ResponseWriter, r *http.Request) {
	if s.installerOff(w, r) {
		return
	}
	st, err := s.inst.Status(r.Context())
	if err != nil {
		s.installerErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st})
}

func (s *Server) handleInstallApp(w http.ResponseWriter, r *http.Request) {
	if s.installerOff(w, r) {
		return
	}
	ctx := r.Context()
	var in struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`         // the catalog's, for apps this Core does not know yet
		RestoreFrom string `json:"restore_from"` // an archived folder whose data the install takes over
	}
	if err := decode(r, &in); err != nil || !slugRe.MatchString(in.Slug) {
		writeErr(w, http.StatusBadRequest, "slug is required")
		return
	}
	if _, err := s.store.GetAppBySlug(ctx, in.Slug); err == nil {
		writeErr(w, http.StatusConflict, appName(in.Slug)+" is already registered here")
		return
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	u := userFrom(ctx)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "sign in first")
		return
	}
	var reserved []int
	if apps, err := s.store.ListApps(ctx); err == nil {
		for _, a := range apps {
			if p := urlPort(a.URL); p > 0 {
				reserved = append(reserved, p)
			}
		}
	}
	name := appName(in.Slug, in.Name)
	aj, err := s.inst.Install(ctx, installer.InstallRequest{Slug: in.Slug, Name: name, RegistrationKey: st.RegistrationKey,
		AdminEmail: u.Email, AdminName: u.Name, Reserved: reserved, RestoreFrom: in.RestoreFrom})
	if err != nil {
		s.installerErr(w, err)
		return
	}
	j := &installJob{ID: aj.ID, Kind: "install", Slug: in.Slug, Name: name, State: "running", Started: time.Now().UTC()}
	s.installs.Store(j.ID, j)
	s.log.Info("app install started", "slug", in.Slug, "by", u.Email)
	go s.followInstall(j)
	writeJSON(w, http.StatusAccepted, j.snapshot())
}

// handleUpgradeApp moves an installed app to the catalog's newest release.
func (s *Server) handleUpgradeApp(w http.ResponseWriter, r *http.Request) {
	if s.installerOff(w, r) {
		return
	}
	ctx := r.Context()
	a, err := s.store.GetAppBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	by := ""
	if u := userFrom(ctx); u != nil {
		by = u.Email
	}
	j, err := s.startUpgrade(ctx, a, by)
	if err != nil {
		s.installerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j.snapshot())
}

// startUpgrade asks the agent to update an app and follows the job.
func (s *Server) startUpgrade(ctx context.Context, a *store.App, by string) (*installJob, error) {
	if a.Slug == "core" {
		return nil, &installer.Error{Status: http.StatusForbidden, Msg: "Pocket Core is updated on its own"}
	}
	// The restart is on purpose: no "app down" event meanwhile.
	s.removing.Store(a.Slug, true)
	aj, err := s.inst.Update(ctx, installer.UpdateRequest{Slug: a.Slug, URL: a.URL})
	if err != nil {
		s.removing.Delete(a.Slug)
		return nil, err
	}
	j := &installJob{ID: aj.ID, Kind: "update", Slug: a.Slug, Name: a.Name, State: "running", Started: time.Now().UTC()}
	s.installs.Store(j.ID, j)
	s.log.Info("app update started", "slug", a.Slug, "by", by)
	go s.followInstall(j)
	return j, nil
}

func (s *Server) handleUninstallApp(w http.ResponseWriter, r *http.Request) {
	if s.installerOff(w, r) {
		return
	}
	ctx := r.Context()
	a, err := s.store.GetAppBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	if a.Slug == "core" {
		writeErr(w, http.StatusForbidden, "Pocket Core cannot remove itself")
		return
	}
	// Removing an app takes it down on purpose: no "app down" event meanwhile.
	s.removing.Store(a.Slug, true)
	aj, err := s.inst.Uninstall(ctx, installer.UninstallRequest{Slug: a.Slug, URL: a.URL, PublicURL: a.PublicURL})
	if err != nil {
		s.removing.Delete(a.Slug)
		s.installerErr(w, err)
		return
	}
	j := &installJob{ID: aj.ID, Kind: "uninstall", Slug: a.Slug, Name: a.Name, State: "running", Started: time.Now().UTC()}
	s.installs.Store(j.ID, j)
	if u := userFrom(ctx); u != nil {
		s.log.Info("app removal started", "slug", a.Slug, "by", u.Email)
	}
	go s.followInstall(j)
	writeJSON(w, http.StatusAccepted, j.snapshot())
}

func (s *Server) handleInstallJob(w http.ResponseWriter, r *http.Request) {
	v, ok := s.installs.Load(chi.URLParam(r, "id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, v.(*installJob).snapshot())
}

// followInstall mirrors the agent's job and then does Core's part.
func (s *Server) followInstall(j *installJob) {
	// A Pocket Box may first download an app it doesn't carry; a big one on a slow line takes a while.
	limit := 30 * time.Minute
	if j.Kind == "install" {
		limit = 3 * time.Hour
	}
	ctx, cancel := context.WithTimeout(s.ctx, limit)
	defer cancel()
	defer s.pruneInstallJobs()
	if j.Kind == "uninstall" || j.Kind == "update" {
		defer s.removing.Delete(j.Slug)
	}
	var aj *installer.Job
	misses := 0
	for {
		select {
		case <-ctx.Done():
			j.finish("failed", "gave up waiting for the installer")
			return
		case <-time.After(time.Second):
		}
		got, err := s.inst.Job(ctx, j.ID)
		if err != nil {
			if misses++; misses > 30 {
				j.finish("failed", "lost the installer: "+err.Error())
				return
			}
			continue
		}
		misses = 0
		aj = got
		j.mu.Lock()
		j.agentLog, j.Result = aj.Log, aj.Result
		j.mu.Unlock()
		if aj.State != "running" {
			break
		}
	}
	if aj.State == "failed" {
		j.finish("failed", aj.Error)
		return
	}
	switch j.Kind {
	case "uninstall":
		s.unregister(ctx, j)
	case "update":
		s.awaitVersion(ctx, j, aj.Result)
	default:
		s.awaitRegistration(ctx, j)
	}
}

// awaitRegistration waits for the new app's first start: registering creates
// its service entry, its "Sign in with Pocket Core" client and its app links.
func (s *Server) awaitRegistration(ctx context.Context, j *installJob) {
	j.logf("Waiting for the app to register with Pocket Core")
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		a, err := s.store.GetAppBySlug(ctx, j.Slug)
		if err == nil {
			j.logf("Registered with Pocket Core as %s", a.Name)
			if cl, err := s.store.GetOIDCClient(ctx, "pocket-"+a.Slug); err == nil && cl.AppID == a.ID {
				j.logf("Sign in with Pocket Core is set up")
			}
			if links, err := s.store.ListAppLinks(ctx, a.ID); err == nil && len(links) > 0 {
				on := 0
				for _, l := range links {
					if l.Enabled {
						on++
					}
				}
				j.logf("App links: %d of %d switched on", on, len(links))
			}
			s.checkApp(ctx, a)
			j.mu.Lock()
			j.Name = a.Name
			j.OpenURL = signedInURL(s.signInStarts(ctx), a.ID, a.EffectivePublicURL())
			j.mu.Unlock()
			j.finish("done", "")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	j.mu.Lock()
	if j.Result != nil {
		j.Result.Warnings = append(j.Result.Warnings, "The app runs but has not registered with Pocket Core yet. Check its log: "+logHint(j.Slug, j.Result))
	}
	j.mu.Unlock()
	j.finish("done", "")
}

// awaitVersion waits for the updated app to register again with its new
// version (apps re-register on every start).
func (s *Server) awaitVersion(ctx context.Context, j *installJob, res *installer.Result) {
	want := ""
	if res != nil {
		want = strings.TrimPrefix(res.Version, "v")
	}
	j.logf("Waiting for the app to report %s to Pocket Core", formatVer(want))
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if a, err := s.store.GetAppBySlug(ctx, j.Slug); err == nil && (want == "" || strings.TrimPrefix(a.Version, "v") == want) {
			s.checkApp(ctx, a)
			j.logf("%s runs %s", a.Name, formatVer(a.Version))
			j.mu.Lock()
			j.OpenURL = signedInURL(s.signInStarts(ctx), a.ID, a.EffectivePublicURL())
			j.mu.Unlock()
			j.finish("done", "")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	j.mu.Lock()
	if j.Result != nil {
		j.Result.Warnings = append(j.Result.Warnings, "The new version runs but has not reported to Pocket Core yet. Check its log: "+logHint(j.Slug, j.Result))
	}
	j.mu.Unlock()
	j.finish("done", "")
}

// logHint is where an app's log is: its container, or on a Pocket Box (no container) its systemd unit.
func logHint(slug string, res *installer.Result) string {
	if res != nil && res.Container != "" {
		return "docker logs " + res.Container
	}
	return "journalctl -u pocket-" + slug
}

func formatVer(v string) string {
	if v == "" {
		return "its new version"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

func (s *Server) unregister(ctx context.Context, j *installJob) {
	a, err := s.store.GetAppBySlug(ctx, j.Slug)
	if err != nil {
		j.finish("done", "")
		return
	}
	if err := s.store.DeleteApp(ctx, a.ID); err != nil {
		j.finish("failed", "the app is gone from the server, but Core could not unregister it: "+err.Error())
		return
	}
	_ = s.hub.Publish(ctx, &store.Event{Topic: "core.app.removed", Source: "core", Subject: "app:" + a.Slug, Payload: mustJSON(map[string]any{"slug": a.Slug})})
	j.logf("Unregistered from Pocket Core: its sign-in client, service entry, webhooks and app links are gone")
	j.finish("done", "")
}

func (s *Server) pruneInstallJobs() {
	s.installs.Range(func(k, v any) bool {
		j := v.(*installJob)
		j.mu.Lock()
		old := !j.ended.IsZero() && time.Since(j.ended) > 24*time.Hour
		j.mu.Unlock()
		if old {
			s.installs.Delete(k)
		}
		return true
	})
}

func urlPort(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(u.Port())
	return p
}
