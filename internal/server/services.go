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
	Type      *string            `json:"type"`
	Name      *string            `json:"name"`
	URL       *string            `json:"url"`
	PublicURL *string            `json:"public_url"`
	Fields    *map[string]string `json:"fields"`
	Enabled   *bool              `json:"enabled"`
	Notes     *string            `json:"notes"`
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
