package server

// First-run setup of a Core that is not on a Pocket Box (deploy/install.sh on any Linux machine
// with Docker): right after the first account, the admin picks the apps to start with, whole
// bundles or one by one, and Core installs them one after the other through the installer agent.
// The queue runs here, not in the browser, so closing the page does not stop it.
//
// On a Pocket Box the box's own wizard (BoxWizard, BOX_AGENT) does this; these routes are for the
// rest. The bundles are Pocket Box's (bundles.json in pocket-homelab-box), so both offer the same.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// The copy that ships with Core, for when GitHub can't be reached.
//
//go:embed setup-bundles.json
var bundledBundles []byte

const bundlesURL = "https://raw.githubusercontent.com/abjelosevic88/pocket-homelab-box/main/bundles.json"

// setupDoneKey is set once the admin finished (or skipped) the setup.
const setupDoneKey = "setup_wizard_done"

// setupQueueKey keeps the install queue across restarts of Core: choosing an address restarts Core
// (its address changes) while the apps may still be installing.
const setupQueueKey = "setup_install_queue"

type savedQueue struct {
	UserID int64       `json:"user_id"`
	Items  []setupItem `json:"items"`
}

type bundle struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Apps        []string `json:"apps"`
}

type bundleList struct {
	Base    []string `json:"base"`
	Bundles []bundle `json:"bundles"`
}

type bundleCache struct {
	mu   sync.Mutex
	list *bundleList
	at   time.Time
}

// setupItem is one app of the setup's install queue.
type setupItem struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	State string `json:"state"` // waiting | installing | done | failed
	Error string `json:"error,omitempty"`
	Job   string `json:"job,omitempty"`
}

type setupQueue struct {
	mu      sync.Mutex
	running bool
	items   []*setupItem
	userID  int64
	save    func(savedQueue) // set by the server: into the settings table
}

func (q *setupQueue) snapshot() map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := make([]setupItem, len(q.items))
	for i, it := range q.items {
		items[i] = *it
	}
	return map[string]any{"running": q.running, "items": items}
}

func (q *setupQueue) set(it *setupItem, state, errText, job string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	it.State, it.Error = state, errText
	if job != "" {
		it.Job = job
	}
	q.persist()
}

// persist saves the queue (q.mu held).
func (q *setupQueue) persist() {
	if q.save == nil {
		return
	}
	sq := savedQueue{UserID: q.userID}
	for _, it := range q.items {
		sq.Items = append(sq.Items, *it)
	}
	q.save(sq)
}

// resumeFirstApps carries on with a queue a restart of Core interrupted.
func (s *Server) resumeFirstApps(ctx context.Context) {
	q := &s.setup
	q.save = func(sq savedQueue) {
		b, _ := json.Marshal(sq)
		_ = s.store.SetKV(context.WithoutCancel(ctx), setupQueueKey, string(b))
	}
	var sq savedQueue
	if json.Unmarshal([]byte(s.store.GetKV(ctx, setupQueueKey)), &sq) != nil || len(sq.Items) == 0 {
		return
	}
	items := make([]*setupItem, len(sq.Items))
	open := false
	for i := range sq.Items {
		items[i] = &sq.Items[i]
		open = open || items[i].State == "waiting" || items[i].State == "installing"
	}
	q.mu.Lock()
	q.items, q.userID = items, sq.UserID
	q.mu.Unlock()
	if !open || s.inst == nil {
		return
	}
	u, err := s.store.GetUser(ctx, sq.UserID)
	if err != nil {
		return
	}
	q.mu.Lock()
	q.running = true
	q.mu.Unlock()
	s.log.Info("setup install: carrying on after a restart", "apps", len(items))
	go s.runSetupInstall(u, items)
}

// setupPending says whether the admin still gets the setup: a Core with an installer agent but no
// Pocket Box agent, nothing installed yet, and the setup not finished or skipped.
func (s *Server) setupPending(ctx context.Context) bool {
	if s.box != nil || s.installerState(ctx) != "" || s.store.GetKV(ctx, setupDoneKey) == "1" {
		return false
	}
	apps, err := s.store.ListApps(ctx)
	return err == nil && len(apps) == 0
}

// bundles: Pocket Box's bundles from GitHub (an hour at most old), or the copy in Core.
func (s *Server) bundles(ctx context.Context) bundleList {
	c := &s.bundleCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list != nil && time.Since(c.at) < time.Hour {
		return *c.list
	}
	var out bundleList
	if raw, err := fetchBundles(ctx); err == nil && json.Unmarshal(raw, &out) == nil && len(out.Bundles) > 0 {
		c.list, c.at = &out, time.Now()
		return out
	}
	_ = json.Unmarshal(bundledBundles, &out)
	if c.list != nil {
		return *c.list
	}
	return out
}

func fetchBundles(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bundlesURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 256<<10))
}

// GET /api/v1/first-apps: whether the setup is due, the bundles and the install queue.
func (s *Server) handleFirstApps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"pending": s.setupPending(r.Context()),
		"bundles": s.bundles(r.Context()),
		"install": s.setup.snapshot(),
	})
}

// POST /api/v1/first-apps/install {apps: [{slug, name}]}: installs them one after the other.
func (s *Server) handleFirstAppsInstall(w http.ResponseWriter, r *http.Request) {
	if s.installerOff(w, r) {
		return
	}
	var in struct {
		Apps []struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"apps"`
	}
	if err := decode(r, &in); err != nil || len(in.Apps) == 0 || len(in.Apps) > 200 {
		writeErr(w, http.StatusBadRequest, "apps is required")
		return
	}
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "sign in first")
		return
	}
	var items []*setupItem
	seen := map[string]bool{}
	for _, a := range in.Apps {
		if !slugRe.MatchString(a.Slug) || a.Slug == "core" {
			writeErr(w, http.StatusBadRequest, "not an app: "+a.Slug)
			return
		}
		if !seen[a.Slug] {
			seen[a.Slug] = true
			items = append(items, &setupItem{Slug: a.Slug, Name: appName(a.Slug, a.Name), State: "waiting"})
		}
	}
	q := &s.setup
	q.mu.Lock()
	if q.running {
		q.mu.Unlock()
		writeErr(w, http.StatusConflict, "the apps chosen before are still being installed")
		return
	}
	q.running, q.items, q.userID = true, items, u.ID
	q.persist()
	q.mu.Unlock()
	user := *u
	go s.runSetupInstall(&user, items)
	writeJSON(w, http.StatusAccepted, q.snapshot())
}

// runSetupInstall installs the queue's apps in order; one that fails doesn't stop the rest.
func (s *Server) runSetupInstall(u *store.User, items []*setupItem) {
	q := &s.setup
	defer func() {
		q.mu.Lock()
		q.running = false
		q.mu.Unlock()
	}()
	for _, it := range items {
		if s.ctx.Err() != nil {
			return // Core is stopping: the saved queue carries on when it is back
		}
		if it.State == "done" || it.State == "failed" {
			continue
		}
		if it.State == "installing" {
			// Interrupted by a restart: the agent kept going, so give the app time to register.
			for i := 0; i < 45 && !s.isRegistered(it.Slug); i++ {
				select {
				case <-s.ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		}
		if s.isRegistered(it.Slug) {
			q.set(it, "done", "", "")
			continue
		}
		q.set(it, "installing", "", "")
		j, err := s.startSetupInstall(u, it)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			if s.isRegistered(it.Slug) {
				q.set(it, "done", "", "")
			} else {
				q.set(it, "failed", err.Error(), "")
			}
			continue
		}
		q.set(it, "installing", "", j.ID)
		for {
			j.mu.Lock()
			state, errText := j.State, j.Error
			j.mu.Unlock()
			if state != "running" {
				if state == "done" {
					q.set(it, "done", "", "")
				} else {
					q.set(it, "failed", errText, "")
				}
				break
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		s.log.Info("setup install", "slug", it.Slug, "state", it.State)
	}
}

func (s *Server) isRegistered(slug string) bool {
	_, err := s.store.GetAppBySlug(s.ctx, slug)
	return err == nil
}

// startSetupInstall starts one install, waiting while another install (from the Apps page, or
// the nightly updates) still holds the agent.
func (s *Server) startSetupInstall(u *store.User, it *setupItem) (*installJob, error) {
	for try := 0; ; try++ {
		j, err := s.startInstall(s.ctx, u, it.Slug, it.Name, "")
		var ie *installer.Error
		if err == nil || try >= 90 || !errors.As(err, &ie) || ie.Status != http.StatusConflict {
			return j, err
		}
		if _, regErr := s.store.GetAppBySlug(s.ctx, it.Slug); regErr == nil {
			return nil, err // registered meanwhile: the conflict is that, not a busy agent
		}
		select {
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// POST /api/v1/first-apps/done: the admin finished or skipped the setup; it is not offered again.
func (s *Server) handleFirstAppsDone(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SetKV(r.Context(), setupDoneKey, "1"); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": false})
}
