package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/abjelosevic88/pocket-homelab-core/internal/remote"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// rclient plays the browser shell: Noise over the relay, then frames.
type rclient struct {
	t    *testing.T
	c    *websocket.Conn
	sess *remote.Session
	key  remote.Key
	next uint32
	ctx  context.Context
}

type remoteRig struct {
	e        *env
	relay    *httptest.Server
	rl       *remote.Relay
	appToken string
	box      []byte
	seen     chan http.Header
}

func newRemoteRig(t *testing.T) *remoteRig {
	e := newEnv(t)
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123", "name": "Owner", "box_name": "Box"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	rig := &remoteRig{e: e, seen: make(chan http.Header, 16)}

	// A Pocket app: accepts Core tokens through introspection, like the real ones.
	mux := http.NewServeMux()
	who := func(r *http.Request) (map[string]any, int) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		b, _ := json.Marshal(map[string]string{"token": tok})
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/auth/introspect", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+rig.appToken)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, 500
		}
		defer res.Body.Close()
		var in map[string]any
		_ = json.NewDecoder(res.Body).Decode(&in)
		if in["active"] != true {
			return in, 401
		}
		return in, 200
	}
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		select {
		case rig.seen <- r.Header.Clone():
		default:
		}
		in, code := who(r)
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "x"})
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(in)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://money.home.test/hello?x=1", http.StatusFound)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if _, code := who(r); code != 200 {
			http.Error(w, "no", 401)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(1 << 22)
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if string(b) == "bye" {
				c.Close(4001, "asked to")
				return
			}
			if err := c.Write(r.Context(), typ, b); err != nil {
				return
			}
		}
	})
	app := httptest.NewServer(mux)
	t.Cleanup(app.Close)

	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var reg struct {
		Token string `json:"token"`
	}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "money", "name": "Pocket Money", "url": app.URL, "public_url": "https://money.home.test"}, &reg); c != 201 {
		t.Fatalf("register %d", c)
	}
	rig.appToken = reg.Token

	rig.rl = &remote.Relay{}
	rig.relay = httptest.NewServer(rig.rl)
	t.Cleanup(rig.relay.Close)

	var got map[string]any
	if c := e.do("PUT", "/api/v1/remote", "", map[string]any{"provider": "custom", "pattern": "{app}.remote.test", "relay_url": rig.relay.URL}, &got); c != 200 {
		t.Fatalf("put remote %d %v", c, got)
	}
	var sec map[string]string
	e.do("GET", "/api/v1/remote/secret", "", nil, &sec)
	rig.rl.Secret = sec["secret"]
	if c := e.do("PUT", "/api/v1/remote", "", map[string]any{"enabled": true}, &got); c != 200 {
		t.Fatalf("enable %d", c)
	}
	if c := e.do("PUT", "/api/v1/remote/apps/money", "", map[string]any{"exposed": true}, nil); c != 200 {
		t.Fatalf("expose %d", c)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !rig.e.s.remote.snapshot().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("Core never connected to the relay: %+v", rig.e.s.remote.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return rig
}

// pairLink makes a new-device QR link and returns its code and the box key.
func (rig *remoteRig) pairLink() string {
	var out map[string]any
	if c := rig.e.do("POST", "/api/v1/me/remote/pair", "", nil, &out); c != 200 {
		rig.e.t.Fatalf("pair link %d %v", c, out)
	}
	if qr, _ := out["qr_svg"].(string); !strings.HasPrefix(qr, "data:image/svg+xml;base64,") {
		rig.e.t.Fatalf("qr_svg must be an image URL, got %.40q", qr)
	}
	code, box := fragment(rig.e.t, out["url"].(string))
	rig.box = box
	return code
}

func fragment(t *testing.T, link string) (string, []byte) {
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.ParseQuery(u.Fragment)
	box, err := base64.RawURLEncoding.DecodeString(q.Get("box"))
	if err != nil || q.Get("pair") == "" {
		t.Fatalf("bad link %s", link)
	}
	return q.Get("pair"), box
}

// dial opens a tunnel as a browser on host would.
func (rig *remoteRig) dial(t *testing.T, host string, key remote.Key, hello map[string]any) (*rclient, map[string]any) {
	t.Helper()
	addr := strings.TrimPrefix(rig.relay.URL, "http://")
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	_, port, _ := net.SplitHostPort(addr)
	c, _, err := websocket.Dial(ctx, "ws://"+host+":"+port+"/__pocket/tunnel", &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(1 << 22)
	t.Cleanup(func() { c.CloseNow() })
	ini, err := remote.NewInitiator(key, rig.box, host)
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := ini.Start()
	c.Write(ctx, websocket.MessageBinary, m1)
	_, m2, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("no handshake reply: %v", err)
	}
	payload, _ := json.Marshal(hello)
	reply, m3, sess, err := ini.Finish(m2, payload)
	if err != nil || string(reply) != `{"v":1}` {
		t.Fatalf("finish %v %q", err, reply)
	}
	c.Write(ctx, websocket.MessageBinary, m3)
	rc := &rclient{t: t, c: c, sess: sess, key: key, next: 1, ctx: ctx}
	return rc, rc.ctl()
}

func (rc *rclient) read() (byte, uint32, []byte) {
	rc.t.Helper()
	_, b, err := rc.c.Read(rc.ctx)
	if err != nil {
		rc.t.Fatalf("read: %v", err)
	}
	p, err := rc.sess.Decrypt(b)
	if err != nil {
		rc.t.Fatalf("decrypt: %v", err)
	}
	typ, id, payload, _ := remote.Decode(p)
	return typ, id, payload
}

func (rc *rclient) ctl() map[string]any {
	rc.t.Helper()
	for {
		typ, _, p := rc.read()
		if typ == remote.FrameCtl {
			var m map[string]any
			json.Unmarshal(p, &m)
			return m
		}
	}
}

func (rc *rclient) send(typ byte, id uint32, p []byte) {
	rc.t.Helper()
	b, err := rc.sess.Encrypt(remote.Encode(typ, id, p))
	if err != nil {
		rc.t.Fatal(err)
	}
	if err := rc.c.Write(rc.ctx, websocket.MessageBinary, b); err != nil {
		rc.t.Fatal(err)
	}
}

func (rc *rclient) sendCtl(v any) {
	b, _ := json.Marshal(v)
	rc.send(remote.FrameCtl, 0, b)
}

type rres struct {
	status int
	h      http.Header
	body   []byte
	err    string
}

func (rc *rclient) do(method, path string, h [][2]string, body []byte) rres {
	rc.t.Helper()
	id := rc.next
	rc.next++
	req, _ := json.Marshal(map[string]any{"m": method, "u": path, "h": h, "b": body != nil})
	rc.send(remote.FrameReq, id, req)
	for off := 0; body != nil && off < len(body); off += remote.MaxChunk {
		rc.send(remote.FrameReqBody, id, body[off:min(off+remote.MaxChunk, len(body))])
	}
	if body != nil {
		rc.send(remote.FrameReqEnd, id, nil)
	}
	out := rres{h: http.Header{}}
	for {
		typ, sid, p := rc.read()
		if sid != id {
			continue
		}
		switch typ {
		case remote.FrameRes:
			var head struct {
				S int         `json:"s"`
				H [][2]string `json:"h"`
			}
			json.Unmarshal(p, &head)
			out.status = head.S
			for _, kv := range head.H {
				out.h.Add(kv[0], kv[1])
			}
		case remote.FrameResBody:
			out.body = append(out.body, p...)
		case remote.FrameResEnd:
			return out
		case remote.FrameResErr:
			out.err = string(p)
			return out
		}
	}
}

func newKey(t *testing.T) remote.Key {
	k, err := remote.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// pairApp pairs a device at the launcher, then hands off to money.
func (rig *remoteRig) pairApp(t *testing.T) (*rclient, *rclient) {
	code := rig.pairLink()
	portal, ready := rig.dial(t, "pocket.remote.test", newKey(t), map[string]any{"t": "pair", "code": code, "name": "Test phone"})
	if ready["t"] != "ready" || ready["kind"] != "portal" {
		t.Fatalf("portal: %v", ready)
	}
	r := portal.do("POST", "/__pocket/api/open", nil, []byte(`{"app":"money"}`))
	if r.status != 200 {
		t.Fatalf("open: %d %s %s", r.status, r.body, r.err)
	}
	var open map[string]string
	json.Unmarshal(r.body, &open)
	if !strings.HasPrefix(open["url"], "https://money.remote.test/#pair=") {
		t.Fatalf("open url %q", open["url"])
	}
	hcode, _ := fragment(t, open["url"])
	app, ready := rig.dial(t, "money.remote.test", newKey(t), map[string]any{"t": "pair", "code": hcode})
	if ready["t"] != "ready" || ready["kind"] != "app" {
		t.Fatalf("app: %v", ready)
	}
	return portal, app
}

func TestRemoteEndToEnd(t *testing.T) {
	rig := newRemoteRig(t)
	portal, app := rig.pairApp(t)

	// The launcher lists the app as allowed and paired.
	r := portal.do("GET", "/__pocket/api/apps", nil, nil)
	var apps struct {
		Apps []map[string]any `json:"apps"`
	}
	json.Unmarshal(r.body, &apps)
	if r.status != 200 || len(apps.Apps) != 1 || apps.Apps[0]["slug"] != "money" || apps.Apps[0]["allowed"] != true {
		t.Fatalf("apps %d %s", r.status, r.body)
	}

	// A request reaches the app as the owner; cookies and identity headers
	// from the browser are dropped, Set-Cookie and frame blockers never return.
	r = app.do("GET", "/hello", [][2]string{{"Cookie", "sid=evil"}, {"X-Remote-User", "mallory"}, {"Remote-Email", "m@x"}, {"X-Custom", "ok"}, {"Accept", "application/json"}}, nil)
	if r.status != 200 || !bytes.Contains(r.body, []byte("owner@example.com")) {
		t.Fatalf("hello %d %s %s", r.status, r.body, r.err)
	}
	h := <-rig.seen
	if h.Get("Cookie") != "" || h.Get("X-Remote-User") != "" || h.Get("Remote-Email") != "" || h.Get("X-Custom") != "ok" || !strings.HasPrefix(h.Get("Authorization"), "Bearer pcu_rly_") {
		t.Fatalf("headers at the app: %v", h)
	}
	if r.h.Get("Set-Cookie") != "" || r.h.Get("X-Frame-Options") != "" || strings.Contains(r.h.Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatalf("response headers leaked: %v", r.h)
	}
	if !bytes.Contains(r.body, []byte(`"remote"`)) {
		t.Fatalf("device should introspect as remote: %s", r.body)
	}

	// Large bodies go both ways in chunks.
	big := bytes.Repeat([]byte("0123456789abcdef"), 20000) // 320 KB
	r = app.do("POST", "/echo", [][2]string{{"Content-Type", "application/octet-stream"}, {"Content-Length", "320000"}}, big)
	if r.status != 200 || !bytes.Equal(r.body, big) {
		t.Fatalf("echo %d len %d %s", r.status, len(r.body), r.err)
	}

	// Redirects to the app's own public address become paths.
	r = app.do("GET", "/redirect", nil, nil)
	if r.status != 302 || r.h.Get("Location") != "/hello?x=1" {
		t.Fatalf("redirect %d %q", r.status, r.h.Get("Location"))
	}

	// Paths must stay on the app.
	if r = app.do("GET", "//evil.example/x", nil, nil); r.err == "" {
		t.Fatalf("absolute path was forwarded: %d", r.status)
	}

	// WebSockets, including a message bigger than one frame.
	wsID := app.next
	app.next++
	open, _ := json.Marshal(map[string]any{"u": "/ws", "p": []string{}})
	app.send(remote.FrameWSOpen, wsID, open)
	if typ, id, _ := app.read(); typ != remote.FrameWSOpened || id != wsID {
		t.Fatalf("ws open: %x", typ)
	}
	app.send(remote.FrameWSText, wsID, append([]byte{1}, "hi"...))
	if typ, _, p := app.read(); typ != remote.FrameWSText || string(p) != "\x01hi" {
		t.Fatalf("ws echo %x %q", typ, p)
	}
	msg := bytes.Repeat([]byte{7}, 100000)
	for off := 0; off < len(msg); off += remote.MaxChunk {
		end := min(off+remote.MaxChunk, len(msg))
		fin := byte(0)
		if end == len(msg) {
			fin = 1
		}
		app.send(remote.FrameWSBinary, wsID, append([]byte{fin}, msg[off:end]...))
	}
	var echoed []byte
	for {
		typ, _, p := app.read()
		if typ != remote.FrameWSBinary {
			t.Fatalf("ws binary %x", typ)
		}
		echoed = append(echoed, p[1:]...)
		if p[0] == 1 {
			break
		}
	}
	if !bytes.Equal(echoed, msg) {
		t.Fatalf("ws binary echo %d bytes", len(echoed))
	}
	app.send(remote.FrameWSText, wsID, append([]byte{1}, "bye"...))
	if typ, _, p := app.read(); typ != remote.FrameWSClose || !bytes.Contains(p, []byte("4001")) {
		t.Fatalf("ws close %x %s", typ, p)
	}

	// Switching the app off for this device ends the session at once.
	var devices []store.RemoteDevice
	var mine map[string]json.RawMessage
	rig.e.do("GET", "/api/v1/me/remote", "", nil, &mine)
	json.Unmarshal(mine["devices"], &devices)
	if len(devices) != 1 || devices[0].Name != "Test phone" || len(devices[0].Keys) != 2 {
		t.Fatalf("devices %+v", devices)
	}
	dev := devices[0].ID
	if c := rig.e.do("PUT", "/api/v1/me/remote/devices/"+itoa(dev), "", map[string]any{"apps": map[string]bool{"money": false}}, nil); c != 200 {
		t.Fatalf("switch off %d", c)
	}
	if m := app.ctl(); m["t"] != "error" || m["code"] != "revoked" {
		t.Fatalf("expected revoked, got %v", m)
	}
	// The token Core injected stops introspecting as active too.
	tok := strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
	var in map[string]any
	rig.e.do("POST", "/api/v1/auth/introspect", rig.appToken, map[string]string{"token": tok}, &in)
	if in["active"] == true {
		t.Fatalf("token still active: %v", in)
	}
	if _, m := rig.dial(t, "money.remote.test", app.key, map[string]any{"t": "open"}); m["code"] != "denied" {
		t.Fatalf("expected denied, got %v", m)
	}
	rig.e.do("PUT", "/api/v1/me/remote/devices/"+itoa(dev), "", map[string]any{"apps": map[string]bool{"money": true}}, nil)
	if _, m := rig.dial(t, "money.remote.test", app.key, map[string]any{"t": "open"}); m["t"] != "ready" {
		t.Fatalf("expected ready again, got %v", m)
	}

	// A key is good for its own address only.
	if _, m := rig.dial(t, "pocket.remote.test", app.key, map[string]any{"t": "open"}); m["code"] != "not_paired" {
		t.Fatalf("app key opened the launcher: %v", m)
	}

	// Exposure off: the app is unreachable for everyone.
	rig.e.do("PUT", "/api/v1/remote/apps/money", "", map[string]any{"exposed": false}, nil)
	if _, m := rig.dial(t, "money.remote.test", app.key, map[string]any{"t": "open"}); m["code"] != "app_off" {
		t.Fatalf("expected app_off, got %v", m)
	}
	rig.e.do("PUT", "/api/v1/remote/apps/money", "", map[string]any{"exposed": true}, nil)

	// Removing the device revokes every key.
	if c := rig.e.do("DELETE", "/api/v1/me/remote/devices/"+itoa(dev), "", nil, nil); c != 204 {
		t.Fatalf("delete %d", c)
	}
	if _, m := rig.dial(t, "money.remote.test", app.key, map[string]any{"t": "open"}); m["code"] != "not_paired" {
		t.Fatalf("expected not_paired, got %v", m)
	}
}

func TestRemotePairingCodes(t *testing.T) {
	rig := newRemoteRig(t)
	code := rig.pairLink()
	// A launcher code cannot pair an app address.
	if _, m := rig.dial(t, "money.remote.test", newKey(t), map[string]any{"t": "pair", "code": code}); m["code"] != "bad_code" {
		t.Fatalf("launcher code paired an app: %v", m)
	}
	// Any try uses a code up; a fresh one pairs once and only once.
	code = rig.pairLink()
	if _, m := rig.dial(t, "pocket.remote.test", newKey(t), map[string]any{"t": "pair", "code": code}); m["t"] != "ready" {
		t.Fatalf("pair: %v", m)
	}
	if _, m := rig.dial(t, "pocket.remote.test", newKey(t), map[string]any{"t": "pair", "code": code}); m["code"] != "bad_code" {
		t.Fatalf("code reused: %v", m)
	}
	// Unknown keys get nothing.
	if _, m := rig.dial(t, "pocket.remote.test", newKey(t), map[string]any{"t": "open"}); m["code"] != "not_paired" {
		t.Fatalf("unknown key: %v", m)
	}
	// A browser that does not know the box key cannot even finish the handshake.
	rig.box = newKey(t).Public
	addr := strings.TrimPrefix(rig.relay.URL, "http://")
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	_, port, _ := net.SplitHostPort(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://pocket.remote.test:"+port+"/__pocket/tunnel", &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	ini, _ := remote.NewInitiator(newKey(t), rig.box, "pocket.remote.test")
	m1, _ := ini.Start()
	c.Write(ctx, websocket.MessageBinary, m1)
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("Core answered a handshake made for another key")
	}
}

func TestRemoteSwitchOffDisconnects(t *testing.T) {
	rig := newRemoteRig(t)
	_, app := rig.pairApp(t)
	if r := app.do("GET", "/hello", nil, nil); r.status != 200 {
		t.Fatalf("hello %d", r.status)
	}
	rig.e.do("PUT", "/api/v1/remote", "", map[string]any{"enabled": false}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := app.c.Read(ctx); err != nil {
			break
		}
	}
	if rig.e.s.remote.snapshot().Connected {
		time.Sleep(200 * time.Millisecond)
		if rig.e.s.remote.snapshot().Connected {
			t.Fatal("Core is still connected to the relay after switching remote access off")
		}
	}
}

// softPasskey is an in-test authenticator (ES256), like Face ID would be.
type softPasskey struct {
	key *ecdsa.PrivateKey
	id  string
}

func (p *softPasskey) authData(host string) []byte {
	rp := sha256.Sum256([]byte(host))
	ad := append(rp[:], wanFlagUP|wanFlagUV)
	return binary.BigEndian.AppendUint32(ad, 1)
}

func (p *softPasskey) clientData(typ, challenge, host string) []byte {
	b, _ := json.Marshal(map[string]string{"type": typ, "challenge": challenge, "origin": "https://" + host})
	return b
}

func TestRemotePasskey(t *testing.T) {
	rig := newRemoteRig(t)
	rig.e.do("PUT", "/api/v1/remote/apps/money", "", map[string]any{"passkey": true}, nil)
	code := rig.pairLink()
	portal, ready := rig.dial(t, "pocket.remote.test", newKey(t), map[string]any{"t": "pair", "code": code, "name": "Phone"})
	if ready["t"] != "ready" {
		t.Fatalf("portal %v", ready)
	}
	r := portal.do("POST", "/__pocket/api/open", nil, []byte(`{"app":"money"}`))
	var open map[string]string
	json.Unmarshal(r.body, &open)
	hcode, _ := fragment(t, open["url"])
	host := "money.remote.test"
	key := newKey(t)
	app, m := rig.dial(t, host, key, map[string]any{"t": "pair", "code": hcode})
	if m["t"] != "passkey-setup" {
		t.Fatalf("expected passkey-setup, got %v", m)
	}
	pk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	soft := &softPasskey{key: pk, id: b64u([]byte("cred-1"))}
	spki, _ := x509.MarshalPKIXPublicKey(&pk.PublicKey)
	app.sendCtl(map[string]any{"t": "passkey-setup", "id": soft.id, "clientData": b64u(soft.clientData("webauthn.create", m["challenge"].(string), host)),
		"authData": b64u(soft.authData(host)), "publicKey": b64u(spki), "alg": -7})
	if m := app.ctl(); m["t"] != "ready" {
		t.Fatalf("after setup: %v", m)
	}

	// Within the grace period no new prompt.
	if _, m := rig.dial(t, host, key, map[string]any{"t": "open"}); m["t"] != "ready" {
		t.Fatalf("grace: %v", m)
	}

	// After it, a signature is needed; a wrong one is refused.
	rig.e.s.remote.passMu.Lock()
	rig.e.s.remote.passOK = map[int64]time.Time{}
	rig.e.s.remote.passMu.Unlock()
	sign := func(challenge string, k *ecdsa.PrivateKey) map[string]any {
		cd := soft.clientData("webauthn.get", challenge, host)
		ad := soft.authData(host)
		d := sha256.Sum256(cd)
		h := sha256.Sum256(append(append([]byte{}, ad...), d[:]...))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, h[:])
		return map[string]any{"t": "passkey", "id": soft.id, "clientData": b64u(cd), "authData": b64u(ad), "signature": b64u(sig)}
	}
	bad, m := rig.dial(t, host, key, map[string]any{"t": "open"})
	if m["t"] != "passkey" || m["id"] != soft.id {
		t.Fatalf("expected passkey prompt, got %v", m)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	bad.sendCtl(sign(m["challenge"].(string), other))
	if m := bad.ctl(); m["t"] != "error" || m["code"] != "passkey" {
		t.Fatalf("wrong signature accepted: %v", m)
	}
	good, m := rig.dial(t, host, key, map[string]any{"t": "open"})
	good.sendCtl(sign(m["challenge"].(string), pk))
	if m := good.ctl(); m["t"] != "ready" {
		t.Fatalf("good signature refused: %v", m)
	}
}

func TestRemoteHeaderFilter(t *testing.T) {
	for k, want := range map[string]bool{"Accept": true, "Range": true, "X-Custom-Thing": true, "Cookie": false, "Authorization": false,
		"X-Remote-User": false, "X-Forwarded-For": false, "Remote-User": false, "X-Auth-Request-Email": false, "X-Api-Key": false, "Origin": false} {
		if got := remoteHeaderAllowed(k); got != want {
			t.Errorf("%s: got %v want %v", k, got, want)
		}
	}
	if got := stripFrameAncestors("default-src 'self'; frame-ancestors 'none'; img-src *"); strings.Contains(got, "frame-ancestors") || !strings.Contains(got, "img-src") {
		t.Errorf("csp %q", got)
	}
	rs := remoteSettings{Pattern: "{app}-r.example.org"}
	if rs.slugFor("money-r.example.org") != "money" || rs.slugFor("evil.money-r.example.org") != "" || rs.slugFor("-r.example.org") != "" {
		t.Error("slugFor")
	}
	if err := validPattern("{app}.remote.example.org"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"remote.example.org", "{app}/x.org", "{app}", "https://{app}.x.org"} {
		if validPattern(bad) == nil {
			t.Errorf("pattern %q accepted", bad)
		}
	}
}
