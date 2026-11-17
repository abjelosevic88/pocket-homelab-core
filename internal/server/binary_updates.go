package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Apps that run as a program on the machine, not in a container (Pocket
// AI), update themselves. The signed catalog lists their release downloads
// under "binaries"; Core reads that list itself (no installer agent
// involved), shows the apps on the Apps page and decides when one should
// update. The app asks Core every minute or so (GET /api/v1/apps/self/update
// with its own token), downloads the file Core names, checks its SHA-256,
// swaps it in and restarts, and says how it went (POST). Nothing has to
// reach into the app, so it works wherever the app runs.

const binaryCatalogTTL = 15 * time.Minute

// An app that has not asked for 20 minutes is not taking updates (too old
// to update itself, stopped, or cut off from Core).
const binarySeenFresh = 20 * time.Minute

type binaryCatalogCache struct {
	mu   sync.Mutex
	list []appstore.Binary
	at   time.Time
}

// binarySeen is what the app said on its last check.
type binarySeen struct {
	OS      string    `json:"os"`
	Arch    string    `json:"arch"`
	Version string    `json:"version"`
	Managed string    `json:"managed,omitempty"` // why it cannot replace itself (Pocket Box, read-only file)
	At      time.Time `json:"at"`
}

// binaryUpdate is an update Core asked for and how far the app got.
type binaryUpdate struct {
	Target    string    `json:"target"`
	State     string    `json:"state"` // requested | downloading | installed | done | failed
	Message   string    `json:"message,omitempty"`
	By        string    `json:"by,omitempty"`
	Job       string    `json:"job,omitempty"`
	Requested time.Time `json:"requested_at"`
	Changed   time.Time `json:"changed_at"`
}

func (s *Server) binaryCatalogURL() string {
	if u := strings.TrimSpace(s.cfg.AppCatalogURL); u != "" && u != "github" {
		u, _, _ = strings.Cut(u, ",") // the first of a list
		return strings.TrimSpace(u)
	}
	return appstore.DefaultURL
}

// binaryApps is the catalog's list of self-updating apps (last good copy
// when the catalog cannot be read).
func (s *Server) binaryApps(ctx context.Context, refresh bool) []appstore.Binary {
	c := &s.bins
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && !c.at.IsZero() && time.Since(c.at) < binaryCatalogTTL {
		return c.list
	}
	keys, err := appstore.ParseKeys(append(slices.Clone(appstore.OfficialKeys), s.cfg.AppCatalogKeys...))
	if err != nil {
		s.log.Warn("app catalog keys", "err", err)
		return c.list
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cat, err := appstore.Fetch(fctx, &http.Client{Timeout: 30 * time.Second}, s.binaryCatalogURL(), keys)
	if err != nil {
		s.log.Warn("app catalog (downloadable apps)", "err", err)
		if c.at.IsZero() {
			c.at = time.Now().Add(-binaryCatalogTTL + time.Minute) // try again in a minute
		}
		return c.list
	}
	c.list, c.at = cat.Binaries, time.Now()
	return c.list
}

func (s *Server) findBinaryApp(ctx context.Context, slug string) *appstore.Binary {
	for _, b := range s.binaryApps(ctx, false) {
		if b.Slug == slug {
			return &b
		}
	}
	return nil
}

func (s *Server) getBinarySeen(ctx context.Context, slug string) *binarySeen {
	var v binarySeen
	if json.Unmarshal([]byte(s.store.GetKV(ctx, "binary_seen:"+slug)), &v) != nil || v.At.IsZero() {
		return nil
	}
	return &v
}

func (s *Server) getBinaryUpdate(ctx context.Context, slug string) *binaryUpdate {
	var v binaryUpdate
	if json.Unmarshal([]byte(s.store.GetKV(ctx, "binary_update:"+slug)), &v) != nil || v.Target == "" {
		return nil
	}
	return &v
}

func (s *Server) setBinaryUpdate(ctx context.Context, slug string, u *binaryUpdate) {
	u.Changed = time.Now().UTC()
	b, _ := json.Marshal(u)
	_ = s.store.SetKV(ctx, "binary_update:"+slug, string(b))
}

// binaryReleases lists the self-updating apps the way the installer lists
// docker apps, with what this server's copy can do. skip: slugs another
// source already covers (a Pocket Box builds Pocket AI into the system).
func (s *Server) binaryReleases(ctx context.Context, refresh bool, skip map[string]bool) []installer.Release {
	apps, _ := s.store.ListApps(ctx)
	have := map[string]*store.App{}
	for i := range apps {
		have[apps[i].Slug] = &apps[i]
	}
	var out []installer.Release
	for _, b := range s.binaryApps(ctx, refresh) {
		if skip[b.Slug] {
			continue
		}
		rel := installer.Release{Kind: "binary", Slug: b.Slug, Repo: b.Repo, RepoURL: b.RepoURL, Description: b.Description,
			Version: b.Version, Tag: b.Tag, Name: b.Name, Category: b.Category, Icon: b.Icon, Page: b.Page, Available: true}
		for p := range b.Files {
			rel.Platforms = append(rel.Platforms, p)
		}
		sort.Strings(rel.Platforms)
		if a := have[b.Slug]; a != nil {
			rel.Running = strings.TrimPrefix(a.Version, "v")
			rel.Updatable, rel.UpdateNote = s.binaryUpdatable(ctx, a, &b)
		}
		out = append(out, rel)
	}
	return out
}

// binaryUpdatable says whether the app here can move to b's version.
func (s *Server) binaryUpdatable(ctx context.Context, a *store.App, b *appstore.Binary) (bool, string) {
	seen := s.getBinarySeen(ctx, a.Slug)
	switch {
	case seen == nil:
		return false, "this version cannot update itself yet; update it by hand once, after that Pocket Core can"
	case time.Since(seen.At) > binarySeenFresh:
		return false, fmt.Sprintf("it has not checked for updates since %s; is it running?", seen.At.Local().Format("2 Jan 15:04"))
	case seen.Managed != "":
		return false, seen.Managed
	}
	if _, ok := b.Files[seen.OS+"/"+seen.Arch]; !ok {
		return false, "there is no download for " + seen.OS + "/" + seen.Arch
	}
	return semverNewer(a.Version, b.Version), ""
}

// startBinaryUpdate asks a self-updating app to move to the catalog's
// newest version and follows it.
func (s *Server) startBinaryUpdate(ctx context.Context, a *store.App, b *appstore.Binary, by string) (*installJob, error) {
	ok, note := s.binaryUpdatable(ctx, a, b)
	if !ok {
		if note == "" {
			note = "it already runs " + formatVer(b.Version)
		}
		return nil, &installer.Error{Status: http.StatusUnprocessableEntity, Msg: a.Name + " cannot be updated from here: " + note}
	}
	if cur := s.getBinaryUpdate(ctx, a.Slug); cur != nil && (cur.State == "requested" || cur.State == "downloading") && time.Since(cur.Changed) < 10*time.Minute {
		if v, ok := s.installs.Load(cur.Job); ok {
			return v.(*installJob), nil
		}
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	j := &installJob{ID: "self-" + hex.EncodeToString(rnd[:]), Kind: "update", Slug: a.Slug, Name: a.Name, State: "running", Started: time.Now().UTC()}
	s.installs.Store(j.ID, j)
	s.setBinaryUpdate(ctx, a.Slug, &binaryUpdate{Target: b.Version, State: "requested", By: by, Job: j.ID, Requested: time.Now().UTC()})
	s.log.Info("app self-update requested", "slug", a.Slug, "version", b.Version, "by", by)
	j.logf("Asked %s to update itself from %s to %s", a.Name, formatVer(a.Version), formatVer(b.Version))
	j.logf("Waiting for %s to pick it up (it checks about once a minute)", a.Name)
	go s.followBinaryUpdate(j, a.Slug, b.Version)
	return j, nil
}

// followBinaryUpdate mirrors the app's reports into the job until the app
// registers again with the new version.
func (s *Server) followBinaryUpdate(j *installJob, slug, target string) {
	ctx := context.Background()
	start := time.Now()
	last := "requested"
	var installedAt time.Time
	fail := func(msg string) {
		if u := s.getBinaryUpdate(ctx, slug); u != nil && u.Job == j.ID {
			u.State, u.Message = "failed", msg
			s.setBinaryUpdate(ctx, slug, u)
		}
		j.finish("failed", msg)
	}
	for time.Since(start) < 45*time.Minute {
		time.Sleep(2 * time.Second)
		u := s.getBinaryUpdate(ctx, slug)
		if u == nil || u.Job != j.ID {
			fail("another update replaced this one")
			return
		}
		if u.State != last {
			last = u.State
			switch u.State {
			case "downloading":
				j.logf("Downloading %s", formatVer(target))
			case "installed":
				installedAt = time.Now()
				msg := u.Message
				if msg == "" {
					msg = "Installed; restarting"
				}
				j.logf("%s", msg)
			case "failed":
				msg := u.Message
				if msg == "" {
					msg = "the app could not update itself"
				}
				j.finish("failed", msg)
				return
			}
		}
		if a, err := s.store.GetAppBySlug(ctx, slug); err == nil && !semverNewer(a.Version, target) && semverRe.MatchString(strings.TrimSpace(a.Version)) {
			if u := s.getBinaryUpdate(ctx, slug); u != nil && u.Job == j.ID {
				u.State, u.Message = "done", ""
				s.setBinaryUpdate(ctx, slug, u)
			}
			s.checkApp(ctx, a)
			j.logf("%s runs %s", a.Name, formatVer(a.Version))
			j.mu.Lock()
			j.OpenURL = signedInURL(s.signInStarts(ctx), a.ID, a.EffectivePublicURL())
			j.mu.Unlock()
			j.finish("done", "")
			return
		}
		switch {
		case last == "requested" && time.Since(start) > 5*time.Minute:
			fail("the app did not pick up the update within 5 minutes; is it running and connected to Pocket Core?")
			return
		case last == "installed" && time.Since(installedAt) > 2*time.Minute:
			// It waits for its agents to finish before restarting; that can take long.
			j.logf("It restarts into %s by itself once nothing is running in it", formatVer(target))
			j.finish("done", "")
			return
		}
	}
	fail("gave up waiting for the app to report " + formatVer(target))
}

// handleSelfUpdateCheck: GET /api/v1/apps/self/update — the app's own
// token. Query: os, arch, version, managed (why it cannot replace itself).
func (s *Server) handleSelfUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller := principalFrom(ctx)
	if caller == nil || caller.App == nil {
		writeErr(w, http.StatusForbidden, "apps only")
		return
	}
	q := r.URL.Query()
	clip := func(v string, n int) string {
		if len(v) > n {
			return v[:n]
		}
		return v
	}
	seen := binarySeen{OS: clip(q.Get("os"), 20), Arch: clip(q.Get("arch"), 20), Version: strings.TrimPrefix(clip(q.Get("version"), 40), "v"),
		Managed: clip(q.Get("managed"), 300), At: time.Now().UTC()}
	if b, err := json.Marshal(seen); err == nil {
		_ = s.store.SetKV(ctx, "binary_seen:"+caller.App.Slug, string(b))
	}
	out := map[string]any{"latest": "", "apply": false}
	b := s.findBinaryApp(ctx, caller.App.Slug)
	if b == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["latest"], out["program"] = b.Version, b.Program
	f, ok := b.Files[seen.OS+"/"+seen.Arch]
	if ok {
		out["file"] = f
	}
	if u := s.getBinaryUpdate(ctx, caller.App.Slug); u != nil && u.State == "requested" && ok && seen.Managed == "" {
		switch {
		case semverNewer(seen.Version, b.Version):
			out["apply"], out["target"] = true, b.Version
		default:
			u.State = "done"
			s.setBinaryUpdate(ctx, caller.App.Slug, u)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSelfUpdateReport: POST /api/v1/apps/self/update — how the update
// Core asked for went. {"state": "downloading|installed|failed", "message"}
func (s *Server) handleSelfUpdateReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller := principalFrom(ctx)
	if caller == nil || caller.App == nil {
		writeErr(w, http.StatusForbidden, "apps only")
		return
	}
	var in struct {
		State   string `json:"state"`
		Message string `json:"message"`
	}
	if err := decode(r, &in); err != nil || (in.State != "downloading" && in.State != "installed" && in.State != "failed") {
		writeErr(w, http.StatusBadRequest, "state is downloading, installed or failed")
		return
	}
	if len(in.Message) > 500 {
		in.Message = in.Message[:500]
	}
	u := s.getBinaryUpdate(ctx, caller.App.Slug)
	if u == nil || u.State == "done" || u.State == "failed" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	u.State, u.Message = in.State, in.Message
	s.setBinaryUpdate(ctx, caller.App.Slug, u)
	if in.State == "failed" {
		s.log.Warn("app self-update failed", "slug", caller.App.Slug, "version", u.Target, "err", in.Message)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
