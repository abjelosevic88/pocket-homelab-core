package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/abjelosevic88/pocket-homelab-core/scripts"
	"github.com/go-chi/chi/v5"
	"golang.org/x/time/rate"
)

func (s *Server) mcpPairingRoutes(r chi.Router) {
	if !s.cfg.MCPOAuthEnabled {
		return
	}
	r.Get("/ai-connections/pair", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		s.handleSPA(w, r)
	})
	r.Get("/mcp/connect.py", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-python; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="pocket-connect.py"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(scripts.MCPConnect)
	})
	// Independent limits keep public initiation/guessing from consuming the
	// authenticated approval or redemption budget. Storage is bounded too.
	for _, endpoint := range []string{"start", "inspect", "approve", "deny", "exchange"} {
		limit := rate.NewLimiter(rate.Every(time.Second), 20)
		if endpoint == "start" {
			limit = rate.NewLimiter(rate.Every(5*time.Second), 10)
		}
		r.Post("/mcp/pair/"+endpoint, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if !s.cfg.MCPOAuthEnabled {
				writeErr(w, 403, "account connections are disabled")
				return
			}
			if !limit.Allow() {
				w.Header().Set("Retry-After", "5")
				writeErr(w, 429, "too many pairing attempts; retry shortly")
				return
			}
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
			defer http.NewResponseController(w).SetReadDeadline(time.Time{})
			s.handleMCPPairing(w, r, endpoint)
		})
	}
}

func pairingJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil || dec.Decode(new(any)) != io.EOF {
		writeErr(w, 400, "invalid pairing request")
		return false
	}
	return true
}

func (s *Server) handleMCPPairing(w http.ResponseWriter, r *http.Request, action string) {
	if action == "start" {
		var in struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
			Days  int    `json:"expires_in_days"`
		}
		if !pairingJSON(w, r, &in) {
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" || len(in.Name) > 80 || strings.ContainsAny(in.Name, "\r\n") || !s.mcpValidScopes(in.Scope) || (in.Days != 1 && in.Days != 7 && in.Days != 30) {
			writeErr(w, 400, "choose a name, supported access scope, and expiry of 1, 7 or 30 days")
			return
		}
		secret, code, err := s.store.CreateMCPTerminalPairing(r.Context(), in.Name, canonicalMCPScopes(in.Scope), in.Days)
		if err != nil {
			s.pairingError(w, err)
			return
		}
		writeJSON(w, 201, map[string]any{"request_secret": secret, "user_code": code, "verification_uri": strings.TrimRight(s.cfg.BaseURL, "/") + "/ai-connections/pair", "expires_in": 600})
		return
	}
	if action == "exchange" {
		var in struct {
			Secret     string `json:"request_secret"`
			Completion string `json:"completion_code"`
		}
		if !pairingJSON(w, r, &in) {
			return
		}
		grant, token, err := s.store.ExchangeMCPTerminalPairing(r.Context(), in.Secret, in.Completion, func(scope string) (any, error) {
			if !s.mcpValidScopes(scope) {
				return nil, store.ErrNotFound
			}
			return mcpAuthorization{Resource: s.mcpResource(), Scope: scope}, nil
		})
		if err != nil {
			s.pairingError(w, err)
			return
		}
		s.log.Info("mcp terminal paired", "user_id", grant.UserID, "connection_id", grant.ID, "scope", grant.Scope)
		writeJSON(w, 200, map[string]any{"token": token, "url": s.mcpResource(), "connection": grant})
		return
	}
	user := s.sessionUser(r)
	if user == nil || user.Disabled {
		writeErr(w, 401, "sign in to Core")
		return
	}
	if !s.mcpConnectionOrigin(r) {
		writeErr(w, 403, "origin rejected")
		return
	}
	var in struct {
		Code string `json:"user_code"`
	}
	if !pairingJSON(w, r, &in) {
		return
	}
	pairing, err := s.store.GetMCPTerminalPairing(r.Context(), in.Code)
	if err != nil {
		s.pairingError(w, err)
		return
	}
	if !s.mcpValidScopes(pairing.Scope) {
		writeErr(w, 400, "requested access is no longer available; start again")
		return
	}
	if action == "inspect" {
		writeJSON(w, 200, pairing)
		return
	}
	completion, err := s.store.DecideMCPTerminalPairing(r.Context(), in.Code, user, action == "approve")
	if err != nil {
		s.pairingError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"completion_code": completion})
}

func (s *Server) pairingError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 400, "pairing code is invalid, expired or already used; start again")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "pairing limit reached; disconnect an existing token or retry later")
		return
	}
	s.fail(w, err, "MCP terminal pairing")
}
