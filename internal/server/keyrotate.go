package server

import (
	"context"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Replacing connector-made keys when a service link is switched off.
//
// An app that reached a service through Core may have kept the person's key
// in memory. When the person (or the admin, for everyone) switches the link
// off, Core replaces the key in the service, so every copy of the old one
// dies; the apps still allowed get the new one at their next ServiceFor.
//
// Only keys the connector minted for an account it CREATED: account status
// ok (not linked, not linked by hand), stored by the connector, holding a
// key. Never a linked account (the owner's), a credential set by hand, or a
// value equal to the service's box-wide key.

const (
	topicKeyReplaced     = "core.service_key_replaced"
	topicKeyReplaceFails = "core.service_key_replace_failed"
	rotateTimeout        = 90 * time.Second // per person
)

// keyJob is one person's connector-made key in one service.
type keyJob struct {
	sv *store.Service
	u  store.User
}

// connectorKey reports whether the person's stored credential in sv is one
// Core may replace: made and stored by the connector for an account it
// created, holding a key that is not the service's box-wide one. why says
// why not (empty when it is simply not a connector-made key).
func (s *Server) connectorKey(ctx context.Context, sv *store.Service, userID int64) (acc *store.ConnectorAccount, ov store.Override, ok bool, why string) {
	acc, err := s.store.GetConnectorAccount(ctx, sv.ID, userID)
	if err != nil || acc.Status != connectors.StatusOK || acc.Manual || acc.RemoteID == "" {
		return nil, ov, false, ""
	}
	ovs, err := s.store.ListOverrides(ctx, userID)
	if err != nil {
		return nil, ov, false, ""
	}
	ov, has := ovs[sv.ID]
	if !has || ov.Source != store.OverrideByConnector {
		return nil, ov, false, ""
	}
	e, _ := catalog.Get(sv.Type)
	key := false
	for _, f := range e.Fields {
		if !f.Secret || f.ConnectorOnly {
			continue
		}
		v := strings.TrimSpace(ov.Fields[f.Key])
		if v == "" {
			continue
		}
		for _, bv := range sv.Fields {
			if strings.TrimSpace(bv) == v {
				return acc, ov, false, "the stored credential is the service's box-wide key; Core never replaces it"
			}
		}
		key = true
	}
	if !key {
		return nil, ov, false, ""
	}
	return acc, ov, true, ""
}

// keyJobs lists the connector-made keys a switch-off of the service link of
// type typ affects: one person's (only != nil) or everyone's.
func (s *Server) keyJobs(ctx context.Context, typ string, only *store.User) []keyJob {
	list, err := s.store.ListServices(ctx)
	if err != nil {
		return nil
	}
	var jobs []keyJob
	for i := range list {
		sv := &list[i]
		if sv.Type != typ || !sv.Enabled {
			continue
		}
		if _, ok := connectors.Get(sv.Type); !ok {
			continue
		}
		accts, err := s.store.ListConnectorAccounts(ctx, sv.ID)
		if err != nil {
			continue
		}
		for _, a := range accts {
			if only != nil && a.UserID != only.ID {
				continue
			}
			if a.Status != connectors.StatusOK || a.Manual {
				continue // cheap filter; connectorKey checks again
			}
			u, err := s.store.GetUser(ctx, a.UserID)
			if err != nil {
				continue
			}
			jobs = append(jobs, keyJob{sv: sv, u: *u})
		}
	}
	return jobs
}

// replaceKeysAfterSwitchOff replaces, in the background, the connector-made
// keys a service link's switch-off affects. reason is off_person or off_admin.
func (s *Server) replaceKeysAfterSwitchOff(ctx context.Context, l *store.AppLink, only *store.User, reason string) {
	typ, ok := serviceType(l.TargetSlug)
	if !ok {
		return
	}
	jobs := s.keyJobs(ctx, typ, only)
	if len(jobs) == 0 {
		return
	}
	app := l.AppSlug
	s.rotations.Add(1)
	go func() {
		defer s.rotations.Done()
		for _, j := range jobs {
			s.replaceKey(s.ctx, j.sv, &j.u, reason, app)
		}
	}()
}

// replaceKey replaces one person's connector-made key in sv, recording the
// outcome on their connector account. Failures are recorded, never fatal.
func (s *Server) replaceKey(ctx context.Context, sv *store.Service, u *store.User, reason, app string) {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	acc, ov, ok, why := s.connectorKey(ctx, sv, u.ID)
	if !ok {
		if why != "" && acc != nil {
			_ = s.store.SetConnectorAccountKey(ctx, sv.ID, u.ID, false, why, "")
		}
		return
	}
	c, found := connectors.Get(sv.Type)
	if !found {
		return
	}
	fail := func(msg string) {
		_ = s.store.SetConnectorAccountKey(ctx, sv.ID, u.ID, false, msg, "")
		s.log.Warn("connector key not replaced", "type", sv.Type, "user", u.Email, "reason", reason, "err", msg)
		_ = s.hub.Publish(ctx, &store.Event{Topic: topicKeyReplaceFails, Source: "core", Subject: "user:" + u.Email,
			Payload: mustJSON(map[string]any{"type": sv.Type, "service_id": sv.ID, "user_email": u.Email, "reason": reason, "app": app, "error": msg})})
	}
	rot, can := c.(connectors.KeyRotator)
	if !can {
		fail(keysNotReplaceable(sv))
		return
	}
	if row, err := s.store.GetConnector(ctx, sv.ID); err != nil || !row.Enabled {
		fail("the " + sv.Name + " connector is off, so the key was not replaced: the old key still works there")
		return
	}
	rctx, cancel := context.WithTimeout(ctx, rotateTimeout)
	defer cancel()
	res, err := rot.RotateKey(rctx, s.connectorTarget(sv), connectors.AccountRef{RemoteID: acc.RemoteID, Email: u.Email, Fields: ov.Fields})
	if len(res.Fields) == 0 {
		msg := "replacing the key failed, the old one still works"
		if err != nil {
			msg = err.Error()
		}
		fail(msg)
		return
	}
	merged := map[string]string{}
	for k, v := range ov.Fields {
		merged[k] = v
	}
	for k, v := range res.Fields {
		merged[k] = v
	}
	o := &store.Override{ServiceID: sv.ID, UserID: u.ID, URL: ov.URL, Fields: merged, Source: store.OverrideByConnector}
	if serr := s.store.SaveOverride(ctx, o, s.entryFor(ctx, sv).IsSecret); serr != nil {
		fail("a new key was made in " + sv.Name + " but Core could not store it (" + serr.Error() + "): provision the person again or set a key by hand")
		return
	}
	keyErr := ""
	if err != nil {
		keyErr = err.Error() // the new key is stored; an old one survived
	}
	_ = s.store.SetConnectorAccountKey(ctx, sv.ID, u.ID, true, keyErr, res.Note)
	if keyErr != "" {
		s.log.Warn("connector key replaced, an old one survived", "type", sv.Type, "user", u.Email, "reason", reason, "err", keyErr)
	} else {
		s.log.Info("connector key replaced", "type", sv.Type, "user", u.Email, "reason", reason, "app", app)
	}
	payload := map[string]any{"type": sv.Type, "service_id": sv.ID, "user_email": u.Email, "reason": reason, "app": app}
	if keyErr != "" {
		payload["error"] = keyErr
	}
	if res.Note != "" {
		payload["note"] = res.Note
	}
	_ = s.hub.Publish(ctx, &store.Event{Topic: topicKeyReplaced, Source: "core", Subject: "user:" + u.Email, Payload: mustJSON(payload)})
	if row, err := s.store.GetConnector(ctx, sv.ID); err == nil && row.Enabled && row.EventsEnabled {
		s.kickHooks(sv.ID) // per-person subscriptions go on with the new key
	}
}

// keysNotReplaceable is what a service whose connector cannot replace keys says.
func keysNotReplaceable(sv *store.Service) string {
	return "Core can't replace keys in " + sv.Name + " automatically: the old key still works there until it is deleted in " + sv.Name
}

// keyRotation says what switching a service link off does to connector-made
// keys of this type: "replaces", "cannot", or "" (no connector makes keys).
func keyRotation(typ string) string {
	c, ok := connectors.Get(typ)
	if !ok || !c.Capabilities().Provision {
		return ""
	}
	if _, can := c.(connectors.KeyRotator); can {
		return "replaces"
	}
	return "cannot"
}

// keyReplacements lists, per person, the last key replacement in services of
// this type (only u's when u is set), for the Links page.
func (s *Server) keyReplacements(ctx context.Context, typ string, u *store.User) []map[string]any {
	out := []map[string]any{}
	list, err := s.store.ListServices(ctx)
	if err != nil {
		return out
	}
	for i := range list {
		sv := &list[i]
		if sv.Type != typ {
			continue
		}
		accts, err := s.store.ListConnectorAccounts(ctx, sv.ID)
		if err != nil {
			continue
		}
		for _, a := range accts {
			if (u != nil && a.UserID != u.ID) || (a.KeyReplacedAt == "" && a.KeyError == "") {
				continue
			}
			p, err := s.store.GetUser(ctx, a.UserID)
			if err != nil {
				continue
			}
			out = append(out, map[string]any{"email": p.Email, "name": p.Name, "replaced_at": a.KeyReplacedAt, "error": a.KeyError, "note": a.KeyNote})
		}
	}
	return out
}
