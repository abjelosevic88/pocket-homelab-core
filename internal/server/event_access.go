package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// A subscription expresses interest, not authority. Cross-app events require
// an approved events:read link and the event owner's current permission.
// Legacy events without an attributable person stay with their publisher/admin.
func (s *Server) appCanReadEvent(ctx context.Context, appID int64, e store.Event) bool {
	a, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return false
	}
	if e.Source == a.Slug {
		return true
	}
	if strings.HasPrefix(e.Source, "connector:") || e.Source == "core" {
		return false
	}
	l, err := s.store.FindLink(ctx, appID, e.Source, "events:read")
	if err != nil {
		return false
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(e.Payload, &payload) != nil {
		return false
	}
	email := ""
	for _, key := range []string{"user_email", "owner_email", "user"} {
		var value string
		if json.Unmarshal(payload[key], &value) == nil && value != "" {
			if email != "" && !strings.EqualFold(email, value) {
				return false
			}
			email = value
		}
	}
	u, err := s.linkPerson(ctx, email)
	return err == nil && s.linkState(ctx, l, u) == linkOn
}

func (s *Server) canReadEvent(ctx context.Context, p *Principal, e store.Event) bool {
	return p != nil && (p.IsAdmin() || p.App != nil && s.appCanReadEvent(ctx, p.App.ID, e))
}

func (s *Server) visibleEvents(ctx context.Context, p *Principal, f store.EventFilter) ([]store.Event, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	ascending := f.Ascending || f.AfterID > 0
	f.Limit = 500
	out := []store.Event{}
	for {
		batch, err := s.store.ListEvents(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, e := range batch {
			if s.canReadEvent(ctx, p, e) {
				out = append(out, e)
				if len(out) == limit {
					return out, nil
				}
			}
		}
		if len(batch) < f.Limit {
			return out, nil
		}
		if ascending {
			f.AfterID = batch[len(batch)-1].ID
		} else {
			f.BeforeID = batch[len(batch)-1].ID
		}
	}
}
