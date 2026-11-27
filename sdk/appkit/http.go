// Package appkit is the server chassis every Pocket app used to copy: the
// OpenID Connect relying party, the order in which a request's credentials
// are tried, the link-route allowlist, connection-mode reports and the small
// HTTP helpers around them. It holds no store: the app hands it functions
// that look people up in its own database, so each app keeps its own user
// type and tables. Standard library only, like the rest of the SDK.
//
// See docs/appkit.md for how an app wires it in.
package appkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// WriteJSON answers v as JSON with that status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteErr answers {"error": msg}, the shape every Pocket app's web UI reads.
func WriteErr(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// SafeNext keeps a return path on this site and out of the API.
func SafeNext(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.HasPrefix(p, "/api/") {
		return "/"
	}
	return p
}

// RandomToken is n random bytes as hex.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// SessionCookie is an app's session cookie.
type SessionCookie struct {
	Name   string
	Secure bool
}

// Set sets the cookie for ttl; a negative ttl clears it.
func (c SessionCookie) Set(w http.ResponseWriter, id string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: c.Name, Value: id, Path: "/", HttpOnly: true,
		Secure: c.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}

// Clear removes the cookie.
func (c SessionCookie) Clear(w http.ResponseWriter) { c.Set(w, "", -time.Second) }

// OutboxHealth is the core_outbox value for /healthz: the SDK outbox's state,
// or {"available": false} when it cannot be read.
func OutboxHealth(ctx context.Context, c *core.Client) any {
	if c == nil {
		return map[string]any{"available": false}
	}
	status, err := c.OutboxStatus(ctx)
	if err != nil {
		return map[string]any{"available": false}
	}
	return status
}

// ClientErrors answers POST /api/v1/client-error: the web UI's ErrorBoundary
// (pocket-ui) reports a crashed screen here so it lands in the server's log.
// Public (a crash can happen on the login page), 16 KiB per report, at most
// 30 reports a minute logged; always 204. who names the signed-in person
// when there is one (may be nil).
func ClientErrors(log *slog.Logger, who func(r *http.Request) string) http.HandlerFunc {
	var mu sync.Mutex
	var window time.Time
	var n int
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message, Stack, Component, URL, Where, Agent string
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mu.Lock()
		if time.Since(window) > time.Minute {
			window, n = time.Now(), 0
		}
		n++
		over := n > 30
		mu.Unlock()
		if !over {
			person := ""
			if who != nil {
				person = who(r)
			}
			log.Warn("web app crashed", "user", person, "where", in.Where, "url", in.URL, "message", in.Message, "stack", in.Stack, "component", in.Component, "agent", in.Agent)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
