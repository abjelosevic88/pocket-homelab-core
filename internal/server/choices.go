package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// choiceView is one slot as the Devices page shows it.
type choiceView struct {
	catalog.Choice
	Available map[string]bool `json:"available"` // option value → in this person's profile
	Chosen    string          `json:"choice"`    // stored; "" = automatic
	Effective string          `json:"effective"` // what the devices use
	Explicit  bool            `json:"explicit"`  // the stored choice decided
}

// visibleTypes are the service types in a person's profile.
func (s *Server) visibleTypes(r *http.Request, u *store.User) (map[string]bool, error) {
	list, err := s.store.ListServices(r.Context())
	if err != nil {
		return nil, err
	}
	types := map[string]bool{}
	for i := range list {
		if list[i].VisibleTo(u) {
			types[list[i].Type] = true
		}
	}
	return types, nil
}

func (s *Server) choiceViews(r *http.Request, u *store.User) ([]choiceView, error) {
	types, err := s.visibleTypes(r, u)
	if err != nil {
		return nil, err
	}
	stored, err := s.store.ListChoices(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	out := []choiceView{}
	for _, c := range catalog.Choices {
		v := choiceView{Choice: c, Available: map[string]bool{}, Chosen: stored[c.Slot]}
		any := false
		for _, o := range c.Options {
			v.Available[o.Value] = types[o.Type]
			any = any || types[o.Type]
		}
		if !any {
			continue
		}
		v.Effective, v.Explicit = c.Resolve(v.Chosen, types)
		out = append(out, v)
	}
	return out, nil
}

// handleMyChoices lists the slots where the person's profile has one of the apps.
func (s *Server) handleMyChoices(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.User == nil {
		writeErr(w, http.StatusForbidden, "people only")
		return
	}
	out, err := s.choiceViews(r, p.User)
	if err != nil {
		s.fail(w, err, "choices")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetChoice stores which app fills a slot ("" = automatic, the Pocket app).
// Devices pick it up on their next profile refresh.
func (s *Server) handleSetChoice(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.User == nil {
		writeErr(w, http.StatusForbidden, "people only")
		return
	}
	c, ok := catalog.ChoiceFor(chi.URLParam(r, "slot"))
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown slot")
		return
	}
	var in struct {
		Provider string `json:"provider"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Provider != "" {
		if _, ok := c.Option(in.Provider); !ok {
			writeErr(w, http.StatusBadRequest, "unknown app for "+c.Title)
			return
		}
	}
	if err := s.store.SetChoice(r.Context(), p.User.ID, c.Slot, in.Provider); err != nil {
		s.fail(w, err, "save choice")
		return
	}
	s.handleMyChoices(w, r)
}
