// Package server wires the HTTP API, auth, the OIDC endpoints and the embedded SPA.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreSDK "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/events"
	"github.com/abjelosevic88/pocket-homelab-core/internal/oidc"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Version is injected at build time.
var Version = "dev"

// Server holds dependencies.
type Server struct {
	cfg       *config.Config
	store     *store.Store
	log       *slog.Logger
	webFS     fs.FS
	hub       *events.Hub
	oidc      *oidc.Provider
	sso       *ssoProvider // upstream provider for signing in to Core itself
	started   time.Time
	requests  atomic.Int64
	errors    atomic.Int64
	http      *http.Client
	connHTTP  *http.Client    // connectors talking to third-party apps
	ctx       context.Context // lives as long as the server; background work hangs off it
	polling   sync.Map        // service id → struct{}: a poll is running
	nextPoll  sync.Map        // service id → time.Time: when the scheduler polls next
	provMu    sync.Mutex      // one provisioning at a time: no duplicate accounts from racing clicks
	rotations sync.WaitGroup  // background key replacements after a service link was switched off

	hookMu    sync.Mutex // one subscription sync at a time
	hookSyncs sync.Map   // service id → hookSyncResult: what the last sync found per person
	hookKick  sync.Map   // service id → struct{}: a background sync is pending
	hookSeen  *seenCache // delivery dedupe
	hookRate  *limiter   // deliveries per hook per minute

	loginLimit *limiter // per IP and per email: 10 failures / 15 min
	pairLimit  *limiter // per IP: 10 bad codes / 15 min
	regLimit   *limiter // per IP: 10 bad registration keys / 15 min
}

// New creates a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, webFS fs.FS) *Server {
	ua := "PocketCore/" + Version + " (+https://github.com/abjelosevic88/pocket-homelab-core)"
	return &Server{cfg: cfg, store: st, log: log, webFS: webFS, started: time.Now(),
		hub:        events.New(st, log, cfg.WebhookAttempts, ua),
		sso:        &ssoProvider{issuerURL: cfg.SSOIssuer},
		http:       &http.Client{Timeout: 8 * time.Second},
		connHTTP:   &http.Client{Timeout: 30 * time.Second},
		ctx:        context.Background(),
		loginLimit: newLimiter(10, 15*time.Minute), pairLimit: newLimiter(10, 15*time.Minute), regLimit: newLimiter(10, 15*time.Minute),
		hookSeen: newSeenCache(hookDedupeWindow), hookRate: newLimiter(hookRatePerMin, time.Minute)}
}

// Bootstrap seeds the first admin (if configured), the registration key and
// the OIDC signing key, then starts the background work.
func (s *Server) Bootstrap(ctx context.Context) error {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && s.cfg.AuthMode == "local" && s.cfg.AdminEmail != "" && s.cfg.AdminPassword != "" {
		if len(s.cfg.AdminPassword) < 8 {
			return errors.New("ADMIN_PASSWORD must be at least 8 characters")
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
		u := &store.User{Email: s.cfg.AdminEmail, Name: s.cfg.AdminName, PasswordHash: string(h), Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
		s.log.Info("admin user created from environment", "email", u.Email)
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	changed := false
	if s.cfg.RegistrationKey != "" && st.RegistrationKey != s.cfg.RegistrationKey {
		st.RegistrationKey = s.cfg.RegistrationKey
		changed = true
	}
	if st.RegistrationKey == "" {
		st.RegistrationKey = crypto.Token("pcreg_", 24)
		changed = true
		s.log.Info("registration key generated; find it under Settings → Apps")
	}
	if n > 0 && !st.SetupComplete {
		st.SetupComplete = true
		changed = true
	}
	if changed {
		if err := s.store.SaveSettings(ctx, st); err != nil {
			return err
		}
	}
	p, err := oidc.New(ctx, s.store, s.cfg.OIDCIssuer)
	if err != nil {
		return fmt.Errorf("oidc: %w", err)
	}
	s.oidc = p
	if s.cfg.DemoData {
		if err := s.seedDemo(ctx); err != nil {
			return err
		}
	}
	_ = s.store.PurgeExpired(ctx)
	s.ctx = ctx
	s.hub.Allow = s.appCanReadEvent
	go s.hub.Run(ctx)
	go s.janitor(ctx)
	go s.connectorScheduler(ctx)
	return nil
}

// janitor polls app health and purges old rows.
func (s *Server) janitor(ctx context.Context) {
	first := time.NewTimer(3 * time.Second)
	health := time.NewTicker(s.cfg.HealthInterval)
	purge := time.NewTicker(time.Hour)
	defer health.Stop()
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			s.checkAllApps(ctx)
		case <-health.C:
			s.checkAllApps(ctx)
		case <-purge.C:
			_ = s.store.PurgeExpired(ctx)
			if n, err := s.store.PurgeEvents(ctx, s.cfg.EventRetention); err == nil && n > 0 {
				s.log.Info("purged old events", "count", n)
			}
		}
	}
}

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5, "application/json", "text/html", "text/css", "application/javascript", "image/svg+xml"))

	r.Get("/pocket-ask.js", func(w http.ResponseWriter, r *http.Request) { coreSDK.ServeAskWidget(w, r, "core", s.cfg.BaseURL) })
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	r.Get("/metrics", s.handleMetrics)
	// Separate, narrowly scoped authentication; never inherits AUTH_MODE=none.
	r.Handle("/mcp", s.mcpHandler())
	s.mcpOAuthRoutes(r)
	s.askRoutes(r)

	// OpenID Connect (browser + relying parties).
	r.Get("/.well-known/openid-configuration", s.handleDiscovery)
	r.Get("/oidc/jwks", s.handleJWKS)
	r.Get("/oidc/authorize", s.handleAuthorize)
	r.Post("/oidc/authorize", s.handleAuthorize)
	r.Post("/oidc/token", s.handleToken)
	r.Get("/oidc/userinfo", s.handleUserinfo)
	r.Post("/oidc/userinfo", s.handleUserinfo)
	r.Get("/oidc/logout", s.handleEndSession)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Timeout(120 * time.Second))
		r.Use(s.sameOrigin)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/setup", s.handleSetup)
		r.Get("/auth/status", s.handleAuthStatus)
		r.Get("/auth/sso/start", s.handleSSOStart)
		r.Get("/auth/sso/callback", s.handleSSOCallback)
		r.Get("/catalog", s.handleCatalog)

		// Pairing claim is authenticated by the code itself.
		r.Post("/pair/claim", s.handlePairClaim)
		r.Get("/pair/{code}/qr.png", s.handlePairQR)
		r.Get("/pair/{code}/qr.svg", s.handlePairQR)

		// App registration: registration key or an existing app token.
		r.Post("/apps/register", s.handleRegisterApp)

		// Push deliveries from a connected app: the hook id and its secret header authenticate.
		r.Post("/connectors/hooks/{hookID}", s.handleConnectorHook)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Put("/auth/me", s.handleUpdateMe)
			r.Post("/auth/introspect", s.handleIntrospect)
			r.Post("/auth/subject", s.handleSubject)
			r.Post("/links/token", s.handleLinkToken)
			r.Get("/links/status", s.handleLinkStatus)
			r.Post("/services/credential", s.handleServiceCredential)
			r.Put("/links/modes", s.handleReportModes)
			r.Get("/me/links", s.handleMyLinks)
			r.Put("/me/links/{id}", s.handleUpdateMyLink)

			r.Get("/system", s.handleSystem)
			r.Get("/overview", s.handleOverview)
			r.Get("/me/services", s.handleMyServices)
			r.Get("/me/ai-connections", s.handleMCPConnections)
			r.Post("/me/ai-connections/token", s.handleMCPConnectionToken)
			r.Delete("/me/ai-connections/{id}", s.handleMCPDisconnect)

			r.Get("/users/{id}/credentials", s.handleListCredentials)
			r.Put("/users/{id}/credentials/{sid}", s.handlePutCredential)
			r.Delete("/users/{id}/credentials/{sid}", s.handleDeleteCredential)

			// The registry, the service list and the bus are for admins and apps;
			// a household member only sees their own devices, profile and credentials.
			r.Group(func(r chi.Router) {
				r.Use(s.requireAdminOrApp)
				r.Get("/apps", s.handleListApps)
				r.Get("/apps/{slug}", s.handleGetApp)
				r.Get("/resolve", s.handleResolve)
				r.Get("/people", s.handlePeople)

				r.Get("/services", s.handleListServices)
				r.Get("/services/{id}", s.handleGetService)

				r.Post("/events", s.handlePublish)
				r.Get("/events", s.handleListEvents)
				r.Get("/events/topics", s.handleTopics)
				r.Get("/events/sources", s.handleEventSources)
				r.Get("/events/stream", s.handleStream)
				r.Get("/events/{id}", s.handleGetEvent)
				r.Get("/webhooks", s.handleListWebhooks)
				r.Post("/webhooks", s.handleCreateWebhook)
				r.Put("/webhooks/{id}", s.handleUpdateWebhook)
				r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
				r.Post("/webhooks/{id}/test", s.handleTestWebhook)
				r.Get("/webhooks/{id}/deliveries", s.handleDeliveries)
			})

			r.Post("/pair", s.handlePairNew)
			r.Get("/pair/{code}", s.handlePairStatus)
			r.Get("/pair/profile", s.handleProfile)
			r.Get("/devices", s.handleListDevices)
			r.Delete("/devices/{id}", s.handleDeleteDevice)

			r.Get("/tokens", s.handleListTokens)
			r.Post("/tokens", s.handleCreateToken)
			r.Delete("/tokens/{id}", s.handleDeleteToken)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Put("/apps/{slug}", s.handleUpdateApp)
				r.Delete("/apps/{slug}", s.handleDeleteApp)
				r.Post("/apps/{slug}/rotate", s.handleRotateApp)
				r.Post("/apps/{slug}/check", s.handleCheckApp)

				r.Post("/services", s.handleCreateService)
				r.Put("/services/{id}", s.handleUpdateService)
				r.Delete("/services/{id}", s.handleDeleteService)
				r.Post("/services/{id}/test", s.handleTestService)

				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Put("/users/{id}", s.handleUpdateUser)
				r.Delete("/users/{id}", s.handleDeleteUser)

				r.Get("/oidc/clients", s.handleListOIDCClients)
				r.Post("/oidc/clients", s.handleCreateOIDCClient)
				r.Put("/oidc/clients/{id}", s.handleUpdateOIDCClient)
				r.Delete("/oidc/clients/{id}", s.handleDeleteOIDCClient)

				r.Get("/links", s.handleListLinks)
				r.Put("/links/{id}", s.handleUpdateLink)

				r.Get("/connectors", s.handleListConnectors)
				r.Get("/services/{id}/connector", s.handleGetConnector)
				r.Put("/services/{id}/connector", s.handlePutConnector)
				r.Post("/services/{id}/connector/test", s.handleTestConnector)
				r.Post("/services/{id}/connector/poll", s.handlePollConnector)
				r.Post("/services/{id}/connector/provision", s.handleProvisionConnector)
				r.Post("/services/{id}/connector/hooks/recreate", s.handleRecreateHooks)
				r.Get("/services/{id}/connector/accounts", s.handleConnectorAccounts)
				r.Put("/services/{id}/connector/accounts/{uid}", s.handleLinkAccount)
				r.Get("/services/{id}/connector/remote-accounts", s.handleRemoteAccounts)
				r.Get("/users/{id}/connectors", s.handleUserConnectors)

				r.Get("/settings", s.handleGetSettings)
				r.Put("/settings", s.handlePutSettings)
				r.Post("/settings/registration-key", s.handleRotateRegistrationKey)
				r.Get("/backup", s.handleBackup)
			})
		})
	})

	r.NotFound(s.handleSPA)
	return r
}

// ---------- plumbing ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/healthz" {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	return dec.Decode(v)
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

// publicBase is the URL Core is reached at for pairing links and OIDC.
func (s *Server) publicBase() string { return s.cfg.BaseURL }

// ---------- health, metrics, system ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version, "uptime_s": int(time.Since(s.started).Seconds())})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	counts := s.store.Counts(r.Context())
	apps, _ := s.store.ListApps(r.Context())
	up := 0
	for _, a := range apps {
		if a.Status == "ok" {
			up++
		}
	}
	ds, _ := s.store.DeliveryStats(r.Context())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP pocket_core_info Build info.\n# TYPE pocket_core_info gauge\npocket_core_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# TYPE pocket_core_uptime_seconds gauge\npocket_core_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# TYPE pocket_core_http_requests_total counter\npocket_core_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# TYPE pocket_core_http_errors_total counter\npocket_core_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# TYPE pocket_core_apps gauge\npocket_core_apps{state=\"registered\"} %d\npocket_core_apps{state=\"up\"} %d\n", len(apps), up)
	fmt.Fprintf(w, "# TYPE pocket_core_event_listeners gauge\npocket_core_event_listeners %d\n", s.hub.Live())
	for st, n := range ds {
		fmt.Fprintf(w, "pocket_core_webhook_deliveries{status=%q} %d\n", st, n)
	}
	for t, n := range counts {
		fmt.Fprintf(w, "pocket_core_rows{table=%q} %d\n", t, n)
	}
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	st, _ := s.store.GetSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"version": Version, "go": runtime.Version(), "os": runtime.GOOS + "/" + runtime.GOARCH,
		"uptime_s": int(time.Since(s.started).Seconds()), "started_at": s.started.UTC().Format(time.RFC3339),
		"base_url": s.cfg.BaseURL, "issuer": s.cfg.OIDCIssuer, "auth_mode": s.cfg.AuthMode, "data_dir": s.cfg.DataDir,
		"counts": s.store.Counts(r.Context()), "listeners": s.hub.Live(), "box_name": st.BoxName,
		"health_interval_s": int(s.cfg.HealthInterval.Seconds()), "event_retention_h": int(s.cfg.EventRetention.Hours()),
	})
}

// handleOverview is the home page in one call. Members get a short version:
// their box, their devices, how many services their profile holds.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(ctx)
	if !p.IsAdmin() && p.App == nil {
		st, _ := s.store.GetSettings(ctx)
		devices, _ := s.store.ListDevices(ctx, p.UserID())
		n := 0
		if list, err := s.store.ListServices(ctx); err == nil {
			for _, sv := range list {
				if sv.VisibleTo(p.User) {
					n++
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"box_name": st.BoxName, "version": Version, "member": true, "devices": devices, "services_enabled": n,
			"apps": []store.App{}, "recent_events": []store.Event{}, "deliveries": map[string]int{}, "listeners": 0, "apps_up": 0, "services_total": n})
		return
	}
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		s.fail(w, err, "list apps")
		return
	}
	services, _ := s.store.ListServices(ctx)
	devices, _ := s.store.ListDevices(ctx, s.scopeUser(r))
	recent, _ := s.store.ListEvents(ctx, store.EventFilter{Limit: 15})
	ds, _ := s.store.DeliveryStats(ctx)
	up := 0
	for _, a := range apps {
		if a.Status == "ok" {
			up++
		}
	}
	enabled := 0
	for _, sv := range services {
		if sv.Enabled {
			enabled++
		}
	}
	st, _ := s.store.GetSettings(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"box_name": st.BoxName, "version": Version,
		"apps": apps, "apps_up": up,
		"services_total": len(services), "services_enabled": enabled,
		"devices": devices, "recent_events": recent, "deliveries": ds, "listeners": s.hub.Live(),
	})
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	tmp := path.Join(s.cfg.DataDir, "backups", "download-"+time.Now().Format("20060102-150405")+".db")
	if err := s.store.Backup(r.Context(), tmp); err != nil {
		s.fail(w, err, "backup")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-core-`+time.Now().Format("20060102-150405")+`.db"`)
	http.ServeFile(w, r, tmp)
}

// handleSPA serves the embedded UI with history fallback.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/oidc/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Pocket Core "+Version+" — API only (web UI not embedded). See /api/v1 and /.well-known/openid-configuration")
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := s.webFS.Open(p); err == nil {
		defer f.Close()
		if st, err := f.Stat(); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.webFS, p)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	idx, err := s.webFS.Open("index.html")
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer idx.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, idx)
}
