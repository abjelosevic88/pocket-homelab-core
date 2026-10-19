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
	if wantSSO != wasSSO {
		var err error
		if wantSSO {
			err = s.enableSSO(ctx, sv, c)
		} else {
			err = c.ConfigureSSO(ctx, s.connectorTarget(sv), connectors.SSOConfig{Enabled: false})
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
	return c.ConfigureSSO(ctx, s.connectorTarget(sv), connectors.SSOConfig{Enabled: true, Issuer: s.oidc.Issuer, ClientID: cl.ClientID, ClientSecret: secret, ButtonText: ssoButton})
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
	switch {
	case errors.Is(err, errPollBusy):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
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
		e := &store.Event{Topic: c.Type() + "." + ev.Name, Source: "connector:" + c.Type(), Subject: subject, Payload: mustJSON(payload)}
		if err := s.hub.Publish(ctx, e); err != nil {
			s.log.Warn("connector event not published", "topic", e.Topic, "err", err)
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

// connectorScheduler polls every enabled connector with events on, each at
// its own interval plus jitter.
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
		if !row.Enabled || !row.EventsEnabled {
			s.nextPoll.Delete(row.ServiceID)
			continue
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
		go func() { _, _, _ = s.pollConnector(ctx, sv, c) }()
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
	}
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	acc, err := c.Provision(pctx, s.connectorTarget(sv), connectors.Person{Email: u.Email, Name: u.Name, KeepOwnCredential: own})
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
			row.Status, row.RemoteID, row.Error, row.UpdatedAt = a.Status, a.RemoteID, a.Error, a.UpdatedAt
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
