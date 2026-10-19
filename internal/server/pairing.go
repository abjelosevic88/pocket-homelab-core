package server

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// pairURL is what the QR code carries. The Pocket Homelab client opens it,
// claims the code and imports the profile; a browser gets a help page.
func (s *Server) pairURL(code string) string {
	return "pockethomelab://pair?core=" + url.QueryEscape(s.publicBase()) + "&code=" + code
}

// handlePairNew mints a pairing code for the calling user.
func (s *Server) handlePairNew(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.User == nil {
		writeErr(w, http.StatusForbidden, "users only")
		return
	}
	var in struct {
		UserID int64 `json:"user_id"`
	}
	_ = decode(r, &in)
	uid := p.User.ID
	if in.UserID > 0 && p.IsAdmin() {
		uid = in.UserID
	}
	pr, err := s.store.CreatePairing(r.Context(), uid, s.cfg.PairTTL)
	if err != nil {
		s.fail(w, err, "create pairing")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"code": pr.Code, "expires_at": pr.ExpiresAt, "url": s.pairURL(pr.Code),
		"qr_png":   s.publicBase() + "/api/v1/pair/" + pr.Code + "/qr.png",
		"qr_svg":   s.publicBase() + "/api/v1/pair/" + pr.Code + "/qr.svg",
		"core_url": s.publicBase(), "ttl_s": int(s.cfg.PairTTL.Seconds()),
	})
}

// handlePairStatus lets the UI poll whether the code was claimed.
func (s *Server) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	pr, err := s.store.PeekPairing(r.Context(), strings.ToUpper(chi.URLParam(r, "code")))
	if err != nil {
		s.fail(w, err, "pairing")
		return
	}
	p := principalFrom(r.Context())
	if !p.IsAdmin() && pr.UserID != p.UserID() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	out := map[string]any{"code": pr.Code, "expires_at": pr.ExpiresAt, "claimed": pr.ClaimedAt != ""}
	if pr.DeviceID > 0 {
		if d, err := s.store.GetDevice(r.Context(), pr.DeviceID); err == nil {
			out["device"] = d
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePairQR renders the code; unauthenticated because the code is random
// and short-lived and the image reveals nothing beyond the URL of Core.
func (s *Server) handlePairQR(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(chi.URLParam(r, "code"))
	if _, err := s.store.PeekPairing(r.Context(), code); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	q, err := qrcode.New(s.pairURL(code), qrcode.Medium)
	if err != nil {
		s.fail(w, err, "qr")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasSuffix(r.URL.Path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(qrSVG(q)))
		return
	}
	q.DisableBorder = false
	png, err := q.PNG(320)
	if err != nil {
		s.fail(w, err, "qr")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func qrSVG(q *qrcode.QRCode) string {
	bm := q.Bitmap()
	n := len(bm)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="100%%" height="100%%" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := range bm {
		for x := range bm[y] {
			if bm[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// handlePairClaim exchanges a code for a device token and the profile.
func (s *Server) handlePairClaim(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(in.Code), "-", ""))
	if in.Code == "" {
		writeErr(w, http.StatusBadRequest, "code required")
		return
	}
	ip := clientIP(r)
	if s.pairLimit.blocked(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many bad codes; try again in 15 minutes")
		return
	}
	pr, err := s.store.PeekPairing(r.Context(), in.Code)
	if err != nil {
		s.pairLimit.fail(ip)
		time.Sleep(500 * time.Millisecond)
		writeErr(w, http.StatusNotFound, "code unknown, expired or already used")
		return
	}
	s.pairLimit.clear(ip)
	if in.Name == "" {
		in.Name = "Device"
	}
	d := &store.Device{UserID: pr.UserID, Name: strings.TrimSpace(in.Name), Platform: strings.ToLower(strings.TrimSpace(in.Platform))}
	plain, err := s.store.CreateDevice(r.Context(), d)
	if err != nil {
		s.fail(w, err, "create device")
		return
	}
	if _, err := s.store.ClaimPairing(r.Context(), in.Code, d.ID); err != nil {
		_ = s.store.DeleteDevice(r.Context(), d.ID)
		writeErr(w, http.StatusNotFound, "code unknown, expired or already used")
		return
	}
	u, _ := s.store.GetUser(r.Context(), pr.UserID)
	prof, err := s.buildProfile(r, u, d, plain)
	if err != nil {
		s.fail(w, err, "profile")
		return
	}
	s.log.Info("device paired", "device", d.Name, "platform", d.Platform, "user", u.Email)
	_ = s.hub.Publish(r.Context(), &store.Event{Topic: "core.device.paired", Source: "core", Subject: fmt.Sprintf("device:%d", d.ID), Payload: mustJSON(map[string]any{"name": d.Name, "platform": d.Platform, "user": u.Email})})
	writeJSON(w, http.StatusCreated, prof)
}

// handleProfile re-issues the profile for an already paired device (or a
// user, for a preview); ?format=env returns the client's env file.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.User == nil {
		writeErr(w, http.StatusForbidden, "users and devices only")
		return
	}
	token := ""
	if p.Device != nil {
		token = bearer(r)
	}
	prof, err := s.buildProfile(r, p.User, p.Device, token)
	if err != nil {
		s.fail(w, err, "profile")
		return
	}
	if r.URL.Query().Get("format") == "env" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="pocket-homelab.env"`)
		_, _ = w.Write([]byte(prof.Env))
		return
	}
	writeJSON(w, http.StatusOK, prof)
}

// Profile is what a paired client imports: one entry per service and the
// same thing rendered as the Pocket Homelab env file.
type Profile struct {
	Box      map[string]any    `json:"box"`
	User     map[string]any    `json:"user"`
	Device   *store.Device     `json:"device,omitempty"`
	Token    string            `json:"token,omitempty"`
	Services []ProfileService  `json:"services"`
	Env      string            `json:"env"`
	Values   map[string]string `json:"values"`
}

// ProfileService is one service in a profile.
type ProfileService struct {
	Type   string            `json:"type"`
	Name   string            `json:"name"`
	Group  string            `json:"group"`
	URL    string            `json:"url"`
	Pocket bool              `json:"pocket"`
	Env    map[string]string `json:"env"`
}

func (s *Server) buildProfile(r *http.Request, u *store.User, d *store.Device, deviceToken string) (*Profile, error) {
	ctx := r.Context()
	list, err := s.store.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	st, _ := s.store.GetSettings(ctx)
	prof := &Profile{
		Box:      map[string]any{"name": st.BoxName, "core_url": s.publicBase(), "version": Version, "issuer": s.cfg.OIDCIssuer},
		User:     map[string]any{"id": u.ID, "email": u.Email, "name": u.Name, "role": u.Role},
		Device:   d,
		Token:    deviceToken,
		Services: []ProfileService{},
		Values:   map[string]string{},
	}
	var lines []string
	lines = append(lines, "# Pocket Homelab configuration", "# Generated by Pocket Core for "+u.Email, "# Box: "+st.BoxName+" · "+s.publicBase(), "")

	// Core itself first.
	core := ProfileService{Type: "core", Name: "Pocket Core", Group: "Pocket", URL: s.publicBase(), Pocket: true, Env: map[string]string{"CORE_URL": s.publicBase()}}
	if deviceToken != "" {
		core.Env["CORE_API_KEY"] = deviceToken
	}
	prof.Services = append(prof.Services, core)

	overrides, err := s.store.ListOverrides(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	for _, sv := range list {
		if !sv.VisibleTo(u) {
			continue
		}
		if ov, ok := overrides[sv.ID]; ok {
			if ov.URL != "" {
				sv.URL, sv.PublicURL = ov.URL, ""
			}
			merged := map[string]string{}
			for k, v := range sv.Fields {
				merged[k] = v
			}
			for k, v := range ov.Fields {
				merged[k] = v
			}
			sv.Fields = merged
		}
		prefix := ""
		if sv.AppID > 0 {
			if a, err := s.store.GetApp(ctx, sv.AppID); err == nil {
				prefix = a.EnvPrefix
			}
		}
		e := catalog.Resolve(sv.Type, sv.Name, prefix, sv.AppID > 0)
		ps := ProfileService{Type: sv.Type, Name: sv.Name, Group: e.Group, URL: sv.EffectivePublicURL(), Pocket: e.Pocket, Env: map[string]string{}}
		if ps.Name == "" {
			ps.Name = e.Name
		}
		if ps.URL != "" {
			ps.Env[e.FullKey(e.URLKey)] = ps.URL
		}
		for k, v := range sv.Fields {
			if v != "" {
				ps.Env[e.FullKey(k)] = v
			}
		}
		if e.Pocket && sv.AppID > 0 && deviceToken != "" {
			if _, set := ps.Env[e.FullKey("API_KEY")]; !set {
				ps.Env[e.FullKey("API_KEY")] = deviceToken
			}
		}
		prof.Services = append(prof.Services, ps)
	}
	sort.SliceStable(prof.Services[1:], func(i, j int) bool {
		a, b := prof.Services[i+1], prof.Services[j+1]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Name < b.Name
	})
	for _, ps := range prof.Services {
		keys := make([]string, 0, len(ps.Env))
		for k := range ps.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines = append(lines, "# "+ps.Name)
		for _, k := range keys {
			prof.Values[k] = ps.Env[k]
			lines = append(lines, k+"="+envQuote(ps.Env[k]))
		}
		lines = append(lines, "")
	}
	prof.Env = strings.Join(lines, "\n")
	return prof, nil
}

func envQuote(v string) string {
	if strings.ContainsAny(v, " #\"'\\\n\t") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v) + `"`
	}
	return v
}

// ---- devices ----

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListDevices(r.Context(), s.scopeUser(r))
	if err != nil {
		s.fail(w, err, "list devices")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDevice(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get device")
		return
	}
	p := principalFrom(r.Context())
	if !p.IsAdmin() && d.UserID != p.UserID() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.DeleteDevice(r.Context(), d.ID); err != nil {
		s.fail(w, err, "delete device")
		return
	}
	_ = s.hub.Publish(r.Context(), &store.Event{Topic: "core.device.revoked", Source: "core", Subject: fmt.Sprintf("device:%d", d.ID), Payload: mustJSON(map[string]any{"name": d.Name})})
	w.WriteHeader(http.StatusNoContent)
}
