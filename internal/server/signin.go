package server

// Sign-in methods: how people sign in to Core itself — an external OpenID provider (PocketID,
// Authentik, Keycloak…), passwords, or both. Admins set them in Settings → Single sign-on; until
// they do, SSO_* and PASSWORD_LOGIN from the environment apply. The client secret is sealed with
// the box key, never returned. Password sign-in can only be switched off while a provider answers,
// so nobody locks the box.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type signinSettings struct {
	Enabled       bool   `json:"enabled"`
	Issuer        string `json:"issuer"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"` // sealed when stored
	Name          string `json:"name"`
	AutoCreate    bool   `json:"auto_create"`
	PasswordLogin bool   `json:"password_login"`
	fromSettings  bool
}

const signinKey = "signin"

func (s *Server) signin() signinSettings {
	s.signinMu.RLock()
	c := s.signinCache
	s.signinMu.RUnlock()
	if c != nil {
		return *c
	}
	v := s.loadSignin(context.Background())
	s.signinMu.Lock()
	s.signinCache = &v
	s.signinMu.Unlock()
	return v
}

func (s *Server) loadSignin(ctx context.Context) signinSettings {
	if raw, ok := s.store.KVGet(ctx, signinKey); ok && raw != "" {
		var v signinSettings
		if json.Unmarshal([]byte(raw), &v) == nil {
			if v.ClientSecret != "" && s.store.Box != nil {
				if plain, err := s.store.Box.Open(v.ClientSecret); err == nil {
					v.ClientSecret = plain
				} else {
					v.ClientSecret = ""
				}
			}
			if v.Name == "" {
				v.Name = "single sign-on"
			}
			v.fromSettings = true
			return v
		}
	}
	c := s.cfg
	return signinSettings{Enabled: c.SSOIssuer != "" && c.SSOClientID != "", Issuer: c.SSOIssuer, ClientID: c.SSOClientID,
		ClientSecret: c.SSOClientSecret, Name: c.SSOName, AutoCreate: c.SSOAutoCreate, PasswordLogin: c.PasswordLogin}
}

// ssoProv is the upstream provider client for the issuer in use (made again when it changes).
func (s *Server) ssoProv() *ssoProvider {
	issuer := s.signin().Issuer
	s.ssoMu.Lock()
	defer s.ssoMu.Unlock()
	if s.sso == nil || s.sso.issuerURL != issuer {
		s.sso = &ssoProvider{issuerURL: issuer}
	}
	return s.sso
}

type signinView struct {
	Enabled       bool   `json:"enabled"`
	Issuer        string `json:"issuer"`
	ClientID      string `json:"client_id"`
	HasSecret     bool   `json:"has_secret"`
	Name          string `json:"name"`
	AutoCreate    bool   `json:"auto_create"`
	PasswordLogin bool   `json:"password_login"`
	CallbackURL   string `json:"callback_url"`
	Source        string `json:"source"` // settings | env | none
	AuthMode      string `json:"auth_mode"`
}

func (s *Server) signinView() signinView {
	c := s.signin()
	src := "none"
	switch {
	case c.fromSettings:
		src = "settings"
	case s.cfg.SSOIssuer != "" || !s.cfg.PasswordLogin:
		src = "env"
	}
	return signinView{Enabled: c.Enabled, Issuer: c.Issuer, ClientID: c.ClientID, HasSecret: c.ClientSecret != "", Name: c.Name,
		AutoCreate: c.AutoCreate, PasswordLogin: c.PasswordLogin, CallbackURL: s.ssoCallbackURL(), Source: src, AuthMode: s.cfg.AuthMode}
}

func (s *Server) handleGetSignin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.signinView())
}

// testIssuer fetches the provider's discovery document.
func (s *Server) testIssuer(ctx context.Context, issuer string) (*ssoDiscovery, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return (&ssoProvider{issuerURL: issuer}).discovery(ctx)
}

func cleanIssuer(raw string) (string, string) {
	v := strings.TrimRight(strings.TrimSpace(raw), "/")
	v = strings.TrimSuffix(v, "/.well-known/openid-configuration")
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return "", "the issuer must be an address like https://auth.example.com"
	}
	return v, ""
}

func (s *Server) handleTestSignin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	issuer, msg := cleanIssuer(in.Issuer)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	d, err := s.testIssuer(r.Context(), issuer)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Core cannot use "+issuer+": "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"issuer": d.Issuer, "authorization_endpoint": d.Auth})
}

func (s *Server) handlePutSignin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled       bool    `json:"enabled"`
		Issuer        string  `json:"issuer"`
		ClientID      string  `json:"client_id"`
		ClientSecret  *string `json:"client_secret"` // omitted or "" keeps the stored one
		Name          string  `json:"name"`
		AutoCreate    bool    `json:"auto_create"`
		PasswordLogin bool    `json:"password_login"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	cur := s.signin()
	v := signinSettings{Enabled: in.Enabled, ClientID: strings.TrimSpace(in.ClientID), Name: strings.TrimSpace(in.Name),
		AutoCreate: in.AutoCreate, PasswordLogin: in.PasswordLogin, ClientSecret: cur.ClientSecret}
	if in.ClientSecret != nil && *in.ClientSecret != "" {
		v.ClientSecret = *in.ClientSecret
	}
	if v.Name == "" {
		v.Name = "single sign-on"
	}
	if strings.TrimSpace(in.Issuer) != "" {
		var msg string
		if v.Issuer, msg = cleanIssuer(in.Issuer); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		if base := strings.TrimRight(s.cfg.BaseURL, "/"); base != "" && strings.EqualFold(v.Issuer, base) {
			writeErr(w, http.StatusBadRequest, "that is Core's own address — point it at the provider people sign in with (PocketID, Authentik…)")
			return
		}
	}
	if v.Enabled {
		if v.Issuer == "" || v.ClientID == "" {
			writeErr(w, http.StatusBadRequest, "the provider needs its issuer address and the client ID you created there")
			return
		}
		if _, err := s.testIssuer(r.Context(), v.Issuer); err != nil {
			writeErr(w, http.StatusBadRequest, "Core cannot reach "+v.Issuer+": "+err.Error())
			return
		}
	}
	if !v.PasswordLogin && !v.Enabled {
		writeErr(w, http.StatusBadRequest, "switch on an identity provider before switching passwords off — otherwise nobody could sign in")
		return
	}
	stored := v
	if stored.ClientSecret != "" {
		if s.store.Box == nil {
			writeErr(w, http.StatusInternalServerError, "the box key is missing")
			return
		}
		stored.ClientSecret = s.store.Box.Seal(stored.ClientSecret)
	}
	b, _ := json.Marshal(stored)
	if err := s.store.KVPut(r.Context(), signinKey, string(b)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	s.signinMu.Lock()
	s.signinCache = nil
	s.signinMu.Unlock()
	writeJSON(w, http.StatusOK, s.signinView())
}

// handleResetSignin forgets what was set here, so the environment (SSO_*, PASSWORD_LOGIN) applies again.
func (s *Server) handleResetSignin(w http.ResponseWriter, r *http.Request) {
	if err := s.store.KVPut(r.Context(), signinKey, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not reset")
		return
	}
	s.signinMu.Lock()
	s.signinCache = nil
	s.signinMu.Unlock()
	writeJSON(w, http.StatusOK, s.signinView())
}
