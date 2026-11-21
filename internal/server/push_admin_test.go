package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/push"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// coreApp is the Pocket Core app on an admin's phone: same pairing and sealing as Pocket Hub,
// its own topic.
func coreAppBody(h hubInstall, kinds ...string) map[string]any {
	b := h.body()
	b["topic"] = push.TopicCoreApp
	if kinds != nil {
		b["kinds"] = kinds
	}
	return b
}

func (f *fakeRelay) reset() {
	f.mu.Lock()
	f.got, f.beats, f.stops = nil, nil, 0
	f.mu.Unlock()
}

func (f *fakeRelay) heartbeats() []heartbeatCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]heartbeatCall(nil), f.beats...)
}

// alertsSettled waits for the background alerts.
func (e *env) alertsSettled() { e.s.alerts.wg.Wait() }

func deviceID(t *testing.T, e *env, token string) int64 {
	t.Helper()
	d, err := e.st.GetDeviceByToken(t.Context(), token, "")
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func TestCoreAppRegistrationKinds(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	e.s.alerts.manual.Store(true)
	phone := newInstall(t, e, 0, "iPhone")

	var st struct {
		Registered   bool
		Kinds        []string
		Admin        bool
		OfflineWatch string `json:"offline_watch"`
	}
	if c := e.do("GET", "/api/v1/devices/me/push", phone.device, nil, &st); c != 200 || st.Registered || !st.Admin || strings.Join(st.Kinds, ",") != "app_down,app_up,job_failed,updates,box_offline" {
		t.Fatalf("before: %d %+v", c, st)
	}
	if c := e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone, "app_down", "nope"), nil); c != 400 {
		t.Fatalf("unknown kind: %d", c)
	}
	if c := e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone, "box_offline", "app_down", "app_down"), &st); c != 200 || strings.Join(st.Kinds, ",") != "app_down,box_offline" {
		t.Fatalf("put: %d %+v", c, st)
	}
	// Re-registering without kinds (a new token on launch) keeps the choice.
	if c := e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone), &st); c != 200 || strings.Join(st.Kinds, ",") != "app_down,box_offline" {
		t.Fatalf("kept: %d %+v", c, st)
	}
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone, []string{}...), nil)
	b := coreAppBody(phone)
	b["kinds"] = []string{}
	if c := e.do("PUT", "/api/v1/devices/me/push", phone.device, b, &st); c != 200 || len(st.Kinds) != 0 {
		t.Fatalf("none: %d %+v", c, st)
	}
	if reg, _ := e.st.GetPushRegistration(t.Context(), deviceID(t, e, phone.device)); reg.Kinds != "none" {
		t.Fatalf("stored %q", reg.Kinds)
	}

	// The test notification says which box and opens it in the app.
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone, "app_down"), nil)
	var tr struct{ Status string }
	if c := e.do("POST", "/api/v1/devices/me/push/test", phone.device, nil, &tr); c != 200 || tr.Status != "sent" {
		t.Fatalf("test %d %+v", c, tr)
	}
	got := f.sent()
	m := phone.open(t, got[len(got)-1].Sealed)
	if got[len(got)-1].Topic != push.TopicCoreApp || m.Title != "Pocket Core" || m.Body != "Notifications from Pocket Box work." ||
		!strings.HasPrefix(m.URL, "pocketcore://box?") || !strings.HasPrefix(m.Thread, "box-") {
		t.Fatalf("test message %+v", m)
	}
	u, _ := url.Parse(m.URL)
	if u.Query().Get("core") != "http://core.test" || u.Query().Get("device") != itoa(deviceID(t, e, phone.device)) {
		t.Fatalf("link %s", m.URL)
	}
}

func TestCoreAppDoesNotGetAppMessages(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	e.s.alerts.manual.Store(true)
	appTok, _ := registerTestApp(t, e, "habits")
	hub := newInstall(t, e, 0, "Mac")
	phone := newInstall(t, e, 0, "iPhone")
	e.do("PUT", "/api/v1/devices/me/push", hub.device, hub.body(), nil)
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone), nil)
	var tally pushTally
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "a@b.c", "title": "x"}, &tally); c != 200 || tally.Devices != 1 || tally.Sent != 1 {
		t.Fatalf("notify %d %+v", c, tally)
	}
	if got := f.sent(); len(got) != 1 || got[0].Token != hub.apns {
		t.Fatalf("relay got %+v", got)
	}
}

// toggleApp is an app whose health check answers up or down.
func toggleApp(t *testing.T, e *env, slug string) (*atomic.Bool, *store.App) {
	t.Helper()
	up := &atomic.Bool{}
	up.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"1.0.0"}`))
	}))
	t.Cleanup(srv.Close)
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": slug, "name": "Pocket Money", "url": srv.URL, "health_path": "/health"}, nil); c != 201 {
		t.Fatalf("register %d", c)
	}
	a, err := e.st.GetAppBySlug(t.Context(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return up, a
}

func TestAppDownAndBackUp(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	e.s.alerts.manual.Store(true)
	e.s.alerts.grace = 0
	e.do("PUT", "/api/v1/settings", "", map[string]any{"box_name": "Home"}, nil)
	hub := newInstall(t, e, 0, "Mac")
	phone := newInstall(t, e, 0, "iPhone")
	quiet := newInstall(t, e, 0, "iPad")
	e.do("PUT", "/api/v1/devices/me/push", hub.device, hub.body(), nil)
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone), nil)
	e.do("PUT", "/api/v1/devices/me/push", quiet.device, coreAppBody(quiet, "job_failed"), nil)
	up, a := toggleApp(t, e, "money")
	e.s.removing.Store("money", true) // the janitor's own checks leave it to this test
	ctx := t.Context()
	check := func(p bool) {
		a, _ = e.st.GetApp(ctx, a.ID)
		c := ctx
		if p {
			c = polled(ctx)
		}
		e.s.checkApp(c, a)
		e.alertsSettled()
	}
	check(true) // up
	f.reset()

	up.Store(false)
	check(true) // down once: could be a restart
	check(false)
	check(false) // "Check now" doesn't count
	if len(f.sent()) != 0 {
		t.Fatalf("alerted after one scheduled check: %+v", f.sent())
	}
	check(true) // down across two scheduled checks
	got := f.sent()
	if len(got) != 1 || got[0].Token != phone.apns || got[0].Topic != push.TopicCoreApp || !got[0].Sound {
		t.Fatalf("relay got %+v", got)
	}
	m := phone.open(t, got[0].Sealed)
	if m.Title != "Money is down on Home" || !strings.Contains(m.Body, "503") || m.Source != "Pocket Core" || !strings.Contains(m.URL, "app=money") {
		t.Fatalf("down message %+v", m)
	}
	check(true)
	check(true)
	if len(f.sent()) != 1 {
		t.Fatal("down repeated")
	}

	// Core restarts while the app is down: "back up" still comes.
	e.s.alerts.mu.Lock()
	e.s.alerts.apps, e.s.alerts.loaded = map[string]*appWatch{}, false
	e.s.alerts.mu.Unlock()
	f.reset()
	up.Store(true)
	check(false)
	got = f.sent()
	if len(got) != 1 || got[0].Token != phone.apns {
		t.Fatalf("back up: %+v", got)
	}
	if m := phone.open(t, got[0].Sealed); m.Title != "Money is back up on Home" || !strings.HasPrefix(m.Body, "It was down for") {
		t.Fatalf("up message %+v", m)
	}
	check(true)
	if len(f.sent()) != 1 {
		t.Fatal("up repeated")
	}

	// Just after Core starts, apps are still starting: no "down" yet.
	e.s.alerts.grace = time.Hour
	f.reset()
	up.Store(false)
	check(true)
	check(true)
	check(true)
	if len(f.sent()) != 0 {
		t.Fatal("alerted during the start grace")
	}

	// A member's Pocket Core app gets nothing: admin alerts are for admins.
	var b store.User
	e.do("POST", "/api/v1/users", "", map[string]any{"email": "m@b.c", "name": "M", "password": "password123", "role": "member"}, &b)
	member := newInstall(t, e, b.ID, "M's iPhone")
	e.do("PUT", "/api/v1/devices/me/push", member.device, coreAppBody(member), nil)
	if regs := e.s.adminRegs(ctx, kindAppDown); len(regs) != 1 || regs[0].APNsToken != phone.apns {
		t.Fatalf("admin regs %+v", regs)
	}
}

func TestJobAlerts(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	e.s.alerts.manual.Store(true)
	e.do("PUT", "/api/v1/settings", "", map[string]any{"box_name": "Home"}, nil)
	phone := newInstall(t, e, 0, "iPhone")
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone), nil)

	j := &installJob{ID: "j1", Kind: "update", Slug: "money", Name: "Pocket Money", State: "running"}
	e.s.trackJob(j)
	j.finish("failed", "the new version didn't start; rolled back to 0.22.0")
	j.finish("failed", "again") // once
	e.alertsSettled()
	got := f.sent()
	if len(got) != 1 {
		t.Fatalf("relay got %d", len(got))
	}
	if m := phone.open(t, got[0].Sealed); m.Title != "Money didn't update on Home" || !strings.Contains(m.Body, "rolled back") || !strings.Contains(m.URL, "app=money") {
		t.Fatalf("failed message %+v", m)
	}

	// Finished jobs only for devices that asked.
	f.reset()
	done := &installJob{ID: "j2", Kind: "install", Slug: "sky", Name: "Pocket Sky", State: "running"}
	e.s.trackJob(done)
	done.finish("done", "")
	e.alertsSettled()
	if len(f.sent()) != 0 {
		t.Fatal("job_done is off by default")
	}
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone, "job_failed", "job_done"), nil)
	rm := &installJob{ID: "j3", Kind: "uninstall", Slug: "sky", Name: "Pocket Sky", State: "running"}
	e.s.trackJob(rm)
	rm.finish("done", "")
	e.alertsSettled()
	got = f.sent()
	if len(got) != 1 {
		t.Fatalf("relay got %d", len(got))
	}
	if m := phone.open(t, got[0].Sealed); m.Title != "Sky was removed from Home" || strings.Contains(m.URL, "app=") {
		t.Fatalf("done message %+v", m)
	}
}

func TestUpdatesDigest(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	e.s.alerts.manual.Store(true)
	e.do("PUT", "/api/v1/settings", "", map[string]any{"box_name": "Home"}, nil)
	ctx := t.Context()
	day := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.Local) }
	waiting := []waitingUpdate{{Slug: "notes", Name: "Pocket Notes", From: "0.14.1", To: "0.14.2"}, {Slug: "money", Name: "Pocket Money", From: "0.22.0", To: "0.23.0"}}
	e.s.noteUpdatesWaiting(ctx, waiting)
	if e.s.sendUpdatesDigest(ctx, day(12, 10)) {
		t.Fatal("sent with nobody to tell")
	}
	phone := newInstall(t, e, 0, "iPhone")
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone), nil)
	if e.s.sendUpdatesDigest(ctx, day(12, 4)) {
		t.Fatal("sent at 4 in the morning")
	}
	if !e.s.sendUpdatesDigest(ctx, day(12, 10)) {
		t.Fatal("no digest")
	}
	e.alertsSettled()
	got := f.sent()
	if len(got) != 1 {
		t.Fatalf("relay got %d", len(got))
	}
	if m := phone.open(t, got[0].Sealed); m.Title != "2 updates waiting on Home" || m.Body != "Notes 0.14.1 → 0.14.2, Money 0.22.0 → 0.23.0" {
		t.Fatalf("digest %+v", m)
	}
	// The same updates again tomorrow: nothing new, no digest.
	e.s.noteUpdatesWaiting(ctx, waiting)
	if e.s.sendUpdatesDigest(ctx, day(13, 10)) {
		t.Fatal("repeated the same digest")
	}
	// Something new: at most once a day.
	waiting = append(waiting, waitingUpdate{Slug: "sky", Name: "Pocket Sky", From: "0.6.1", To: "0.6.2"})
	e.s.noteUpdatesWaiting(ctx, waiting)
	if !e.s.sendUpdatesDigest(ctx, day(13, 10)) {
		t.Fatal("new update not told")
	}
	waiting[2].To = "0.6.3"
	e.s.noteUpdatesWaiting(ctx, waiting)
	if e.s.sendUpdatesDigest(ctx, day(13, 15)) {
		t.Fatal("twice a day")
	}
	if !e.s.sendUpdatesDigest(ctx, day(14, 10)) {
		t.Fatal("not the next day")
	}
}

func TestBoxOfflineHeartbeat(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	e.s.alerts.manual.Store(true)
	e.do("PUT", "/api/v1/settings", "", map[string]any{"box_name": "Home"}, nil)
	ctx := t.Context()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)

	// Nobody wants it: the relay is told to stop watching (once).
	e.s.heartbeatOnce(ctx, now)
	e.s.heartbeatOnce(ctx, now)
	if f.stops != 1 || len(f.heartbeats()) != 0 || e.s.offlineWatch() != watchOff {
		t.Fatalf("stops %d beats %d watch %s", f.stops, len(f.heartbeats()), e.s.offlineWatch())
	}

	phone := newInstall(t, e, 0, "iPhone")
	pad := newInstall(t, e, 0, "iPad")
	hub := newInstall(t, e, 0, "Mac")
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone), nil)
	padBody := coreAppBody(pad, "app_down")
	e.do("PUT", "/api/v1/devices/me/push", pad.device, padBody, nil)
	e.do("PUT", "/api/v1/devices/me/push", hub.device, hub.body(), nil)
	e.s.heartbeatOnce(ctx, now)
	beats := f.heartbeats()
	if len(beats) != 1 || beats[0].Deadline != 600 || len(beats[0].Messages) != 1 {
		t.Fatalf("beats %+v", beats)
	}
	out := beats[0].Messages[0]
	if out.Token != phone.apns || out.Topic != push.TopicCoreApp || out.CollapseID == "" || !strings.HasPrefix(out.Thread, "box-") {
		t.Fatalf("offline out %+v", out)
	}
	if strings.Contains(out.Sealed, "offline") {
		t.Fatal("plaintext in the heartbeat")
	}
	m := phone.open(t, out.Sealed)
	if m.Title != "Home is offline" || !strings.Contains(m.Body, "since 12:00") || !strings.HasPrefix(m.URL, "pocketcore://box?") {
		t.Fatalf("offline message %+v", m)
	}
	var st struct {
		OfflineWatch string `json:"offline_watch"`
	}
	if e.do("GET", "/api/v1/devices/me/push", phone.device, nil, &st); st.OfflineWatch != watchOn {
		t.Fatalf("watch %+v", st)
	}
	if len(f.sent()) != 0 {
		t.Fatal("sent something on a plain heartbeat")
	}

	// The relay sent the offline notices meanwhile: Core says it's back, to the same devices,
	// replacing the offline one on the phone.
	f.mu.Lock()
	f.fireNext = true
	f.mu.Unlock()
	e.s.heartbeatOnce(ctx, now.Add(40*time.Minute))
	got := f.sent()
	if len(got) != 1 || got[0].Token != phone.apns || got[0].CollapseID != out.CollapseID {
		t.Fatalf("back online %+v", got)
	}
	if m := phone.open(t, got[0].Sealed); m.Title != "Home is back online" || m.Body != "It was offline for about 40 min, from 12:00." {
		t.Fatalf("back message %+v", m)
	}

	// An old relay: say so and stop asking for an hour.
	f.mu.Lock()
	f.oldRelay = true
	f.mu.Unlock()
	f.reset()
	e.s.heartbeatOnce(ctx, now.Add(42*time.Minute))
	if e.s.offlineWatch() != watchOldRelay {
		t.Fatalf("watch %s", e.s.offlineWatch())
	}
	f.mu.Lock()
	f.oldRelay = false
	f.mu.Unlock()
	e.s.heartbeatOnce(ctx, now.Add(50*time.Minute))
	if len(f.heartbeats()) != 0 {
		t.Fatal("asked an old relay again within the hour")
	}
	e.s.heartbeatOnce(ctx, now.Add(43*time.Minute+time.Hour))
	if len(f.heartbeats()) != 1 || e.s.offlineWatch() != watchOn {
		t.Fatal("didn't try again after the hour")
	}

	// The relay can't be reached: no relay state, retried next beat.
	f.mu.Lock()
	f.beatError = 502
	f.mu.Unlock()
	e.s.heartbeatOnce(ctx, now.Add(2*time.Hour))
	if e.s.offlineWatch() != watchNoRelay {
		t.Fatalf("watch %s", e.s.offlineWatch())
	}

	// The last device turns it off: the relay stops watching.
	f.mu.Lock()
	f.beatError = 0
	f.mu.Unlock()
	f.reset()
	e.do("PUT", "/api/v1/devices/me/push", phone.device, coreAppBody(phone, "app_down"), nil)
	e.s.heartbeatOnce(ctx, now.Add(3*time.Hour))
	if f.stops != 1 || len(f.heartbeats()) != 0 {
		t.Fatalf("not stopped: stops %d beats %d", f.stops, len(f.heartbeats()))
	}
}

func TestAdminKindsCodec(t *testing.T) {
	if s, bad := encodeKinds([]string{"updates", "app_down"}); s != "app_down,updates" || bad != "" {
		t.Fatalf("%q %q", s, bad)
	}
	if s, _ := encodeKinds(nil); s != "none" || len(decodeKinds("none")) != 0 {
		t.Fatal("none")
	}
	if strings.Join(decodeKinds("app_down,gone,box_offline"), ",") != "app_down,box_offline" {
		t.Fatal("unknown kinds kept")
	}
	if about(90*time.Second) != "1 min" || about(125*time.Minute) != "2 h 5 min" || about(72*time.Hour) != "3 days" {
		t.Fatal("about")
	}
	if shortName("Pocket Money", "money") != "Money" || shortName("", "photolab") == "" {
		t.Fatal("shortName")
	}
}
