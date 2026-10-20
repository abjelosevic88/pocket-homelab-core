package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

var topicRe = func(t string) bool {
	if t == "" || len(t) > 120 {
		return false
	}
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// handlePublish stores and fans out one event. Apps publish under their own
// slug; users and devices may publish too (source = their label).
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Topic   string          `json:"topic"`
		Subject string          `json:"subject"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Topic = strings.ToLower(strings.TrimSpace(in.Topic))
	if !topicRe(in.Topic) {
		writeErr(w, http.StatusBadRequest, "topic must be lowercase dotted words, e.g. kitchen.cooked")
		return
	}
	p := principalFrom(r.Context())
	if p.App != nil && !strings.HasPrefix(in.Topic, p.App.Slug+".") {
		writeErr(w, http.StatusForbidden, "apps publish under their own slug: "+p.App.Slug+".*")
		return
	}
	if len(in.Payload) > 256<<10 {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload over 256 KiB")
		return
	}
	e := &store.Event{Topic: in.Topic, Source: p.Label(), Subject: strings.TrimSpace(in.Subject), Payload: in.Payload}
	if err := s.hub.Publish(r.Context(), e); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	f := store.EventFilter{Topic: strings.ToLower(q.Get("topic")), Source: q.Get("source"), Subject: q.Get("subject"), AfterID: after, Since: q.Get("since"), Limit: queryInt(r, "limit")}
	list, err := s.store.ListEvents(r.Context(), f)
	if err != nil {
		s.fail(w, err, "list events")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	e, err := s.store.GetEvent(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get event")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handleTopics(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.Topics(r.Context())
	if err != nil {
		s.fail(w, err, "topics")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleEventSources counts the stored events per source (core, an app's
// slug, connector:<type>), for the event log's source filter.
func (s *Server) handleEventSources(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.EventSources(r.Context())
	if err != nil {
		s.fail(w, err, "event sources")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleStream is Server-Sent Events: ?topics=kitchen.*,habits.marked and an
// optional Last-Event-ID header (or ?after=) to replay what was missed.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	topics := strings.ToLower(r.URL.Query().Get("topics"))
	if topics == "" {
		topics = "*"
	}
	// By default every message is a plain "message" so EventSource.onmessage
	// sees it; ?typed=1 names the SSE event after the topic instead.
	typed := r.URL.Query().Get("typed") == "1"
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub := s.hub.Subscribe(topics)
	defer s.hub.Unsubscribe(sub)

	send := func(e store.Event) bool {
		b, _ := json.Marshal(e)
		var err error
		if typed {
			_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Topic, b)
		} else {
			_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, b)
		}
		if err != nil {
			return false
		}
		fl.Flush()
		return true
	}

	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if after == 0 {
		after, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	}
	if after > 0 {
		for _, pat := range strings.Split(topics, ",") {
			missed, _ := s.store.ListEvents(r.Context(), store.EventFilter{Topic: strings.TrimSpace(pat), AfterID: after, Limit: 500})
			for _, e := range missed {
				if !send(e) {
					return
				}
			}
		}
	}
	fmt.Fprintf(w, ": connected topics=%s\n\n", topics)
	fl.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-sub.C:
			if !send(e) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// ---- webhooks ----

func (s *Server) webhookScope(r *http.Request) int64 {
	p := principalFrom(r.Context())
	if p.App != nil {
		return p.App.ID
	}
	return 0
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.App == nil && !p.IsAdmin() {
		writeErr(w, http.StatusForbidden, "admin or app only")
		return
	}
	list, err := s.store.ListWebhooks(r.Context(), s.webhookScope(r))
	if err != nil {
		s.fail(w, err, "list webhooks")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type webhookIn struct {
	Name    *string `json:"name"`
	URL     *string `json:"url"`
	Topics  *string `json:"topics"`
	Secret  *string `json:"secret"`
	Enabled *bool   `json:"enabled"`
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.App == nil && !p.IsAdmin() {
		writeErr(w, http.StatusForbidden, "admin or app only")
		return
	}
	var in webhookIn
	if err := decode(r, &in); err != nil || in.URL == nil || strings.TrimSpace(*in.URL) == "" {
		writeErr(w, http.StatusBadRequest, "url required")
		return
	}
	wh := &store.Webhook{AppID: s.webhookScope(r), Enabled: true, Topics: "*"}
	applyWebhook(wh, in)
	if err := s.store.SaveWebhook(r.Context(), wh); err != nil {
		s.fail(w, err, "create webhook")
		return
	}
	writeJSON(w, http.StatusCreated, wh)
}

func applyWebhook(wh *store.Webhook, in webhookIn) {
	if in.Name != nil {
		wh.Name = strings.TrimSpace(*in.Name)
	}
	if in.URL != nil {
		wh.URL = strings.TrimSpace(*in.URL)
	}
	if in.Topics != nil && strings.TrimSpace(*in.Topics) != "" {
		wh.Topics = strings.ToLower(strings.TrimSpace(*in.Topics))
	}
	if in.Secret != nil {
		wh.Secret = *in.Secret
	}
	if in.Enabled != nil {
		wh.Enabled = *in.Enabled
	}
}

func (s *Server) ownWebhook(r *http.Request) (*store.Webhook, bool) {
	wh, err := s.store.GetWebhook(r.Context(), idParam(r, "id"))
	if err != nil {
		return nil, false
	}
	p := principalFrom(r.Context())
	if p.IsAdmin() || (p.App != nil && wh.AppID == p.App.ID) {
		return wh, true
	}
	return nil, false
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	wh, ok := s.ownWebhook(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var in webhookIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	applyWebhook(wh, in)
	if err := s.store.SaveWebhook(r.Context(), wh); err != nil {
		s.fail(w, err, "update webhook")
		return
	}
	writeJSON(w, http.StatusOK, wh)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	wh, ok := s.ownWebhook(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.DeleteWebhook(r.Context(), wh.ID); err != nil {
		s.fail(w, err, "delete webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	wh, ok := s.ownWebhook(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	e := &store.Event{ID: 0, Topic: "core.test", Source: "core", Subject: "webhook:" + strconv.FormatInt(wh.ID, 10), Payload: mustJSON(map[string]any{"hello": "from Pocket Core", "at": time.Now().UTC().Format(time.RFC3339)}), CreatedAt: store.Now()}
	code, err := s.hub.Test(r.Context(), wh, e)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "status": code, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": code})
}

func (s *Server) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	wh, ok := s.ownWebhook(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	list, err := s.store.ListDeliveries(r.Context(), wh.ID, queryInt(r, "limit"))
	if err != nil {
		s.fail(w, err, "deliveries")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
