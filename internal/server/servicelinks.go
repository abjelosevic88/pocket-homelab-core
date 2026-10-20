package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Service connections: a Pocket app reaching a third-party service (Immich,
// Maloja, Dawarich…) for a person through Core, instead of keeping its own
// pasted URL and key. The app lists the services in its manifest
// (`services`), Core keeps them as links whose target is "service:<type>"
// with the single scope "account" (same switches, views and counters as app
// links), and hands out the service's address plus the person's OWN
// credential, admins included. The box-wide key never leaves Core: it is
// usually the owner's account or an admin key that sees everyone, and stays
// for Core's connectors. `pocket-core credentials claim-box` copies it into
// a person's own credentials once, for the account it really belongs to.

const (
	servicePrefix = "service:"
	serviceScope  = "account"
	linkNoAccount = "no_account" // the person has no credential of their own there
)

func init() {
	linkStateText[linkNoAccount] = "the person has no account of their own there yet (a connector or an admin can add one)"
}

// ServiceUse is a third-party service an app reaches for a person.
type ServiceUse struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

func cleanServiceUses(in []ServiceUse) []store.LinkWant {
	out := []store.LinkWant{}
	seen := map[string]bool{}
	for _, u := range in {
		typ := strings.ToLower(strings.TrimSpace(u.Type))
		if !slugRe.MatchString(typ) || seen[typ] {
			continue
		}
		seen[typ] = true
		out = append(out, store.LinkWant{Target: servicePrefix + typ, Scope: serviceScope, Reason: strings.TrimSpace(u.Reason)})
	}
	return out
}

// serviceType returns the service type when a link's target is a service.
func serviceType(target string) (string, bool) {
	if strings.HasPrefix(target, servicePrefix) {
		return strings.TrimPrefix(target, servicePrefix), true
	}
	return "", false
}

// serviceOf finds the service of this type the person may use (any enabled
// one when u is nil); Pocket apps registered as services don't count.
func (s *Server) serviceOf(ctx context.Context, typ string, u *store.User) *store.Service {
	list, err := s.store.ListServices(ctx)
	if err != nil {
		return nil
	}
	var any *store.Service
	for i := range list {
		sv := &list[i]
		if sv.Type != typ || !sv.Enabled {
			continue
		}
		if u == nil || sv.VisibleTo(u) {
			return sv
		}
		if any == nil {
			any = sv
		}
	}
	if u == nil {
		return any
	}
	return nil
}

// serviceCredential is the person's way into the service: their own fields,
// set by hand or by a connector. Connector-only fields never leave Core.
func (s *Server) serviceCredential(ctx context.Context, sv *store.Service, u *store.User) (fields map[string]string, source string, ok bool) {
	e, _ := catalog.Get(sv.Type)
	keep := func(in map[string]string) map[string]string {
		out := map[string]string{}
		for _, f := range e.Fields {
			if f.ConnectorOnly {
				continue
			}
			if v := strings.TrimSpace(in[f.Key]); v != "" {
				out[f.Key] = v
			}
		}
		return out
	}
	if ovs, err := s.store.ListOverrides(ctx, u.ID); err == nil {
		if ov, has := ovs[sv.ID]; has && ownCredential(e, sv, ov) {
			return keep(ov.Fields), "personal", true
		}
	}
	return nil, "", false
}

// boxFields are the service's box-wide credential fields that could be shared
// (never the connector-only admin keys).
func boxFields(e catalog.Entry, sv *store.Service) map[string]string {
	out := map[string]string{}
	for _, f := range e.Fields {
		if !f.ConnectorOnly && strings.TrimSpace(sv.Fields[f.Key]) != "" {
			out[f.Key] = sv.Fields[f.Key]
		}
	}
	return out
}

// keyless: the service is used without a key on this box (none is filled in).
func keyless(e catalog.Entry, sv *store.Service) bool { return len(boxFields(e, sv)) == 0 }

// ownCredential: the person already has their own way in — their own key
// (a user id alone, as connectors write for linked accounts, is not a way in),
// or for a service used without a key, their own entry with its address.
func ownCredential(e catalog.Entry, sv *store.Service, ov store.Override) bool {
	secrets, any := false, false
	for _, f := range e.Fields {
		if f.ConnectorOnly {
			continue
		}
		set := strings.TrimSpace(ov.Fields[f.Key]) != ""
		if f.Secret {
			secrets = true
			if set {
				return true
			}
		} else if set {
			any = true
		}
	}
	if keyless(e, sv) {
		return ov.URL != "" || any
	}
	return !secrets && any
}

// ClaimBoxCredentials copies each service's box-wide credential into the
// person's own, where they have none yet, so apps reaching services through
// Core keep working for the account the box key belongs to. Connector-only
// fields and Pocket apps are skipped; an existing personal entry is never
// overwritten. Only the types in `only` when given, else every service some
// app reaches through Core (its service links). Returns what it copied (or
// would, with dry).
func ClaimBoxCredentials(ctx context.Context, st *store.Store, u *store.User, only []string, dry bool) ([]string, error) {
	want := map[string]bool{}
	for _, t := range only {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			want[t] = true
		}
	}
	if len(want) == 0 {
		links, err := st.ListLinks(ctx)
		if err != nil {
			return nil, err
		}
		for _, l := range links {
			if typ, ok := serviceType(l.TargetSlug); ok {
				want[typ] = true
			}
		}
	}
	list, err := st.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	ovs, err := st.ListOverrides(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	done := []string{}
	for i := range list {
		sv := &list[i]
		e, ok := catalog.Get(sv.Type)
		if !ok || e.Pocket || !sv.Enabled || !want[sv.Type] {
			continue
		}
		fields := boxFields(e, sv)
		ov, has := ovs[sv.ID]
		if has && ownCredential(e, sv, ov) {
			continue
		}
		// Add the key to what the person has (a connector's user id stays, and so
		// does its marker), never replacing a value they set.
		merged := map[string]string{}
		for k, v := range ov.Fields {
			merged[k] = v
		}
		for k, v := range fields {
			if strings.TrimSpace(merged[k]) == "" {
				merged[k] = v
			}
		}
		o := &store.Override{ServiceID: sv.ID, UserID: u.ID, Fields: merged, URL: ov.URL, Source: ov.Source}
		if len(fields) == 0 && o.URL == "" {
			o.URL = sv.EffectivePublicURL() // keyless: the address is the way in
		}
		done = append(done, sv.Type)
		if !dry {
			if err := st.SaveOverride(ctx, o, e.IsSecret); err != nil {
				return done, err
			}
		}
	}
	return done, nil
}

// serviceLinkState is linkState for a service target.
func (s *Server) serviceLinkState(ctx context.Context, l *store.AppLink, u *store.User) string {
	typ, _ := serviceType(l.TargetSlug)
	if l.Scope != serviceScope {
		return linkNotOffered
	}
	if s.serviceOf(ctx, typ, nil) == nil {
		return linkNoTarget
	}
	if u == nil {
		if !l.Enabled {
			return linkOffAdmin
		}
		return linkOn
	}
	sv := s.serviceOf(ctx, typ, u)
	if sv == nil || !s.appAdmits(ctx, l.AppID, u) {
		return linkNotInAudience
	}
	if !l.Enabled {
		return linkOffAdmin
	}
	if s.store.LinkOptedOut(ctx, l.ID, u.ID) {
		return linkOffPerson
	}
	if _, _, ok := s.serviceCredential(ctx, sv, u); !ok {
		return linkNoAccount
	}
	return linkOn
}

// serviceLinkView fills a service link's names for the Links page.
func (s *Server) serviceLinkView(ctx context.Context, v map[string]any, typ string, u *store.User) {
	name := typ
	if e, ok := catalog.Get(typ); ok {
		name = e.Name
	}
	if sv := s.serviceOf(ctx, typ, nil); sv != nil {
		v["target_registered"] = true
		if sv.Name != "" {
			name = sv.Name
		}
	}
	v["target_name"], v["target_kind"], v["target_type"], v["offered"] = name, "service", typ, true
	v["scope_name"] = "Use your " + name
	v["scope_description"] = "The app gets " + name + "'s address and your own key there ; the box's key never leaves Core."
	// switching off replaces keys the connector made ("replaces"), or can't ("cannot")
	v["key_rotation"] = keyRotation(typ)
	if v["key_rotation"] != "" {
		v["key_replacements"] = s.keyReplacements(ctx, typ, u)
	}
}

// handleServiceCredential gives the calling app the service's address and the
// person's credential, when its service link is on for them.
//
//	POST /api/v1/services/credential {"user": "a@b.c", "type": "immich"}
func (s *Server) handleServiceCredential(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller := principalFrom(ctx)
	if caller.App == nil {
		writeErr(w, http.StatusForbidden, "apps only")
		return
	}
	var in struct {
		User string `json:"user"`
		Type string `json:"type"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	u, err := s.linkPerson(ctx, in.User)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "unknown person", "reason": "unknown_user"})
		return
	}
	l, err := s.store.FindLink(ctx, caller.App.ID, servicePrefix+typ, serviceScope)
	state := linkNotDeclared
	if err == nil {
		state = s.serviceLinkState(ctx, l, u)
	}
	if state != linkOn {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": typ + ": " + linkStateText[state], "reason": state})
		return
	}
	sv := s.serviceOf(ctx, typ, u)
	fields, source, _ := s.serviceCredential(ctx, sv, u)
	key := ""
	if e, ok := catalog.Get(typ); ok {
		for _, f := range e.Fields {
			if f.Secret && fields[f.Key] != "" {
				key = fields[f.Key]
				break
			}
		}
	}
	s.store.TouchLinks(ctx, []int64{l.ID})
	s.log.Debug("service credential", "app", caller.App.Slug, "service", typ, "user", u.Email, "source", source)
	writeJSON(w, http.StatusOK, map[string]any{
		"type": typ, "name": sv.Name, "url": strings.TrimRight(sv.URL, "/"), "public_url": strings.TrimRight(sv.EffectivePublicURL(), "/"),
		"fields": fields, "key": key, "source": source,
	})
}
