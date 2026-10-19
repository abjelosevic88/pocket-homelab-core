package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

const masked = "••••••••"

// serviceOut is a service with secrets masked and catalog info attached.
type serviceOut struct {
	store.Service
	Catalog catalog.Entry `json:"catalog"`
}

func maskService(sv store.Service, e catalog.Entry) serviceOut {
	out := serviceOut{Service: sv, Catalog: e}
	out.Fields = make(map[string]string, len(sv.Fields))
	for k, v := range sv.Fields {
		if e.IsSecret(k) && v != "" {
			out.Fields[k] = masked
		} else {
			out.Fields[k] = v
		}
	}
	return out
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, catalog.All())
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListServices(r.Context())
	if err != nil {
		s.fail(w, err, "list services")
		return
	}
	out := make([]serviceOut, 0, len(list))
	for _, sv := range list {
		out = append(out, maskService(sv, s.entryForCtx(r, &sv)))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) entryForCtx(r *http.Request, sv *store.Service) catalog.Entry {
	prefix := ""
	if sv.AppID > 0 {
		if a, err := s.store.GetApp(r.Context(), sv.AppID); err == nil {
			prefix = a.EnvPrefix
		}
	}
	return catalog.Resolve(sv.Type, sv.Name, prefix, sv.AppID > 0)
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	sv, err := s.store.GetService(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get service")
		return
	}
	writeJSON(w, http.StatusOK, maskService(*sv, s.entryForCtx(r, sv)))
}

type serviceIn struct {
	Audience      *string            `json:"audience"`
	AudienceUsers *[]int64           `json:"audience_users"`
	Type          *string            `json:"type"`
	Name          *string            `json:"name"`
	URL           *string            `json:"url"`
	PublicURL     *string            `json:"public_url"`
	Fields        *map[string]string `json:"fields"`
	Enabled       *bool              `json:"enabled"`
	Notes         *string            `json:"notes"`
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var in serviceIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Type == nil || strings.TrimSpace(*in.Type) == "" {
		writeErr(w, http.StatusBadRequest, "type required")
		return
	}
	sv := &store.Service{Type: strings.ToLower(strings.TrimSpace(*in.Type)), Enabled: true, Fields: map[string]string{}}
	applyService(sv, in)
	e := catalog.Resolve(sv.Type, sv.Name, "", false)
	if sv.Name == "" {
		sv.Name = e.Name
	}
	if err := s.store.SaveService(r.Context(), sv, e.IsSecret); err != nil {
		s.fail(w, err, "create service")
		return
	}
	writeJSON(w, http.StatusCreated, maskService(*sv, e))
}

func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	sv, err := s.store.GetService(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get service")
		return
	}
	var in serviceIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if sv.AppID > 0 {
		in.Type = nil // a registered app owns its type
	}
	// Masked secrets mean "keep".
	if in.Fields != nil {
		for k, v := range *in.Fields {
			if v == masked {
				(*in.Fields)[k] = sv.Fields[k]
			}
		}
	}
	applyService(sv, in)
	e := s.entryForCtx(r, sv)
	if err := s.store.SaveService(r.Context(), sv, e.IsSecret); err != nil {
		s.fail(w, err, "update service")
		return
	}
	writeJSON(w, http.StatusOK, maskService(*sv, e))
}

func applyService(sv *store.Service, in serviceIn) {
	if in.Type != nil {
		sv.Type = strings.ToLower(strings.TrimSpace(*in.Type))
	}
	if in.Name != nil {
		sv.Name = strings.TrimSpace(*in.Name)
	}
	if in.URL != nil {
		sv.URL = strings.TrimRight(strings.TrimSpace(*in.URL), "/")
	}
	if in.PublicURL != nil {
		sv.PublicURL = strings.TrimRight(strings.TrimSpace(*in.PublicURL), "/")
	}
	if in.Fields != nil {
		f := map[string]string{}
		for k, v := range *in.Fields {
			k = strings.ToUpper(strings.TrimSpace(k))
			if k != "" {
				f[k] = strings.TrimSpace(v)
			}
		}
		sv.Fields = f
	}
	if in.Enabled != nil {
		sv.Enabled = *in.Enabled
	}
	if in.Notes != nil {
		sv.Notes = *in.Notes
	}
	if in.Audience != nil {
		sv.Audience = strings.ToLower(strings.TrimSpace(*in.Audience))
	}
	if in.AudienceUsers != nil {
		sv.AudienceUsers = *in.AudienceUsers
	}
}

// ---- per-user credential overrides ----

type overrideOut struct {
	Service serviceOut        `json:"service"`
	URL     string            `json:"url"`
	Fields  map[string]string `json:"fields"` // masked
	Set     bool              `json:"set"`
	Visible bool              `json:"visible"`
}

// handleListCredentials lists, for one user, every service with that user's
// overrides (masked). Admins see anyone; a member sees only themselves.
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	uid := idParam(r, "id")
	p := principalFrom(r.Context())
	if !p.IsAdmin() && p.UserID() != uid {
		writeErr(w, http.StatusForbidden, "not yours")
		return
	}
	u, err := s.store.GetUser(r.Context(), uid)
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	list, err := s.store.ListServices(r.Context())
	if err != nil {
		s.fail(w, err, "list services")
		return
	}
	ovs, err := s.store.ListOverrides(r.Context(), uid)
	if err != nil {
		s.fail(w, err, "list overrides")
		return
	}
	out := []overrideOut{}
	for _, sv := range list {
		e := s.entryForCtx(r, &sv)
		o := overrideOut{Service: maskService(sv, e), Fields: map[string]string{}, Visible: sv.VisibleTo(u)}
		if !p.IsAdmin() {
			// a member only sees the shape, never the box-wide values
			o.Service.Fields = map[string]string{}
			o.Service.URL, o.Service.PublicURL = "", ""
		}
		if ov, ok := ovs[sv.ID]; ok {
			o.Set, o.URL = true, ov.URL
			for k, v := range ov.Fields {
				if e.IsSecret(k) && v != "" {
					o.Fields[k] = masked
				} else {
					o.Fields[k] = v
				}
			}
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutCredential(w http.ResponseWriter, r *http.Request) {
	uid, sid := idParam(r, "id"), idParam(r, "sid")
	p := principalFrom(r.Context())
	if !p.IsAdmin() && p.UserID() != uid {
		writeErr(w, http.StatusForbidden, "not yours")
		return
	}
	sv, err := s.store.GetService(r.Context(), sid)
	if err != nil {
		s.fail(w, err, "get service")
		return
	}
	var in struct {
		URL    *string            `json:"url"`
		Fields *map[string]string `json:"fields"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ovs, _ := s.store.ListOverrides(r.Context(), uid)
	cur := ovs[sid]
	o := &store.Override{ServiceID: sid, UserID: uid, URL: cur.URL, Fields: cur.Fields}
	if o.Fields == nil {
		o.Fields = map[string]string{}
	}
	if in.URL != nil {
		o.URL = strings.TrimRight(strings.TrimSpace(*in.URL), "/")
	}
	if in.Fields != nil {
		f := map[string]string{}
		for k, v := range *in.Fields {
			k = strings.ToUpper(strings.TrimSpace(k))
			if v == masked {
				v = cur.Fields[k]
			}
			if k != "" {
				f[k] = strings.TrimSpace(v)
			}
		}
		o.Fields = f
	}
	e := s.entryForCtx(r, sv)
	if err := s.store.SaveOverride(r.Context(), o, e.IsSecret); err != nil {
		s.fail(w, err, "save override")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "set": o.URL != "" || len(o.Fields) > 0})
}

func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	uid, sid := idParam(r, "id"), idParam(r, "sid")
	p := principalFrom(r.Context())
	if !p.IsAdmin() && p.UserID() != uid {
		writeErr(w, http.StatusForbidden, "not yours")
		return
	}
	if err := s.store.DeleteOverride(r.Context(), sid, uid); err != nil {
		s.fail(w, err, "delete override")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	sv, err := s.store.GetService(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get service")
		return
	}
	if sv.AppID > 0 {
		writeErr(w, http.StatusBadRequest, "this service mirrors a registered app; remove the app instead")
		return
	}
	if err := s.store.DeleteService(r.Context(), sv.ID); err != nil {
		s.fail(w, err, "delete service")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestService does a plain reachability check of the service URL.
func (s *Server) handleTestService(w http.ResponseWriter, r *http.Request) {
	sv, err := s.store.GetService(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get service")
		return
	}
	if sv.URL == "" {
		writeErr(w, http.StatusBadRequest, "no url")
		return
	}
	start := time.Now()
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, sv.URL, nil)
	res, err := s.http.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": trimErr(err)})
		return
	}
	res.Body.Close()
	writeJSON(w, http.StatusOK, map[string]any{"ok": res.StatusCode < 500, "status": res.StatusCode, "ms": time.Since(start).Milliseconds()})
}
