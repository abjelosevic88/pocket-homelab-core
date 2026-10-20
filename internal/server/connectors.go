package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/oidc"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// ssoButton is what the app's login page shows.
const ssoButton = "Sign in with Pocket Core"

var errPollBusy = errors.New("a poll of this connector is already running")

// connectorTarget hands a connector what it needs and nothing else.
func (s *Server) connectorTarget(sv *store.Service) connectors.Target {
	base := sv.URL
	if base == "" {
		base = sv.PublicURL
	}
	return connectors.Target{BaseURL: base, PublicURL: sv.EffectivePublicURL(), Fields: sv.Fields, HTTP: s.connHTTP, DataDir: filepath.Join(s.cfg.DataDir, "connectors")}
}

func (s *Server) entryFor(ctx context.Context, sv *store.Service) catalog.Entry {
	prefix := ""
	if sv.AppID > 0 {
		if a, err := s.store.GetApp(ctx, sv.AppID); err == nil {
			prefix = a.EnvPrefix
		}
	}
	return catalog.Resolve(sv.Type, sv.Name, prefix, sv.AppID > 0)
}

// connectorRow returns the stored row, or the defaults for a never-configured one.
func (s *Server) connectorRow(ctx context.Context, serviceID int64) (*store.Connector, bool, error) {
	row, err := s.store.GetConnector(ctx, serviceID)
	if errors.Is(err, store.ErrNotFound) {
		return &store.Connector{ServiceID: serviceID, PollIntervalS: store.DefaultPollInterval}, false, nil
	}
	return row, err == nil, err
}

// loadConnector resolves {id} to the service and its connector, answering for the caller on failure.
func (s *Server) loadConnector(w http.ResponseWriter, r *http.Request) (*store.Service, connectors.Connector, *store.Connector, bool) {
	sv, err := s.store.GetService(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get service")
		return nil, nil, nil, false
	}
	c, ok := connectors.Get(sv.Type)
	if !ok {
		writeErr(w, http.StatusNotFound, "no connector for service type "+sv.Type)
		return nil, nil, nil, false
	}
	row, _, err := s.connectorRow(r.Context(), sv.ID)
	if err != nil {
		s.fail(w, err, "get connector")
		return nil, nil, nil, false
	}
	return sv, c, row, true
}

type connectorOut struct {
	store.Connector
	Type         string                  `json:"type"`
	ServiceName  string                  `json:"service_name"`
	Capabilities connectors.Capabilities `json:"capabilities"`
	Configured   bool                    `json:"configured"`
	Redirects    []string                `json:"redirects"`
	Issuer       string                  `json:"issuer"`
	OIDCClient   *store.OIDCClient       `json:"oidc_client"`
	Polling      bool                    `json:"polling"`
	Push         *hookView               `json:"push,omitempty"` // events arrive by push: the subscriptions and their counters
	KeyRotation  string                  `json:"key_rotation"`   // switching a service link off: "replaces" the connector-made key, "cannot", or ""
}

func (s *Server) connectorView(ctx context.Context, sv *store.Service, c connectors.Connector, row *store.Connector) connectorOut {
	_, cerr := s.store.GetConnector(ctx, sv.ID)
	out := connectorOut{Connector: *row, Type: c.Type(), ServiceName: sv.Name, Capabilities: c.Capabilities(), Configured: cerr == nil,
		Redirects: []string{}, Issuer: s.oidc.Issuer}
	if pub := sv.EffectivePublicURL(); pub != "" && out.Capabilities.SSO {
		out.Redirects = c.SSORedirects(pub)
	}
	if cl, err := s.store.GetOIDCClientByService(ctx, sv.ID); err == nil {
		out.OIDCClient = cl
	}
	_, out.Polling = s.polling.Load(sv.ID)
	out.KeyRotation = keyRotation(c.Type())
	if p, ok := c.(connectors.Pusher); ok && out.Capabilities.Events {
		out.Push = s.hookStatus(ctx, sv, p, row.Enabled && row.EventsEnabled)
	}
	return out
}

// handleListConnectors lists every connector Core knows and the service it would act on.
func (s *Server) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	type item struct {
		Type         string                  `json:"type"`
		Name         string                  `json:"name"`
		Capabilities connectors.Capabilities `json:"capabilities"`
		ServiceID    int64                   `json:"service_id,omitempty"`
		Connector    *connectorOut           `json:"connector,omitempty"`
	}
	out := []item{}
	for _, c := range connectors.All() {
		e := catalog.Resolve(c.Type(), "", "", false)
		it := item{Type: c.Type(), Name: e.Name, Capabilities: c.Capabilities()}
		if sv, err := s.store.GetServiceByType(ctx, c.Type()); err == nil {
			it.ServiceID = sv.ID
			if row, _, err := s.connectorRow(ctx, sv.ID); err == nil {
				v := s.connectorView(ctx, sv, c, row)
				it.Connector = &v
			}
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetConnector(w http.ResponseWriter, r *http.Request) {
	sv, c, row, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.connectorView(r.Context(), sv, c, row))
}

// handlePutConnector saves the switches. Turning sign-in on or off acts on
// the app right away and only sticks when the app accepted the change.
func (s *Server) handlePutConnector(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, c, row, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled           *bool `json:"enabled"`
		ProvisionOnCreate *bool `json:"provision_on_create"`
		SSOEnabled        *bool `json:"sso_enabled"`
		EventsEnabled     *bool `json:"events_enabled"`
		PollIntervalS     *int  `json:"poll_interval_s"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	caps := c.Capabilities()
	switch {
	case in.ProvisionOnCreate != nil && *in.ProvisionOnCreate && !caps.Provision,
		in.SSOEnabled != nil && *in.SSOEnabled && !caps.SSO,
		in.EventsEnabled != nil && *in.EventsEnabled && !caps.Events:
		writeErr(w, http.StatusBadRequest, "the "+c.Type()+" connector cannot do that")
		return
	}
	wasEvents, wasSSO := row.EventsEnabled, row.SSOEnabled
	wasOn := row.Enabled && row.EventsEnabled
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}
	if in.ProvisionOnCreate != nil {
		row.ProvisionOnCreate = *in.ProvisionOnCreate
	}
	if in.EventsEnabled != nil {
		row.EventsEnabled = *in.EventsEnabled
	}
	if in.PollIntervalS != nil {
		row.PollIntervalS = *in.PollIntervalS
	}
	wantSSO := wasSSO
	if in.SSOEnabled != nil {
		wantSSO = *in.SSOEnabled
	}
	if wantSSO && !wasSSO && !row.Enabled {
		writeErr(w, http.StatusBadRequest, "turn the connector on first")
		return
	}
	if err := s.store.SaveConnector(ctx, row); err != nil {
		s.fail(w, err, "save connector")
		return
	}
	if row.EventsEnabled && !wasEvents {
		// a fresh baseline: what happened while events were off is not news
		_ = s.store.ResetConnectorState(ctx, sv.ID)
		s.nextPoll.Store(sv.ID, time.Now().Add(5*time.Second))
	}
	if nowOn := row.Enabled && row.EventsEnabled; nowOn != wasOn {
		// push subscriptions follow the switch right away: created on, deleted off
		err := s.syncHooks(ctx, sv, c, hookReconcile)
		s.setEventsError(ctx, sv.ID, err)
		if err != nil {
			s.log.Warn("connector subscriptions not in step", "type", c.Type(), "events", nowOn, "err", err)
		}
	}
	if wantSSO != wasSSO {
		var err error
		if wantSSO {
			err = s.enableSSO(ctx, sv, c)
		} else {
			err = c.ConfigureSSO(ctx, s.connectorTarget(sv), connectors.SSOConfig{Enabled: false, People: s.ssoPeople(ctx, sv)})
		}
		if err != nil {
			_ = s.store.SetConnectorError(ctx, sv.ID, "sign-in: "+err.Error())
			s.log.Warn("connector sign-in change failed", "type", c.Type(), "enable", wantSSO, "err", err)
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		row.SSOEnabled = wantSSO
		if err := s.store.SaveConnector(ctx, row); err != nil {
			s.fail(w, err, "save connector")
			return
		}
		_ = s.store.SetConnectorError(ctx, sv.ID, "")
		s.log.Info("connector sign-in changed", "type", c.Type(), "enabled", wantSSO)
	}
	row, _, _ = s.connectorRow(ctx, sv.ID)
	writeJSON(w, http.StatusOK, s.connectorView(ctx, sv, c, row))
}

// enableSSO makes (or reuses) the service's OIDC client with the connector's
// redirect URIs, mints a fresh secret and hands it to the app.
func (s *Server) enableSSO(ctx context.Context, sv *store.Service, c connectors.Connector) error {
	pub := sv.EffectivePublicURL()
	if pub == "" {
		return errors.New("set the service's public URL first: it is where people sign in")
	}
	cl, err := s.store.GetOIDCClientByService(ctx, sv.ID)
	if errors.Is(err, store.ErrNotFound) {
		cl = &store.OIDCClient{Name: sv.Name + " (connector)", ServiceID: sv.ID}
	} else if err != nil {
		return err
	}
	cl.RedirectURIs = c.SSORedirects(pub)
	secret, err := s.store.SaveOIDCClient(ctx, cl, true)
	if err != nil {
		return err
	}
	return c.ConfigureSSO(ctx, s.connectorTarget(sv), connectors.SSOConfig{Enabled: true, Issuer: s.oidc.Issuer, ClientID: cl.ClientID, ClientSecret: secret, ButtonText: ssoButton,
		Links: s.ssoLinks(ctx, sv), People: s.ssoPeople(ctx, sv)})
}

// ssoPeople maps each person in the audience (by email) to the app account
// linked to them; "" when none.
func (s *Server) ssoPeople(ctx context.Context, sv *store.Service) map[string]string {
	out := map[string]string{}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return out
	}
	accts, _ := s.store.ListConnectorAccounts(ctx, sv.ID)
	byUser := map[int64]store.ConnectorAccount{}
	for _, a := range accts {
		byUser[a.UserID] = a
	}
	for i := range users {
		u := &users[i]
		if u.Disabled || !sv.InAudience(u) {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(u.Email))
		if a, ok := byUser[u.ID]; ok && a.RemoteID != "" && a.Status != connectors.StatusError {
			out[email] = a.RemoteID
		} else {
			out[email] = ""
		}
	}
	return out
}

// ssoLinks maps the username Core sends at sign-in (preferred_username) to
// the app account linked to each person in the audience; "" when none.
func (s *Server) ssoLinks(ctx context.Context, sv *store.Service) map[string]string {
	out := map[string]string{}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return out
	}
	accts, _ := s.store.ListConnectorAccounts(ctx, sv.ID)
	byUser := map[int64]store.ConnectorAccount{}
	for _, a := range accts {
		byUser[a.UserID] = a
	}
	for i := range users {
		u := &users[i]
		name := fmt.Sprint(oidc.Claims(u)["preferred_username"])
		a, ok := byUser[u.ID]
		if ok && a.RemoteID != "" && a.Status != connectors.StatusError && !u.Disabled && sv.InAudience(u) {
			out[name] = a.RemoteID
		} else if _, set := out[name]; !set {
			out[name] = ""
		}
	}
	return out
}

func (s *Server) handleTestConnector(w http.ResponseWriter, r *http.Request) {
	sv, c, _, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	start := time.Now()
	info, err := c.Test(ctx, s.connectorTarget(sv))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "ms": time.Since(start).Milliseconds()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "info": info, "ms": time.Since(start).Milliseconds()})
}

func (s *Server) handlePollConnector(w http.ResponseWriter, r *http.Request) {
	sv, c, row, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	if !row.Enabled {
		writeErr(w, http.StatusBadRequest, "turn the connector on first")
		return
	}
	if !c.Capabilities().Events {
		writeErr(w, http.StatusBadRequest, "the "+c.Type()+" connector has no events")
		return
	}
	n, baseline, err := s.pollConnector(r.Context(), sv, c)
	if errors.Is(err, errPollBusy) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	var herr error
	if row.EventsEnabled {
		herr = s.syncHooks(r.Context(), sv, c, hookReconcile)
		if err == nil {
			s.setEventsError(r.Context(), sv.ID, herr)
		}
	}
	switch {
	case err != nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	case herr != nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "subscriptions: " + herr.Error(), "events": n, "baseline": baseline})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "events": n, "baseline": baseline})
	}
}

// pollConnector runs one poll: never two at once per service, with a
// timeout, publishing what changed and saving the new state.
func (s *Server) pollConnector(ctx context.Context, sv *store.Service, c connectors.Connector) (published int, baseline bool, err error) {
	if _, busy := s.polling.LoadOrStore(sv.ID, struct{}{}); busy {
		return 0, false, errPollBusy
	}
	defer s.polling.Delete(sv.ID)
	row, err := s.store.GetConnector(ctx, sv.ID) // fresh state, read under the lock
	if err != nil {
		return 0, false, err
	}
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	baseline = strings.TrimSpace(row.State) == ""
	evs, next, err := c.Poll(pctx, s.connectorTarget(sv), json.RawMessage(row.State))
	if err != nil {
		_ = s.store.SaveConnectorPoll(ctx, sv.ID, nil, err.Error())
		s.log.Warn("connector poll failed", "type", c.Type(), "err", err)
		return 0, baseline, err
	}
	for _, ev := range evs {
		if ev.UserEmail == "" && ev.RemoteUserID != "" {
			ev.UserEmail = s.personForRemote(ctx, sv, ev.RemoteUserID)
		}
		if ev.Personal && ev.UserEmail == "" {
			continue // someone's own activity, and Core knows no one for that account
		}
		if err := s.publishConnectorEvent(ctx, c, ev); err != nil {
			s.log.Warn("connector event not published", "type", c.Type(), "event", ev.Name, "err", err)
			continue
		}
		published++
	}
	st := string(next)
	if err := s.store.SaveConnectorPoll(ctx, sv.ID, &st, ""); err != nil {
		return published, baseline, err
	}
	return published, baseline, nil
}

// publishConnectorEvent puts one connector event on the bus as
// <type>.<name> from connector:<type>, with user_email when it is a person's.
func (s *Server) publishConnectorEvent(ctx context.Context, c connectors.Connector, ev connectors.Event) error {
	payload := map[string]any{}
	for k, v := range ev.Payload {
		payload[k] = v
	}
	subject := ev.Subject
	if ev.UserEmail != "" {
		payload["user_email"] = ev.UserEmail
		if subject == "" {
			subject = "user:" + ev.UserEmail
		}
	}
	return s.hub.Publish(ctx, &store.Event{Topic: c.Type() + "." + ev.Name, Source: "connector:" + c.Type(), Subject: subject, Payload: mustJSON(payload)})
}

// connectorScheduler checks every enabled connector with events on, each at
// its own interval plus jitter: a poll for what pushes do not cover, then a
// subscription sync that re-creates what was deleted in the app. A connector
// switched off that still has subscriptions keeps being retried until they
// are gone.
func (s *Server) connectorScheduler(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scheduleConnectors(ctx)
		}
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d)))
}

func (s *Server) scheduleConnectors(ctx context.Context) {
	rows, err := s.store.ListConnectors(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, row := range rows {
		on := row.Enabled && row.EventsEnabled
		if !on {
			if hs, err := s.store.ListConnectorHooks(ctx, row.ServiceID); err != nil || len(hs) == 0 {
				s.nextPoll.Delete(row.ServiceID)
				continue
			}
		}
		interval := time.Duration(row.PollIntervalS) * time.Second
		v, seen := s.nextPoll.Load(row.ServiceID)
		if !seen {
			// first sight since start: not all at once, and not sooner than the interval allows
			next := now.Add(10*time.Second + jitter(50*time.Second))
			if lp, err := time.Parse(time.RFC3339Nano, row.LastPollAt); err == nil && lp.Add(interval).After(next) {
				next = lp.Add(interval + jitter(interval/10))
			}
			s.nextPoll.Store(row.ServiceID, next)
			continue
		}
		if now.Before(v.(time.Time)) {
			continue
		}
		s.nextPoll.Store(row.ServiceID, now.Add(interval+jitter(interval/10)))
		sv, err := s.store.GetService(ctx, row.ServiceID)
		if err != nil {
			continue
		}
		c, ok := connectors.Get(sv.Type)
		if !ok || !c.Capabilities().Events {
			continue
		}
		go s.checkConnector(ctx, sv, c, on)
	}
}

// checkConnector is one scheduled round for a service.
func (s *Server) checkConnector(ctx context.Context, sv *store.Service, c connectors.Connector, on bool) {
	if on {
		if _, _, err := s.pollConnector(ctx, sv, c); err != nil {
			if !errors.Is(err, errPollBusy) {
				_ = s.syncHooks(ctx, sv, c, hookReconcile) // keep the subscriptions healthy; the poll error stays shown
			}
			return
		}
	}
	err := s.syncHooks(ctx, sv, c, hookReconcile)
	s.setEventsError(ctx, sv.ID, err)
	if err != nil {
		s.log.Warn("connector subscriptions not in step", "type", c.Type(), "err", err)
	}
}

// ---- provisioning ----

type provisionResult struct {
	UserID     int64  `json:"user_id"`
	Email      string `json:"email"`
	Status     string `json:"status"` // ok | linked | error | skipped
	RemoteID   string `json:"remote_id,omitempty"`
	Error      string `json:"error,omitempty"`
	Note       string `json:"note,omitempty"`
	Credential string `json:"credential"` // connector (stored as their override) | own (theirs kept) | none
}

func (s *Server) handleProvisionConnector(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, c, row, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	var in struct {
		UserID int64 `json:"user_id"`
		All    bool  `json:"all"`
	}
	if err := decode(r, &in); err != nil || (in.UserID == 0 && !in.All) {
		writeErr(w, http.StatusBadRequest, `{"user_id": N} or {"all": true} required`)
		return
	}
	if !row.Enabled {
		writeErr(w, http.StatusBadRequest, "turn the connector on first")
		return
	}
	if !c.Capabilities().Provision {
		writeErr(w, http.StatusBadRequest, "the "+c.Type()+" connector cannot make accounts")
		return
	}
	var users []store.User
	if in.All {
		list, err := s.store.ListUsers(ctx)
		if err != nil {
			s.fail(w, err, "list users")
			return
		}
		accts, _ := s.store.ListConnectorAccounts(ctx, sv.ID)
		done := map[int64]bool{}
		for _, a := range accts {
			done[a.UserID] = a.Status != connectors.StatusError
		}
		for _, u := range list {
			if !done[u.ID] && !u.Disabled && sv.InAudience(&u) {
				users = append(users, u)
			}
		}
	} else {
		u, err := s.store.GetUser(ctx, in.UserID)
		if err != nil {
			s.fail(w, err, "get user")
			return
		}
		users = []store.User{*u}
	}
	out := []provisionResult{}
	for i := range users {
		out = append(out, s.provisionOne(ctx, sv, c, &users[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// provisionNewUser runs, in the background, every connector set to make
// accounts for new people.
func (s *Server) provisionNewUser(userID int64) {
	go func() {
		ctx := s.ctx
		rows, err := s.store.ListConnectors(ctx)
		if err != nil {
			return
		}
		for _, row := range rows {
			if !row.Enabled || !row.ProvisionOnCreate {
				continue
			}
			sv, err := s.store.GetService(ctx, row.ServiceID)
			if err != nil {
				continue
			}
			c, ok := connectors.Get(sv.Type)
			if !ok || !c.Capabilities().Provision {
				continue
			}
			u, err := s.store.GetUser(ctx, userID)
			if err != nil {
				return
			}
			res := s.provisionOne(ctx, sv, c, u)
			s.log.Info("connector provisioned new person", "type", c.Type(), "user", u.Email, "status", res.Status)
		}
	}()
}

// provisionOne makes or links one person's account. Only people in the
// service's audience; a person's own credential is never replaced; an
// account Core already provisioned is not touched again.
func (s *Server) provisionOne(ctx context.Context, sv *store.Service, c connectors.Connector, u *store.User) provisionResult {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	res := provisionResult{UserID: u.ID, Email: u.Email, Credential: "none"}
	if u.Disabled {
		res.Status, res.Note = "skipped", "disabled account"
		return res
	}
	if !sv.InAudience(u) {
		res.Status, res.Note = "skipped", "not in the audience of "+sv.Name
		return res
	}
	ovs, _ := s.store.ListOverrides(ctx, u.ID)
	ov, hasOv := ovs[sv.ID]
	own := hasOv && ov.Source != store.OverrideByConnector
	if own {
		res.Credential = "own"
	} else if hasOv {
		res.Credential = "connector"
	}
	if a, err := s.store.GetConnectorAccount(ctx, sv.ID, u.ID); err == nil && a.Status == connectors.StatusOK {
		res.Status, res.RemoteID, res.Note = a.Status, a.RemoteID, "already provisioned"
		return res
	} else if err == nil && a.Manual {
		res.Status, res.RemoteID, res.Note = a.Status, a.RemoteID, "linked by hand; unlink first to provision"
		return res
	}
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	acc, err := c.Provision(pctx, s.connectorTarget(sv), connectors.Person{Email: u.Email, Name: u.Name, KeepOwnCredential: own, Linked: s.linkedElsewhere(ctx, u.ID, sv.ID)})
	rec := &store.ConnectorAccount{ServiceID: sv.ID, UserID: u.ID, RemoteID: acc.RemoteID, Status: acc.Status}
	if err != nil {
		rec.Status, rec.Error = connectors.StatusError, err.Error()
		_ = s.store.SaveConnectorAccount(ctx, rec)
		s.log.Warn("connector provisioning failed", "type", c.Type(), "user", u.Email, "err", err)
		res.Status, res.Error, res.RemoteID = rec.Status, rec.Error, rec.RemoteID
		return res
	}
	if rec.Status != connectors.StatusLinked {
		rec.Status = connectors.StatusOK
	}
	if err := s.store.SaveConnectorAccount(ctx, rec); err != nil {
		res.Status, res.Error = connectors.StatusError, err.Error()
		return res
	}
	res.Status, res.RemoteID, res.Note = rec.Status, rec.RemoteID, acc.Note
	if len(acc.Fields) > 0 && !own {
		o := &store.Override{ServiceID: sv.ID, UserID: u.ID, Fields: acc.Fields, Source: store.OverrideByConnector}
		if hasOv {
			o.URL = ov.URL
		}
		if err := s.store.SaveOverride(ctx, o, s.entryFor(ctx, sv).IsSecret); err != nil {
			rec.Status, rec.Error = connectors.StatusError, "account made but its credential could not be stored: "+err.Error()
			_ = s.store.SaveConnectorAccount(ctx, rec)
			res.Status, res.Error = rec.Status, rec.Error
			return res
		}
		res.Credential = "connector"
	}
	if row, err := s.store.GetConnector(ctx, sv.ID); err == nil && row.Enabled && row.EventsEnabled {
		s.kickHooks(sv.ID) // a newly connected person gets their subscriptions
	}
	_ = s.hub.Publish(ctx, &store.Event{Topic: "core.connector.provisioned", Source: "core", Subject: fmt.Sprintf("user:%s", u.Email),
		Payload: mustJSON(map[string]any{"type": c.Type(), "service_id": sv.ID, "user_email": u.Email, "status": res.Status, "created": acc.Created})})
	return res
}

// accountRow is one person in a connector's people table.
type accountRow struct {
	UserID     int64  `json:"user_id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Disabled   bool   `json:"disabled"`
	InAudience bool   `json:"in_audience"`
	Status     string `json:"status"` // ok | linked | error | "" (not yet)
	RemoteID   string `json:"remote_id"`
	Error      string `json:"error"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Credential string `json:"credential"` // connector | own | ""
	Manual     bool   `json:"manual"`     // linked by hand
	// replacing the connector-made key after a service link was switched off
	KeyReplacedAt string `json:"key_replaced_at,omitempty"`
	KeyError      string `json:"key_error,omitempty"`
	KeyNote       string `json:"key_note,omitempty"`
}

func (s *Server) handleConnectorAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, _, _, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		s.fail(w, err, "list users")
		return
	}
	accts, err := s.store.ListConnectorAccounts(ctx, sv.ID)
	if err != nil {
		s.fail(w, err, "list accounts")
		return
	}
	byUser := map[int64]store.ConnectorAccount{}
	for _, a := range accts {
		byUser[a.UserID] = a
	}
	out := []accountRow{}
	for i := range users {
		u := &users[i]
		a, has := byUser[u.ID]
		in := sv.InAudience(u)
		if !in && !has {
			continue
		}
		row := accountRow{UserID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, Disabled: u.Disabled, InAudience: in}
		if has {
			row.Status, row.RemoteID, row.Error, row.UpdatedAt, row.Manual = a.Status, a.RemoteID, a.Error, a.UpdatedAt, a.Manual
			row.KeyReplacedAt, row.KeyError, row.KeyNote = a.KeyReplacedAt, a.KeyError, a.KeyNote
		}
		if ovs, err := s.store.ListOverrides(ctx, u.ID); err == nil {
			if ov, ok := ovs[sv.ID]; ok {
				row.Credential = ov.Source
				if row.Credential == store.OverrideByUser {
					row.Credential = "own"
				}
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUserConnectors is one person's status across every connected service.
func (s *Server) handleUserConnectors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := s.store.GetUser(ctx, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	accts, _ := s.store.ListUserConnectorAccounts(ctx, u.ID)
	byService := map[int64]store.ConnectorAccount{}
	for _, a := range accts {
		byService[a.ServiceID] = a
	}
	ovs, _ := s.store.ListOverrides(ctx, u.ID)
	type item struct {
		ServiceID   int64  `json:"service_id"`
		ServiceName string `json:"service_name"`
		Type        string `json:"type"`
		Enabled     bool   `json:"enabled"`
		InAudience  bool   `json:"in_audience"`
		Status      string `json:"status"`
		RemoteID    string `json:"remote_id"`
		Error       string `json:"error"`
		UpdatedAt   string `json:"updated_at,omitempty"`
		Credential  string `json:"credential"`
	}
	out := []item{}
	for _, c := range connectors.All() {
		sv, err := s.store.GetServiceByType(ctx, c.Type())
		if err != nil {
			continue
		}
		row, _, _ := s.connectorRow(ctx, sv.ID)
		it := item{ServiceID: sv.ID, ServiceName: sv.Name, Type: c.Type(), Enabled: row != nil && row.Enabled, InAudience: sv.InAudience(u)}
		if a, ok := byService[sv.ID]; ok {
			it.Status, it.RemoteID, it.Error, it.UpdatedAt = a.Status, a.RemoteID, a.Error, a.UpdatedAt
		}
		if ov, ok := ovs[sv.ID]; ok {
			it.Credential = ov.Source
			if it.Credential == store.OverrideByUser {
				it.Credential = "own"
			}
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

// linkedElsewhere is the person's accounts in the other services Core
// connected them to: service type → account id.
func (s *Server) linkedElsewhere(ctx context.Context, userID, except int64) map[string]string {
	out := map[string]string{}
	accts, err := s.store.ListUserConnectorAccounts(ctx, userID)
	if err != nil {
		return out
	}
	for _, a := range accts {
		if a.ServiceID == except || a.RemoteID == "" || a.Status == connectors.StatusError {
			continue
		}
		if sv, err := s.store.GetService(ctx, a.ServiceID); err == nil && sv.Enabled {
			out[sv.Type] = a.RemoteID
		}
	}
	return out
}

// personForRemote finds the Core person linked to an app account, for
// events from apps without emails. Only people who are enabled and in the
// service's audience are named.
func (s *Server) personForRemote(ctx context.Context, sv *store.Service, remoteID string) string {
	accts, err := s.store.ListConnectorAccounts(ctx, sv.ID)
	if err != nil {
		return ""
	}
	for _, a := range accts {
		if a.RemoteID != remoteID || a.Status == connectors.StatusError {
			continue
		}
		u, err := s.store.GetUser(ctx, a.UserID)
		if err != nil || u.Disabled || !sv.InAudience(u) {
			return ""
		}
		return u.Email
	}
	return ""
}

// handleRemoteAccounts lists the app's accounts for linking by hand.
func (s *Server) handleRemoteAccounts(w http.ResponseWriter, r *http.Request) {
	sv, c, _, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	l, isLinker := c.(connectors.Linker)
	if !isLinker {
		writeErr(w, http.StatusBadRequest, "the "+c.Type()+" connector matches people by email; there is nothing to pick")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := l.RemoteAccounts(ctx, s.connectorTarget(sv))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleLinkAccount ties a person to an existing app account by hand
// ({"remote_id": "…"}), or unlinks them ({"remote_id": ""}). A credential
// the person set themselves is never touched.
func (s *Server) handleLinkAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, c, row, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	l, isLinker := c.(connectors.Linker)
	if !isLinker {
		writeErr(w, http.StatusBadRequest, "the "+c.Type()+" connector matches people by email")
		return
	}
	var in struct {
		RemoteID *string `json:"remote_id"`
	}
	if err := decode(r, &in); err != nil || in.RemoteID == nil {
		writeErr(w, http.StatusBadRequest, `{"remote_id": "…"} required ("" unlinks)`)
		return
	}
	u, err := s.store.GetUser(ctx, idParam(r, "uid"))
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	s.provMu.Lock()
	defer s.provMu.Unlock()
	ovs, _ := s.store.ListOverrides(ctx, u.ID)
	ov, hasOv := ovs[sv.ID]
	byConnector := hasOv && ov.Source == store.OverrideByConnector
	remote := strings.TrimSpace(*in.RemoteID)
	if remote == "" {
		if err := s.store.DeleteConnectorAccount(ctx, sv.ID, u.ID); err != nil {
			s.fail(w, err, "unlink")
			return
		}
		if byConnector {
			_ = s.store.DeleteOverride(ctx, sv.ID, u.ID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "", "remote_id": "", "note": s.reapplySSO(ctx, sv, c, row)})
		return
	}
	if !row.Enabled {
		writeErr(w, http.StatusBadRequest, "turn the connector on first")
		return
	}
	if !sv.InAudience(u) {
		writeErr(w, http.StatusBadRequest, u.Email+" is not in the audience of "+sv.Name)
		return
	}
	accts, _ := s.store.ListConnectorAccounts(ctx, sv.ID)
	for _, a := range accts {
		if a.RemoteID == remote && a.UserID != u.ID && a.Status != connectors.StatusError {
			writeErr(w, http.StatusConflict, "that account is already linked to someone else; unlink them first")
			return
		}
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	acc, err := l.Link(lctx, s.connectorTarget(sv), remote)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	rec := &store.ConnectorAccount{ServiceID: sv.ID, UserID: u.ID, RemoteID: acc.RemoteID, Status: connectors.StatusLinked, Manual: true}
	if err := s.store.SaveConnectorAccount(ctx, rec); err != nil {
		s.fail(w, err, "link")
		return
	}
	cred := "none"
	if hasOv && !byConnector {
		cred = "own"
	} else if len(acc.Fields) > 0 {
		o := &store.Override{ServiceID: sv.ID, UserID: u.ID, Fields: acc.Fields, Source: store.OverrideByConnector}
		if hasOv {
			o.URL = ov.URL
		}
		if err := s.store.SaveOverride(ctx, o, s.entryFor(ctx, sv).IsSecret); err != nil {
			s.fail(w, err, "save credential")
			return
		}
		cred = "connector"
	}
	note := acc.Note
	if n := s.reapplySSO(ctx, sv, c, row); n != "" {
		note += "; " + n
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": rec.Status, "remote_id": rec.RemoteID, "note": note, "credential": cred})
}

// reapplySSO hands the app the new links while sign-in is on, so a person
// linked by hand lands in that account at their next sign-in.
func (s *Server) reapplySSO(ctx context.Context, sv *store.Service, c connectors.Connector, row *store.Connector) string {
	if !row.Enabled || !row.SSOEnabled {
		return ""
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	if err := s.enableSSO(sctx, sv, c); err != nil {
		s.log.Warn("connector sign-in links not updated", "type", c.Type(), "err", err)
		return "sign-in links NOT updated: " + err.Error()
	}
	return "sign-in now lands in this account"
}
