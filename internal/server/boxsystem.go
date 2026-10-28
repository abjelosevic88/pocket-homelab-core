package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// boxAgent is the Pocket Box agent: a root service on the box that changes
// what Core, running unprivileged, cannot (time zone, Wi-Fi, Tailscale, the
// console password). It answers JSON on the unix socket in BOX_AGENT. Without
// BOX_AGENT (any Core that is not on a Pocket Box) there is no agent, the
// /box/system routes answer 404 and the setup wizard never shows.
type boxAgent struct {
	client *http.Client

	mu       sync.Mutex
	done     bool      // the wizard was finished; never asked again
	askedAt  time.Time // when "not finished" was last read
	lastDone bool
}

func newBoxAgent(socket string) *boxAgent {
	if socket == "" {
		return nil
	}
	return &boxAgent{client: &http.Client{
		// Joining Wi-Fi or Tailscale can take a minute.
		Timeout: 2 * time.Minute,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}}
}

func (a *boxAgent) call(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return a.client.Do(req)
}

// wizardPending says whether the owner still has to go through the box setup
// wizard. Asked on every page load, so "finished" is remembered for good and
// "not finished" for ten seconds.
func (a *boxAgent) wizardPending(ctx context.Context) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return false
	}
	if time.Since(a.askedAt) < 10*time.Second {
		return !a.lastDone
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := a.call(ctx, http.MethodGet, "/v1/wizard", nil)
	if err != nil {
		return false // an agent that does not answer must not trap people in the wizard
	}
	defer res.Body.Close()
	var out struct {
		Done bool `json:"wizard_done"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil {
		return false
	}
	a.askedAt, a.lastDone, a.done = time.Now(), out.Done, out.Done
	return !out.Done
}

func (a *boxAgent) forget() {
	a.mu.Lock()
	a.askedAt = time.Time{}
	a.mu.Unlock()
}

// boxSystemRoutes maps Core's admin routes onto the agent's.
var boxSystemRoutes = map[string]string{
	"GET /status":       "GET /v1/status",
	"GET /timezones":    "GET /v1/timezones",
	"PUT /timezone":     "PUT /v1/timezone",
	"GET /wifi":         "GET /v1/wifi",
	"POST /wifi":        "POST /v1/wifi",
	"POST /tailscale":   "POST /v1/tailscale",
	"DELETE /tailscale": "DELETE /v1/tailscale",
	"PUT /password":     "PUT /v1/password",
	"POST /wizard":      "POST /v1/wizard",
}

// handleBoxSystem passes an admin's request on to the agent and its answer
// back. The agent checks what it is given; Core only decides who may ask.
func (s *Server) handleBoxSystem(w http.ResponseWriter, r *http.Request) {
	if s.box == nil {
		writeErr(w, http.StatusNotFound, "this Core is not on a Pocket Box")
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/api/v1/box/system")
	target, ok := boxSystemRoutes[r.Method+" "+sub]
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	method, path, _ := strings.Cut(target, " ")
	var body io.Reader
	if r.Body != nil && method != http.MethodGet {
		// Read it whole: the agent wants a Content-Length, not a chunked stream.
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "unreadable body")
			return
		}
		body = bytes.NewReader(raw)
	}
	res, err := s.box.call(r.Context(), method, path, body)
	if err != nil {
		s.log.Warn("box agent unreachable", "path", path, "err", err)
		writeErr(w, http.StatusBadGateway, "the box agent did not answer")
		return
	}
	defer res.Body.Close()
	if path == "/v1/wizard" && res.StatusCode == http.StatusOK {
		s.box.forget()
	}
	if method != http.MethodGet && res.StatusCode == http.StatusOK {
		s.log.Info("box settings changed", "change", sub, "by", s.actorEmail(r))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 1<<20))
}

// actorEmail is who made the request, for the log.
func (s *Server) actorEmail(r *http.Request) string {
	if p, _ := s.resolve(r); p != nil && p.User != nil {
		return p.User.Email
	}
	return ""
}
