package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/push"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Pocket Hub notifications (docs/push.md). A paired device registers its APNs token and its
// X25519 public key; apps (Pocket Alerts) ask Core to notify a person; Core seals the message to
// each of that person's devices and hands the sealed blobs to the Pocket Push relay, which alone
// holds Apple's key and never sees the text.

// pushExpiry is how long APNs keeps a notification for a device that is offline.
const pushExpiry = 24 * time.Hour

// pushDevice is the caller when it is a paired device (pcd_), else it answers 403 and returns nil.
func pushDevice(w http.ResponseWriter, r *http.Request) *Principal {
	p := principalFrom(r.Context())
	if p == nil || p.Kind != "device" || p.Device == nil || p.User == nil || p.User.Disabled {
		writeErr(w, http.StatusForbidden, "paired devices only")
		return nil
	}
	return p
}

// PUT /api/v1/devices/me/push {token, environment, topic, public_key, enabled?, sound?}
func (s *Server) handlePutDevicePush(w http.ResponseWriter, r *http.Request) {
	p := pushDevice(w, r)
	if p == nil {
		return
	}
	var in struct {
		Token       string `json:"token"`
		Environment string `json:"environment"`
		Topic       string `json:"topic"`
		PublicKey   string `json:"public_key"`
		Enabled     *bool  `json:"enabled"`
		Sound       *bool  `json:"sound"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	tok := strings.TrimSpace(in.Token)
	switch in.Environment {
	case "development", "production":
		tok = strings.ToLower(tok)
		if _, err := hex.DecodeString(tok); err != nil || len(tok) < 64 || len(tok) > 200 {
			writeErr(w, http.StatusBadRequest, "token must be the APNs device token in hex (64–200 characters)")
			return
		}
	case push.EnvironmentFCM:
		// Android: a Firebase registration token, case-sensitive.
		if !fcmToken.MatchString(tok) {
			writeErr(w, http.StatusBadRequest, "token must be the phone's Firebase registration token")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "environment must be development, production or fcm")
		return
	}
	if !slices.Contains(s.cfg.PushTopics, in.Topic) {
		writeErr(w, http.StatusBadRequest, "topic must be one of "+strings.Join(s.cfg.PushTopics, ", "))
		return
	}
	in.PublicKey = strings.TrimSpace(in.PublicKey)
	if _, err := push.ParsePublicKey(in.PublicKey); err != nil {
		writeErr(w, http.StatusBadRequest, "public_key must be the standard base64 of a 32-byte X25519 key")
		return
	}
	reg := &store.PushRegistration{DeviceID: p.Device.ID, APNsToken: tok, Environment: in.Environment, Topic: in.Topic,
		PublicKey: in.PublicKey, Enabled: in.Enabled == nil || *in.Enabled, Sound: in.Sound == nil || *in.Sound}
	if err := s.store.SavePushRegistration(r.Context(), reg); err != nil {
		s.fail(w, err, "save push registration")
		return
	}
	s.log.Info("push registered", "device", p.Device.ID, "user", p.User.ID, "topic", in.Topic, "environment", in.Environment, "enabled", reg.Enabled, "token_tail", tail(tok))
	writeJSON(w, http.StatusOK, map[string]any{"registered": true, "enabled": reg.Enabled, "sound": reg.Sound, "relay": s.pushRelay.StatusFor(r.Context(), reg.Environment)})
}

// GET /api/v1/devices/me/push
func (s *Server) handleGetDevicePush(w http.ResponseWriter, r *http.Request) {
	p := pushDevice(w, r)
	if p == nil {
		return
	}
	reg, err := s.store.GetPushRegistration(r.Context(), p.Device.ID)
	if errors.Is(err, store.ErrNotFound) {
		relay := s.pushRelay.StatusFor(r.Context(), pushEnvironmentGuess(p.Device.Platform))
		writeJSON(w, http.StatusOK, map[string]any{"registered": false, "enabled": false, "sound": false, "relay": relay, "last_sent_at": nil, "last_error": ""})
		return
	}
	if err != nil {
		s.fail(w, err, "get push registration")
		return
	}
	relay := s.pushRelay.StatusFor(r.Context(), reg.Environment)
	var sent any
	if reg.LastSentAt != "" {
		sent = reg.LastSentAt
	}
	writeJSON(w, http.StatusOK, map[string]any{"registered": true, "enabled": reg.Enabled, "sound": reg.Sound, "relay": relay,
		"environment": reg.Environment, "topic": reg.Topic, "last_sent_at": sent, "last_error": reg.LastError, "updated_at": reg.UpdatedAt})
}

// DELETE /api/v1/devices/me/push
func (s *Server) handleDeleteDevicePush(w http.ResponseWriter, r *http.Request) {
	p := pushDevice(w, r)
	if p == nil {
		return
	}
	if err := s.store.DeletePushRegistration(r.Context(), p.Device.ID); err != nil {
		s.fail(w, err, "delete push registration")
		return
	}
	s.log.Info("push unregistered", "device", p.Device.ID, "user", p.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{"registered": false})
}

// POST /api/v1/devices/me/push/test → {status, reason}
func (s *Server) handleTestDevicePush(w http.ResponseWriter, r *http.Request) {
	p := pushDevice(w, r)
	if p == nil {
		return
	}
	reg, err := s.store.GetPushRegistration(r.Context(), p.Device.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "this device has no notifications registered")
		return
	}
	if err != nil {
		s.fail(w, err, "get push registration")
		return
	}
	m := push.Message{Title: "Pocket Hub", Body: "Notifications work", Source: "Pocket Core", ID: "test-" + strconv.FormatInt(time.Now().Unix(), 10), Sound: true, Thread: "pocket-core"}
	res, _ := s.pushSend(r.Context(), []store.PushRegistration{*reg}, m)
	out := map[string]any{"status": push.Sent}
	switch {
	case res.Gone > 0:
		out["status"], out["reason"] = push.Gone, res.Reason
	case res.Failed > 0:
		out["status"], out["reason"] = push.Failed, res.Reason
	}
	writeJSON(w, http.StatusOK, out)
}

// pushTally is POST /push/notify's answer.
type pushTally struct {
	Devices int    `json:"devices"`
	Sent    int    `json:"sent"`
	Failed  int    `json:"failed"`
	Gone    int    `json:"gone"`
	Reason  string `json:"reason,omitempty"`

	unreachable bool // the relay couldn't be reached (or 5xx): worth retrying
	limited     bool // the relay's limit for this box
}

// POST /api/v1/push/notify (app or admin) {email | user_id, title, body, url, source, id, sound, thread}
func (s *Server) handlePushNotify(w http.ResponseWriter, r *http.Request) {
	caller := principalFrom(r.Context())
	// A device token acts for its owner, but notifying is for apps and admins themselves.
	if caller == nil || caller.Kind == "device" || (caller.App == nil && !caller.IsAdmin()) {
		writeErr(w, http.StatusForbidden, "apps and admins only")
		return
	}
	var in struct {
		Email  string          `json:"email"`
		UserID int64           `json:"user_id"`
		Title  string          `json:"title"`
		Body   string          `json:"body"`
		URL    string          `json:"url"`
		Source string          `json:"source"`
		ID     json.RawMessage `json:"id"`
		Sound  *bool           `json:"sound"`
		Thread string          `json:"thread"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	in.Title, in.Body = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	if in.Title == "" && in.Body == "" {
		writeErr(w, http.StatusBadRequest, "title or body required")
		return
	}
	if in.URL != "" {
		if u, err := url.Parse(in.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			writeErr(w, http.StatusBadRequest, "url must be an http(s) address")
			return
		}
	}
	var u *store.User
	var err error
	switch {
	case strings.TrimSpace(in.Email) != "":
		u, err = s.store.GetUserByEmail(r.Context(), strings.ToLower(strings.TrimSpace(in.Email)))
	case in.UserID > 0:
		u, err = s.store.GetUser(r.Context(), in.UserID)
	default:
		writeErr(w, http.StatusBadRequest, "email or user_id required")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such person")
		return
	}
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	// An app only notifies people its service's audience includes (as /auth/introspect).
	if caller.App != nil && !s.appAdmits(r.Context(), caller.App.ID, u) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "this person may not use this app", "reason": "not_in_audience"})
		return
	}
	if u.Disabled {
		writeJSON(w, http.StatusOK, pushTally{Reason: "this account is disabled"})
		return
	}
	regs, err := s.store.UserPushRegistrations(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err, "list push registrations")
		return
	}
	if len(regs) == 0 {
		writeJSON(w, http.StatusOK, pushTally{Reason: "no Pocket Hub device has notifications on"})
		return
	}
	m := push.Message{Title: in.Title, Body: in.Body, URL: in.URL, Source: in.Source, ID: rawID(in.ID), Sound: in.Sound == nil || *in.Sound, Thread: in.Thread}
	res, _ := s.pushSend(r.Context(), regs, m)
	s.log.Info("push notify", "caller", caller.Label(), "user", u.ID, "devices", res.Devices, "sent", res.Sent, "failed", res.Failed, "gone", res.Gone, "reason", res.Reason)
	status := http.StatusOK
	switch {
	case res.Sent == 0 && res.unreachable:
		status = http.StatusBadGateway
	case res.Sent == 0 && res.limited:
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, res)
}

// rawID accepts a JSON string or number as the message id.
func rawID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

// pushSend seals m for every registration, sends them through the relay and records the outcome:
// sent → last_sent_at, gone → the registration is deleted, failed/limited → last_error.
func (s *Server) pushSend(ctx context.Context, regs []store.PushRegistration, m push.Message) (pushTally, error) {
	t := pushTally{Devices: len(regs)}
	m.At = time.Now().UTC().Format(time.RFC3339)
	exp := time.Now().Add(pushExpiry).Unix()
	outs := make([]push.Out, 0, len(regs))
	sendable := make([]store.PushRegistration, 0, len(regs))
	note := func(reason string) {
		if t.Reason == "" {
			t.Reason = reason
		}
	}
	for _, reg := range regs {
		dm := m
		dm.Sound = m.Sound && reg.Sound
		sealed, err := push.SealMessage(reg.PublicKey, dm)
		if err != nil {
			t.Failed++
			note("the device's key is not usable; open Pocket Hub to register again")
			_ = s.store.MarkPushError(ctx, reg.ID, "the device's key is not usable")
			continue
		}
		outs = append(outs, push.Out{Token: reg.APNsToken, Environment: reg.Environment, Topic: reg.Topic, Sealed: sealed,
			Sound: dm.Sound, Thread: m.Thread, Expires: exp, Priority: 10})
		sendable = append(sendable, reg)
	}
	if len(outs) == 0 {
		return t, nil
	}
	results, err := s.pushRelay.Send(ctx, outs)
	if errors.Is(err, push.ErrUnreachable) {
		t.unreachable = true
	}
	for i, res := range results {
		reg := sendable[i]
		switch res.Status {
		case push.Sent:
			t.Sent++
			_ = s.store.MarkPushSent(ctx, reg.ID)
		case push.Gone:
			t.Gone++
			if reg.Environment == push.EnvironmentFCM {
				note("Google says this phone no longer takes notifications; it was removed")
			} else {
				note("Apple says this device no longer takes notifications; it was removed")
			}
			_ = s.store.DeletePushRegistrationByID(ctx, reg.ID)
			s.log.Info("push registration gone", "device", reg.DeviceID, "token_tail", tail(reg.APNsToken), "reason", res.Reason)
		default:
			if res.Status == push.Limited {
				t.limited = true
			}
			t.Failed++
			reason := res.Reason
			if reason == "" {
				reason = res.Status
			}
			note(reason)
			_ = s.store.MarkPushError(ctx, reg.ID, reason)
		}
	}
	return t, err
}

// fcmToken is the shape of a Firebase registration token (URL-safe characters and colons).
var fcmToken = regexp.MustCompile(`^[A-Za-z0-9_:\-]{32,1000}$`)

// pushEnvironmentGuess is the environment a not-yet-registered device will use, for its relay state.
func pushEnvironmentGuess(platform string) string {
	if platform == "android" {
		return push.EnvironmentFCM
	}
	return ""
}

// tail is the last 6 characters of a device token, the most a log line shows.
func tail(tok string) string {
	if len(tok) <= 6 {
		return tok
	}
	return tok[len(tok)-6:]
}
