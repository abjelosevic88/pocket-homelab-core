package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Nightly app updates and update notifications, in the background.
//
// Once a day at the hour set under Settings → Install and remove apps, Core
// reads the app catalog. With "Update apps automatically" on, every app the
// installer can update moves to its newest release, one after the other
// (each with a data backup and a rollback, see internal/installer). Each
// update, each failure and each new version it could not or may not apply
// is announced once: as an event on the bus (core.app.updated,
// core.app.update_failed, core.apps.updates_available) and, unless
// switched off, as a notification to an ntfy topic or Apprise URL.

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// semverNewer reports b > a for plain X.Y.Z versions.
func semverNewer(a, b string) bool {
	x, y := semverRe.FindStringSubmatch(strings.TrimSpace(a)), semverRe.FindStringSubmatch(strings.TrimSpace(b))
	if x == nil || y == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		p, _ := strconv.Atoi(x[i])
		q, _ := strconv.Atoi(y[i])
		if p != q {
			return q > p
		}
	}
	return false
}

// updateRun is what one pass found and did.
type updateRun struct {
	Updated   []string `json:"updated"`   // "Pocket Notes 0.11.0 → 0.12.0"
	Failed    []string `json:"failed"`    // "Pocket Notes 0.12.0: <why>"
	Available []string `json:"available"` // new versions not applied (announced this time)
	Skipped   []string `json:"skipped"`   // already announced
}

var autoUpdateMu sync.Mutex

// autoUpdater wakes every few minutes and runs the nightly pass once a day.
func (s *Server) autoUpdater(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st, err := s.store.GetSettings(ctx)
			if err != nil || s.installerState(ctx) != "" {
				continue
			}
			now := time.Now()
			if now.Hour() != st.AutoUpdateHour || s.store.GetKV(ctx, "auto_update_last") == now.Format("2006-01-02") {
				continue
			}
			_ = s.store.SetKV(ctx, "auto_update_last", now.Format("2006-01-02"))
			if _, err := s.runUpdates(ctx, st.AutoUpdate); err != nil {
				s.log.Warn("nightly app updates", "err", err)
			}
		}
	}
}

// runUpdates is one pass; apply=false only announces new versions.
func (s *Server) runUpdates(ctx context.Context, apply bool) (*updateRun, error) {
	if !autoUpdateMu.TryLock() {
		return nil, errors.New("an update pass is already running")
	}
	defer autoUpdateMu.Unlock()
	run := &updateRun{}
	rels, err := s.inst.Catalog(ctx, true)
	if err != nil {
		return nil, err
	}
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]*store.App{}
	for i := range apps {
		have[apps[i].Slug] = &apps[i]
	}
	for _, rel := range rels {
		a := have[rel.Slug]
		if a == nil || a.Slug == "core" || !semverNewer(a.Version, rel.Version) {
			continue
		}
		label := fmt.Sprintf("%s %s → %s", a.Name, strings.TrimPrefix(a.Version, "v"), rel.Version)
		if apply && rel.Updatable {
			if err := s.updateAndWait(ctx, a); err != nil {
				run.Failed = append(run.Failed, fmt.Sprintf("%s %s: %v", a.Name, rel.Version, err))
				s.announce(ctx, "core.app.update_failed", a.Slug, rel.Version, "Update failed: "+a.Name,
					fmt.Sprintf("%s could not be updated to %s and is back on %s.\n%v", a.Name, rel.Version, strings.TrimPrefix(a.Version, "v"), err), 4, "warning")
			} else {
				run.Updated = append(run.Updated, label)
				s.announce(ctx, "core.app.updated", a.Slug, rel.Version, "Updated "+a.Name,
					fmt.Sprintf("%s is now on %s (was %s).", a.Name, rel.Version, strings.TrimPrefix(a.Version, "v")), 2, "arrow_up")
			}
			_ = s.store.SetKV(ctx, "update_announced:"+a.Slug, rel.Version)
			continue
		}
		if s.store.GetKV(ctx, "update_announced:"+a.Slug) == rel.Version {
			run.Skipped = append(run.Skipped, label)
			continue
		}
		why := ""
		switch {
		case !rel.Updatable && rel.UpdateNote != "":
			why = " (update it by hand: " + rel.UpdateNote + ")"
		case !rel.Updatable:
			why = " (update it by hand)"
		}
		run.Available = append(run.Available, label+why)
		_ = s.store.SetKV(ctx, "update_announced:"+a.Slug, rel.Version)
	}
	if len(run.Available) > 0 {
		title := "1 app update available"
		if n := len(run.Available); n > 1 {
			title = fmt.Sprintf("%d app updates available", n)
		}
		s.announce(ctx, "core.apps.updates_available", "", "", title, strings.Join(run.Available, "\n"), 3, "package")
	}
	return run, nil
}

// updateAndWait runs one update job to its end. The agent does one job at
// a time, so a busy agent is waited for.
func (s *Server) updateAndWait(ctx context.Context, a *store.App) error {
	var j *installJob
	for try := 0; ; try++ {
		var err error
		j, err = s.startUpgrade(ctx, a, "automatic update")
		var ie *installer.Error
		if err == nil {
			break
		}
		if !errors.As(err, &ie) || ie.Status != http.StatusConflict || try >= 20 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
	deadline := time.Now().Add(40 * time.Minute)
	for time.Now().Before(deadline) {
		snap := j.snapshot()
		switch snap["state"] {
		case "done":
			return nil
		case "failed":
			if e, _ := snap["error"].(string); e != "" {
				return errors.New(e)
			}
			return errors.New("the update failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("gave up waiting for the update")
}

// announce puts a notice on the bus and, unless switched off, sends it.
func (s *Server) announce(ctx context.Context, topic, slug, version, title, body string, priority int, tag string) {
	payload := map[string]any{"title": title, "message": body}
	subject := "core"
	if slug != "" {
		payload["slug"], payload["version"] = slug, version
		subject = "app:" + slug
	}
	_ = s.hub.Publish(ctx, &store.Event{Topic: topic, Source: "core", Subject: subject, Payload: mustJSON(payload)})
	st, err := s.store.GetSettings(ctx)
	if err != nil || !st.NotifyUpdates {
		return
	}
	if err := s.notify(ctx, st, title, body, priority, tag); err != nil {
		s.log.Warn("notification not sent", "title", title, "err", err)
	}
}

// notifyTarget is the address notifications go to.
func (s *Server) notifyTarget(st store.Settings) string {
	if st.NotifyURL != "" {
		return st.NotifyURL
	}
	return s.cfg.NotifyURL
}

// notify sends one notification: Apprise API for …/notify/<key> URLs,
// ntfy for anything else (POST the text, Title/Priority/Tags headers).
// user:password@ in the URL becomes Basic auth.
func (s *Server) notify(ctx context.Context, st store.Settings, title, body string, priority int, tag string) error {
	target := s.notifyTarget(st)
	if target == "" {
		return errors.New("no notification address set")
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the notification address %q is not an http(s) URL", target)
	}
	var req *http.Request
	if strings.Contains(u.Path, "/notify/") {
		typ := "info"
		if priority >= 4 {
			typ = "failure"
		} else if tag == "arrow_up" {
			typ = "success"
		}
		b, _ := json.Marshal(map[string]string{"title": title, "body": body, "type": typ})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Title", title)
		req.Header.Set("Priority", strconv.Itoa(priority))
		if tag != "" {
			req.Header.Set("Tags", tag)
		}
	}
	if u.User != nil {
		pw, _ := u.User.Password()
		req.SetBasicAuth(u.User.Username(), pw)
	}
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s answered %s", u.Host, res.Status)
	}
	return nil
}

// handleCheckUpdates runs the nightly pass now (POST /installer/check-updates).
func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	if s.installerOff(w, r) {
		return
	}
	st, err := s.store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	// Updates can take a while: run detached from the request.
	type result struct {
		run *updateRun
		err error
	}
	out := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Hour)
		defer cancel()
		run, err := s.runUpdates(ctx, st.AutoUpdate)
		out <- result{run, err}
	}()
	select {
	case res := <-out:
		if res.err != nil {
			writeErr(w, http.StatusConflict, res.err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res.run)
	case <-time.After(20 * time.Second):
		writeJSON(w, http.StatusAccepted, map[string]any{"running": true})
	}
}

// handleNotifyTest sends a test notification (POST /settings/notify-test).
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	if err := s.notify(r.Context(), st, "Pocket Core test", "Notifications from Pocket Core arrive here: app updates and failed updates.", 3, "bell"); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "to": redactURL(s.notifyTarget(st))})
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	return u.String()
}
