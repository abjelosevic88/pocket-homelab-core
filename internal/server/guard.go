package server

import (
	"context"
	"net"
	"net/http"
	"net/netip"
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

type viaProxyKey struct{}

// realIP takes the client address and scheme from X-Forwarded-* only when the
// request came from a proxy Core trusts (TRUST_PROXY on, peer inside
// TRUSTED_PROXIES). Anyone else reaching Core's port directly could otherwise
// pick any address (and dodge the per-address login limit) or claim https.
func (s *Server) realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.TrustProxy && s.trustedPeer(clientIP(r)) {
			if ip := s.forwardedClient(r); ip != "" {
				r.RemoteAddr = ip
			}
			r = r.WithContext(context.WithValue(r.Context(), viaProxyKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) trustedPeer(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// forwardedClient walks X-Forwarded-For from the right and stops at the first
// address that is not a trusted proxy: entries left of it were written by the
// client and prove nothing. X-Real-IP is used when there is no list.
func (s *Server) forwardedClient(r *http.Request) string {
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, f := range strings.Split(h, ",") {
			if f = strings.TrimSpace(f); f != "" {
				hops = append(hops, f)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			return ""
		}
		if i == 0 || !s.trustedPeer(a.String()) {
			return a.Unmap().String()
		}
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return a.Unmap().String()
	}
	return ""
}

// viaTrustedProxy reports whether realIP accepted this request's forwarded headers.
func viaTrustedProxy(r *http.Request) bool {
	v, _ := r.Context().Value(viaProxyKey{}).(bool)
	return v
}
