package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/go-chi/chi/v5"
)

//go:embed ask.html
var askHTML []byte

func (s *Server) askRoutes(r chi.Router) {
	r.Get("/ask", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// Frame ancestry is checked against registered applications, not a wildcard domain.
		ancestors := []string{"'self'"}
		apps, _ := s.store.ListApps(r.Context())
		for _, app := range apps {
			if u, e := url.Parse(app.EffectivePublicURL()); e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
				ancestors = append(ancestors, u.Scheme+"://"+u.Host)
			}
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors "+strings.Join(ancestors, " ")+"; base-uri 'none'; form-action 'self'")
		w.Write(askHTML)
	})
	r.Route("/api/v1/ask", func(r chi.Router) {
		r.Use(s.askAuth)
		r.Get("/status", s.handleAskStatus)
		r.Post("/connect", s.handleAskConnect)
		r.HandleFunc("/threads", s.handleAskProxy)
		r.HandleFunc("/threads/*", s.handleAskProxy)
	})
}

func (s *Server) askAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		u := s.sessionUser(r)
		if u == nil || u.Disabled {
			writeErr(w, 401, "Sign in to Pocket Core to use Ask.")
			return
		}
		if r.Method != "GET" && !s.mcpConnectionOrigin(r) {
			writeErr(w, 403, "origin rejected")
			return
		}
		if !s.cfg.MCPOAuthEnabled || !s.cfg.MCPAppToolsEnabled {
			writeErr(w, 503, "Pocket Ask requires Core application tools to be enabled.")
			return
		}
		apps, err := s.store.MCPVisibleApps(r.Context(), u)
		if err != nil || !slices.Contains(apps, "ai") {
			writeErr(w, 403, "Pocket AI is not shared with your account.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) askGrant(ctx context.Context, u *store.User) (string, error) {
	var id string
	err := s.store.DB.QueryRowContext(ctx, `SELECT g.id FROM pocket_ask_connections a JOIN mcp_oauth_grants g ON g.id=a.grant_id WHERE a.user_id=? AND g.user_id=? AND g.auth_revision=? AND g.expires_at>?`, u.ID, u.ID, u.AuthRevision, store.Now()).Scan(&id)
	return id, err
}
func (s *Server) handleAskStatus(w http.ResponseWriter, r *http.Request) {
	u := s.sessionUser(r)
	grant, _ := s.askGrant(r.Context(), u)
	apps, _ := s.store.MCPVisibleApps(r.Context(), u)
	ai, _ := s.store.GetAppBySlug(r.Context(), "ai")
	aiURL := ""
	if ai != nil {
		aiURL = ai.EffectivePublicURL()
	}
	writeJSON(w, 200, map[string]any{"connected": grant != "", "apps": apps, "ai_url": aiURL, "name": u.Name, "user_id": u.ID})
}
func (s *Server) askAI(ctx context.Context, grant, method, path string, body []byte) (int, []byte, error) {
	return s.askAIRequest(ctx, grant, method, "/api/v1/ask"+path, "application/json", body)
}

// Paths are supplied by Core handlers, never arbitrary client URLs.
func (s *Server) askAIRequest(ctx context.Context, grant, method, path, contentType string, body []byte) (int, []byte, error) {
	app, err := s.store.GetAppBySlug(ctx, "ai")
	if err != nil {
		return 0, nil, err
	}
	base, err := url.Parse(app.URL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "http" && base.Scheme != "https") {
		return 0, nil, errors.New("invalid Pocket AI URL")
	}
	token, err := s.store.CreateMCPAppToken(ctx, grant, mcpAuthorization{Resource: s.mcpResource(), Scope: "status:read apps:read apps:write", Target: "ai"})
	if err != nil {
		return 0, nil, err
	}
	defer s.store.DeleteMCPAppToken(context.WithoutCancel(ctx), token)
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(app.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: 55 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, errors.New("Pocket AI could not be reached")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 || (len(raw) > 0 && !json.Valid(raw)) {
		return 0, nil, errors.New("invalid Pocket AI response")
	}
	return res.StatusCode, raw, nil
}
func (s *Server) handleAskConnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Approved bool `json:"approved"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in) != nil || !in.Approved {
		writeErr(w, 400, "Approve Pocket Ask access first.")
		return
	}
	u := s.sessionUser(r)
	old, _ := s.askGrant(r.Context(), u)
	scope := "status:read apps:read apps:write"
	grant, token, err := s.store.CreateMCPConnectionToken(r.Context(), u, "Pocket Ask", scope, 30, mcpAuthorization{Resource: s.mcpResource(), Scope: scope})
	if err != nil {
		writeErr(w, 409, "Could not connect Ask. Disconnect an old AI connection and try again.")
		return
	}
	success := false
	defer func() {
		if !success {
			_ = s.store.DeleteMCPGrant(context.WithoutCancel(r.Context()), grant.ID, u.ID)
		}
	}()
	raw, _ := json.Marshal(map[string]string{"token": token})
	code, _, err := s.askAI(r.Context(), grant.ID, "POST", "/connect", raw)
	if err != nil || code != 204 {
		writeErr(w, 502, "Pocket AI could not save the connection. Try again shortly.")
		return
	}
	_, err = s.store.DB.ExecContext(r.Context(), `INSERT INTO pocket_ask_connections(user_id,grant_id) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET grant_id=excluded.grant_id`, u.ID, grant.ID)
	if err != nil {
		s.fail(w, err, "save Ask connection")
		return
	}
	success = true
	if old != "" {
		_ = s.store.DeleteMCPGrant(r.Context(), old, u.ID)
	}
	w.WriteHeader(204)
}

var askProxyPath = regexp.MustCompile(`^/threads(/[a-f0-9-]{36}(/messages|/interrupt|/requests/[A-Za-z0-9_-]{1,160})?)?$`)

func (s *Server) handleAskProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/ask")
	if !askProxyPath.MatchString(path) || (r.Method != "GET" && r.Method != "POST") || (r.Method == "GET" && strings.Count(path, "/") > 2) {
		writeErr(w, 404, "not found")
		return
	}
	u := s.sessionUser(r)
	grant, err := s.askGrant(r.Context(), u)
	if err != nil {
		writeErr(w, 409, "Reconnect Pocket Ask to continue.")
		return
	}
	limit := int64(64 << 10)
	if r.Method == "POST" && path == "/threads" {
		limit = 3 << 20 // one bounded photo preview plus the ordinary text context
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		writeErr(w, 413, "request too large")
		return
	}
	var photoCleanup func()
	if r.Method == "POST" && path == "/threads" {
		var in struct {
			Source struct {
				App string `json:"app"`
				URL string `json:"url"`
			} `json:"source"`
		}
		if json.Unmarshal(raw, &in) != nil {
			writeErr(w, 400, "invalid question")
			return
		}
		apps, _ := s.store.MCPVisibleApps(r.Context(), u)
		app, e := s.store.GetAppBySlug(r.Context(), in.Source.App)
		if in.Source.App == "core" {
			app = &store.App{PublicURL: s.cfg.BaseURL}
			e = nil
			apps = append(apps, "core")
		}
		target, e2 := url.Parse(in.Source.URL)
		if e != nil || e2 != nil || !slices.Contains(apps, in.Source.App) {
			writeErr(w, 403, "This application is not available to you.")
			return
		}
		origin, e := url.Parse(app.EffectivePublicURL())
		if e != nil || target.User != nil || target.Scheme != origin.Scheme || target.Host != origin.Host {
			writeErr(w, 400, "The item must belong to its Pocket application.")
			return
		}
		var status int
		raw, photoCleanup, status, err = s.prepareAskPhoto(r.Context(), grant, in.Source.App, raw)
		if err != nil {
			writeErr(w, status, err.Error())
			return
		}
	}
	code, out, err := s.askAI(r.Context(), grant, r.Method, path, raw)
	if photoCleanup != nil && (err != nil || code >= 400) {
		photoCleanup() // AI refuses to remove a photo already attached to a thread.
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(out)
}
