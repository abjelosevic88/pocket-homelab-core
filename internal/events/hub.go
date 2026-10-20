// Package events is the bus: publish persists an event, fans it out to live
// SSE subscribers and enqueues webhook deliveries that a worker drains with
// backoff. Everything is backed by the store so a restart loses nothing.
package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Hub fans events out.
type Hub struct {
	st        *store.Store
	log       *slog.Logger
	http      *http.Client
	maxTry    int
	mu        sync.Mutex
	subs      map[*Subscriber]struct{}
	wake      chan struct{}
	Allow     func(context.Context, int64, store.Event) bool
	userAgent string
}

// Subscriber is one live SSE connection.
type Subscriber struct {
	Topics string
	C      chan store.Event
}

// New creates a hub.
func New(st *store.Store, log *slog.Logger, maxAttempts int, userAgent string) *Hub {
	return &Hub{st: st, log: log, http: &http.Client{Timeout: 15 * time.Second}, maxTry: maxAttempts, subs: map[*Subscriber]struct{}{}, wake: make(chan struct{}, 1), userAgent: userAgent}
}

// Subscribe registers a live listener; call Unsubscribe when done.
func (h *Hub) Subscribe(topics string) *Subscriber {
	if topics == "" {
		topics = "*"
	}
	s := &Subscriber{Topics: topics, C: make(chan store.Event, 64)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes a listener.
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Live returns the number of connected listeners.
func (h *Hub) Live() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Publish stores the event, notifies listeners and queues webhooks.
func (h *Hub) Publish(ctx context.Context, e *store.Event) error {
	e.Topic = strings.ToLower(strings.TrimSpace(e.Topic))
	if e.Topic == "" {
		return fmt.Errorf("topic required")
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(e.Payload) {
		return fmt.Errorf("payload must be valid JSON")
	}
	hooks, err := h.st.ListWebhooks(ctx, 0)
	if err != nil {
		return err
	}
	ids := []int64{}
	for _, w := range hooks {
		if w.Enabled && w.Matches(e.Topic) && (w.AppID == 0 || h.Allow != nil && h.Allow(ctx, w.AppID, *e)) {
			ids = append(ids, w.ID)
		}
	}
	if err := h.st.InsertEventDeliveries(ctx, e, ids); err != nil {
		return err
	}
	// Channels are wakeups only. Consumers read from the durable log using
	// their cursor, so coalescing wakeups cannot discard an event.
	h.mu.Lock()
	for s := range h.subs {
		if store.TopicMatches(s.Topics, e.Topic) {
			select {
			case s.C <- *e:
			default:
			}
		}
	}
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}

	return nil
}

// Run drains due deliveries until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		h.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		case <-t.C:
		}
	}
}

func (h *Hub) drain(ctx context.Context) {
	for {
		due, err := h.st.DueDeliveries(ctx, 20)
		if err != nil || len(due) == 0 {
			return
		}
		var workers sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, d := range due {
			sem <- struct{}{}
			workers.Add(1)
			go func(d store.Delivery) { defer workers.Done(); defer func() { <-sem }(); h.attempt(ctx, d) }(d)
		}
		workers.Wait()
		if len(due) < 20 {
			return
		}
	}
}

func (h *Hub) attempt(ctx context.Context, d store.Delivery) {
	w, err := h.st.GetWebhook(ctx, d.WebhookID)
	if err != nil {
		_ = h.st.MarkDelivery(ctx, d.ID, "failed", 0, "webhook gone", time.Now())
		return
	}
	e, err := h.st.GetEvent(ctx, d.EventID)
	if err != nil {
		_ = h.st.MarkDelivery(ctx, d.ID, "failed", 0, "event gone", time.Now())
		return
	}
	if !w.Enabled || (w.AppID != 0 && (h.Allow == nil || !h.Allow(ctx, w.AppID, *e))) {
		_ = h.st.MarkDelivery(ctx, d.ID, "failed", 0, "subscription disabled or permission revoked", time.Now())
		return
	}
	code, derr := h.deliver(ctx, w, e)
	if derr == nil {
		_ = h.st.MarkDelivery(ctx, d.ID, "ok", code, "", time.Now())
		return
	}
	attempts := d.Attempts + 1
	if attempts >= h.maxTry {
		h.log.Warn("webhook gave up", "webhook", w.URL, "event", e.ID, "attempts", attempts, "err", derr)
		_ = h.st.MarkDelivery(ctx, d.ID, "failed", code, derr.Error(), time.Now())
		return
	}
	// 10s, 20s, 40s … capped at an hour.
	wait := time.Duration(math.Min(float64(10*time.Second)*math.Pow(2, float64(attempts-1)), float64(time.Hour)))
	_ = h.st.MarkDelivery(ctx, d.ID, "pending", code, derr.Error(), time.Now().Add(wait))
}

// Deliver posts one event to a webhook once; used by the worker and by "Test".
func (h *Hub) deliver(ctx context.Context, w *store.Webhook, e *store.Event) (int, error) {
	body, _ := json.Marshal(e)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", h.userAgent)
	req.Header.Set("X-Pocket-Event", e.Topic)
	req.Header.Set("X-Pocket-Event-ID", fmt.Sprint(e.ID))
	if w.Secret != "" {
		req.Header.Set("X-Pocket-Signature", Sign(w.Secret, body))
	}
	res, err := h.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return res.StatusCode, nil
}

// Test sends an event to a webhook immediately and reports the result.
func (h *Hub) Test(ctx context.Context, w *store.Webhook, e *store.Event) (int, error) {
	return h.deliver(ctx, w, e)
}

// Sign returns "sha256=<hex hmac>" of body with the secret.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature header against the body.
func Verify(secret string, body []byte, header string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(header))
}
