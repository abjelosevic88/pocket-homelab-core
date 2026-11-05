package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skip2/go-qrcode"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/remote"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// remoteTokenAccess resolves a pcu_rly_ token for the app asking about it.
func (s *Server) remoteTokenAccess(ctx context.Context, tok, slug string) (*remoteAccess, error) {
	keyID, app, _, err := s.store.RemoteToken(ctx, tok)
	if err != nil || app != slug {
		return nil, store.ErrNotFound
	}
	k, err := s.store.GetRemoteKey(ctx, keyID)
	if err != nil {
		return nil, err
	}
	return s.checkRemote(ctx, k, slug)
}

// introspectRemoteToken answers an app about a token Core injected into a
// request it forwarded from a remote device. Only the app it was made for
// gets "active", and only while every switch still allows it.
func (s *Server) introspectRemoteToken(w http.ResponseWriter, r *http.Request, caller *Principal, tok string) {
	if caller.App == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	acc, err := s.remoteTokenAccess(r.Context(), tok, caller.App.Slug)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	u := acc.User
	writeJSON(w, http.StatusOK, map[string]any{"active": true, "kind": "device", "scope": "", "iss": s.oidc.Issuer,
		// Short, so an app's introspection cache notices a switch-off quickly.
		"expires_at": store.Stamp(time.Now().Add(time.Minute)),
		"user":       map[string]any{"id": u.ID, "sub": u.Subject, "auth_revision": u.AuthRevision, "email": u.Email, "name": u.Name, "role": u.Role},
		"device":     map[string]any{"id": acc.Device.ID, "name": acc.Device.Name + " (remote)", "platform": "remote"}})
}

type remoteAppView struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Address       string `json:"address"`
	Exposed       bool   `json:"exposed"`
	DeviceDefault bool   `json:"device_default"`
	Passkey       bool   `json:"passkey"`
	Sensitive     bool   `json:"sensitive"`
}

func (s *Server) remoteAppViews(ctx context.Context, rs remoteSettings, onlyExposed bool) ([]remoteAppView, error) {
	apps, err := s.remoteApps(ctx)
	if err != nil {
		return nil, err
	}
	out := []remoteAppView{}
	for _, a := range apps {
		if onlyExposed && !a.Exposed {
			continue
		}
		addr := ""
		if rs.Pattern != "" {
			addr = "https://" + rs.host(a.Slug)
		}
		out = append(out, remoteAppView{Slug: a.Slug, Name: a.Name, Address: addr, Exposed: a.Exposed,
			DeviceDefault: a.DeviceDefault, Passkey: a.Passkey, Sensitive: sensitiveApps[a.Slug]})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// GET /api/v1/remote (admin): settings without secrets, status, apps, devices.
func (s *Server) handleGetRemote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rs := s.remoteSettings(ctx)
	apps, err := s.remoteAppViews(ctx, rs, false)
	if err != nil {
		s.fail(w, err, "list remote apps")
		return
	}
	devices, err := s.store.ListRemoteDevices(ctx, 0)
	if err != nil {
		s.fail(w, err, "list remote devices")
		return
	}
	portal := ""
	if rs.Pattern != "" {
		portal = "https://" + rs.host(portalSlug)
	}
	cfSynced := rs.Provider == "cloudflare" && rs.CFDeployed == remote.WorkerHash()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": rs.Enabled, "provider": rs.Provider, "pattern": rs.Pattern, "relay_url": rs.RelayURL,
		"has_secret": rs.Secret != "", "cf_account": rs.CFAccount, "cf_worker": rs.CFWorker, "has_cf_token": rs.CFToken != "",
		"cf_deployed": rs.CFDeployed != "", "cf_current": cfSynced, "cf_domains": rs.CFDomains,
		"portal": portal, "box_url": rs.boxURL(), "status": s.remote.snapshot(), "apps": apps, "devices": devices,
		"cf_syncing": s.remote.cfPending.Load(), "cf_ready": s.remote.readyHosts(), "cf_error": derefStr(s.remote.cfErr.Load()),
	})
}

// PUT /api/v1/remote (admin).
func (s *Server) handlePutRemote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled   *bool   `json:"enabled"`
		Provider  *string `json:"provider"`
		Pattern   *string `json:"pattern"`
		RelayURL  *string `json:"relay_url"`
		CFAccount *string `json:"cf_account"`
		CFToken   *string `json:"cf_token"`
		CFWorker  *string `json:"cf_worker"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := r.Context()
	rs := s.remoteSettings(ctx)
	if in.Provider != nil {
		if *in.Provider != "cloudflare" && *in.Provider != "custom" {
			writeErr(w, http.StatusBadRequest, "provider must be cloudflare or custom")
			return
		}
		rs.Provider = *in.Provider
	}
	if in.Pattern != nil {
		p := strings.ToLower(strings.TrimSpace(*in.Pattern))
		if p != "" {
			if err := validPattern(p); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		rs.Pattern = p
	}
	if in.RelayURL != nil {
		rs.RelayURL = strings.TrimSpace(*in.RelayURL)
	}
	if in.CFAccount != nil {
		rs.CFAccount = strings.TrimSpace(*in.CFAccount)
	}
	if in.CFToken != nil && strings.TrimSpace(*in.CFToken) != "" {
		rs.CFToken = s.store.Box.Seal(strings.TrimSpace(*in.CFToken))
	}
	if in.CFWorker != nil && slugRe.MatchString(strings.TrimSpace(*in.CFWorker)) {
		rs.CFWorker = strings.TrimSpace(*in.CFWorker)
	}
	if rs.Secret == "" {
		rs.Secret = s.store.Box.Seal(crypto.Token("prs_", 32))
	}
	if in.Enabled != nil {
		if *in.Enabled && rs.Pattern == "" {
			writeErr(w, http.StatusBadRequest, "set the address pattern first")
			return
		}
		rs.Enabled = *in.Enabled
	}
	if _, err := s.boxKey(ctx); err != nil {
		s.fail(w, err, "box key")
		return
	}
	rs.BoxKey = s.remoteSettings(ctx).BoxKey
	if err := s.saveRemoteSettings(ctx, rs); err != nil {
		s.fail(w, err, "save remote settings")
		return
	}
	if !rs.Enabled {
		s.remote.closeWhere(func(*remoteChan) bool { return true })
	}
	s.handleGetRemote(w, r)
}

// GET /api/v1/remote/secret (admin): the relay secret, for a self-hosted relay.
func (s *Server) handleRemoteSecret(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"secret": s.open(s.remoteSettings(r.Context()).Secret)})
}

// POST /api/v1/remote/secret (admin): a new relay secret.
func (s *Server) handleRotateRemoteSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rs := s.remoteSettings(ctx)
	sec := crypto.Token("prs_", 32)
	rs.Secret = s.store.Box.Seal(sec)
	rs.CFDeployed = "" // the Worker holds the old one until it is uploaded again
	if err := s.saveRemoteSettings(ctx, rs); err != nil {
		s.fail(w, err, "save remote settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": sec})
}

// POST /api/v1/remote/reset (admin): new box key; every device pairs again.
func (s *Server) handleResetRemote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.remote.closeWhere(func(*remoteChan) bool { return true })
	if err := s.store.ResetRemote(ctx); err != nil {
		s.fail(w, err, "reset remote")
		return
	}
	rs := s.remoteSettings(ctx)
	rs.BoxKey = ""
	if err := s.saveRemoteSettings(ctx, rs); err != nil {
		s.fail(w, err, "save remote settings")
		return
	}
	if _, err := s.boxKey(ctx); err != nil {
		s.fail(w, err, "box key")
		return
	}
	s.handleGetRemote(w, r)
}

func (s *Server) cloudflare(rs remoteSettings) *remote.Cloudflare {
	return &remote.Cloudflare{Account: rs.CFAccount, Token: s.open(rs.CFToken), Worker: rs.CFWorker}
}

// remoteHosts is every address the relay must answer: the launcher and each
// reachable app.
func (s *Server) remoteHosts(ctx context.Context, rs remoteSettings) ([]string, error) {
	apps, err := s.remoteApps(ctx)
	if err != nil {
		return nil, err
	}
	hosts := []string{rs.host(portalSlug)}
	for _, a := range apps {
		if a.Exposed {
			hosts = append(hosts, rs.host(a.Slug))
		}
	}
	return hosts, nil
}

// deployCloudflare uploads the Worker (when it changed) and syncs domains.
func (s *Server) deployCloudflare(ctx context.Context, force bool) error {
	s.remote.cfMu.Lock()
	defer s.remote.cfMu.Unlock()
	rs := s.remoteSettings(ctx)
	if rs.Provider != "cloudflare" {
		return errors.New("the relay is not on Cloudflare")
	}
	if rs.Pattern == "" {
		return errors.New("set the address pattern first")
	}
	cf := s.cloudflare(rs)
	if err := cf.Verify(ctx); err != nil {
		return err
	}
	hash := remote.WorkerHash()
	if force || rs.CFDeployed != hash {
		portal := "https://" + rs.host(portalSlug)
		err := cf.Deploy(ctx, s.open(rs.Secret), portal, !rs.CFMigrated)
		if remote.IsMigrationError(err) {
			err = cf.Deploy(ctx, s.open(rs.Secret), portal, rs.CFMigrated)
			if err == nil {
				rs.CFMigrated = !rs.CFMigrated
			}
		}
		if err != nil {
			return err
		}
		rs.CFMigrated, rs.CFDeployed = true, hash
	}
	hosts, err := s.remoteHosts(ctx, rs)
	if err != nil {
		return err
	}
	syncErr := cf.SyncDomains(ctx, hosts)
	if syncErr == nil {
		rs.CFDomains = hosts
	}
	b := s.remoteSettings(ctx) // keep switches changed meanwhile
	b.CFDeployed, b.CFMigrated, b.CFDomains = rs.CFDeployed, rs.CFMigrated, rs.CFDomains
	if err := s.saveRemoteSettings(ctx, b); err != nil {
		return err
	}
	return syncErr
}

// POST /api/v1/remote/deploy (admin).
func (s *Server) handleDeployRemote(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.deployCloudflare(ctx, true); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.handleGetRemote(w, r)
}

// PUT /api/v1/remote/apps/{slug} (admin).
func (s *Server) handlePutRemoteApp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "slug")
	app, err := s.remoteApp(ctx, slug)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such app")
		return
	}
	var in struct {
		Exposed       *bool `json:"exposed"`
		DeviceDefault *bool `json:"device_default"`
		Passkey       *bool `json:"passkey"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ra := store.RemoteApp{App: slug, Exposed: app.Exposed, DeviceDefault: app.DeviceDefault, Passkey: app.Passkey}
	if in.Exposed != nil {
		ra.Exposed = *in.Exposed
	}
	if in.DeviceDefault != nil {
		ra.DeviceDefault = *in.DeviceDefault
	}
	if in.Passkey != nil {
		ra.Passkey = *in.Passkey
	}
	if err := s.store.PutRemoteApp(ctx, ra); err != nil {
		s.fail(w, err, "save remote app")
		return
	}
	s.remote.recheck(ctx)
	rs := s.remoteSettings(ctx)
	if in.Exposed != nil && rs.Provider == "cloudflare" && rs.CFDeployed != "" {
		// The address appears or disappears at Cloudflare in the background.
		s.remote.syncAddresses()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- everyone: own devices ----

// GET /api/v1/me/remote: whether remote access is on, the apps, my devices.
func (s *Server) handleMyRemote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if u == nil {
		writeErr(w, http.StatusForbidden, "people only")
		return
	}
	rs := s.remoteSettings(ctx)
	apps, err := s.remoteAppViews(ctx, rs, true)
	if err != nil {
		s.fail(w, err, "list remote apps")
		return
	}
	devices, err := s.store.ListRemoteDevices(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "list remote devices")
		return
	}
	portal := ""
	if rs.Pattern != "" {
		portal = "https://" + rs.host(portalSlug)
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": rs.Enabled, "connected": s.remote.snapshot().Connected,
		"portal": portal, "apps": apps, "devices": devices})
}

// POST /api/v1/me/remote/pair: a one-time QR code that pairs a new device.
func (s *Server) handleRemotePair(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if u == nil {
		writeErr(w, http.StatusForbidden, "people only")
		return
	}
	rs := s.remoteSettings(ctx)
	if !rs.Enabled || rs.Pattern == "" {
		writeErr(w, http.StatusConflict, "remote access is switched off")
		return
	}
	box, err := s.boxKey(ctx)
	if err != nil {
		s.fail(w, err, "box key")
		return
	}
	code, err := s.store.CreateRemoteCode(ctx, store.RemoteCode{UserID: u.ID, App: portalSlug}, remoteCodeTTL)
	if err != nil {
		s.fail(w, err, "pairing code")
		return
	}
	link := "https://" + rs.host(portalSlug) + "/#pair=" + code + "&box=" + base64.RawURLEncoding.EncodeToString(box.Public)
	q, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		s.fail(w, err, "qr")
		return
	}
	img := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(qrSVG(q)))
	writeJSON(w, http.StatusOK, map[string]any{"url": link, "qr_svg": img, "expires_at": store.Stamp(time.Now().Add(remoteCodeTTL))})
}

// myRemoteDevice loads a device the caller owns (or any, for admins).
func (s *Server) myRemoteDevice(w http.ResponseWriter, r *http.Request) *store.RemoteDevice {
	u := userFrom(r.Context())
	d, err := s.store.GetRemoteDevice(r.Context(), idParam(r, "id"))
	if err != nil || u == nil || (d.UserID != u.ID && !u.IsAdmin()) {
		writeErr(w, http.StatusNotFound, "no such device")
		return nil
	}
	return d
}

// PUT /api/v1/me/remote/devices/{id}: rename, switch apps on or off.
func (s *Server) handleUpdateRemoteDevice(w http.ResponseWriter, r *http.Request) {
	d := s.myRemoteDevice(w, r)
	if d == nil {
		return
	}
	var in struct {
		Name *string         `json:"name"`
		Apps map[string]bool `json:"apps"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := r.Context()
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 60 {
			writeErr(w, http.StatusBadRequest, "name must be 1-60 characters")
			return
		}
		if err := s.store.RenameRemoteDevice(ctx, d.ID, n); err != nil {
			s.fail(w, err, "rename device")
			return
		}
	}
	for app, on := range in.Apps {
		if !slugRe.MatchString(app) || app == portalSlug {
			continue
		}
		if err := s.store.SetRemoteDeviceApp(ctx, d.ID, app, on); err != nil {
			s.fail(w, err, "device app")
			return
		}
	}
	s.remote.recheck(ctx)
	d, _ = s.store.GetRemoteDevice(ctx, d.ID)
	writeJSON(w, http.StatusOK, d)
}

// DELETE /api/v1/me/remote/devices/{id}: revoke the whole device.
func (s *Server) handleDeleteRemoteDevice(w http.ResponseWriter, r *http.Request) {
	d := s.myRemoteDevice(w, r)
	if d == nil {
		return
	}
	s.remote.closeWhere(func(c *remoteChan) bool { return c.deviceID() == d.ID })
	if err := s.store.DeleteRemoteDevice(r.Context(), d.ID); err != nil {
		s.fail(w, err, "delete device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/me/remote/devices/{id}/keys/{kid}: forget one app address.
func (s *Server) handleDeleteRemoteKey(w http.ResponseWriter, r *http.Request) {
	d := s.myRemoteDevice(w, r)
	if d == nil {
		return
	}
	kid := idParam(r, "kid")
	s.remote.closeWhere(func(c *remoteChan) bool { k := c.key(); return k != nil && k.ID == kid })
	if err := s.store.DeleteRemoteKey(r.Context(), d.ID, kid); err != nil {
		s.fail(w, err, "delete key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
