package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// limiter is a small fixed-window counter keyed by string (an IP, an email):
// after max hits inside window the key is blocked until the window ends.
// It protects login, pairing claims and registration from guessing without
// any external dependency; a restart clears it, which is fine for one box.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*bucket
}

type bucket struct {
	n     int
	reset time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string]*bucket{}}
}

// blocked reports whether key has exhausted its attempts.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.hits[key]
	if !ok || time.Now().After(b.reset) {
		return false
	}
	return b.n >= l.max
}

// fail records a failed attempt for key.
func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.hits[key]
	if !ok || now.After(b.reset) {
		b = &bucket{reset: now.Add(l.window)}
		l.hits[key] = b
	}
	b.n++
	if len(l.hits) > 10000 { // forgetful under pressure rather than unbounded
		for k, v := range l.hits {
			if now.After(v.reset) {
				delete(l.hits, k)
			}
		}
	}
}

// clear forgets a key after a success.
func (l *limiter) clear(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// clientIP strips the port from RemoteAddr (RealIP middleware already applied).
func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// sameOrigin rejects cookie-authenticated state changes whose Origin (or
// Referer) is another site. Bearer-token calls are exempt: a token cannot be
// attached by a cross-site form. SameSite=Lax on the cookie already blocks
// most of this; the check closes the gap for browsers that ignore it.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if bearer(r) != "" {
			next.ServeHTTP(w, r)
			return
		}
		if _, err := r.Cookie(sessionCookie); err != nil {
			next.ServeHTTP(w, r) // no session to abuse
			return
		}
		src := r.Header.Get("Origin")
		if src == "" {
			src = r.Header.Get("Referer")
		}
		if src == "" {
			next.ServeHTTP(w, r) // same-origin fetches from older clients omit both
			return
		}
		u, err := url.Parse(src)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			if b, err2 := url.Parse(s.cfg.BaseURL); err == nil && err2 == nil && strings.EqualFold(u.Host, b.Host) {
				next.ServeHTTP(w, r)
				return
			}
			writeErr(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}
