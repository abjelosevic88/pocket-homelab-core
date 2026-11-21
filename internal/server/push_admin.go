package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/push"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Admin alerts for the Pocket Core app (docs/push.md#the-pocket-core-app). An admin's phone that
// registered with the topic org.bjelke.pocketcore hears from Core itself: an app went down or came
// back, an install/update/removal failed (or finished), updates are waiting, the box went offline
// or is back. Each registration picks the kinds it wants. These devices never get the apps'
// messages from /push/notify (Pocket Hub shows those), so nothing arrives twice.

// Alert kinds, as the app sends them in PUT /devices/me/push {"kinds": […]}.
const (
	kindAppDown    = "app_down"    // an app stayed down across two health checks
	kindAppUp      = "app_up"      // an app that was reported down answers again
	kindJobFailed  = "job_failed"  // an install, update or removal failed
	kindJobDone    = "job_done"    // an install, update or removal finished (off by default)
	kindUpdates    = "updates"     // updates waiting: a digest, at most once a day
	kindBoxOffline = "box_offline" // the box went offline (sent by the relay) / is back online
)

var alertKinds = []struct {
	name string
	on   bool // on by default
}{
	{kindAppDown, true}, {kindAppUp, true}, {kindJobFailed, true}, {kindJobDone, false}, {kindUpdates, true}, {kindBoxOffline, true},
}

func alertKindNames() []string {
	out := make([]string, len(alertKinds))
	for i, k := range alertKinds {
		out[i] = k.name
	}
	return out
}

// encodeKinds turns the app's list into the stored form (known kinds in a fixed order; "none" for
// an empty list). bad is the first unknown kind.
func encodeKinds(in []string) (stored, bad string) {
	var out []string
	for _, k := range alertKinds {
		if slices.Contains(in, k.name) {
			out = append(out, k.name)
		}
	}
	for _, k := range in {
		if !slices.Contains(alertKindNames(), k) {
			return "", k
		}
	}
	if len(out) == 0 {
		return "none", ""
	}
	return strings.Join(out, ","), ""
}

// decodeKinds is the list a stored value stands for ("" = the defaults).
func decodeKinds(stored string) []string {
	out := []string{}
	switch stored {
	case "none":
	case "":
		for _, k := range alertKinds {
			if k.on {
				out = append(out, k.name)
			}
		}
	default:
		for _, k := range strings.Split(stored, ",") {
			if slices.Contains(alertKindNames(), k) {
				out = append(out, k)
			}
		}
	}
	return out
}

func wantsKind(reg store.PushRegistration, kind string) bool {
	return slices.Contains(decodeKinds(reg.Kinds), kind)
}

// ---- words and links ----

// boxLabel is what the box is called in a notification.
func (s *Server) boxLabel(ctx context.Context) string {
	if st, err := s.store.GetSettings(ctx); err == nil && strings.TrimSpace(st.BoxName) != "" {
		return strings.TrimSpace(st.BoxName)
	}
	return "Your box"
}

// pushBoxKey tells this box apart in thread and collapse ids (the relay allows 64 bytes).
func (s *Server) pushBoxKey() string {
	h := sha256.Sum256([]byte(s.publicBase()))
	return hex.EncodeToString(h[:8])
}

// boxThread groups this box's notifications on the phone.
func (s *Server) boxThread() string { return "box-" + s.pushBoxKey() }

// statusCollapse makes "back online" replace "offline" on the phone (per box).
func (s *Server) statusCollapse() string { return "status-" + s.pushBoxKey() }

// coreAppLink opens this box (and an app's page) in the Pocket Core app. The device id lets the
// app find the box even when it paired through another address than Core's own.
func (s *Server) coreAppLink(slug string, deviceID int64) string {
	q := url.Values{}
	q.Set("core", s.publicBase())
	if deviceID > 0 {
		q.Set("device", strconv.FormatInt(deviceID, 10))
	}
	if slug != "" {
		q.Set("app", slug)
	}
	return "pocketcore://box?" + q.Encode()
}

// shortName is "Money" for "Pocket Money".
func shortName(name, slug string) string {
	n := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "Pocket "))
	if n == "" {
		n = appName(slug)
		n = strings.TrimSpace(strings.TrimPrefix(n, "Pocket "))
	}
	return n
}

// about says a duration in plain words: "4 min", "2 h 5 min", "3 days".
func about(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// ---- sending ----

// adminAlerts holds what the alerts remember between calls.
type adminAlerts struct {
	mu     sync.Mutex
	grace  time.Duration // no "app down" this soon after Core starts (apps are still starting)
	apps   map[string]*appWatch
	loaded bool

	kick     chan struct{} // run the box-offline heartbeat now
	watch    string        // offline watch state for GET /devices/me/push
	watching int           // 0 unknown, 1 the relay watches this box, -1 it doesn't
	retryAt  time.Time     // an old relay: ask again then
	wg       sync.WaitGroup
	manual   atomic.Bool // tests: the background loop leaves the heartbeat and digest to them
}

func newAdminAlerts() *adminAlerts {
	return &adminAlerts{grace: 3 * time.Minute, apps: map[string]*appWatch{}, kick: make(chan struct{}, 1), watch: watchOff}
}

// Offline watch states (GET /devices/me/push "offline_watch").
const (
	watchOff       = "off"           // no admin device wants box-offline alerts (or PUSH_OFFLINE_AFTER=off)
	watchOn        = "on"            // the relay watches this box
	watchNoRelay   = "no_relay"      // no relay token, or the relay can't be reached
	watchOldRelay  = "relay_too_old" // the relay has no /v1/heartbeat yet
	watchStarting  = "starting"      // asked, no answer yet
	alertSourceTag = "Pocket Core"
)

// adminRegs are the enabled Pocket Core app registrations of admins that want kind.
func (s *Server) adminRegs(ctx context.Context, kind string) []store.PushRegistration {
	regs, err := s.store.AdminPushRegistrations(ctx, push.TopicCoreApp)
	if err != nil {
		s.log.Warn("admin alerts: list registrations", "err", err)
		return nil
	}
	out := regs[:0]
	for _, r := range regs {
		if wantsKind(r, kind) {
			out = append(out, r)
		}
	}
	return out
}

// adminAlert sends one alert to every admin device that wants its kind, in the background (the
// relay may take a while; health checks and jobs don't wait for it).
func (s *Server) adminAlert(kind, title, body, slug string) {
	s.alerts.wg.Add(1)
	go func() {
		defer s.alerts.wg.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 30*time.Second)
		defer cancel()
		regs := s.adminRegs(ctx, kind)
		if len(regs) == 0 {
			return
		}
		id := kind + "-" + slug + "-" + strconv.FormatInt(time.Now().Unix(), 10)
		res, _ := s.pushSendEach(ctx, regs, func(r store.PushRegistration) push.Message {
			return push.Message{Title: title, Body: body, URL: s.coreAppLink(slug, r.DeviceID), Source: alertSourceTag, ID: id, Sound: true, Thread: s.boxThread()}
		}, "")
		s.log.Info("admin alert", "kind", kind, "app", slug, "devices", res.Devices, "sent", res.Sent, "failed", res.Failed, "gone", res.Gone, "reason", res.Reason)
	}()
}

// ---- app down / back up ----

// appWatch is one app's run of failed checks.
type appWatch struct {
	DownSince time.Time `json:"down_since"`
	Checks    int       `json:"-"` // failed scheduled checks in a row
	Alerted   bool      `json:"-"` // "down" went out; "back up" is owed
}

type polledKey struct{}

// polled marks the scheduled health checks: only they count towards "down" (a "Check now" twice
// in a row is not "down for two checks").
func polled(ctx context.Context) context.Context { return context.WithValue(ctx, polledKey{}, true) }

const downAlertedKV = "push_down_alerted" // slug → when it went down, for apps whose "down" went out

// noteHealth sees every health check. An app is reported down after two scheduled checks in a row
// failed (a restart or an update in between doesn't alert; Core skips apps it is updating or
// removing), and back up on the first good check after that.
func (s *Server) noteHealth(ctx context.Context, a *store.App, status, msg string) {
	al := s.alerts
	al.mu.Lock()
	defer al.mu.Unlock()
	s.loadDownAlerted(ctx)
	w := al.apps[a.Slug]
	switch status {
	case "down":
		if w == nil {
			w = &appWatch{DownSince: time.Now()}
			al.apps[a.Slug] = w
		}
		if ctx.Value(polledKey{}) != nil {
			w.Checks++
		}
		if w.Alerted || w.Checks < 2 || time.Since(s.started) < al.grace {
			return
		}
		w.Alerted = true
		s.saveDownAlerted(ctx)
		body := msg
		if body == "" {
			body = "It doesn't answer its health check."
		}
		s.adminAlert(kindAppDown, fmt.Sprintf("%s is down on %s", shortName(a.Name, a.Slug), s.boxLabel(ctx)), body, a.Slug)
	case "ok":
		if w == nil {
			return
		}
		delete(al.apps, a.Slug)
		if !w.Alerted {
			return
		}
		s.saveDownAlerted(ctx)
		s.adminAlert(kindAppUp, fmt.Sprintf("%s is back up on %s", shortName(a.Name, a.Slug), s.boxLabel(ctx)),
			"It was down for "+about(time.Since(w.DownSince))+".", a.Slug)
	}
}

// forgetApps drops apps that are no longer registered (removed while down).
func (s *Server) forgetApps(ctx context.Context, present map[string]bool) {
	al := s.alerts
	al.mu.Lock()
	defer al.mu.Unlock()
	s.loadDownAlerted(ctx)
	changed := false
	for slug, w := range al.apps {
		if !present[slug] {
			delete(al.apps, slug)
			changed = changed || w.Alerted
		}
	}
	if changed {
		s.saveDownAlerted(ctx)
	}
}

// loadDownAlerted reads which apps' "down" went out before a restart, so "back up" still comes.
// Called with al.mu held.
func (s *Server) loadDownAlerted(ctx context.Context) {
	al := s.alerts
	if al.loaded {
		return
	}
	al.loaded = true
	var m map[string]time.Time
	if json.Unmarshal([]byte(s.store.GetKV(ctx, downAlertedKV)), &m) != nil {
		return
	}
	for slug, since := range m {
		al.apps[slug] = &appWatch{DownSince: since, Checks: 2, Alerted: true}
	}
}

// saveDownAlerted keeps the apps whose "down" went out. Called with al.mu held.
func (s *Server) saveDownAlerted(ctx context.Context) {
	m := map[string]time.Time{}
	for slug, w := range s.alerts.apps {
		if w.Alerted {
			m[slug] = w.DownSince
		}
	}
	b, _ := json.Marshal(m)
	_ = s.store.SetKV(context.WithoutCancel(ctx), downAlertedKV, string(b))
}

// ---- installs, updates, removals ----

// trackJob keeps an install job for the API and alerts admins when it ends.
func (s *Server) trackJob(j *installJob) {
	j.mu.Lock()
	j.onEnd = s.jobEnded
	j.mu.Unlock()
	s.installs.Store(j.ID, j)
}

// jobEnded alerts admins about a finished or failed install, update or removal.
func (s *Server) jobEnded(j *installJob) {
	j.mu.Lock()
	kind, slug, name, state, errMsg := j.Kind, j.Slug, j.Name, j.State, j.Error
	version := ""
	if j.Result != nil {
		version = strings.TrimPrefix(j.Result.Version, "v")
	}
	j.mu.Unlock()
	ctx := context.WithoutCancel(s.ctx)
	app, box := shortName(name, slug), s.boxLabel(ctx)
	if state == "failed" {
		body := errMsg
		if body == "" {
			body = "Open the box to see its log."
		}
		title := ""
		switch kind {
		case "install":
			title = fmt.Sprintf("%s didn't install on %s", app, box)
		case "update":
			title = fmt.Sprintf("%s didn't update on %s", app, box)
		default:
			title = fmt.Sprintf("%s wasn't removed from %s", app, box)
		}
		s.adminAlert(kindJobFailed, title, body, slug)
		return
	}
	switch kind {
	case "install":
		body := "It's ready."
		if version != "" {
			body = "Version " + version + " is ready."
		}
		s.adminAlert(kindJobDone, fmt.Sprintf("%s is installed on %s", app, box), body, slug)
	case "update":
		body := "Done."
		if version != "" {
			body = "Now on " + version + "."
		}
		s.adminAlert(kindJobDone, fmt.Sprintf("%s is updated on %s", app, box), body, slug)
	default:
		s.adminAlert(kindJobDone, fmt.Sprintf("%s was removed from %s", app, box), "Its data is kept.", "")
	}
}

// ---- updates waiting ----

// waitingUpdate is one app with a newer version not applied.
type waitingUpdate struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

const (
	updatesPendingKV = "push_updates_pending" // what the last update pass found waiting
	updatesSeenKV    = "push_updates_seen"    // slug → version already in a digest
	updatesSentKV    = "push_updates_sent_at" // when the last digest went out
)

// noteUpdatesWaiting keeps what the nightly pass found; the digest goes out in the daytime.
func (s *Server) noteUpdatesWaiting(ctx context.Context, waiting []waitingUpdate) {
	b, _ := json.Marshal(waiting)
	_ = s.store.SetKV(ctx, updatesPendingKV, string(b))
	s.kickWatch()
}

// digestHours is when a digest may go out (the box's local time): not in the night.
func digestHours(t time.Time) bool { return t.Hour() >= 9 && t.Hour() < 21 }

// sendUpdatesDigest sends "3 updates waiting on Home" once something new is waiting, at most once
// a day and only in the daytime.
func (s *Server) sendUpdatesDigest(ctx context.Context, now time.Time) bool {
	var waiting []waitingUpdate
	if json.Unmarshal([]byte(s.store.GetKV(ctx, updatesPendingKV)), &waiting) != nil || len(waiting) == 0 {
		return false
	}
	seen := map[string]string{}
	_ = json.Unmarshal([]byte(s.store.GetKV(ctx, updatesSeenKV)), &seen)
	fresh := false
	for _, u := range waiting {
		if seen[u.Slug] != u.To {
			fresh = true
		}
	}
	if !fresh || !digestHours(now) {
		return false
	}
	if last, err := time.Parse(time.RFC3339, s.store.GetKV(ctx, updatesSentKV)); err == nil && now.Sub(last) < 20*time.Hour {
		return false
	}
	if len(s.adminRegs(ctx, kindUpdates)) == 0 {
		return false // nobody to tell; a phone that turns it on later still hears about these
	}
	box := s.boxLabel(ctx)
	parts := make([]string, 0, len(waiting))
	for _, u := range waiting {
		parts = append(parts, fmt.Sprintf("%s %s → %s", shortName(u.Name, u.Slug), u.From, u.To))
		seen[u.Slug] = u.To
	}
	title, slug := fmt.Sprintf("%d updates waiting on %s", len(waiting), box), ""
	if len(waiting) == 1 {
		title, slug = fmt.Sprintf("An update for %s is waiting on %s", shortName(waiting[0].Name, waiting[0].Slug), box), waiting[0].Slug
	}
	s.adminAlert(kindUpdates, title, strings.Join(parts, ", "), slug)
	b, _ := json.Marshal(seen)
	_ = s.store.SetKV(ctx, updatesSeenKV, string(b))
	_ = s.store.SetKV(ctx, updatesSentKV, now.Format(time.RFC3339))
	return true
}

// ---- box offline: the relay's dead-man's switch ----

const heartbeatOKKV = "push_heartbeat_ok_at" // the last heartbeat the relay took

// kickWatch runs the heartbeat (and the digest check) now instead of at the next beat.
func (s *Server) kickWatch() {
	select {
	case s.alerts.kick <- struct{}{}:
	default:
	}
}

// heartbeatEvery is how often Core checks in with the relay.
func (s *Server) heartbeatEvery() time.Duration {
	d := s.cfg.PushOfflineAfter / 5
	return min(max(d, 30*time.Second), 2*time.Minute)
}

// pushWatch checks in with the relay while admins want box-offline alerts, and sends the
// updates digest when it's due.
func (s *Server) pushWatch(ctx context.Context) {
	t := time.NewTimer(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.alerts.kick:
		}
		if !s.alerts.manual.Load() {
			s.heartbeatOnce(ctx, time.Now())
			s.sendUpdatesDigest(ctx, time.Now())
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(s.heartbeatEvery())
	}
}

// heartbeatOnce sends one heartbeat with fresh "offline" notices sealed for every admin device
// that wants them, or stops the watch when none does. When the relay says the last notices went
// out, Core tells the same devices the box is back.
func (s *Server) heartbeatOnce(ctx context.Context, now time.Time) {
	al := s.alerts
	var regs []store.PushRegistration
	if s.cfg.PushOfflineAfter > 0 {
		regs = s.adminRegs(ctx, kindBoxOffline)
	}
	if len(regs) == 0 {
		al.mu.Lock()
		watching := al.watching
		al.watch = watchOff
		al.mu.Unlock()
		if watching >= 0 { // unknown after a start, or watched: make sure the relay forgets it
			if _, err := s.pushRelay.StopHeartbeat(ctx); err == nil || errors.Is(err, push.ErrNoHeartbeat) {
				al.mu.Lock()
				al.watching = -1
				al.mu.Unlock()
			}
		}
		return
	}
	al.mu.Lock()
	if now.Before(al.retryAt) {
		al.mu.Unlock()
		return
	}
	if al.watch == watchOff {
		al.watch = watchStarting
	}
	al.mu.Unlock()
	if len(regs) > 50 {
		regs = regs[:50] // the relay's limit per heartbeat
	}
	box := s.boxLabel(ctx)
	off := push.Message{Title: box + " is offline",
		Body:   "No word from it since " + now.Format("15:04") + ". It may be switched off, without power or without internet.",
		Source: alertSourceTag, ID: "offline-" + strconv.FormatInt(now.Unix(), 10), Sound: true, Thread: s.boxThread(), At: now.UTC().Format(time.RFC3339)}
	outs := make([]push.Out, 0, len(regs))
	for _, r := range regs {
		m := off
		m.URL = s.coreAppLink("", r.DeviceID)
		m.Sound = r.Sound
		sealed, err := push.SealMessage(r.PublicKey, m)
		if err != nil {
			continue
		}
		outs = append(outs, push.Out{Token: r.APNsToken, Environment: r.Environment, Topic: r.Topic, Sealed: sealed,
			Sound: m.Sound, Thread: m.Thread, CollapseID: s.statusCollapse(), Priority: 10})
	}
	if len(outs) == 0 {
		return
	}
	res, err := s.pushRelay.Heartbeat(ctx, s.cfg.PushOfflineAfter, outs)
	al.mu.Lock()
	switch {
	case errors.Is(err, push.ErrNoHeartbeat):
		if al.watch != watchOldRelay {
			s.log.Info("box offline alerts need a newer push relay (no /v1/heartbeat); asking again in an hour", "relay", s.pushRelay.URL())
		}
		al.watch, al.watching, al.retryAt = watchOldRelay, -1, now.Add(time.Hour)
	case err != nil:
		if al.watch != watchNoRelay {
			s.log.Warn("box offline heartbeat", "err", err)
		}
		al.watch = watchNoRelay
	default:
		if al.watching != 1 {
			s.log.Info("the push relay watches this box", "deadline", s.cfg.PushOfflineAfter.String(), "devices", len(outs))
		}
		al.watch, al.watching = watchOn, 1
	}
	al.mu.Unlock()
	if err != nil {
		return
	}
	lastOK, _ := time.Parse(time.RFC3339, s.store.GetKV(ctx, heartbeatOKKV))
	_ = s.store.SetKV(ctx, heartbeatOKKV, now.UTC().Format(time.RFC3339))
	if !res.Fired {
		return
	}
	body := "It's reachable again."
	if !lastOK.IsZero() && now.After(lastOK) {
		body = fmt.Sprintf("It was offline for about %s, from %s.", about(now.Sub(lastOK)), lastOK.Local().Format("15:04"))
	}
	s.log.Info("box back online after the relay sent its offline notices", "fired_at", res.FiredAt.Format(time.RFC3339))
	back, _ := s.pushSendEach(ctx, regs, func(r store.PushRegistration) push.Message {
		return push.Message{Title: box + " is back online", Body: body, URL: s.coreAppLink("", r.DeviceID), Source: alertSourceTag,
			ID: "online-" + strconv.FormatInt(now.Unix(), 10), Sound: true, Thread: s.boxThread()}
	}, s.statusCollapse())
	s.log.Info("admin alert", "kind", "box_online", "devices", back.Devices, "sent", back.Sent, "failed", back.Failed, "reason", back.Reason)
}

// offlineWatch is the box-offline state for GET /devices/me/push.
func (s *Server) offlineWatch() string {
	if s.cfg.PushOfflineAfter == 0 {
		return watchOff
	}
	s.alerts.mu.Lock()
	defer s.alerts.mu.Unlock()
	return s.alerts.watch
}
