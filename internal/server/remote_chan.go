package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/abjelosevic88/pocket-homelab-core/internal/remote"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// remoteChan is one browser session: handshake, checks, then streams.
type remoteChan struct {
	h     *remoteHub
	relay *websocket.Conn
	id    uint32
	host  string
	ip    string
	slug  string

	in     chan []byte
	out    chan remoteOut
	ctx    context.Context
	cancel context.CancelFunc
	sess   *remote.Session

	mu      sync.Mutex
	acc     *remoteAccess
	token   string
	streams map[uint32]*remoteStream
}

type remoteOut struct {
	b     []byte
	raw   bool // a handshake message, not encrypted with the session
	close bool // end the session after everything before it was sent
}

type remoteStream struct {
	cancel context.CancelFunc
	body   chan []byte // request body chunks; nil when the request has none
	wsIn   chan wsMsg  // browser → app WebSocket messages
	wsBuf  []byte      // a message arriving in pieces
}

type wsMsg struct {
	typ  websocket.MessageType
	data []byte
}

const (
	remoteMaxStreams = 256
	remoteTokenTTL   = 7 * 24 * time.Hour
	remoteCodeTTL    = 10 * time.Minute
)

func newRemoteChan(h *remoteHub, relay *websocket.Conn, id uint32, host, ip string) *remoteChan {
	ctx, cancel := context.WithCancel(context.Background())
	return &remoteChan{h: h, relay: relay, id: id, host: strings.ToLower(host), ip: ip,
		in: make(chan []byte, 256), out: make(chan remoteOut, 64), ctx: ctx, cancel: cancel, streams: map[uint32]*remoteStream{}}
}

func (c *remoteChan) key() *store.RemoteKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.acc == nil {
		return nil
	}
	return c.acc.Key
}

func (c *remoteChan) deviceID() int64 {
	if k := c.key(); k != nil {
		return k.DeviceID
	}
	return 0
}

// deliver queues a frame from the relay; a browser that floods gets cut off.
func (c *remoteChan) deliver(b []byte) {
	select {
	case c.in <- b:
	case <-c.ctx.Done():
	default:
		c.stop()
	}
}

func (c *remoteChan) stop() { c.cancel() }

func (c *remoteChan) send(o remoteOut) bool {
	select {
	case c.out <- o:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *remoteChan) frame(typ byte, id uint32, payload []byte) bool {
	return c.send(remoteOut{b: remote.Encode(typ, id, payload)})
}

func (c *remoteChan) ctl(v any) bool {
	b, _ := json.Marshal(v)
	return c.frame(remote.FrameCtl, 0, b)
}

// fail tells the browser why and ends the session.
func (c *remoteChan) fail(code, msg string) {
	if c.sess != nil {
		c.ctl(map[string]string{"t": "error", "code": code, "message": msg})
	}
	c.send(remoteOut{close: true})
}

func (c *remoteChan) writer() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case o := <-c.out:
			if o.close {
				c.cancel()
				return
			}
			data := o.b
			if !o.raw {
				var err error
				if data, err = c.sess.Encrypt(o.b); err != nil {
					c.cancel()
					return
				}
			}
			wctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
			err := c.relay.Write(wctx, websocket.MessageBinary, remote.Encode(remote.RelayData, c.id, data))
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *remoteChan) next(timeout time.Duration) ([]byte, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case b := <-c.in:
		return b, nil
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case <-t.C:
		return nil, errors.New("remote: browser went quiet")
	}
}

// nextCtl reads the next control message during setup.
func (c *remoteChan) nextCtl(timeout time.Duration) (map[string]any, error) {
	for {
		b, err := c.next(timeout)
		if err != nil {
			return nil, err
		}
		plain, err := c.sess.Decrypt(b)
		if err != nil {
			return nil, err
		}
		typ, _, payload, err := remote.Decode(plain)
		if err != nil || typ != remote.FrameCtl {
			continue
		}
		var m map[string]any
		if json.Unmarshal(payload, &m) == nil && m["t"] != "ping" {
			return m, nil
		}
	}
}

func (c *remoteChan) run(parent context.Context) {
	s := c.h.s
	go func() {
		select {
		case <-parent.Done():
			c.cancel()
		case <-c.ctx.Done():
		}
	}()
	go c.writer()
	defer func() {
		c.cancel()
		c.h.drop(c.id, c)
		c.mu.Lock()
		tok := c.token
		for _, st := range c.streams {
			st.cancel()
		}
		c.mu.Unlock()
		if tok != "" {
			s.store.DeleteRemoteToken(context.Background(), tok)
		}
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.relay.Write(wctx, websocket.MessageBinary, remote.Encode(remote.RelayClose, c.id, nil))
		cancel()
	}()
	ctx := c.ctx
	rs := s.remoteSettings(ctx)
	c.slug = rs.slugFor(c.host)
	if c.slug == "" || !rs.Enabled {
		return
	}
	key, err := s.boxKey(ctx)
	if err != nil {
		return
	}
	resp, err := remote.NewResponder(key, c.host)
	if err != nil {
		return
	}
	m1, err := c.next(30 * time.Second)
	if err != nil {
		return
	}
	m2, err := resp.Accept(m1, []byte(`{"v":1}`))
	if err != nil {
		return
	}
	c.send(remoteOut{b: m2, raw: true})
	m3, err := c.next(30 * time.Second)
	if err != nil {
		return
	}
	payload, peer, sess, err := resp.Finish(m3)
	if err != nil {
		return
	}
	c.sess = sess
	var hello struct {
		T    string `json:"t"`
		Code string `json:"code"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(payload, &hello)

	k, err := c.authenticate(ctx, hello.T, hello.Code, hello.Name, hex.EncodeToString(peer))
	if err != nil {
		c.fail(remoteErrCode(err), remoteErrMessage(err))
		<-c.ctx.Done()
		return
	}
	acc, err := s.checkRemote(ctx, k, c.slug)
	if err != nil {
		c.fail(remoteErrCode(err), remoteErrMessage(err))
		<-c.ctx.Done()
		return
	}
	if acc.App != nil && acc.App.Passkey && !(k.HasPasskey && c.h.passkeyFresh(k.ID)) {
		if err := c.passkey(ctx, acc); err != nil {
			c.fail("passkey", err.Error())
			<-c.ctx.Done()
			return
		}
		c.h.passkeyTouch(k.ID)
	}
	if acc.App != nil {
		tok, err := s.store.CreateRemoteToken(ctx, k.ID, c.slug, remoteTokenTTL)
		if err != nil {
			c.fail("error", "Core could not start the session.")
			return
		}
		c.mu.Lock()
		c.token = tok
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.acc = acc
	c.mu.Unlock()
	s.store.TouchRemoteKey(ctx, k, c.ip)
	ready := map[string]any{"t": "ready", "kind": "app", "family": s.familyName(ctx),
		"user":   map[string]string{"name": acc.User.Name, "email": acc.User.Email},
		"device": map[string]any{"id": acc.Device.ID, "name": acc.Device.Name},
		"portal": "https://" + rs.host(portalSlug)}
	if acc.App == nil {
		ready["kind"] = "portal"
	} else {
		ready["app"] = map[string]string{"slug": acc.App.Slug, "name": acc.App.Name}
	}
	c.ctl(ready)

	for {
		b, err := c.next(10 * time.Minute)
		if err != nil {
			return
		}
		plain, err := c.sess.Decrypt(b)
		if err != nil {
			return
		}
		typ, id, p, err := remote.Decode(plain)
		if err != nil {
			return
		}
		c.dispatch(typ, id, append([]byte(nil), p...))
	}
}

type remoteErr struct{ code, msg string }

func (e *remoteErr) Error() string { return e.msg }

func remoteErrCode(err error) string {
	var re *remoteErr
	switch {
	case errors.As(err, &re):
		return re.code
	case errors.Is(err, errRemoteOff):
		return "remote_off"
	case errors.Is(err, errRemoteAppOff):
		return "app_off"
	case errors.Is(err, errRemoteDenied):
		return "denied"
	case errors.Is(err, errRemoteNotFound):
		return "not_paired"
	}
	return "error"
}

func remoteErrMessage(err error) string {
	switch remoteErrCode(err) {
	case "remote_off":
		return "Remote access is switched off in Core."
	case "app_off":
		return "This app is not reachable remotely. An admin can switch it on in Core → Remote access."
	case "denied":
		return "This device is not allowed to open this app. Switch it on in Core → Remote access → Devices."
	case "not_paired":
		return "This device is not paired with this address."
	}
	return err.Error()
}

// authenticate pairs a new key with a one-time code or finds a known key.
func (c *remoteChan) authenticate(ctx context.Context, mode, code, name, pub string) (*store.RemoteKey, error) {
	s := c.h.s
	if mode != "pair" {
		k, err := s.store.GetRemoteKeyByPublic(ctx, pub)
		if err != nil {
			return nil, errRemoteNotFound
		}
		return k, nil
	}
	if s.pairLimit.blocked("remote:" + c.ip) {
		return nil, &remoteErr{"bad_code", "Too many wrong codes. Wait 15 minutes."}
	}
	rc, err := s.store.TakeRemoteCode(ctx, code)
	if err != nil || rc.App != c.slug {
		s.pairLimit.fail("remote:" + c.ip)
		return nil, &remoteErr{"bad_code", "This pairing link is used up or expired. Make a new one in Core → Remote access."}
	}
	devID := rc.DeviceID
	if devID == 0 {
		name = strings.TrimSpace(name)
		if len(name) > 60 {
			name = name[:60]
		}
		if name == "" {
			name = "Device"
		}
		if devID, err = s.store.CreateRemoteDevice(ctx, rc.UserID, name); err != nil {
			return nil, err
		}
	} else if d, err := s.store.GetRemoteDevice(ctx, devID); err != nil || d.UserID != rc.UserID {
		return nil, errRemoteNotFound
	}
	k, err := s.store.PutRemoteKey(ctx, devID, c.slug, pub)
	if err != nil {
		return nil, err
	}
	s.log.Info("remote: paired", "device", devID, "app", c.slug, "ip", c.ip)
	return k, nil
}

func (c *remoteChan) dispatch(typ byte, id uint32, p []byte) {
	c.mu.Lock()
	st := c.streams[id]
	c.mu.Unlock()
	switch typ {
	case remote.FrameCtl:
		// keepalive pings; nothing to answer
	case remote.FrameReq, remote.FrameWSOpen:
		c.mu.Lock()
		n := len(c.streams)
		c.mu.Unlock()
		if st != nil || n >= remoteMaxStreams {
			c.frame(remote.FrameResErr, id, []byte("too many requests"))
			return
		}
		if a := c.access(); a != nil && a.App != nil && a.App.Passkey {
			c.h.passkeyTouch(a.Key.ID)
		}
		if typ == remote.FrameReq {
			c.startRequest(id, p)
		} else {
			c.startWS(id, p)
		}
	case remote.FrameReqBody:
		if st != nil && st.body != nil {
			select {
			case st.body <- p:
			case <-c.ctx.Done():
			}
		}
	case remote.FrameReqEnd:
		if st != nil && st.body != nil {
			close(st.body)
			st.body = nil
		}
	case remote.FrameCancel:
		if st != nil {
			st.cancel()
		}
	case remote.FrameWSText, remote.FrameWSBinary:
		if st == nil || st.wsIn == nil || len(p) < 1 {
			return
		}
		st.wsBuf = append(st.wsBuf, p[1:]...)
		if p[0] == 1 {
			mt := websocket.MessageText
			if typ == remote.FrameWSBinary {
				mt = websocket.MessageBinary
			}
			msg := st.wsBuf
			st.wsBuf = nil
			select {
			case st.wsIn <- wsMsg{mt, msg}:
			case <-c.ctx.Done():
			}
		}
	case remote.FrameWSClose:
		if st != nil {
			if st.wsIn != nil {
				close(st.wsIn)
				st.wsIn = nil
			}
		}
	}
}

func (c *remoteChan) access() *remoteAccess {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acc
}

func (c *remoteChan) addStream(id uint32, st *remoteStream) {
	c.mu.Lock()
	c.streams[id] = st
	c.mu.Unlock()
}

func (c *remoteChan) endStream(id uint32) {
	c.mu.Lock()
	st := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if st != nil {
		st.cancel()
	}
}

type remoteReq struct {
	M string      `json:"m"`
	U string      `json:"u"`
	H [][2]string `json:"h"`
	B bool        `json:"b"`
	P []string    `json:"p"`
}

func validRemotePath(u string) bool {
	return strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") && !strings.ContainsAny(u, "\r\n\\")
}

func (c *remoteChan) startRequest(id uint32, p []byte) {
	var rq remoteReq
	if json.Unmarshal(p, &rq) != nil || !validRemotePath(rq.U) || rq.M == "" || strings.ContainsAny(rq.M, " \r\n") {
		c.frame(remote.FrameResErr, id, []byte("bad request"))
		return
	}
	sctx, cancel := context.WithCancel(c.ctx)
	st := &remoteStream{cancel: cancel}
	var body io.Reader
	if rq.B {
		pr, pw := io.Pipe()
		st.body = make(chan []byte, 64)
		go func(ch chan []byte) {
			for {
				select {
				case b, ok := <-ch:
					if !ok {
						pw.Close()
						return
					}
					if _, err := pw.Write(b); err != nil {
						return
					}
				case <-sctx.Done():
					pw.CloseWithError(sctx.Err())
					return
				}
			}
		}(st.body)
		body = pr
	}
	c.addStream(id, st)
	go func() {
		defer c.endStream(id)
		if strings.HasPrefix(rq.U, "/__pocket/") {
			c.portal(sctx, id, rq, body)
			return
		}
		c.proxy(sctx, id, rq, body)
	}()
}

var remoteHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Request headers a browser may pass to an app. Everything else (cookies,
// Authorization, identity headers a proxy-mode app would trust) is dropped.
var remoteReqAllow = map[string]bool{"Accept": true, "Accept-Language": true, "Content-Type": true, "Range": true,
	"If-Range": true, "If-Match": true, "If-None-Match": true, "If-Modified-Since": true, "If-Unmodified-Since": true,
	"Cache-Control": true, "Pragma": true, "Last-Event-Id": true, "X-Requested-With": true, "Content-Language": true}

var remoteReqDenyWords = []string{"user", "email", "auth", "token", "forward", "real-ip", "remote", "proxy", "key", "group",
	"role", "client-ip", "original", "cookie", "session", "secret", "identity", "login"}

func remoteHeaderAllowed(k string) bool {
	k = http.CanonicalHeaderKey(k)
	if remoteReqAllow[k] {
		return true
	}
	if !strings.HasPrefix(k, "X-") {
		return false
	}
	lk := strings.ToLower(k)
	for _, w := range remoteReqDenyWords {
		if strings.Contains(lk, w) {
			return false
		}
	}
	return true
}

var remoteResDrop = map[string]bool{"Set-Cookie": true, "Set-Cookie2": true, "Connection": true, "Keep-Alive": true,
	"Transfer-Encoding": true, "Upgrade": true, "Proxy-Authenticate": true, "Trailer": true, "Te": true,
	"X-Frame-Options": true, "Strict-Transport-Security": true, "Alt-Svc": true, "Clear-Site-Data": true}

func (c *remoteChan) proxy(ctx context.Context, id uint32, rq remoteReq, body io.Reader) {
	acc := c.access()
	if acc == nil || acc.App == nil {
		c.frame(remote.FrameResErr, id, []byte("not an app session"))
		return
	}
	req, err := http.NewRequestWithContext(ctx, rq.M, strings.TrimRight(acc.App.Target, "/")+rq.U, body)
	if err != nil {
		c.frame(remote.FrameResErr, id, []byte("bad request"))
		return
	}
	for _, kv := range rq.H {
		if remoteHeaderAllowed(kv[0]) {
			req.Header.Add(kv[0], kv[1])
		}
		if strings.EqualFold(kv[0], "Content-Length") {
			if n, err := strconv.ParseInt(kv[1], 10, 64); err == nil && n >= 0 {
				req.ContentLength = n
			}
		}
	}
	if body == nil {
		req.ContentLength = 0
	}
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", c.ip)
	res, err := remoteHTTP.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			c.frame(remote.FrameResErr, id, []byte("the app did not answer"))
		}
		return
	}
	defer res.Body.Close()
	h := [][2]string{}
	for k, vs := range res.Header {
		if remoteResDrop[k] || (res.Uncompressed && (k == "Content-Length" || k == "Content-Encoding")) {
			continue
		}
		for _, v := range vs {
			switch k {
			case "Content-Security-Policy", "Content-Security-Policy-Report-Only":
				v = stripFrameAncestors(v)
			case "Location", "Content-Location":
				v = c.h.s.remoteRewriteURL(ctx, v, acc.App)
			}
			h = append(h, [2]string{k, v})
		}
	}
	head, _ := json.Marshal(map[string]any{"s": res.StatusCode, "h": h})
	if !c.frame(remote.FrameRes, id, head) {
		return
	}
	c.pump(ctx, id, res.Body)
}

func (c *remoteChan) pump(ctx context.Context, id uint32, r io.Reader) {
	buf := make([]byte, remote.MaxChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 && !c.frame(remote.FrameResBody, id, append([]byte(nil), buf[:n]...)) {
			return
		}
		if err == io.EOF {
			c.frame(remote.FrameResEnd, id, nil)
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				c.frame(remote.FrameResErr, id, []byte("the response broke off"))
			}
			return
		}
	}
}

// stripFrameAncestors lets the app page sit in the shell's same-origin frame.
func stripFrameAncestors(csp string) string {
	parts := strings.Split(csp, ";")
	out := parts[:0]
	for _, p := range parts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "frame-ancestors") {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// remoteRewriteURL turns links to the app's own addresses into paths, and
// links to another remotely reachable app into its remote address.
func (s *Server) remoteRewriteURL(ctx context.Context, v string, app *remoteApp) string {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return v
	}
	host := strings.ToLower(u.Hostname())
	path := u.RequestURI()
	if u.Fragment != "" {
		path += "#" + u.EscapedFragment()
	}
	if t, err := url.Parse(app.Target); err == nil && strings.EqualFold(t.Host, u.Host) {
		return path
	}
	rs := s.remoteSettings(ctx)
	apps, _ := s.store.ListApps(ctx)
	for _, a := range apps {
		pub, err := url.Parse(a.EffectivePublicURL())
		if err != nil || !strings.EqualFold(pub.Hostname(), host) {
			continue
		}
		if a.Slug == app.Slug {
			return path
		}
		return "https://" + rs.host(a.Slug) + path
	}
	if pub, err := url.Parse(s.cfg.BaseURL); err == nil && strings.EqualFold(pub.Hostname(), host) {
		if app.Slug == coreSlug {
			return path
		}
		return "https://" + rs.host(coreSlug) + path
	}
	return v
}

// startWS opens a WebSocket to the app and pipes messages both ways.
func (c *remoteChan) startWS(id uint32, p []byte) {
	var rq remoteReq
	acc := c.access()
	if json.Unmarshal(p, &rq) != nil || !validRemotePath(rq.U) || acc == nil || acc.App == nil {
		c.wsClose(id, 1008, "bad request")
		return
	}
	sctx, cancel := context.WithCancel(c.ctx)
	st := &remoteStream{cancel: cancel, wsIn: make(chan wsMsg, 64)}
	c.addStream(id, st)
	in := st.wsIn
	go func() {
		defer c.endStream(id)
		target := strings.TrimRight(acc.App.Target, "/") + rq.U
		target = "ws" + strings.TrimPrefix(target, "http")
		c.mu.Lock()
		tok := c.token
		c.mu.Unlock()
		dctx, dcancel := context.WithTimeout(sctx, 20*time.Second)
		conn, _, err := websocket.Dial(dctx, target, &websocket.DialOptions{Subprotocols: rq.P,
			HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-For": {c.ip}}})
		dcancel()
		if err != nil {
			c.wsClose(id, 1011, "the app refused the connection")
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(32 << 20)
		opened, _ := json.Marshal(map[string]string{"p": conn.Subprotocol()})
		c.frame(remote.FrameWSOpened, id, opened)
		go func() {
			for {
				select {
				case m, ok := <-in:
					if !ok {
						conn.Close(websocket.StatusNormalClosure, "")
						return
					}
					if err := conn.Write(sctx, m.typ, m.data); err != nil {
						conn.CloseNow()
						return
					}
				case <-sctx.Done():
					return
				}
			}
		}()
		for {
			mt, data, err := conn.Read(sctx)
			if err != nil {
				code, reason := 1000, ""
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					code, reason = int(ce.Code), ce.Reason
				} else if sctx.Err() != nil {
					return
				} else {
					code = 1006
				}
				c.wsClose(id, code, reason)
				return
			}
			typ := remote.FrameWSText
			if mt == websocket.MessageBinary {
				typ = remote.FrameWSBinary
			}
			for first := true; first || len(data) > 0; first = false {
				n := min(len(data), remote.MaxChunk)
				fin := byte(0)
				if n == len(data) {
					fin = 1
				}
				if !c.frame(typ, id, append([]byte{fin}, data[:n]...)) {
					return
				}
				data = data[n:]
			}
		}
	}()
}

func (c *remoteChan) wsClose(id uint32, code int, reason string) {
	b, _ := json.Marshal(map[string]any{"c": code, "r": reason})
	c.frame(remote.FrameWSClose, id, b)
}

// respond answers a stream with a whole body (launcher requests).
func (c *remoteChan) respond(id uint32, status int, ctype string, body []byte) {
	head, _ := json.Marshal(map[string]any{"s": status, "h": [][2]string{{"Content-Type", ctype}, {"Cache-Control", "no-store"}}})
	c.frame(remote.FrameRes, id, head)
	c.pump(c.ctx, id, bytes.NewReader(body))
}

func (c *remoteChan) respondJSON(id uint32, status int, v any) {
	b, _ := json.Marshal(v)
	c.respond(id, status, "application/json", b)
}

// portal serves the launcher's own API, answered by Core itself.
func (c *remoteChan) portal(ctx context.Context, id uint32, rq remoteReq, body io.Reader) {
	s := c.h.s
	acc := c.access()
	path, _, _ := strings.Cut(rq.U, "?")
	if acc == nil || acc.App != nil {
		c.respondJSON(id, 404, map[string]string{"error": "not found"})
		return
	}
	switch {
	case rq.M == "GET" && path == "/__pocket/api/apps":
		apps, err := s.remoteApps(ctx)
		if err != nil {
			c.respondJSON(id, 500, map[string]string{"error": "could not list apps"})
			return
		}
		d, _ := s.store.GetRemoteDevice(ctx, acc.Device.ID)
		paired := map[string]bool{}
		if d != nil {
			for _, k := range d.Keys {
				paired[k.App] = true
			}
		}
		rs := s.remoteSettings(ctx)
		out := []map[string]any{}
		for _, a := range apps {
			if !a.Exposed || (a.Slug != coreSlug && !s.appAdmits(ctx, a.AppID, acc.User)) {
				continue
			}
			_, err := s.checkRemote(ctx, &store.RemoteKey{DeviceID: acc.Device.ID, App: a.Slug}, a.Slug)
			out = append(out, map[string]any{"slug": a.Slug, "name": a.Name, "url": "https://" + rs.host(a.Slug) + "/",
				"allowed": err == nil, "paired": paired[a.Slug], "passkey": a.Passkey})
		}
		c.respondJSON(id, 200, map[string]any{"apps": out, "user": acc.User.Name, "device": acc.Device.Name, "family": s.familyName(ctx)})
	case rq.M == "POST" && path == "/__pocket/api/open":
		var in struct {
			App string `json:"app"`
		}
		if body != nil {
			_ = json.NewDecoder(io.LimitReader(body, 4096)).Decode(&in)
		}
		k := &store.RemoteKey{DeviceID: acc.Device.ID, App: in.App}
		if _, err := s.checkRemote(ctx, k, in.App); err != nil || in.App == portalSlug {
			c.respondJSON(id, 403, map[string]string{"error": remoteErrMessage(err)})
			return
		}
		code, err := s.store.CreateRemoteCode(ctx, store.RemoteCode{UserID: acc.User.ID, DeviceID: acc.Device.ID, App: in.App}, remoteCodeTTL)
		if err != nil {
			c.respondJSON(id, 500, map[string]string{"error": "could not open the app"})
			return
		}
		box, _ := s.boxKey(ctx)
		rs := s.remoteSettings(ctx)
		c.respondJSON(id, 200, map[string]string{"url": "https://" + rs.host(in.App) + "/#pair=" + code + "&box=" + base64.RawURLEncoding.EncodeToString(box.Public)})
	case rq.M == "GET" && strings.HasPrefix(path, "/__pocket/api/icon/"):
		c.icon(ctx, id, strings.TrimPrefix(path, "/__pocket/api/icon/"))
	default:
		c.respondJSON(id, 404, map[string]string{"error": "not found"})
	}
}

// icon fetches an app's icon from the app itself (or Core's bundled copy).
func (c *remoteChan) icon(ctx context.Context, id uint32, slug string) {
	s := c.h.s
	if s.webFS != nil {
		if b, err := fs.ReadFile(s.webFS, "icons/pocket-"+slug+".svg"); err == nil {
			c.respond(id, 200, "image/svg+xml", b)
			return
		}
	}
	app, err := s.remoteApp(ctx, slug)
	if err != nil || !app.Exposed || app.Icon == "" || !strings.HasPrefix(app.Icon, "/") {
		c.respond(id, 404, "text/plain", nil)
		return
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(app.Target, "/")+app.Icon, nil)
	res, err := remoteHTTP.Do(req)
	if err != nil {
		c.respond(id, 502, "text/plain", nil)
		return
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	ct := res.Header.Get("Content-Type")
	if res.StatusCode != 200 || !strings.HasPrefix(ct, "image/") {
		c.respond(id, 404, "text/plain", nil)
		return
	}
	c.respond(id, 200, ct, b)
}
