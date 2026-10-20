package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Manifest is what a Pocket app sends when it registers. See docs/manifest.md.
type Manifest struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Version     string   `json:"version,omitempty"`
	URL         string   `json:"url"`
	PublicURL   string   `json:"public_url,omitempty"`
	HealthPath  string   `json:"health_path,omitempty"`
	EnvPrefix   string   `json:"env_prefix,omitempty"`
	Publishes   []string `json:"publishes,omitempty"`
	Subscribes  []struct {
		Topics string `json:"topics"`
		URL    string `json:"url"`
		Secret string `json:"secret,omitempty"`
	} `json:"subscribes,omitempty"`
	OIDC *struct {
		RedirectURIs []string `json:"redirect_uris"`
		Public       bool     `json:"public,omitempty"`
	} `json:"oidc,omitempty"`
	Offers []Offer `json:"offers,omitempty"` // what other apps may do here for a person
	// Services are third-party services the app reaches for a person through Core.
	Services []ServiceUse `json:"services,omitempty"`
	Uses     []Use        `json:"uses,omitempty"` // what this app does in other apps for a person
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// handleRegisterApp creates or updates an app from its manifest.
//
// Authenticate with the registration key (first time) or the app's own
// pca_ token (every later start). The response carries the app token only
// when it was just minted; apps must persist it.
func (s *Server) handleRegisterApp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := bearer(r)
	if tok == "" {
		writeErr(w, http.StatusUnauthorized, "registration key or app token required")
		return
	}
	var m Manifest
	if err := decode(r, &m); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	m.Slug = strings.ToLower(strings.TrimSpace(m.Slug))
	if !slugRe.MatchString(m.Slug) {
		writeErr(w, http.StatusBadRequest, "slug must be lowercase letters, digits and dashes")
		return
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.URL) == "" {
		writeErr(w, http.StatusBadRequest, "name and url are required")
		return
	}
	if _, err := url.ParseRequestURI(m.URL); err != nil {
		writeErr(w, http.StatusBadRequest, "url must be absolute")
		return
	}
	m.URL = strings.TrimRight(m.URL, "/")
	m.PublicURL = strings.TrimRight(m.PublicURL, "/")
	if m.EnvPrefix == "" {
		if e, ok := catalog.Get(m.Slug); ok {
			m.EnvPrefix = e.Prefix
		} else {
			m.EnvPrefix = catalog.Custom(m.Slug, m.Name, "", true).Prefix
		}
	}
	m.EnvPrefix = strings.ToUpper(m.EnvPrefix)
	m.Offers = cleanOffers(m.Offers)
	wants := append(cleanUses(m.Slug, m.Uses), cleanServiceUses(m.Services)...)
	raw, _ := json.Marshal(m)

	st, _ := s.store.GetSettings(ctx)
	var existing *store.App
	var byToken *store.App
	if strings.HasPrefix(tok, "pca_") {
		a, err := s.store.GetAppByToken(ctx, tok)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "app token rejected")
			return
		}
		if a.Slug != m.Slug {
			writeErr(w, http.StatusForbidden, "token belongs to another app")
			return
		}
		byToken = a
		existing = a
	} else if ip := clientIP(r); s.regLimit.blocked(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many bad registration keys; try again in 15 minutes")
		return
	} else if tok != st.RegistrationKey || st.RegistrationKey == "" {
		s.regLimit.fail(ip)
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "registration key rejected")
		return
	} else if a, err := s.store.GetAppBySlug(ctx, m.Slug); err == nil {
		existing = a
	}

	var plain string
	app := existing
	if app == nil {
		app = &store.App{}
	}
	app.Slug, app.Name, app.Description, app.Icon = m.Slug, m.Name, m.Description, m.Icon
	app.Version, app.URL, app.PublicURL, app.HealthPath, app.EnvPrefix = m.Version, m.URL, m.PublicURL, m.HealthPath, m.EnvPrefix
	app.Manifest = raw
	var err error
	switch {
	case existing == nil:
		plain, err = s.store.CreateApp(ctx, app)
	case byToken == nil:
		// Re-registered with the registration key: the app lost its token, mint a new one.
		if err = s.store.UpdateApp(ctx, app); err == nil {
			plain, err = s.store.RotateAppToken(ctx, app.ID)
		}
	default:
		err = s.store.UpdateApp(ctx, app)
	}
	if err != nil {
		s.fail(w, err, "register app")
		return
	}
	app, _ = s.store.GetApp(ctx, app.ID)

	// Mirror as a service so paired devices receive it.
	entry := catalog.Resolve(app.Slug, app.Name, app.EnvPrefix, true)
	sv, serr := s.store.GetServiceByApp(ctx, app.ID)
	if serr != nil {
		sv = &store.Service{Type: entry.Type, Enabled: true, Fields: map[string]string{}}
		if other, err := s.store.GetServiceByType(ctx, entry.Type); err == nil && other.AppID == 0 {
			sv = other // an admin pre-created it by hand; adopt it
		}
	}
	sv.Name, sv.URL, sv.PublicURL, sv.AppID = app.Name, app.URL, app.PublicURL, app.ID
	if err := s.store.SaveService(ctx, sv, entry.IsSecret); err != nil {
		s.log.Warn("mirror service", "app", app.Slug, "err", err)
	}

	// Webhook subscriptions from the manifest (idempotent on app+url).
	for _, sub := range m.Subscribes {
		u := sub.URL
		if strings.HasPrefix(u, "/") {
			u = app.URL + u
		}
		wh := &store.Webhook{AppID: app.ID, Name: app.Name, URL: u, Topics: sub.Topics, Secret: sub.Secret, Enabled: true}
		if err := s.store.SaveWebhook(ctx, wh); err != nil {
			s.log.Warn("manifest webhook", "app", app.Slug, "url", u, "err", err)
		}
	}

	// App links from the manifest's "uses": new ones start switched on.
	if err := s.store.SyncAppLinks(ctx, app.ID, wants); err != nil {
		s.log.Warn("manifest links", "app", app.Slug, "err", err)
	}

	// OIDC client from the manifest; the secret is returned only when minted.
	resp := map[string]any{"app": app, "core": map[string]any{
		"base_url": s.publicBase(), "events_url": s.publicBase() + "/api/v1/events",
		"stream_url": s.publicBase() + "/api/v1/events/stream", "introspect_url": s.publicBase() + "/api/v1/auth/introspect",
		"issuer": s.cfg.OIDCIssuer,
	}}
	if plain != "" {
		resp["token"] = plain
	}
	if m.OIDC != nil && len(m.OIDC.RedirectURIs) > 0 {
		cl, err := s.store.GetOIDCClient(ctx, "pocket-"+app.Slug)
		newSecret := false
		if err != nil {
			cl = &store.OIDCClient{ClientID: "pocket-" + app.Slug}
			newSecret = !m.OIDC.Public
		}
		cl.Name, cl.RedirectURIs, cl.AppID = app.Name, m.OIDC.RedirectURIs, app.ID
		secret, err := s.store.SaveOIDCClient(ctx, cl, newSecret)
		if err != nil {
			s.log.Warn("manifest oidc client", "app", app.Slug, "err", err)
		} else {
			oc := map[string]any{"client_id": cl.ClientID, "issuer": s.cfg.OIDCIssuer, "discovery": s.cfg.OIDCIssuer + "/.well-known/openid-configuration"}
			if secret != "" {
				oc["client_secret"] = secret
			}
			resp["oidc"] = oc
		}
	}
	go s.checkApp(context.WithoutCancel(ctx), app)
	s.log.Info("app registered", "slug", app.Slug, "url", app.URL, "new", existing == nil)
	_ = s.hub.Publish(ctx, &store.Event{Topic: "core.app.registered", Source: "core", Subject: "app:" + app.Slug, Payload: mustJSON(map[string]any{"slug": app.Slug, "name": app.Name, "version": app.Version, "new": existing == nil})})
	status := http.StatusOK
	if existing == nil {
		status = http.StatusCreated
	}
	writeJSON(w, status, resp)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApps(r.Context())
	if err != nil {
		s.fail(w, err, "list apps")
		return
	}
	// open_url: Open on the Apps page lands in the app already signed in.
	type appOut struct {
		store.App
		OpenURL string `json:"open_url"`
	}
	starts := s.signInStarts(r.Context())
	out := make([]appOut, 0, len(apps))
	for i := range apps {
		out = append(out, appOut{App: apps[i], OpenURL: signedInURL(starts, apps[i].ID, apps[i].EffectivePublicURL())})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAppBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	hooks, _ := s.store.ListWebhooks(r.Context(), a.ID)
	var client *store.OIDCClient
	if c, err := s.store.GetOIDCClient(r.Context(), "pocket-"+a.Slug); err == nil {
		client = c
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": a, "webhooks": hooks, "oidc_client": client})
}

// handleResolve answers "where is X?" for sibling apps: ?slug=habits
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(r.URL.Query().Get("slug"))
	if slug == "" {
		apps, err := s.store.ListApps(r.Context())
		if err != nil {
			s.fail(w, err, "list apps")
			return
		}
		out := map[string]any{}
		for _, a := range apps {
			out[a.Slug] = map[string]any{"url": a.URL, "public_url": a.EffectivePublicURL(), "status": a.Status, "version": a.Version}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	a, err := s.store.GetAppBySlug(r.Context(), slug)
	if err != nil {
		s.fail(w, err, "resolve")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": a.Slug, "name": a.Name, "url": a.URL, "public_url": a.EffectivePublicURL(), "status": a.Status, "version": a.Version})
}

func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAppBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Icon        *string `json:"icon"`
		URL         *string `json:"url"`
		PublicURL   *string `json:"public_url"`
		HealthPath  *string `json:"health_path"`
		EnvPrefix   *string `json:"env_prefix"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&a.Name, in.Name)
	set(&a.Description, in.Description)
	set(&a.Icon, in.Icon)
	set(&a.URL, in.URL)
	set(&a.PublicURL, in.PublicURL)
	set(&a.HealthPath, in.HealthPath)
	set(&a.EnvPrefix, in.EnvPrefix)
	a.URL, a.PublicURL, a.EnvPrefix = strings.TrimRight(a.URL, "/"), strings.TrimRight(a.PublicURL, "/"), strings.ToUpper(a.EnvPrefix)
	if err := s.store.UpdateApp(r.Context(), a); err != nil {
		s.fail(w, err, "update app")
		return
	}
	if sv, err := s.store.GetServiceByApp(r.Context(), a.ID); err == nil {
		sv.Name, sv.URL, sv.PublicURL = a.Name, a.URL, a.PublicURL
		entry := catalog.Resolve(a.Slug, a.Name, a.EnvPrefix, true)
		_ = s.store.SaveService(r.Context(), sv, entry.IsSecret)
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAppBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	if err := s.store.DeleteApp(r.Context(), a.ID); err != nil {
		s.fail(w, err, "delete app")
		return
	}
	_ = s.hub.Publish(r.Context(), &store.Event{Topic: "core.app.removed", Source: "core", Subject: "app:" + a.Slug, Payload: mustJSON(map[string]any{"slug": a.Slug})})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotateApp(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAppBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	plain, err := s.store.RotateAppToken(r.Context(), a.ID)
	if err != nil {
		s.fail(w, err, "rotate token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": a.Slug, "token": plain})
}

func (s *Server) handleCheckApp(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAppBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		s.fail(w, err, "get app")
		return
	}
	s.checkApp(r.Context(), a)
	a, _ = s.store.GetApp(r.Context(), a.ID)
	writeJSON(w, http.StatusOK, a)
}

// ---- health polling ----

func (s *Server) checkAllApps(ctx context.Context) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return
	}
	for i := range apps {
		s.checkApp(ctx, &apps[i])
	}
}

func (s *Server) checkApp(ctx context.Context, a *store.App) {
	status, version, msg := "ok", "", ""
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL+a.HealthPath, nil)
	if err != nil {
		status, msg = "down", err.Error()
	} else {
		res, err := s.http.Do(req)
		if err != nil {
			status, msg = "down", trimErr(err)
		} else {
			var body struct {
				Status  string `json:"status"`
				Version string `json:"version"`
			}
			_ = json.NewDecoder(res.Body).Decode(&body)
			res.Body.Close()
			if res.StatusCode >= 400 {
				status, msg = "down", "HTTP "+res.Status
			}
			version = body.Version
		}
	}
	if a.Status != status && a.Status != "unknown" {
		_ = s.hub.Publish(context.WithoutCancel(ctx), &store.Event{Topic: "core.app." + status, Source: "core", Subject: "app:" + a.Slug, Payload: mustJSON(map[string]any{"slug": a.Slug, "name": a.Name, "error": msg})})
	}
	_ = s.store.SetAppHealth(context.WithoutCancel(ctx), a.ID, status, version, msg)
}

func trimErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}
