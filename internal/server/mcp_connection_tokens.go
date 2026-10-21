package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Browser session + exact Origin are required even if a bearer header is present.
func (s *Server) mcpConnectionOrigin(r *http.Request) bool {
	base, err := url.Parse(s.cfg.BaseURL)
	return err == nil && len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == base.Scheme+"://"+base.Host
}

func (s *Server) handleMCPConnectionToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	user := s.sessionUser(r)
	if user == nil || user.Disabled {
		writeErr(w, 401, "sign in to Core")
		return
	}
	if !s.mcpConnectionOrigin(r) {
		writeErr(w, 403, "origin rejected")
		return
	}
	if !s.cfg.MCPOAuthEnabled {
		writeErr(w, 403, "account connections are disabled")
		return
	}
	var in struct {
		Name     string `json:"name"`
		Scope    string `json:"scope"`
		Days     int    `json:"expires_in_days"`
		Approved bool   `json:"approved"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF {
		writeErr(w, 400, "invalid connection request")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 || strings.ContainsAny(in.Name, "\r\n") {
		writeErr(w, 400, "give this connection a name (up to 80 characters)")
		return
	}
	if !in.Approved || !s.mcpValidScopes(in.Scope) {
		writeErr(w, 400, "approve a supported access scope")
		return
	}
	if in.Days != 1 && in.Days != 7 && in.Days != 30 {
		writeErr(w, 400, "choose an expiry of 1, 7 or 30 days")
		return
	}
	scope := canonicalMCPScopes(in.Scope)
	grant, token, err := s.store.CreateMCPConnectionToken(r.Context(), user, in.Name, scope, in.Days, mcpAuthorization{Resource: s.mcpResource(), Scope: scope})
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "disconnect an existing token first (maximum 20 active tokens)")
		return
	}
	if err != nil {
		s.fail(w, err, "create MCP connection token")
		return
	}
	s.log.Info("mcp connection token created", "user_id", user.ID, "connection_id", grant.ID, "scope", scope)
	writeJSON(w, 201, map[string]any{"connection": grant, "token": token, "url": s.mcpResource()})
}
