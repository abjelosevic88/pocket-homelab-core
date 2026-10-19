package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Push events: a connector that implements connectors.Pusher gets
// subscriptions in its app (Immich: workflows with a webhook step) that call
// POST /api/v1/connectors/hooks/{id} with a per-hook secret. Core keeps the
// subscriptions in step with the Events switch, the people connected and
// the audience (syncHooks), and turns each delivery into one event.

const (
	hookBodyLimit    = 256 << 10
	hookDedupeWindow = 24 * time.Hour
	hookRatePerMin   = 1200 // per hook; a phone's first backup is bursty, abuse is not
)

type hookMode int

const (
	hookReconcile hookMode = iota // create what is missing, delete what should not exist
	hookRecreate                  // delete and create every subscription again (new secrets)
	hookTeardown                  // delete everything, whatever the switches say
)

// hookPersonState is what the last sync found for one person (0 = global).
type hookPersonState struct {
	State  string // ok | needs_key | error | off
	Detail string
}

type hookSyncResult struct {
	At     time.Time
	People map[int64]hookPersonState
}

// seenCache remembers delivery keys for the dedupe window.
type seenCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
}

func newSeenCache(ttl time.Duration) *seenCache {
	return &seenCache{ttl: ttl, m: map[string]time.Time{}}
}

// first reports whether key is new inside the window, and remembers it.
func (c *seenCache) first(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if at, ok := c.m[key]; ok && now.Sub(at) < c.ttl {
		return false
	}
	if len(c.m) > 50000 {
		for k, at := range c.m {
			if now.Sub(at) >= c.ttl {
				delete(c.m, k)
			}
		}
		if len(c.m) > 200000 { // forgetful under pressure rather than unbounded
			c.m = map[string]time.Time{}
		}
	}
	c.m[key] = now
	return true
}

func (c *seenCache) forget(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

func (s *Server) hookURL(id string) string {
	return strings.TrimRight(s.cfg.BaseURL, "/") + "/api/v1/connectors/hooks/" + id
}

// withFields is t acting with fields laid over the box-wide ones.
func withFields(t connectors.Target, over map[string]string) connectors.Target {
	f := make(map[string]string, len(t.Fields)+len(over))
	for k, v := range t.Fields {
		f[k] = v
	}
	for k, v := range over {
		if v != "" {
			f[k] = v
		}
	}
	t.Fields = f
	return t
}

// boxWho asks, at most once per sync, whose account the box-wide credential is.
type boxWho struct {
	once sync.Once
	who  connectors.RemoteUser
	err  error
}

func (b *boxWho) get(ctx context.Context, p connectors.Pusher, t connectors.Target) (connectors.RemoteUser, error) {
	b.once.Do(func() { b.who, b.err = p.WhoAmI(ctx, t) })
	return b.who, b.err
}

// personTarget picks the credential a person's subscriptions are made with:
// their own (a connector-minted or hand-set override), else the box-wide one
// when it is that very person's account. Anything else needs a key.
func (s *Server) personTarget(ctx context.Context, sv *store.Service, base connectors.Target, p connectors.Pusher, box *boxWho, u *store.User, acct store.ConnectorAccount) (connectors.Target, hookPersonState) {
	ovs, _ := s.store.ListOverrides(ctx, u.ID)
	if ov, ok := ovs[sv.ID]; ok && len(ov.Fields) > 0 {
		t := withFields(base, ov.Fields)
		who, err := p.WhoAmI(ctx, t)
		if err != nil {
			return t, hookPersonState{State: "error", Detail: err.Error()}
		}
		if who.ID != acct.RemoteID {
			return t, hookPersonState{State: "error", Detail: fmt.Sprintf("their key in Core belongs to another %s account (%s)", sv.Name, who.Email)}
		}
		return t, hookPersonState{}
	}
	who, err := box.get(ctx, p, base)
	if err == nil && who.ID == acct.RemoteID {
		return base, hookPersonState{}
	}
	return base, hookPersonState{State: "needs_key", Detail: fmt.Sprintf("Core has no %s key of theirs: provision them, or set their own key under their credentials", sv.Name)}
}

// removalTarget is the credential to delete a person's subscriptions with.
func (s *Server) removalTarget(ctx context.Context, sv *store.Service, base connectors.Target, p connectors.Pusher, box *boxWho, uid int64) (connectors.Target, bool) {
	if uid == 0 {
		return base, true
	}
	if ovs, err := s.store.ListOverrides(ctx, uid); err == nil {
		if ov, ok := ovs[sv.ID]; ok && len(ov.Fields) > 0 {
			return withFields(base, ov.Fields), true
		}
	}
	acct, err := s.store.GetConnectorAccount(ctx, sv.ID, uid)
	if err != nil {
		return base, false
	}
	if who, err := box.get(ctx, p, base); err == nil && who.ID == acct.RemoteID {
		return base, true
	}
	return base, false
}

// syncHooks brings the app's subscriptions in line with what should exist.
// One sync at a time box-wide; errors are per person and summed up.
func (s *Server) syncHooks(ctx context.Context, sv *store.Service, c connectors.Connector, mode hookMode) error {
	p, ok := c.(connectors.Pusher)
	if !ok {
		return nil
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	// a caller hanging up must not leave half the subscriptions made
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	row, _, err := s.connectorRow(ctx, sv.ID)
	if err != nil {
		return err
	}
	on := row.Enabled && row.EventsEnabled && mode != hookTeardown
	hooks, err := s.store.ListConnectorHooks(ctx, sv.ID)
	if err != nil {
		return err
	}
	byUser := map[int64][]store.ConnectorHook{}
	for _, h := range hooks {
		byUser[h.UserID] = append(byUser[h.UserID], h)
	}
	base := s.connectorTarget(sv)
	box := &boxWho{}
	res := hookSyncResult{At: time.Now(), People: map[int64]hookPersonState{}}
	desired := map[int64]connectors.Target{}
	hold := map[int64]bool{} // could not check this person now: leave their subscriptions as they are
	labels := map[int64]string{0: sv.Name}
	if on && !p.PushPerUser() {
		desired[0] = base
	} else if on {
		users, err := s.store.ListUsers(ctx)
		if err != nil {
			return err
		}
		accts, err := s.store.ListConnectorAccounts(ctx, sv.ID)
		if err != nil {
			return err
		}
		byID := map[int64]store.ConnectorAccount{}
		for _, a := range accts {
			byID[a.UserID] = a
		}
		for i := range users {
			u := &users[i]
			labels[u.ID] = u.Email
			a, has := byID[u.ID]
			if !has || a.Status == connectors.StatusError || a.RemoteID == "" {
				continue // not connected: nothing to subscribe for
			}
			switch {
			case u.Disabled:
				res.People[u.ID] = hookPersonState{State: "off", Detail: "disabled account"}
			case !sv.InAudience(u):
				res.People[u.ID] = hookPersonState{State: "off", Detail: "outside the audience of " + sv.Name}
			default:
				t, st := s.personTarget(ctx, sv, base, p, box, u, a)
				if st.State != "" {
					res.People[u.ID] = st
					hold[u.ID] = st.State == "error"
				} else {
					desired[u.ID] = t
				}
			}
		}
	}
	var errs []string
	// Delete what should not exist (person gone, outside the audience, no key any more, or events off).
	for uid, hs := range byUser {
		if _, keep := desired[uid]; keep || hold[uid] {
			continue
		}
		t, can := s.removalTarget(ctx, sv, base, p, box, uid)
		for _, h := range hs {
			if can {
				if err := p.Unsubscribe(ctx, t, h.RemoteWorkflowID); err != nil {
					msg := "could not delete it in " + sv.Name + ": " + err.Error()
					_ = s.store.SetConnectorHookCheck(ctx, h.ID, "error", msg)
					errs = append(errs, fmt.Sprintf("%s: %s", labelFor(labels, uid), msg))
					continue
				}
			} else {
				s.log.Warn("connector hook dropped without deleting it in the app (no credential left)", "type", c.Type(), "user_id", uid, "remote_id", h.RemoteWorkflowID)
			}
			_ = s.store.DeleteConnectorHook(ctx, h.ID)
			s.log.Info("connector hook removed", "type", c.Type(), "user_id", uid, "trigger", h.Trigger)
		}
	}
	// Create or heal what should exist.
	ids := make([]int64, 0, len(desired))
	for uid := range desired {
		ids = append(ids, uid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, uid := range ids {
		st := s.ensureHooks(ctx, sv, c, p, uid, desired[uid], byUser[uid], mode == hookRecreate)
		res.People[uid] = st
		if st.State == "error" {
			errs = append(errs, fmt.Sprintf("%s: %s", labelFor(labels, uid), st.Detail))
		}
	}
	s.hookSyncs.Store(sv.ID, res)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func labelFor(labels map[int64]string, uid int64) string {
	if l, ok := labels[uid]; ok {
		return l
	}
	return fmt.Sprintf("person %d", uid)
}

// ensureHooks makes sure one person's (or the global) subscriptions exist.
func (s *Server) ensureHooks(ctx context.Context, sv *store.Service, c connectors.Connector, p connectors.Pusher, uid int64, t connectors.Target, have []store.ConnectorHook, recreate bool) hookPersonState {
	subs, err := p.Subscriptions(ctx, t)
	if err != nil {
		for _, h := range have {
			_ = s.store.SetConnectorHookCheck(ctx, h.ID, "error", err.Error())
		}
		return hookPersonState{State: "error", Detail: err.Error()}
	}
	byTrigger := map[string]store.ConnectorHook{}
	recorded := map[string]bool{}
	for _, h := range have {
		byTrigger[h.Trigger] = h
		recorded[h.RemoteWorkflowID] = true
	}
	triggers := p.PushTriggers()
	ours := map[string]bool{}
	for _, tr := range triggers {
		ours[tr.Name] = true
	}
	// Subscriptions with our names that Core has no record of (a restored
	// database, a crash between create and save) can never authenticate: remove them.
	for id, name := range subs {
		if ours[name] && !recorded[id] {
			if err := p.Unsubscribe(ctx, t, id); err == nil {
				s.log.Info("removed an orphaned connector subscription", "type", c.Type(), "user_id", uid, "remote_id", id)
			}
		}
	}
	var errs []string
	healed := 0
	for _, tr := range triggers {
		h, known := byTrigger[tr.Trigger]
		if known && !recreate {
			if _, present := subs[h.RemoteWorkflowID]; present {
				_ = s.store.SetConnectorHookCheck(ctx, h.ID, "ok", "")
				continue
			}
			healed++
		}
		if known && recreate {
			_ = p.Unsubscribe(ctx, t, h.RemoteWorkflowID)
		}
		id := h.ID
		if !known {
			id = crypto.Token("", 18)
		}
		secret := crypto.Token("", 32)
		rid, err := p.Subscribe(ctx, t, connectors.Subscription{Trigger: tr.Trigger, Name: tr.Name, URL: s.hookURL(id),
			Description: "Managed by Pocket Core (" + s.cfg.BaseURL + "): sends each asset to Core's event bus. Turn Events off in Core to remove it.",
			HeaderName:  connectors.PushHeader, HeaderValue: secret})
		if err != nil {
			if known {
				_ = s.store.SetConnectorHookCheck(ctx, h.ID, "error", err.Error())
			}
			errs = append(errs, err.Error())
			continue
		}
		rec := &store.ConnectorHook{ID: id, ServiceID: sv.ID, UserID: uid, Trigger: tr.Trigger, RemoteWorkflowID: rid, SecretHash: crypto.Hash(secret)}
		if err := s.store.SaveConnectorHook(ctx, rec); err != nil {
			_ = p.Unsubscribe(ctx, t, rid) // never leave a subscription Core cannot authenticate
			errs = append(errs, "saving the hook failed: "+err.Error())
			continue
		}
		s.log.Info("connector hook created", "type", c.Type(), "user_id", uid, "trigger", tr.Trigger, "remote_id", rid, "healed", known)
	}
	if len(errs) > 0 {
		return hookPersonState{State: "error", Detail: strings.Join(errs, "; ")}
	}
	if healed > 0 {
		return hookPersonState{State: "ok", Detail: fmt.Sprintf("re-created %d missing", healed)}
	}
	return hookPersonState{State: "ok"}
}

// kickHooks runs a sync for a service in the background, coalescing bursts.
func (s *Server) kickHooks(serviceID int64) {
	if _, pending := s.hookKick.LoadOrStore(serviceID, struct{}{}); pending {
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.hookKick.Delete(serviceID)
		ctx := s.ctx
		row, err := s.store.GetConnector(ctx, serviceID)
		if err != nil {
			return
		}
		hs, _ := s.store.ListConnectorHooks(ctx, serviceID)
		if !(row.Enabled && row.EventsEnabled) && len(hs) == 0 {
			return
		}
		sv, err := s.store.GetService(ctx, serviceID)
		if err != nil {
			return
		}
		c, ok := connectors.Get(sv.Type)
		if !ok {
			return
		}
		err = s.syncHooks(ctx, sv, c, hookReconcile)
		s.setEventsError(ctx, sv.ID, err)
	}()
}

// kickAllHooks: something about people changed (disabled, deleted, role).
func (s *Server) kickAllHooks() {
	rows, err := s.store.ListConnectors(s.ctx)
	if err != nil {
		return
	}
	for _, r := range rows {
		s.kickHooks(r.ServiceID)
	}
}

// dropUserHooks deletes a person's subscriptions (in one service, or all
// with serviceID 0) while the credential they were made with still exists:
// before the person is deleted or their key is replaced or removed.
func (s *Server) dropUserHooks(ctx context.Context, userID, serviceID int64) {
	all, err := s.store.ListUserConnectorHooks(ctx, userID)
	if err != nil {
		return
	}
	hooks := all[:0]
	for _, h := range all {
		if serviceID == 0 || h.ServiceID == serviceID {
			hooks = append(hooks, h)
		}
	}
	if len(hooks) == 0 {
		return
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, h := range hooks {
		sv, err := s.store.GetService(ctx, h.ServiceID)
		if err != nil {
			continue
		}
		c, ok := connectors.Get(sv.Type)
		p, isPusher := c.(connectors.Pusher)
		if !ok || !isPusher {
			continue
		}
		base := s.connectorTarget(sv)
		if t, can := s.removalTarget(ctx, sv, base, p, &boxWho{}, userID); can {
			if err := p.Unsubscribe(ctx, t, h.RemoteWorkflowID); err != nil {
				s.log.Warn("could not delete a person's subscription in the app", "type", c.Type(), "user_id", userID, "err", err)
			}
		}
		_ = s.store.DeleteConnectorHook(ctx, h.ID)
	}
}

// setEventsError records (or clears) an events problem without hiding others.
func (s *Server) setEventsError(ctx context.Context, serviceID int64, err error) {
	if err != nil {
		_ = s.store.SetConnectorError(ctx, serviceID, "events: "+err.Error())
		return
	}
	if row, e := s.store.GetConnector(ctx, serviceID); e == nil && strings.HasPrefix(row.LastError, "events: ") {
		_ = s.store.SetConnectorError(ctx, serviceID, "")
	}
}

// ---- the receiver ----

// handleConnectorHook takes one delivery from an app. Public: the hook id
// and its secret header are the only authentication. Unknown hook or wrong
// secret: 404, nothing recorded.
func (s *Server) handleConnectorHook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "hookID")
	got := crypto.Hash(r.Header.Get(connectors.PushHeader))
	h, err := s.store.GetConnectorHook(ctx, id)
	if err != nil || r.Header.Get(connectors.PushHeader) == "" || subtle.ConstantTimeCompare([]byte(got), []byte(h.SecretHash)) != 1 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.hookRate.blocked(h.ID) {
		writeErr(w, http.StatusTooManyRequests, "too many deliveries for this hook")
		return
	}
	s.hookRate.fail(h.ID) // counts every delivery, not only failures
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hookBodyLimit))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "delivery too large")
		return
	}
	published, drop, err := s.acceptDelivery(ctx, h, body)
	if err != nil {
		var bad badDelivery
		if errors.As(err, &bad) {
			_ = s.store.RecordConnectorHookDelivery(ctx, h.ID, false, "unreadable delivery")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.log.Warn("connector delivery not published", "hook", h.ID, "err", err)
		writeErr(w, http.StatusInternalServerError, "could not publish")
		return
	}
	_ = s.store.RecordConnectorHookDelivery(ctx, h.ID, published, drop)
	writeJSON(w, http.StatusAccepted, map[string]any{"published": published, "dropped": drop})
}

type badDelivery struct{ error }

// acceptDelivery decodes, attributes and publishes one delivery.
func (s *Server) acceptDelivery(ctx context.Context, h *store.ConnectorHook, body []byte) (bool, string, error) {
	sv, err := s.store.GetService(ctx, h.ServiceID)
	if err != nil {
		return false, "", err
	}
	c, ok := connectors.Get(sv.Type)
	p, isPusher := c.(connectors.Pusher)
	if !ok || !isPusher {
		return false, "the " + sv.Type + " connector takes no deliveries", nil
	}
	if row, err := s.store.GetConnector(ctx, sv.ID); err != nil || !row.Enabled || !row.EventsEnabled {
		return false, "events are off", nil
	}
	d, err := p.Decode(s.connectorTarget(sv), h.Trigger, body)
	if err != nil {
		return false, "", badDelivery{err}
	}
	if d.Drop != "" {
		return false, d.Drop, nil
	}
	var u *store.User
	var linked string // when Core connected the person (connector_accounts.updated_at)
	if h.UserID != 0 {
		a, err := s.store.GetConnectorAccount(ctx, sv.ID, h.UserID)
		if err != nil || a.RemoteID == "" || a.RemoteID != d.OwnerID {
			return false, "asset owner is not this hook's person", nil
		}
		linked = a.UpdatedAt
		if u, err = s.store.GetUser(ctx, h.UserID); err != nil {
			return false, "asset owner is not a Core user", nil
		}
	} else {
		accts, _ := s.store.ListConnectorAccounts(ctx, sv.ID)
		for _, a := range accts {
			if a.RemoteID != "" && a.RemoteID == d.OwnerID && a.Status != connectors.StatusError {
				if u, err = s.store.GetUser(ctx, a.UserID); err == nil {
					linked = a.UpdatedAt
					break
				}
			}
		}
		if u == nil {
			return false, "asset owner is not a Core user", nil
		}
	}
	if u.Disabled || !sv.InAudience(u) {
		return false, "asset owner is disabled or outside the audience", nil
	}
	if at, err := time.Parse(time.RFC3339Nano, linked); err == nil && !d.At.IsZero() && !d.At.After(at.Truncate(time.Second)) {
		return false, "happened before Core connected the person", nil
	}
	key := fmt.Sprintf("%d|%s", sv.ID, d.DedupeKey)
	if d.DedupeKey != "" && !s.hookSeen.first(key) {
		return false, "duplicate delivery", nil
	}
	ev := d.Event
	ev.UserEmail = u.Email
	if err := s.publishConnectorEvent(ctx, c, ev); err != nil {
		s.hookSeen.forget(key)
		return false, "", err
	}
	return true, "", nil
}

// ---- status for the UI ----

type hookWorkflowOut struct {
	Trigger          string `json:"trigger"`
	Name             string `json:"name"`
	RemoteWorkflowID string `json:"remote_workflow_id"`
	State            string `json:"state"`
	Error            string `json:"error,omitempty"`
}

type hookPersonOut struct {
	UserID         int64             `json:"user_id"`
	Email          string            `json:"email"`
	Name           string            `json:"name"`
	State          string            `json:"state"` // ok | missing | needs_key | error | off
	Detail         string            `json:"detail,omitempty"`
	Workflows      []hookWorkflowOut `json:"workflows"`
	Received       int64             `json:"received"`
	Published      int64             `json:"published"`
	Dropped        int64             `json:"dropped"`
	LastDrop       string            `json:"last_drop,omitempty"`
	LastReceivedAt string            `json:"last_received_at,omitempty"`
}

type hookView struct {
	PerUser        bool                     `json:"per_user"`
	Triggers       []connectors.PushTrigger `json:"triggers"`
	URL            string                   `json:"url"`
	CheckedAt      string                   `json:"checked_at,omitempty"`
	Received       int64                    `json:"received"`
	Published      int64                    `json:"published"`
	Dropped        int64                    `json:"dropped"`
	LastReceivedAt string                   `json:"last_received_at,omitempty"`
	People         []hookPersonOut          `json:"people"`
}

func (s *Server) hookStatus(ctx context.Context, sv *store.Service, p connectors.Pusher, eventsOn bool) *hookView {
	v := &hookView{PerUser: p.PushPerUser(), Triggers: p.PushTriggers(), URL: s.hookURL("…"), People: []hookPersonOut{}}
	hooks, _ := s.store.ListConnectorHooks(ctx, sv.ID)
	var last hookSyncResult
	if r, ok := s.hookSyncs.Load(sv.ID); ok {
		last = r.(hookSyncResult)
		v.CheckedAt = last.At.UTC().Format(time.RFC3339Nano)
	}
	byUser := map[int64][]store.ConnectorHook{}
	for _, h := range hooks {
		byUser[h.UserID] = append(byUser[h.UserID], h)
	}
	uids := map[int64]bool{}
	for uid := range byUser {
		uids[uid] = true
	}
	for uid := range last.People {
		uids[uid] = true
	}
	names := map[string]string{}
	for _, tr := range v.Triggers {
		names[tr.Trigger] = tr.Name
	}
	for uid := range uids {
		po := hookPersonOut{UserID: uid, Workflows: []hookWorkflowOut{}}
		if uid == 0 {
			po.Name = "Everyone (one global set)"
		} else if u, err := s.store.GetUser(ctx, uid); err == nil {
			po.Email, po.Name = u.Email, u.Name
		} else {
			po.Name = fmt.Sprintf("deleted person %d", uid)
		}
		hs := byUser[uid]
		errMsg := ""
		for _, h := range hs {
			po.Workflows = append(po.Workflows, hookWorkflowOut{Trigger: h.Trigger, Name: names[h.Trigger], RemoteWorkflowID: h.RemoteWorkflowID, State: h.State, Error: h.Error})
			po.Received += h.Received
			po.Published += h.Published
			po.Dropped += h.Dropped
			if h.LastReceivedAt > po.LastReceivedAt {
				po.LastReceivedAt, po.LastDrop = h.LastReceivedAt, h.LastDrop
			}
			if h.State == "error" && errMsg == "" {
				errMsg = h.Error
			}
		}
		st, synced := last.People[uid]
		switch {
		case errMsg != "":
			po.State, po.Detail = "error", errMsg
		case synced && st.State != "ok":
			po.State, po.Detail = st.State, st.Detail
		case len(hs) > 0 && len(hs) < len(v.Triggers):
			po.State = "missing"
		case len(hs) > 0:
			po.State, po.Detail = "ok", st.Detail
		case eventsOn:
			po.State = "missing"
		default:
			po.State = "off"
		}
		v.Received += po.Received
		v.Published += po.Published
		v.Dropped += po.Dropped
		if po.LastReceivedAt > v.LastReceivedAt {
			v.LastReceivedAt = po.LastReceivedAt
		}
		v.People = append(v.People, po)
	}
	sort.Slice(v.People, func(i, j int) bool { return v.People[i].UserID < v.People[j].UserID })
	return v
}

// handleRecreateHooks deletes and recreates every subscription (new secrets).
func (s *Server) handleRecreateHooks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, c, row, ok := s.loadConnector(w, r)
	if !ok {
		return
	}
	if _, isPusher := c.(connectors.Pusher); !isPusher {
		writeErr(w, http.StatusBadRequest, "the "+c.Type()+" connector has no push events")
		return
	}
	if !row.Enabled || !row.EventsEnabled {
		writeErr(w, http.StatusBadRequest, "turn the connector and its events on first")
		return
	}
	err := s.syncHooks(ctx, sv, c, hookRecreate)
	s.setEventsError(ctx, sv.ID, err)
	row, _, _ = s.connectorRow(ctx, sv.ID)
	out := s.connectorView(ctx, sv, c, row)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "connector": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "connector": out})
}
