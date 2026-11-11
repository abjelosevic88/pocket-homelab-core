package server

import (
	"net/http"
	"net/url"
	"time"
)

// handleDeviceWebSession lets a paired device (a pcd_ token) open a Core web session for its
// owner, so a native app that shows Pocket apps in web views (Pocket Hub) lands in them signed
// in: the apps' "Sign in with Pocket Core" finds this session at /oidc/authorize instead of the
// login page (where a passkey cannot work inside an app's web view). A device token already acts
// for its owner across the API, so this grants nothing the token cannot already do; revoking the
// device stops new sessions, and signing out ends the one made here like any other.
//
// POST /api/v1/auth/device/web-session (device token) →
// {cookie, session, secure, expires_at, domain, base_url}
func (s *Server) handleDeviceWebSession(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.Kind != "device" || p.User == nil || p.User.Disabled {
		writeErr(w, http.StatusForbidden, "paired devices only")
		return
	}
	id, err := s.store.CreateSession(r.Context(), p.User.ID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	domain := ""
	if u, err := url.Parse(s.cfg.BaseURL); err == nil {
		domain = u.Hostname()
	}
	s.log.Info("device web session", "device", p.Device.ID, "user", p.User.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"cookie":     sessionCookie,
		"session":    id,
		"secure":     s.cfg.SecureCookies,
		"expires_at": time.Now().Add(s.cfg.SessionTTL).UTC().Format(time.RFC3339),
		"domain":     domain,
		"base_url":   s.cfg.BaseURL,
	})
}
