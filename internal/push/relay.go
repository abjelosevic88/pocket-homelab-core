package push

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultRelayURL is used when PUSH_RELAY_URL is not set.
const DefaultRelayURL = "https://push.pockethomelab.com"

// Relay status values (GET /devices/me/push "relay").
const (
	StatusReady      = "ready"        // the relay answers and holds Apple's key
	StatusNoRelay    = "no_relay"     // no sender token, or the relay can't be reached / refuses it
	StatusNoAppleKey = "no_apple_key" // the relay answers but has no Apple push key yet
)

// Per-message results from the relay.
const (
	Sent    = "sent"
	Gone    = "gone"
	Failed  = "failed"
	Limited = "limited"
)

// BatchSize is the most messages one POST /v1/send carries.
const BatchSize = 100

// statusTTL is how long GET /v1/status is trusted.
const statusTTL = time.Minute

// ReasonNoRelay is the failure reason when Core has no relay token.
const ReasonNoRelay = "no push relay set up on this box"

// ErrUnreachable wraps a transport error or a relay 5xx: the whole batch didn't go out and may be retried.
var ErrUnreachable = errors.New("push relay unreachable")

// Out is one message for the relay (POST /v1/send).
type Out struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	Topic       string `json:"topic"`
	Sealed      string `json:"sealed"`
	Sound       bool   `json:"sound"`
	Thread      string `json:"thread,omitempty"`
	CollapseID  string `json:"collapse_id,omitempty"`
	Expires     int64  `json:"expires"`
	Priority    int    `json:"priority"`
}

// Result is the relay's answer for one message.
type Result struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Config sets up a Relay.
type Config struct {
	URL          string // empty = DefaultRelayURL
	Token        string // PUSH_RELAY_TOKEN (pps_…)
	NamesAccount string // PUSH_NAMES_ACCOUNT: JSON {"name","token"} for /v1/enroll
	DataDir      string // where an enrolled token is kept (push-relay.token, 0600)
	UserAgent    string
	Log          *slog.Logger
	HTTP         *http.Client // nil = 10 s timeout
}

// Relay talks to the Pocket Push relay.
type Relay struct {
	cfg  Config
	http *http.Client

	mu         sync.Mutex
	token      string
	enrolled   bool // token came from /v1/enroll (kept in DataDir)
	lastEnroll time.Time
	status     string
	statusAt   time.Time
}

// NewRelay builds a client. A token from config wins; else one enrolled earlier is read from DataDir.
func NewRelay(cfg Config) *Relay {
	if cfg.URL == "" {
		cfg.URL = DefaultRelayURL
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	r := &Relay{cfg: cfg, http: cfg.HTTP, token: strings.TrimSpace(cfg.Token)}
	if r.http == nil {
		r.http = &http.Client{Timeout: 10 * time.Second}
	}
	if r.token == "" && cfg.DataDir != "" {
		if b, err := os.ReadFile(r.tokenFile()); err == nil {
			r.token, r.enrolled = strings.TrimSpace(string(b)), true
		}
	}
	return r
}

// URL is the relay's base URL.
func (r *Relay) URL() string { return r.cfg.URL }

func (r *Relay) tokenFile() string { return filepath.Join(r.cfg.DataDir, "push-relay.token") }

// currentToken returns the sender token, enrolling first when Core has none but has a Pocket
// Names account (at most one attempt a minute).
func (r *Relay) currentToken(ctx context.Context) string {
	r.mu.Lock()
	tok := r.token
	try := tok == "" && r.cfg.NamesAccount != "" && time.Since(r.lastEnroll) > time.Minute
	if try {
		r.lastEnroll = time.Now()
	}
	r.mu.Unlock()
	if !try {
		return tok
	}
	tok, err := r.enroll(ctx)
	if err != nil {
		r.cfg.Log.Warn("push relay enroll", "relay", r.cfg.URL, "err", err)
		return ""
	}
	return tok
}

func (r *Relay) enroll(ctx context.Context) (string, error) {
	b, err := os.ReadFile(r.cfg.NamesAccount)
	if err != nil {
		return "", fmt.Errorf("read PUSH_NAMES_ACCOUNT: %w", err)
	}
	var acct struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &acct); err != nil || acct.Name == "" || acct.Token == "" {
		return "", errors.New("PUSH_NAMES_ACCOUNT is not {\"name\",\"token\"}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL+"/v1/enroll", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(acct.Name+":"+acct.Token)))
	r.headers(req)
	res, err := r.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("enroll: HTTP %d", res.StatusCode)
	}
	var out struct {
		Token  string `json:"token"`
		Sender string `json:"sender"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil || !strings.HasPrefix(out.Token, "pps_") {
		return "", errors.New("enroll: no sender token in the answer")
	}
	if r.cfg.DataDir != "" {
		if err := os.WriteFile(r.tokenFile(), []byte(out.Token+"\n"), 0o600); err != nil {
			r.cfg.Log.Warn("push relay token not saved", "err", err)
		}
	}
	r.mu.Lock()
	r.token, r.enrolled, r.statusAt = out.Token, true, time.Time{}
	r.mu.Unlock()
	r.cfg.Log.Info("enrolled with the push relay", "relay", r.cfg.URL, "sender", out.Sender)
	return out.Token, nil
}

// refused drops an enrolled token the relay no longer accepts, so the next call enrolls again.
func (r *Relay) refused(tok string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enrolled && r.token == tok {
		r.token, r.enrolled = "", false
		if r.cfg.DataDir != "" {
			_ = os.Remove(r.tokenFile())
		}
	}
}

func (r *Relay) headers(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	if r.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", r.cfg.UserAgent)
	}
}

// Status is ready | no_relay | no_apple_key, from the relay's GET /v1/status (cached a minute).
func (r *Relay) Status(ctx context.Context) string {
	r.mu.Lock()
	if r.status != "" && time.Since(r.statusAt) < statusTTL {
		st := r.status
		r.mu.Unlock()
		return st
	}
	r.mu.Unlock()
	st := r.fetchStatus(ctx)
	r.mu.Lock()
	r.status, r.statusAt = st, time.Now()
	r.mu.Unlock()
	return st
}

func (r *Relay) fetchStatus(ctx context.Context) string {
	tok := r.currentToken(ctx)
	if tok == "" {
		return StatusNoRelay
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL+"/v1/status", nil)
	if err != nil {
		return StatusNoRelay
	}
	r.headers(req)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := r.http.Do(req)
	if err != nil {
		r.cfg.Log.Debug("push relay status", "err", err)
		return StatusNoRelay
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		r.refused(tok)
		return StatusNoRelay
	}
	if res.StatusCode != http.StatusOK {
		return StatusNoRelay
	}
	var out struct {
		Apple string `json:"apple"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil {
		return StatusNoRelay
	}
	if out.Apple == "ready" {
		return StatusReady
	}
	return StatusNoAppleKey
}

// Send hands messages to the relay in batches of BatchSize and returns one Result per message,
// in order. A missing token answers every message failed (ReasonNoRelay). A batch the relay
// didn't take (network, 5xx) is failed with ErrUnreachable's text and the error is returned.
func (r *Relay) Send(ctx context.Context, msgs []Out) ([]Result, error) {
	out := make([]Result, 0, len(msgs))
	tok := r.currentToken(ctx)
	if tok == "" {
		for range msgs {
			out = append(out, Result{Status: Failed, Reason: ReasonNoRelay})
		}
		return out, nil
	}
	var firstErr error
	for i := 0; i < len(msgs); i += BatchSize {
		batch := msgs[i:min(i+BatchSize, len(msgs))]
		res, err := r.sendBatch(ctx, tok, batch)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			reason := err.Error()
			status := Failed
			var lim limitedErr
			if errors.As(err, &lim) {
				status, reason = Limited, "the push relay's limit for this box is reached"
			}
			for range batch {
				out = append(out, Result{Status: status, Reason: reason})
			}
			continue
		}
		out = append(out, res...)
	}
	r.noteSent(out)
	return out, firstErr
}

// noteSent refreshes the cached status from what the relay just did.
func (r *Relay) noteSent(res []Result) {
	for _, x := range res {
		st := ""
		switch {
		case x.Status == Sent:
			st = StatusReady
		case x.Status == Failed && strings.Contains(strings.ToLower(x.Reason), "no apple push key"):
			st = StatusNoAppleKey
		}
		if st != "" {
			r.mu.Lock()
			r.status, r.statusAt = st, time.Now()
			r.mu.Unlock()
			return
		}
	}
}

type limitedErr struct{ retryAfter string }

func (e limitedErr) Error() string { return "push relay limit reached" }

func (r *Relay) sendBatch(ctx context.Context, tok string, batch []Out) ([]Result, error) {
	body, err := json.Marshal(map[string]any{"messages": batch})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL+"/v1/send", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.headers(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, shortErr(err))
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		r.refused(tok)
		r.mu.Lock()
		r.status, r.statusAt = StatusNoRelay, time.Now()
		r.mu.Unlock()
		return nil, errors.New("the push relay refused this box's sender token")
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, limitedErr{retryAfter: res.Header.Get("Retry-After")}
	case res.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnreachable, res.StatusCode)
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("push relay: HTTP %d", res.StatusCode)
	}
	var out struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("push relay: unreadable answer: %v", err)
	}
	if len(out.Results) != len(batch) {
		return nil, fmt.Errorf("push relay: %d results for %d messages", len(out.Results), len(batch))
	}
	return out.Results, nil
}

// shortErr drops the URL from a transport error.
func shortErr(err error) string {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner.Error()
		}
	}
	return err.Error()
}
