package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/push"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// fakeRelay is a Pocket Push relay: it records what Core sends and answers per device token.
type fakeRelay struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	apple  string            // ready | missing
	google string            // ready | missing | "" (an old relay that doesn't say)
	answer map[string]string // device token → status (default sent)
	got    []push.Out
	calls  int
}

func newFakeRelay(t *testing.T) *fakeRelay {
	f := &fakeRelay{t: t, apple: "ready", answer: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if r.Header.Get("Authorization") != "Bearer pps_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			st := map[string]any{"sender": "home", "apple": f.apple}
			if f.google != "" {
				st["google"] = f.google
			}
			writeJSON(w, 200, st)
		case "/v1/send":
			var in struct {
				Messages []push.Out `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Messages) > push.BatchSize {
				w.WriteHeader(400)
				return
			}
			res := []push.Result{}
			for _, m := range in.Messages {
				f.got = append(f.got, m)
				st := f.answer[m.Token]
				switch {
				case st != "":
					res = append(res, push.Result{Status: st, Reason: "Unregistered"})
				case m.Environment == push.EnvironmentFCM && f.google != "ready":
					res = append(res, push.Result{Status: "failed", Reason: "no Firebase key on the relay yet"})
				case m.Environment != push.EnvironmentFCM && f.apple != "ready":
					res = append(res, push.Result{Status: "failed", Reason: "no Apple push key on the relay yet"})
				default:
					res = append(res, push.Result{Status: "sent"})
				}
			}
			writeJSON(w, 200, map[string]any{"results": res})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelay) sent() []push.Out {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]push.Out(nil), f.got...)
}

// hubInstall is one Pocket Hub install: a paired device token, an APNs token and its X25519 key.
type hubInstall struct {
	device string
	apns   string
	key    *ecdh.PrivateKey
}

func (h hubInstall) body() map[string]any {
	return map[string]any{"token": h.apns, "environment": "development", "topic": "org.bjelke.pockethub.mac",
		"public_key": base64.StdEncoding.EncodeToString(h.key.PublicKey().Bytes()), "enabled": true, "sound": true}
}

func (h hubInstall) open(t *testing.T, sealed string) push.Message {
	t.Helper()
	pt, err := push.Open(h.key.Bytes(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	var m push.Message
	if err := json.Unmarshal(pt, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newInstall(t *testing.T, e *env, userID int64, name string) hubInstall {
	t.Helper()
	var pair struct {
		Code string `json:"code"`
	}
	body := map[string]any{}
	if userID > 0 {
		body["user_id"] = userID
	}
	if c := e.do("POST", "/api/v1/pair", "", body, &pair); c != 201 {
		t.Fatalf("pair %d", c)
	}
	var prof Profile
	if c := e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": name, "platform": "macos"}, &prof); c != 201 {
		t.Fatalf("claim %d", c)
	}
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok := make([]byte, 32)
	_, _ = rand.Read(tok)
	return hubInstall{device: prof.Token, apns: hex.EncodeToString(tok), key: k}
}

func pushEnv(t *testing.T, relayURL, relayToken string) *env {
	t.Setenv("PUSH_RELAY_URL", relayURL)
	t.Setenv("PUSH_RELAY_TOKEN", relayToken)
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil)
	return e
}

func registerTestApp(t *testing.T, e *env, slug string) (token string, appID int64) {
	t.Helper()
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var reg struct {
		App   store.App `json:"app"`
		Token string    `json:"token"`
	}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": slug, "name": "Pocket " + slug, "url": e.srv.URL}, &reg); c != 201 {
		t.Fatalf("register %d", c)
	}
	return reg.Token, reg.App.ID
}

func TestDevicePushRegistration(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	mac := newInstall(t, e, 0, "Mac")

	// No token at all → 401; a person's session or an app → 403: devices only.
	res, err := http.Get(e.srv.URL + "/api/v1/devices/me/push")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("anonymous: %d", res.StatusCode)
	}
	if c := e.do("PUT", "/api/v1/devices/me/push", "", mac.body(), nil); c != 403 {
		t.Fatalf("session: %d", c)
	}
	appTok, _ := registerTestApp(t, e, "habits")
	if c := e.do("GET", "/api/v1/devices/me/push", appTok, nil, nil); c != 403 {
		t.Fatalf("app: %d", c)
	}

	// Validation.
	bad := []func(m map[string]any){
		func(m map[string]any) { m["token"] = "abc" },
		func(m map[string]any) { m["token"] = strings.Repeat("z", 64) },
		func(m map[string]any) { m["token"] = strings.Repeat("a", 202) },
		func(m map[string]any) { m["environment"] = "staging" },
		func(m map[string]any) { m["topic"] = "com.example.other" },
		func(m map[string]any) { m["public_key"] = base64.StdEncoding.EncodeToString(make([]byte, 33)) },
		func(m map[string]any) { m["public_key"] = "not base64!" },
	}
	for i, mod := range bad {
		b := mac.body()
		mod(b)
		if c := e.do("PUT", "/api/v1/devices/me/push", mac.device, b, nil); c != 400 {
			t.Fatalf("bad body %d accepted: %d", i, c)
		}
	}

	var st struct {
		Registered, Enabled, Sound bool
		Relay                      string
		LastSentAt                 *string `json:"last_sent_at"`
		LastError                  string  `json:"last_error"`
	}
	if c := e.do("GET", "/api/v1/devices/me/push", mac.device, nil, &st); c != 200 || st.Registered || st.Relay != "ready" {
		t.Fatalf("before: %d %+v", c, st)
	}
	b := mac.body()
	b["token"] = strings.ToUpper(mac.apns)
	if c := e.do("PUT", "/api/v1/devices/me/push", mac.device, b, &st); c != 200 || !st.Registered || st.Relay != "ready" {
		t.Fatalf("put: %d %+v", c, st)
	}
	if c := e.do("GET", "/api/v1/devices/me/push", mac.device, nil, &st); c != 200 || !st.Registered || !st.Enabled || !st.Sound || st.LastSentAt != nil {
		t.Fatalf("get: %d %+v", c, st)
	}

	// The test message reaches the relay sealed; only the device opens it.
	var tr struct{ Status, Reason string }
	if c := e.do("POST", "/api/v1/devices/me/push/test", mac.device, nil, &tr); c != 200 || tr.Status != "sent" {
		t.Fatalf("test: %d %+v", c, tr)
	}
	got := f.sent()
	if len(got) != 1 || got[0].Token != mac.apns || got[0].Environment != "development" || got[0].Topic != "org.bjelke.pockethub.mac" || got[0].Priority != 10 || got[0].Expires == 0 {
		t.Fatalf("relay got %+v", got)
	}
	if strings.Contains(got[0].Sealed, "Notifications") {
		t.Fatal("plaintext on the wire")
	}
	if m := mac.open(t, got[0].Sealed); m.V != 1 || m.Body != "Notifications work" || !m.Sound {
		t.Fatalf("opened %+v", m)
	}
	e.do("GET", "/api/v1/devices/me/push", mac.device, nil, &st)
	if st.LastSentAt == nil || st.LastError != "" {
		t.Fatalf("after test %+v", st)
	}

	// Sound off on the device: messages arrive silent.
	b = mac.body()
	b["sound"] = false
	e.do("PUT", "/api/v1/devices/me/push", mac.device, b, nil)
	e.do("POST", "/api/v1/devices/me/push/test", mac.device, nil, nil)
	if got := f.sent(); got[len(got)-1].Sound || mac.open(t, got[len(got)-1].Sealed).Sound {
		t.Fatal("sound off ignored")
	}

	// The Devices list shows notifications on.
	var devs []store.Device
	e.do("GET", "/api/v1/devices", "", nil, &devs)
	if len(devs) != 1 || devs[0].Push == nil || !devs[0].Push.Enabled {
		t.Fatalf("devices %+v", devs)
	}

	// Unregister.
	if c := e.do("DELETE", "/api/v1/devices/me/push", mac.device, nil, &st); c != 200 || st.Registered {
		t.Fatalf("delete: %d", c)
	}
	devs = nil
	e.do("GET", "/api/v1/devices", "", nil, &devs)
	if devs[0].Push != nil {
		t.Fatal("still listed with notifications")
	}
	if c := e.do("POST", "/api/v1/devices/me/push/test", mac.device, nil, nil); c != 404 {
		t.Fatalf("test unregistered: %d", c)
	}

	// Revoking the device removes its registration.
	e.do("PUT", "/api/v1/devices/me/push", mac.device, mac.body(), nil)
	e.do("DELETE", "/api/v1/devices/"+itoa(devs[0].ID), "", nil, nil)
	if _, err := e.st.GetPushRegistration(t.Context(), devs[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("registration survived revoke: %v", err)
	}
}

func TestPushNotify(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	appTok, appID := registerTestApp(t, e, "habits")
	var b store.User
	if c := e.do("POST", "/api/v1/users", "", map[string]any{"email": "b@b.c", "name": "B", "password": "password123", "role": "member"}, &b); c != 201 && c != 200 {
		t.Fatalf("user %d", c)
	}
	mac := newInstall(t, e, 0, "Mac")
	phone := newInstall(t, e, 0, "iPhone")
	other := newInstall(t, e, b.ID, "B's Mac")
	for _, h := range []hubInstall{mac, phone, other} {
		if c := e.do("PUT", "/api/v1/devices/me/push", h.device, h.body(), nil); c != 200 {
			t.Fatalf("register %d", c)
		}
	}
	// The phone turned notifications off: it is not notified.
	off := phone.body()
	off["enabled"] = false
	e.do("PUT", "/api/v1/devices/me/push", phone.device, off, nil)

	msg := map[string]any{"email": "A@B.C", "title": "Backup finished", "body": "Nightly backup took 4 min", "url": "https://alerts.example.com/alerts/42",
		"source": "Backups", "id": 42, "sound": true, "thread": "Backups"}
	var tally pushTally
	if c := e.do("POST", "/api/v1/push/notify", appTok, msg, &tally); c != 200 || tally.Devices != 1 || tally.Sent != 1 || tally.Failed != 0 {
		t.Fatalf("notify: %d %+v", c, tally)
	}
	got := f.sent()
	if len(got) != 1 || got[0].Token != mac.apns || got[0].Thread != "Backups" || !got[0].Sound {
		t.Fatalf("relay got %+v", got)
	}
	m := mac.open(t, got[0].Sealed)
	if m.Title != "Backup finished" || m.Body != "Nightly backup took 4 min" || m.URL != "https://alerts.example.com/alerts/42" || m.Source != "Backups" || m.ID != "42" || m.At == "" || m.Thread != "Backups" {
		t.Fatalf("opened %+v", m)
	}

	// Both on: fan-out to both; APNs says the phone is gone → its registration is deleted.
	e.do("PUT", "/api/v1/devices/me/push", phone.device, phone.body(), nil)
	f.mu.Lock()
	f.answer[phone.apns] = "gone"
	f.mu.Unlock()
	if c := e.do("POST", "/api/v1/push/notify", appTok, msg, &tally); c != 200 || tally.Devices != 2 || tally.Sent != 1 || tally.Gone != 1 {
		t.Fatalf("fan-out: %d %+v", c, tally)
	}
	var st struct{ Registered bool }
	if e.do("GET", "/api/v1/devices/me/push", phone.device, nil, &st); st.Registered {
		t.Fatal("gone registration kept")
	}

	// By user_id, as an admin.
	if c := e.do("POST", "/api/v1/push/notify", "", map[string]any{"user_id": b.ID, "body": "Hi"}, &tally); c != 200 || tally.Sent != 1 {
		t.Fatalf("admin by id: %d %+v", c, tally)
	}

	// Audience: an app only notifies people who may use it.
	if _, err := e.st.DB.Exec(`UPDATE services SET audience = 'admins' WHERE app_id = ?`, appID); err != nil {
		t.Fatal(err)
	}
	var refusal map[string]any
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "b@b.c", "title": "x"}, &refusal); c != 403 || refusal["reason"] != "not_in_audience" {
		t.Fatalf("outside audience: %d %v", c, refusal)
	}
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "a@b.c", "title": "x"}, nil); c != 200 {
		t.Fatalf("inside audience: %d", c)
	}

	// Who may call, and what.
	if c := e.do("POST", "/api/v1/push/notify", mac.device, msg, nil); c != 403 {
		t.Fatalf("device caller: %d", c)
	}
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "nobody@b.c", "title": "x"}, nil); c != 404 {
		t.Fatalf("unknown person: %d", c)
	}
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "a@b.c"}, nil); c != 400 {
		t.Fatalf("empty message: %d", c)
	}
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "a@b.c", "title": "x", "url": "javascript:alert(1)"}, nil); c != 400 {
		t.Fatalf("bad url: %d", c)
	}

	// Nobody registered: devices 0, not an error.
	e.do("DELETE", "/api/v1/devices/me/push", mac.device, nil, nil)
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "a@b.c", "title": "x"}, &tally); c != 200 || tally.Devices != 0 {
		t.Fatalf("no devices: %d %+v", c, tally)
	}

	// The relay has no Apple key yet: failed, with the relay's reason, and the device's last_error.
	e.do("PUT", "/api/v1/devices/me/push", mac.device, mac.body(), nil)
	f.mu.Lock()
	f.apple = "missing"
	f.mu.Unlock()
	if c := e.do("POST", "/api/v1/push/notify", appTok, map[string]any{"email": "a@b.c", "title": "x"}, &tally); c != 200 || tally.Failed != 1 || !strings.Contains(tally.Reason, "Apple") {
		t.Fatalf("no apple key: %d %+v", c, tally)
	}
	var full struct {
		Relay     string
		LastError string `json:"last_error"`
	}
	e.do("GET", "/api/v1/devices/me/push", mac.device, nil, &full)
	if full.Relay != "no_apple_key" || !strings.Contains(full.LastError, "Apple") {
		t.Fatalf("status %+v", full)
	}
}

func TestPushWithoutRelay(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "") // no sender token, no Pocket Names account
	mac := newInstall(t, e, 0, "Mac")
	var st struct {
		Registered bool
		Relay      string
	}
	if c := e.do("PUT", "/api/v1/devices/me/push", mac.device, mac.body(), &st); c != 200 || !st.Registered || st.Relay != "no_relay" {
		t.Fatalf("put: %d %+v", c, st)
	}
	var tr struct{ Status, Reason string }
	if c := e.do("POST", "/api/v1/devices/me/push/test", mac.device, nil, &tr); c != 200 || tr.Status != "failed" || tr.Reason != push.ReasonNoRelay {
		t.Fatalf("test: %d %+v", c, tr)
	}
	var tally pushTally
	if c := e.do("POST", "/api/v1/push/notify", "", map[string]any{"email": "a@b.c", "title": "x"}, &tally); c != 200 || tally.Devices != 1 || tally.Failed != 1 || tally.Reason != push.ReasonNoRelay {
		t.Fatalf("notify: %d %+v", c, tally)
	}
	if f.calls != 0 {
		t.Fatalf("relay called %d times without a token", f.calls)
	}
}

func TestPushRelayUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	e := pushEnv(t, dead.URL, "pps_test")
	mac := newInstall(t, e, 0, "Mac")
	var st struct{ Relay string }
	e.do("PUT", "/api/v1/devices/me/push", mac.device, mac.body(), &st)
	if st.Relay != "no_relay" {
		t.Fatalf("relay %q", st.Relay)
	}
	var tally pushTally
	if c := e.do("POST", "/api/v1/push/notify", "", map[string]any{"email": "a@b.c", "title": "x"}, &tally); c != 502 || tally.Failed != 1 || !strings.Contains(tally.Reason, "unreachable") {
		t.Fatalf("notify: %d %+v", c, tally)
	}
}

// Pocket Hub for Android signs up with environment "fcm" and its Firebase registration token.
func TestAndroidPushRegistration(t *testing.T) {
	f := newFakeRelay(t)
	e := pushEnv(t, f.srv.URL, "pps_test")
	phone := newInstall(t, e, 0, "Pixel")
	fcmTok := "dQw4w9WgXcQ:APA91bH-Abc_DEF123ghiJKLmnoPQRstuVWXyz0123456789abcdefABCDEF-_ghijklmno"
	body := map[string]any{"token": fcmTok, "environment": "fcm", "topic": "org.bjelke.pockethub",
		"public_key": base64.StdEncoding.EncodeToString(phone.key.PublicKey().Bytes()), "enabled": true, "sound": true}

	for i, bad := range []string{"short", "has spaces in it and is long enough to pass the length check", strings.Repeat("a", 1001)} {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		b["token"] = bad
		if c := e.do("PUT", "/api/v1/devices/me/push", phone.device, b, nil); c != 400 {
			t.Fatalf("bad fcm token %d accepted: %d", i, c)
		}
	}

	// An old relay says nothing about Google: Android can't be reached through it.
	var reg struct{ Relay string }
	if c := e.do("PUT", "/api/v1/devices/me/push", phone.device, body, &reg); c != 200 || reg.Relay != push.StatusNoGoogleKey {
		t.Fatalf("register: %d %+v", c, reg)
	}

	f.mu.Lock()
	f.google = "ready"
	f.mu.Unlock()
	e2 := pushEnv(t, f.srv.URL, "pps_test") // a fresh Core: no cached relay state
	phone2 := newInstall(t, e2, 0, "Pixel")
	body["public_key"] = base64.StdEncoding.EncodeToString(phone2.key.PublicKey().Bytes())
	if c := e2.do("PUT", "/api/v1/devices/me/push", phone2.device, body, &reg); c != 200 || reg.Relay != "ready" {
		t.Fatalf("register: %d %+v", c, reg)
	}
	var test struct{ Status, Reason string }
	if c := e2.do("POST", "/api/v1/devices/me/push/test", phone2.device, nil, &test); c != 200 || test.Status != "sent" {
		t.Fatalf("test: %d %+v", c, test)
	}
	got := f.sent()
	last := got[len(got)-1]
	if last.Token != fcmTok || last.Environment != "fcm" || last.Topic != "org.bjelke.pockethub" {
		t.Fatalf("relay got %+v (the FCM token must keep its case)", last)
	}
	if m := phone2.open(t, last.Sealed); m.Body != "Notifications work" {
		t.Fatalf("sealed %+v", m)
	}

	// Google says the token is gone: the registration is removed and the reason names Google.
	f.mu.Lock()
	f.answer[fcmTok] = "gone"
	f.mu.Unlock()
	if c := e2.do("POST", "/api/v1/devices/me/push/test", phone2.device, nil, &test); c != 200 || test.Status != "gone" || !strings.Contains(test.Reason, "Google") {
		t.Fatalf("gone: %d %+v", c, test)
	}
}
