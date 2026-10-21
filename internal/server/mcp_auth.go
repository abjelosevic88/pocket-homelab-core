package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"golang.org/x/time/rate"
)

type mcpClientKey struct{}

func mcpClient(ctx context.Context) config.MCPClient {
	c, _ := ctx.Value(mcpClientKey{}).(config.MCPClient)
	return c
}

func (s *Server) mcpAccess(next http.Handler) http.Handler {
	// Bound aggregate work even when multiple credentials are used. No IP/header trust.
	requests := rate.NewLimiter(rate.Every(time.Second/2), 20)
	slots := make(chan struct{}, 4)
	base, _ := url.Parse(s.cfg.BaseURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !requests.Allow() {
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, "MCP request limit reached")
			return
		}
		if origins, present := r.Header["Origin"]; present {
			if len(origins) != 1 || base == nil || origins[0] != base.Scheme+"://"+base.Host {
				writeErr(w, http.StatusForbidden, "MCP origin rejected")
				return
			}
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(r.Header.Values("Authorization")) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			s.mcpChallenge(w)
			writeErr(w, http.StatusUnauthorized, "MCP bearer token required")
			return
		}
		var client config.MCPClient
		if s.cfg.MCPOAuthEnabled && strings.HasPrefix(parts[1], "pcma_") {
			secret, err := s.store.GetMCPSecret(r.Context(), parts[1], "access", "")
			var binding mcpAuthorization
			if err == nil && json.Unmarshal(secret.Data, &binding) == nil && binding.Resource == s.mcpResource() {
				user, err := s.store.GetUser(r.Context(), secret.UserID)
				if err == nil {
					apps, err := s.store.MCPVisibleApps(r.Context(), user)
					if err != nil {
						writeErr(w, 503, "MCP permissions unavailable")
						return
					}
					client = config.MCPClient{ID: fmt.Sprintf("oauth:%s:user:%d", secret.ClientID, user.ID), Apps: apps}
				}
			}
		} else {
			clients := []config.MCPClient{{ID: "environment", TokenHash: s.cfg.MCPTokenHash, Apps: s.cfg.MCPApps}}
			if s.cfg.MCPClientsFile != "" {
				var err error
				clients, err = config.ReadMCPClients(s.cfg.MCPClientsFile)
				if err != nil {
					s.log.Warn("MCP credential configuration unavailable")
					writeErr(w, http.StatusServiceUnavailable, "MCP credential configuration unavailable")
					return
				}
			}
			actual := sha256.Sum256([]byte(parts[1]))
			for _, candidate := range clients {
				expected, err := hex.DecodeString(candidate.TokenHash)
				if err == nil && len(expected) == sha256.Size && subtle.ConstantTimeCompare(actual[:], expected) == 1 &&
					(candidate.ExpiresAt.IsZero() || time.Now().Before(candidate.ExpiresAt)) {
					client = candidate
				}
			}
		}
		if client.ID == "" {
			s.mcpChallenge(w)
			writeErr(w, http.StatusUnauthorized, "MCP bearer token rejected or expired")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, "MCP concurrent request limit reached")
			return
		}
		// Bound slow request-body uploads, including from older clients.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
		defer http.NewResponseController(w).SetReadDeadline(time.Time{})
		ctx := context.WithValue(r.Context(), mcpClientKey{}, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
