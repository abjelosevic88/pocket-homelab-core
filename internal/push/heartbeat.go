package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TopicCoreApp is the Pocket Core app's APNs topic (its bundle id). Its registrations get Core's
// own admin alerts (app down, failed installs, box offline…) and never the apps' messages from
// /push/notify, which go to Pocket Hub.
const TopicCoreApp = "org.bjelke.pocketcore"

// ErrNoHeartbeat is answered when the relay is too old to watch boxes (no /v1/heartbeat).
var ErrNoHeartbeat = errors.New("the push relay can't watch boxes yet (update Pocket Push)")

// HeartbeatResult is the relay's answer to a heartbeat.
type HeartbeatResult struct {
	Watching bool      `json:"watching"`
	DueAt    time.Time `json:"due_at"`
	// Fired: the previous heartbeat's offline notices went out (the box missed its deadline).
	Fired   bool      `json:"fired"`
	FiredAt time.Time `json:"fired_at"`
}

// Heartbeat tells the relay this box is alive: if it doesn't hear again within deadline, it sends
// msgs (the sealed "offline" notices) once. The answer says whether the previous ones went out.
func (r *Relay) Heartbeat(ctx context.Context, deadline time.Duration, msgs []Out) (HeartbeatResult, error) {
	body, err := json.Marshal(map[string]any{"deadline": int(deadline / time.Second), "messages": msgs})
	if err != nil {
		return HeartbeatResult{}, err
	}
	return r.heartbeat(ctx, http.MethodPut, body)
}

// StopHeartbeat asks the relay to stop watching this box.
func (r *Relay) StopHeartbeat(ctx context.Context) (HeartbeatResult, error) {
	return r.heartbeat(ctx, http.MethodDelete, nil)
}

func (r *Relay) heartbeat(ctx context.Context, method string, body []byte) (HeartbeatResult, error) {
	var out HeartbeatResult
	tok := r.currentToken(ctx)
	if tok == "" {
		return out, errors.New(ReasonNoRelay)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.cfg.URL+"/v1/heartbeat", rd)
	if err != nil {
		return out, err
	}
	r.headers(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := r.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrUnreachable, shortErr(err))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		r.refused(tok)
		return out, errors.New("the push relay refused this box's sender token")
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed:
		return out, ErrNoHeartbeat
	case res.StatusCode >= 500:
		return out, fmt.Errorf("%w: HTTP %d", ErrUnreachable, res.StatusCode)
	case res.StatusCode != http.StatusOK:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		return out, fmt.Errorf("push relay heartbeat: %s", e.Error)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("push relay heartbeat: unreadable answer: %v", err)
	}
	return out, nil
}
