package remote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Relay is the self-hosted relay: one box, many browser channels. It never
// sees plaintext; it only moves frames between the two sockets.
type Relay struct {
	Secret     string // what Core sends as its bearer token
	Portal     string // the launcher's address, handed to unpaired pages
	TrustProxy bool   // take the client address from X-Forwarded-For
	Log        *slog.Logger

	mu    sync.Mutex
	box   *relayBox
	next  uint32
	chans map[uint32]*relayChan
}

// relayChan queues frames for one browser so a slow phone never stalls the
// box socket (and with it everybody else).
type relayChan struct {
	c    *websocket.Conn
	out  chan []byte
	once sync.Once
}

func newRelayChan(c *websocket.Conn) *relayChan {
	ch := &relayChan{c: c, out: make(chan []byte, 512)}
	go func() {
		for msg := range ch.out {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var err error
			if msg == nil {
				err = c.Write(ctx, websocket.MessageText, []byte("pong"))
			} else {
				err = c.Write(ctx, websocket.MessageBinary, msg)
			}
			cancel()
			if err != nil {
				c.CloseNow()
				for range ch.out {
				}
				return
			}
		}
		c.Close(websocket.StatusNormalClosure, "")
	}()
	return ch
}

func (ch *relayChan) send(msg []byte) {
	defer func() { _ = recover() }() // closed meanwhile
	select {
	case ch.out <- msg:
	default:
		ch.c.CloseNow() // the browser stopped reading
	}
}

// sendText answers a keepalive; it rides the same queue as data.
func (ch *relayChan) sendText() { ch.send(nil) }

func (ch *relayChan) finish() { ch.once.Do(func() { close(ch.out) }) }

type relayBox struct {
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

// MaxChannels bounds the browser sockets one box serves at once.
const MaxChannels = 256

func (rl *Relay) log() *slog.Logger {
	if rl.Log != nil {
		return rl.Log
	}
	return slog.Default()
}

func (rl *Relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/__pocket/box":
		rl.serveBox(w, r)
	case "/__pocket/tunnel":
		rl.serveTunnel(w, r)
	case "/__pocket/config.json":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"portal": rl.Portal})
	case "/__pocket/health":
		rl.mu.Lock()
		up := rl.box != nil
		rl.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"box": up})
	default:
		ServeAsset(w, r)
	}
}

func (rl *Relay) serveBox(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if rl.Secret == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(rl.Secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	b := &relayBox{c: c, ctx: ctx, cancel: cancel}
	rl.mu.Lock()
	old, oldChans := rl.box, rl.chans
	rl.box, rl.chans = b, map[uint32]*relayChan{}
	rl.mu.Unlock()
	if old != nil {
		old.cancel()
		old.c.Close(websocket.StatusPolicyViolation, "replaced by a newer box connection")
	}
	for _, ch := range oldChans {
		ch.c.Close(websocket.StatusGoingAway, "box reconnected")
	}
	rl.log().Info("relay: box connected", "from", rl.clientIP(r))
	defer func() {
		cancel()
		rl.mu.Lock()
		var chans map[uint32]*relayChan
		if rl.box == b {
			rl.box, chans = nil, rl.chans
			rl.chans = nil
		}
		rl.mu.Unlock()
		for _, ch := range chans {
			ch.c.Close(websocket.StatusTryAgainLater, "box offline")
		}
		c.CloseNow()
		rl.log().Info("relay: box disconnected")
	}()
	for {
		typ, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			if string(msg) == "ping" {
				_ = c.Write(ctx, websocket.MessageText, []byte("pong"))
			}
			continue
		}
		ft, id, payload, err := Decode(msg)
		if err != nil {
			continue
		}
		rl.mu.Lock()
		ch := rl.chans[id]
		if ft == RelayClose {
			delete(rl.chans, id)
		}
		rl.mu.Unlock()
		if ch == nil {
			continue
		}
		switch ft {
		case RelayData:
			ch.send(append([]byte(nil), payload...))
		case RelayClose:
			ch.finish()
		}
	}
}

func (rl *Relay) serveTunnel(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	rl.mu.Lock()
	b := rl.box
	if b == nil {
		rl.mu.Unlock()
		c.Close(websocket.StatusTryAgainLater, "box offline")
		return
	}
	if len(rl.chans) >= MaxChannels {
		rl.mu.Unlock()
		c.Close(websocket.StatusTryAgainLater, "too many connections")
		return
	}
	rl.next++
	id := rl.next
	ch := newRelayChan(c)
	rl.chans[id] = ch
	rl.mu.Unlock()

	open, _ := json.Marshal(map[string]string{"host": hostOnly(r.Host), "ip": rl.clientIP(r)})
	ctx := b.ctx
	if err := b.c.Write(ctx, websocket.MessageBinary, Encode(RelayOpen, id, open)); err != nil {
		c.Close(websocket.StatusTryAgainLater, "box offline")
		return
	}
	defer func() {
		rl.mu.Lock()
		_, still := rl.chans[id]
		delete(rl.chans, id)
		rl.mu.Unlock()
		if still {
			_ = b.c.Write(ctx, websocket.MessageBinary, Encode(RelayClose, id, nil))
		}
		ch.finish()
		c.CloseNow()
	}()
	for {
		typ, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			if string(msg) == "ping" {
				ch.sendText()
			}
			continue
		}
		if err := b.c.Write(ctx, websocket.MessageBinary, Encode(RelayData, id, msg)); err != nil {
			return
		}
	}
}

func (rl *Relay) clientIP(r *http.Request) string {
	if rl.TrustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(h)
}
