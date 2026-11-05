package server

// Remote access: Core keeps one WebSocket open to a relay and runs an
// end-to-end encrypted session with every browser that connects through it.
// docs/remote.md has the whole picture; internal/remote has the wire format.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/abjelosevic88/pocket-homelab-core/internal/remote"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// portalSlug is the launcher's address label: pairing and the app list.
const portalSlug = "pocket"

// coreSlug is Core's own UI as a remote app.
const coreSlug = "core"

// sensitiveApps start switched off for new devices and ask for a passkey:
// Pocket AI runs commands on the server, Core manages the whole box.
var sensitiveApps = map[string]bool{"ai": true, coreSlug: true}

const remoteKV = "remote"

// remoteSettings is stored as JSON in the settings table; secrets sealed.
type remoteSettings struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"` // cloudflare | custom
	Pattern  string `json:"pattern"`  // {app}.remote.example.org
	RelayURL string `json:"relay_url"`
	Secret   string `json:"secret"`  // sealed: the relay's bearer token for Core
	BoxKey   string `json:"box_key"` // sealed hex: private || public

	CFAccount  string   `json:"cf_account"`
	CFToken    string   `json:"cf_token"` // sealed
	CFWorker   string   `json:"cf_worker"`
	CFDeployed string   `json:"cf_deployed"` // asset hash of the uploaded Worker
	CFMigrated bool     `json:"cf_migrated"`
	CFDomains  []string `json:"cf_domains"` // hostnames Core created
}


// validPattern accepts host patterns like {app}.remote.example.org or {app}-r.example.org.
func validPattern(p string) error {
	if strings.Count(p, "{app}") != 1 {
		return errors.New("the address pattern needs exactly one {app}")
	}
	host := strings.Replace(p, "{app}", "x", 1)
	if strings.ContainsAny(host, "/:@ ") || !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return errors.New("the address pattern must be a host name such as {app}.remote.example.org")
	}
	return nil
}

func (rs *remoteSettings) host(slug string) string {
	return strings.ToLower(strings.Replace(rs.Pattern, "{app}", slug, 1))
}

// slugFor maps an address back to the app label in it.
func (rs *remoteSettings) slugFor(host string) string {
	if rs.Pattern == "" {
		return ""
	}
	pre, suf, _ := strings.Cut(strings.ToLower(rs.Pattern), "{app}")
	host = strings.ToLower(host)
	if !strings.HasPrefix(host, pre) || !strings.HasSuffix(host, suf) || len(host) <= len(pre)+len(suf) {
		return ""
	}
	slug := host[len(pre) : len(host)-len(suf)]
	if !slugRe.MatchString(slug) {
		return ""
	}
	return slug
}

// boxURL is where Core dials the relay.
func (rs *remoteSettings) boxURL() string {
	u := strings.TrimSpace(rs.RelayURL)
	if rs.Provider == "cloudflare" || u == "" {
		if rs.Pattern == "" {
			return ""
		}
		u = "wss://" + rs.host(portalSlug)
	}
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	case !strings.HasPrefix(u, "ws://") && !strings.HasPrefix(u, "wss://"):
		u = "wss://" + u
	}
	pu, err := url.Parse(u)
	if err != nil {
		return ""
	}
	pu.Path, pu.RawQuery = "/__pocket/box", ""
	return pu.String()
}

func (s *Server) remoteSettings(ctx context.Context) remoteSettings {
	var rs remoteSettings
	if raw := s.store.GetKV(ctx, remoteKV); raw != "" {
		_ = json.Unmarshal([]byte(raw), &rs)
	}
	if rs.CFWorker == "" {
		rs.CFWorker = "pocket-relay"
	}
	if rs.Provider == "" {
		rs.Provider = "cloudflare"
	}
	return rs
}

func (s *Server) saveRemoteSettings(ctx context.Context, rs remoteSettings) error {
	b, _ := json.Marshal(rs)
	if err := s.store.SetKV(ctx, remoteKV, string(b)); err != nil {
		return err
	}
	s.remote.kick()
	return nil
}

func (s *Server) open(sealed string) string {
	if sealed == "" {
		return ""
	}
	v, err := s.store.Box.Open(sealed)
	if err != nil {
		return ""
	}
	return v
}

// boxKey returns the box's X25519 key, creating it on first use.
func (s *Server) boxKey(ctx context.Context) (remote.Key, error) {
	s.remote.keyMu.Lock()
	defer s.remote.keyMu.Unlock()
	rs := s.remoteSettings(ctx)
	if raw, err := hex.DecodeString(s.open(rs.BoxKey)); err == nil && len(raw) == 64 {
		return remote.Key{Private: raw[:32], Public: raw[32:]}, nil
	}
	k, err := remote.GenerateKey()
	if err != nil {
		return remote.Key{}, err
	}
	rs.BoxKey = s.store.Box.Seal(hex.EncodeToString(append(append([]byte{}, k.Private...), k.Public...)))
	b, _ := json.Marshal(rs)
	return k, s.store.SetKV(ctx, remoteKV, string(b))
}

// remoteApp is one app's effective switches.
type remoteApp struct {
	Slug          string
	Name          string
	Target        string // base URL Core forwards to
	Icon          string
	AppID         int64
	Exposed       bool
	DeviceDefault bool
	Passkey       bool
}

// remoteApps lists every app that can be offered remotely, with its switches.
func (s *Server) remoteApps(ctx context.Context) ([]remoteApp, error) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	set, err := s.store.ListRemoteApps(ctx)
	if err != nil {
		return nil, err
	}
	out := []remoteApp{}
	add := func(ra remoteApp) {
		if c, ok := set[ra.Slug]; ok {
			ra.Exposed, ra.DeviceDefault, ra.Passkey = c.Exposed, c.DeviceDefault, c.Passkey
		} else {
			ra.DeviceDefault, ra.Passkey = !sensitiveApps[ra.Slug], sensitiveApps[ra.Slug]
		}
		out = append(out, ra)
	}
	add(remoteApp{Slug: coreSlug, Name: s.familyName(ctx) + " Core", Target: fmt.Sprintf("http://127.0.0.1:%d", s.cfg.Port), Icon: "/icons/pocket-core.svg"})
	for _, a := range apps {
		if a.Slug == portalSlug || a.Slug == coreSlug || !slugRe.MatchString(a.Slug) || a.URL == "" {
			continue
		}
		add(remoteApp{Slug: a.Slug, Name: a.Name, Target: a.URL, Icon: a.Icon, AppID: a.ID})
	}
	return out, nil
}

func (s *Server) familyName(ctx context.Context) string {
	if st, err := s.store.GetSettings(ctx); err == nil && st.FamilyName != "" {
		return st.FamilyName
	}
	return "Pocket"
}

func (s *Server) remoteApp(ctx context.Context, slug string) (*remoteApp, error) {
	list, err := s.remoteApps(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Slug == slug {
			return &list[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// remoteAccess is who a device key belongs to and whether it may be used now.
type remoteAccess struct {
	Key    *store.RemoteKey
	Device *store.RemoteDevice
	User   *store.User
	App    *remoteApp // nil for the launcher
}

var (
	errRemoteOff      = errors.New("remote access is switched off")
	errRemoteAppOff   = errors.New("this app is not reachable remotely")
	errRemoteDenied   = errors.New("this device may not open this app")
	errRemoteNotFound = errors.New("this device is not paired")
)

// checkRemote re-checks every switch for a key on an app. It runs at session
// start and on every introspection, so switching anything off applies at once.
func (s *Server) checkRemote(ctx context.Context, k *store.RemoteKey, slug string) (*remoteAccess, error) {
	if !s.remoteSettings(ctx).Enabled {
		return nil, errRemoteOff
	}
	if k == nil || k.App != slug {
		return nil, errRemoteNotFound
	}
	d, err := s.store.GetRemoteDevice(ctx, k.DeviceID)
	if err != nil {
		return nil, errRemoteNotFound
	}
	u, err := s.store.GetUser(ctx, d.UserID)
	if err != nil || u.Disabled {
		return nil, errRemoteNotFound
	}
	acc := &remoteAccess{Key: k, Device: d, User: u}
	if slug == portalSlug {
		return acc, nil
	}
	app, err := s.remoteApp(ctx, slug)
	if err != nil || !app.Exposed {
		return nil, errRemoteAppOff
	}
	allowed, set := d.Apps[slug]
	if !set {
		allowed = app.DeviceDefault
	}
	if !allowed {
		return nil, errRemoteDenied
	}
	if slug == coreSlug {
		// Core's own UI: anyone with a Core account, as in a browser at home.
	} else if !s.appAdmits(ctx, app.AppID, u) {
		return nil, errRemoteDenied
	}
	acc.App = app
	return acc, nil
}

// ---- the relay connection ----

type remoteStatus struct {
	Connected bool   `json:"connected"`
	Since     string `json:"since,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Sessions  int    `json:"sessions"`
}

type remoteHub struct {
	s     *Server
	keyMu sync.Mutex
	kickC chan struct{}

	mu     sync.Mutex
	conn   *websocket.Conn
	status remoteStatus
	chans  map[uint32]*remoteChan

	passMu sync.Mutex
	passOK map[int64]time.Time // key id → passkey verified until (rolling)

	cfMu      sync.Mutex  // one Cloudflare deploy or sync at a time
	cfKick    chan struct{} // an app was switched on or off: sync addresses
	cfPending atomic.Bool
	cfErr     atomic.Pointer[string]
}

func newRemoteHub(s *Server) *remoteHub {
	return &remoteHub{s: s, kickC: make(chan struct{}, 1), cfKick: make(chan struct{}, 1), chans: map[uint32]*remoteChan{}, passOK: map[int64]time.Time{}}
}

// kick makes the loop re-read the settings (reconnect or disconnect).
func (h *remoteHub) kick() {
	if h == nil {
		return
	}
	h.mu.Lock()
	c := h.conn
	h.mu.Unlock()
	if c != nil {
		c.Close(websocket.StatusNormalClosure, "settings changed")
	}
	select {
	case h.kickC <- struct{}{}:
	default:
	}
}

// syncAddresses asks for the Cloudflare addresses to follow the app switches.
// Switching apps answers at once; the sync runs here, once for a burst of flips.
func (h *remoteHub) syncAddresses() {
	h.cfPending.Store(true)
	select {
	case h.cfKick <- struct{}{}:
	default:
	}
}

func (h *remoteHub) cfSyncer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.cfKick:
		}
		// Let a burst of flips settle into one sync.
		select {
		case <-ctx.Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
		for len(h.cfKick) > 0 {
			<-h.cfKick
		}
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := h.s.deployCloudflare(sctx, false)
		cancel()
		if err != nil {
			msg := err.Error()
			h.cfErr.Store(&msg)
			h.s.log.Warn("remote: Cloudflare addresses did not update", "err", err)
		} else {
			h.cfErr.Store(nil)
		}
		if len(h.cfKick) == 0 {
			h.cfPending.Store(false)
		}
	}
}

func (h *remoteHub) snapshot() remoteStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.status
	st.Sessions = len(h.chans)
	return st
}

func (h *remoteHub) run(ctx context.Context) {
	backoff := time.Second
	for {
		rs := h.s.remoteSettings(ctx)
		target, secret := rs.boxURL(), h.s.open(rs.Secret)
		if !rs.Enabled || target == "" || secret == "" {
			h.setStatus(false, "")
			select {
			case <-ctx.Done():
				return
			case <-h.kickC:
				backoff = time.Second
				continue
			}
		}
		err := h.connect(ctx, target, secret)
		if ctx.Err() != nil {
			return
		}
		msg := ""
		if err != nil {
			msg = err.Error()
			h.s.log.Warn("remote: relay connection ended", "relay", target, "err", err)
		}
		h.setStatus(false, msg)
		select {
		case <-ctx.Done():
			return
		case <-h.kickC:
			backoff = time.Second
		case <-time.After(backoff):
			if backoff < time.Minute {
				backoff *= 2
			}
		}
	}
}

func (h *remoteHub) setStatus(up bool, errMsg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if up != h.status.Connected {
		h.status.Since = store.Now()
	}
	h.status.Connected = up
	if errMsg != "" || up {
		h.status.LastError = errMsg
	}
}

func (h *remoteHub) connect(ctx context.Context, target, secret string) error {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, resp, err := websocket.Dial(dctx, target, &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer " + secret}}})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == 401 {
			return errors.New("the relay refused Core's secret")
		}
		return err
	}
	c.SetReadLimit(1 << 20)
	cctx, stop := context.WithCancel(ctx)
	defer stop()
	h.mu.Lock()
	h.conn = c
	h.mu.Unlock()
	h.setStatus(true, "")
	h.s.log.Info("remote: connected to relay", "relay", target)
	defer func() {
		h.mu.Lock()
		h.conn = nil
		chans := h.chans
		h.chans = map[uint32]*remoteChan{}
		h.mu.Unlock()
		for _, ch := range chans {
			ch.stop()
		}
		c.CloseNow()
	}()
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				wctx, cancel := context.WithTimeout(cctx, 10*time.Second)
				err := c.Write(wctx, websocket.MessageText, []byte("ping"))
				cancel()
				if err != nil {
					c.CloseNow()
					return
				}
			}
		}
	}()
	for {
		typ, msg, err := c.Read(cctx)
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) && ce.Code == websocket.StatusNormalClosure {
				return nil
			}
			return err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		ft, id, payload, err := remote.Decode(msg)
		if err != nil {
			continue
		}
		switch ft {
		case remote.RelayOpen:
			var open struct{ Host, IP string }
			_ = json.Unmarshal(payload, &open)
			ch := newRemoteChan(h, c, id, open.Host, open.IP)
			h.mu.Lock()
			if old := h.chans[id]; old != nil {
				old.stop()
			}
			h.chans[id] = ch
			h.mu.Unlock()
			go ch.run(cctx)
		case remote.RelayData:
			h.mu.Lock()
			ch := h.chans[id]
			h.mu.Unlock()
			if ch != nil {
				ch.deliver(append([]byte(nil), payload...))
			}
		case remote.RelayClose:
			h.mu.Lock()
			ch := h.chans[id]
			delete(h.chans, id)
			h.mu.Unlock()
			if ch != nil {
				ch.stop()
			}
		}
	}
}

func (h *remoteHub) drop(id uint32, ch *remoteChan) {
	h.mu.Lock()
	if h.chans[id] == ch {
		delete(h.chans, id)
	}
	h.mu.Unlock()
}

// closeWhere ends every session matching f (a switch was turned off).
func (h *remoteHub) closeWhere(f func(*remoteChan) bool) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	var hit []*remoteChan
	for _, ch := range h.chans {
		if f(ch) {
			hit = append(hit, ch)
		}
	}
	h.mu.Unlock()
	for _, ch := range hit {
		ch.fail("revoked", "Access was switched off in Core.")
	}
	return len(hit)
}

// recheck closes sessions whose switches no longer allow them.
func (h *remoteHub) recheck(ctx context.Context) {
	h.closeWhere(func(ch *remoteChan) bool {
		k := ch.key()
		if k == nil {
			return false
		}
		cur, err := h.s.store.GetRemoteKey(ctx, k.ID)
		if err != nil {
			return true
		}
		_, err = h.s.checkRemote(ctx, cur, ch.slug)
		return err != nil
	})
}

const passkeyGrace = 15 * time.Minute

func (h *remoteHub) passkeyFresh(keyID int64) bool {
	h.passMu.Lock()
	defer h.passMu.Unlock()
	return time.Now().Before(h.passOK[keyID])
}

func (h *remoteHub) passkeyTouch(keyID int64) {
	h.passMu.Lock()
	h.passOK[keyID] = time.Now().Add(passkeyGrace)
	h.passMu.Unlock()
}
