package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// App links: one Pocket app acting for a person in another. The acting app
// lists what it needs in its manifest (`uses`), the target lists what it
// allows (`offers`), Core keeps one switch per link for the admin and one per
// person, and swaps the acting app's pca_ token for a short-lived pcx_ token
// that only the target app accepts. See docs/links.md.

const linkTokenTTL = 5 * time.Minute

var scopeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,60}$`)

// Offer is one scope an app lets other apps use for a person.
type Offer struct {
	Scope       string `json:"scope"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Use is what an app needs in another app.
type Use struct {
	App    string   `json:"app"`
	Scopes []string `json:"scopes"`
	Reason string   `json:"reason,omitempty"`
}

// Link states, worst first; "on" is the only one that issues tokens.
const (
	linkOn            = "on"
	linkOffAdmin      = "off_admin"       // the admin switched it off for the box
	linkOffPerson     = "off_person"      // the person switched it off for themselves
	linkNoTarget      = "target_missing"  // the target app is not registered
	linkNotOffered    = "not_offered"     // the target does not offer the scope
	linkNotInAudience = "not_in_audience" // the person may not use one of the two apps
	linkNotDeclared   = "not_declared"    // the acting app never asked for it
)

var linkStateText = map[string]string{
	linkOn:            "on",
	linkOffAdmin:      "switched off for the whole box",
	linkOffPerson:     "switched off by the person",
	linkNoTarget:      "the target app is not registered with Core",
	linkNotOffered:    "the target app does not offer this",
	linkNotInAudience: "the person does not have access to both apps",
	linkNotDeclared:   "the app did not ask for this in its manifest",
}

// appOffers reads the offers from an app's stored manifest.
func appOffers(a *store.App) map[string]Offer {
	var m struct {
		Offers []Offer `json:"offers"`
	}
	_ = json.Unmarshal(a.Manifest, &m)
	out := map[string]Offer{}
	for _, o := range m.Offers {
		out[o.Scope] = o
	}
	return out
}

// cleanOffers and cleanUses validate the manifest parts; bad entries are dropped.
func cleanOffers(in []Offer) []Offer {
	out := []Offer{}
	seen := map[string]bool{}
	for _, o := range in {
		o.Scope = strings.ToLower(strings.TrimSpace(o.Scope))
		if !scopeRe.MatchString(o.Scope) || seen[o.Scope] {
			continue
		}
		seen[o.Scope] = true
		o.Name, o.Description = strings.TrimSpace(o.Name), strings.TrimSpace(o.Description)
		out = append(out, o)
	}
	return out
}

func cleanUses(self string, in []Use) []store.LinkWant {
	out := []store.LinkWant{}
	seen := map[[2]string]bool{}
	for _, u := range in {
		target := strings.ToLower(strings.TrimSpace(u.App))
		if !slugRe.MatchString(target) || target == self {
			continue
		}
		for _, sc := range u.Scopes {
			sc = strings.ToLower(strings.TrimSpace(sc))
			k := [2]string{target, sc}
			if !scopeRe.MatchString(sc) || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, store.LinkWant{Target: target, Scope: sc, Reason: strings.TrimSpace(u.Reason)})
		}
	}
	return out
}

// linkState decides whether a link works for a person right now.
func (s *Server) linkState(ctx context.Context, l *store.AppLink, u *store.User) string {
	if _, ok := serviceType(l.TargetSlug); ok {
		return s.serviceLinkState(ctx, l, u)
	}
	target, err := s.store.GetAppBySlug(ctx, l.TargetSlug)
	if err != nil {
		return linkNoTarget
	}
	if _, ok := appOffers(target)[l.Scope]; !ok {
		return linkNotOffered
	}
	if u != nil && (!s.appAdmits(ctx, l.AppID, u) || !s.appAdmits(ctx, target.ID, u)) {
		return linkNotInAudience
	}
	if !l.Enabled {
		return linkOffAdmin
	}
	if u != nil && s.store.LinkOptedOut(ctx, l.ID, u.ID) {
		return linkOffPerson
	}
	return linkOn
}

func splitScopes(scope string, list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, sc := range append(strings.Fields(scope), list...) {
		sc = strings.ToLower(strings.TrimSpace(sc))
		if sc != "" && !seen[sc] {
			seen[sc] = true
			out = append(out, sc)
		}
	}
	sort.Strings(out)
	return out
}

// linkPerson finds the person an app wants to act for, by email.
func (s *Server) linkPerson(ctx context.Context, email string) (*store.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("user (email) required")
	}
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil || u.Disabled {
		return nil, store.ErrNotFound
	}
	return u, nil
}

// handleLinkToken swaps the calling app's token for a pcx_ token that lets it
// act for one person in one target app, for the requested scopes.
//
//	POST /api/v1/links/token {"user": "a@b.c", "app": "money", "scope": "transactions:write"}
func (s *Server) handleLinkToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller := principalFrom(ctx)
	if caller.App == nil {
		writeErr(w, http.StatusForbidden, "apps only")
		return
	}
	var in struct {
		User   string   `json:"user"`
		App    string   `json:"app"`
		Scope  string   `json:"scope"`
		Scopes []string `json:"scopes"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	target := strings.ToLower(strings.TrimSpace(in.App))
	scopes := splitScopes(in.Scope, in.Scopes)
	if _, ok := serviceType(target); ok {
		writeErr(w, http.StatusBadRequest, "services are reached through /api/v1/services/credential")
		return
	}
	if target == "" || len(scopes) == 0 {
		writeErr(w, http.StatusBadRequest, "app and scope required")
		return
	}
	u, err := s.linkPerson(ctx, in.User)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "unknown person", "reason": "unknown_user"})
		return
	}
	ids := []int64{}
	for _, sc := range scopes {
		l, err := s.store.FindLink(ctx, caller.App.ID, target, sc)
		state := linkNotDeclared
		if err == nil {
			state = s.linkState(ctx, l, u)
			ids = append(ids, l.ID)
		}
		if state != linkOn {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": target + " " + sc + ": " + linkStateText[state], "reason": state, "scope": sc})
			return
		}
	}
	targetApp, _ := s.store.GetAppBySlug(ctx, target)
	tok, err := s.store.CreateLinkToken(ctx, &store.LinkToken{AppID: caller.App.ID, TargetSlug: target, UserID: u.ID, Scope: strings.Join(scopes, " ")}, linkTokenTTL)
	if err != nil {
		s.fail(w, err, "link token")
		return
	}
	s.store.TouchLinks(ctx, ids)
	s.log.Debug("link token", "app", caller.App.Slug, "target", target, "user", u.Email, "scope", scopes)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": tok, "token_type": "Bearer", "expires_in": int(linkTokenTTL.Seconds()), "scope": strings.Join(scopes, " "),
		"app": target, "url": targetApp.URL, "public_url": targetApp.EffectivePublicURL(),
	})
}

// handleLinkStatus tells the calling app which of its links work for a person
// (so it can show or hide an option) without issuing anything.
//
//	GET /api/v1/links/status?user=a@b.c[&app=money]
func (s *Server) handleLinkStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller := principalFrom(ctx)
	if caller.App == nil {
		writeErr(w, http.StatusForbidden, "apps only")
		return
	}
	u, err := s.linkPerson(ctx, r.URL.Query().Get("user"))
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	only := strings.ToLower(r.URL.Query().Get("app"))
	links, err := s.store.ListAppLinks(ctx, caller.App.ID)
	if err != nil {
		s.fail(w, err, "links")
		return
	}
	out := []map[string]any{}
	for i := range links {
		l := &links[i]
		if only != "" && l.TargetSlug != only {
			continue
		}
		st := s.linkState(ctx, l, u)
		out = append(out, map[string]any{"app": l.TargetSlug, "scope": l.Scope, "state": st, "on": st == linkOn, "detail": linkStateText[st]})
	}
	writeJSON(w, http.StatusOK, out)
}

// introspectLinkToken answers for a pcx_ token: only the target app (or an
// admin) learns anything, and the links are re-checked so a switch turned off
// takes effect at once rather than when the token expires.
func (s *Server) introspectLinkToken(w http.ResponseWriter, r *http.Request, caller *Principal, tok string) {
	ctx := r.Context()
	inactive := func(reason string) {
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "reason": reason})
	}
	t, err := s.store.GetLinkToken(ctx, tok)
	if err != nil {
		inactive("")
		return
	}
	if caller.App != nil && caller.App.Slug != t.TargetSlug {
		inactive("wrong_audience")
		return
	}
	u, err := s.store.GetUser(ctx, t.UserID)
	if err != nil || u.Disabled {
		inactive("")
		return
	}
	actor, err := s.store.GetApp(ctx, t.AppID)
	if err != nil {
		inactive("")
		return
	}
	for _, sc := range strings.Fields(t.Scope) {
		l, err := s.store.FindLink(ctx, t.AppID, t.TargetSlug, sc)
		if err != nil {
			inactive(linkNotDeclared)
			return
		}
		if st := s.linkState(ctx, l, u); st != linkOn {
			inactive(st)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active": true, "kind": "delegated", "scope": t.Scope, "audience": t.TargetSlug, "expires_at": t.ExpiresAt,
		"user": map[string]any{"id": u.ID, "email": u.Email, "name": u.Name, "role": u.Role},
		"app":  map[string]any{"id": actor.ID, "slug": actor.Slug, "name": actor.Name},
	})
}

// linkView is a link with the names and words a person needs to decide.
func (s *Server) linkView(ctx context.Context, l *store.AppLink, u *store.User) map[string]any {
	v := map[string]any{"id": l.ID, "app_slug": l.AppSlug, "app_name": l.AppName, "target_slug": l.TargetSlug, "target_name": l.TargetSlug,
		"scope": l.Scope, "scope_name": l.Scope, "scope_description": "", "reason": l.Reason, "enabled": l.Enabled,
		"uses": l.Uses, "last_used_at": l.LastUsedAt, "opt_outs": l.OptOuts, "target_registered": false, "offered": false}
	if typ, ok := serviceType(l.TargetSlug); ok {
		s.serviceLinkView(ctx, v, typ)
	} else if target, err := s.store.GetAppBySlug(ctx, l.TargetSlug); err == nil {
		v["target_name"], v["target_registered"] = target.Name, true
		if o, ok := appOffers(target)[l.Scope]; ok {
			v["offered"] = true
			if o.Name != "" {
				v["scope_name"] = o.Name
			}
			v["scope_description"] = o.Description
		}
	}
	st := s.linkState(ctx, l, u)
	v["state"], v["detail"] = st, linkStateText[st]
	return v
}

// handleListLinks is the admin's view of every link on the box.
func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.ListLinks(r.Context())
	if err != nil {
		s.fail(w, err, "links")
		return
	}
	modes := s.modesByPair(r.Context())
	out := []map[string]any{}
	for i := range links {
		v := s.linkView(r.Context(), &links[i], nil)
		people := modes[[2]any{links[i].AppID, links[i].TargetSlug}]
		count := map[string]int{"core": 0, "direct": 0, "off": 0}
		for _, m := range people {
			count[m["mode"].(string)]++
		}
		v["modes"], v["mode_people"] = count, people
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// modesByPair groups the reported connection modes by (app id, target).
func (s *Server) modesByPair(ctx context.Context) map[[2]any][]map[string]any {
	out := map[[2]any][]map[string]any{}
	list, err := s.store.ListLinkModes(ctx)
	if err != nil {
		return out
	}
	for _, m := range list {
		k := [2]any{m.AppID, m.Target}
		out[k] = append(out[k], map[string]any{"email": m.UserEmail, "name": m.UserName, "mode": m.Mode, "updated_at": m.UpdatedAt})
	}
	return out
}

// handleReportModes records how an app actually connects each person to each
// target: "core", "direct" (a pasted URL and key Core can't switch) or "off".
// Report only, so the Links page can show the whole map.
//
//	PUT /api/v1/links/modes {"modes": [{"user": "a@b.c", "target": "money", "mode": "direct"}]}
func (s *Server) handleReportModes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller := principalFrom(ctx)
	if caller.App == nil {
		writeErr(w, http.StatusForbidden, "apps only")
		return
	}
	var in struct {
		Modes []struct {
			User   string `json:"user"`
			Target string `json:"target"`
			Mode   string `json:"mode"`
		} `json:"modes"`
	}
	if err := decode(r, &in); err != nil || len(in.Modes) > 2000 {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rows := []store.LinkMode{}
	skipped := 0
	for _, m := range in.Modes {
		target := strings.ToLower(strings.TrimSpace(m.Target))
		mode := strings.ToLower(strings.TrimSpace(m.Mode))
		typ, isSvc := serviceType(target)
		valid := (isSvc && slugRe.MatchString(typ)) || (!isSvc && slugRe.MatchString(target) && target != caller.App.Slug)
		if !valid || (mode != "core" && mode != "direct" && mode != "off") {
			skipped++
			continue
		}
		u, err := s.linkPerson(ctx, m.User)
		if err != nil {
			skipped++
			continue
		}
		rows = append(rows, store.LinkMode{UserID: u.ID, Target: target, Mode: mode})
	}
	if err := s.store.SetLinkModes(ctx, caller.App.ID, rows); err != nil {
		s.fail(w, err, "link modes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": len(rows), "skipped": skipped})
}

// handleUpdateLink flips the box-wide switch.
func (s *Server) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(r, &in); err != nil || in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "enabled required")
		return
	}
	l, err := s.store.GetLink(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "link")
		return
	}
	if err := s.store.SetLinkEnabled(r.Context(), l.ID, *in.Enabled); err != nil {
		s.fail(w, err, "link")
		return
	}
	l, _ = s.store.GetLink(r.Context(), l.ID)
	_ = s.hub.Publish(r.Context(), &store.Event{Topic: "core.link.changed", Source: "core", Subject: "app:" + l.AppSlug,
		Payload: mustJSON(map[string]any{"app": l.AppSlug, "target": l.TargetSlug, "scope": l.Scope, "enabled": l.Enabled})})
	writeJSON(w, http.StatusOK, s.linkView(r.Context(), l, nil))
}

// handleMyLinks lists the links that can act for the caller, with their own switch.
func (s *Server) handleMyLinks(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusForbidden, "people only")
		return
	}
	links, err := s.store.ListLinks(r.Context())
	if err != nil {
		s.fail(w, err, "links")
		return
	}
	modes := s.modesByPair(r.Context())
	out := []map[string]any{}
	for i := range links {
		v := s.linkView(r.Context(), &links[i], u)
		switch v["state"] {
		case linkNoTarget, linkNotOffered, linkNotInAudience:
			continue // nothing the person could switch
		}
		v["on_for_me"] = !s.store.LinkOptedOut(r.Context(), links[i].ID, u.ID)
		for _, m := range modes[[2]any{links[i].AppID, links[i].TargetSlug}] {
			if m["email"] == u.Email {
				v["my_mode"] = m["mode"]
			}
		}
		delete(v, "opt_outs")
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateMyLink is the person's own switch for a link.
func (s *Server) handleUpdateMyLink(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusForbidden, "people only")
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(r, &in); err != nil || in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "enabled required")
		return
	}
	l, err := s.store.GetLink(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "link")
		return
	}
	if err := s.store.SetLinkOptOut(r.Context(), l.ID, u.ID, !*in.Enabled); err != nil {
		s.fail(w, err, "link")
		return
	}
	v := s.linkView(r.Context(), l, u)
	v["on_for_me"] = *in.Enabled
	writeJSON(w, http.StatusOK, v)
}
