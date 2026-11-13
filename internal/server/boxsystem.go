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
	"GET /updates":      "GET /v1/updates",
	"POST /updates":     "POST /v1/updates",
	"PUT /updates":      "PUT /v1/updates",
	"POST /restart":     "POST /v1/restart",
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
	s.passToBox(w, r, s.box, target, sub)
}

// boxAddressRoutes maps Core's admin routes onto the box address service's:
// how the box gets its padlock (Pocket's address, the owner's own domain on
// Cloudflare with "Sign in with Cloudflare" or an API token, or the box's own
// certificate).
var boxAddressRoutes = map[string]string{
	"GET ":                    "GET /v1/address",
	"PUT ":                    "PUT /v1/address",
	"POST /cloudflare/zones":  "POST /v1/address/cloudflare/zones",
	"POST /cloudflare/start":  "POST /v1/address/cloudflare/start",
	"POST /cloudflare/finish": "POST /v1/address/cloudflare/finish",
}

func (s *Server) handleBoxAddress(w http.ResponseWriter, r *http.Request) {
	if s.boxAddr == nil {
		writeErr(w, http.StatusNotFound, "this Core is not on a Pocket Box")
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/api/v1/box/address")
	target, ok := boxAddressRoutes[r.Method+" "+sub]
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.passToBox(w, r, s.boxAddr, target, "address"+sub)
}

// boxRemoteRoutes: Tailscale for a Core installed with install.sh, which the installer agent
// runs next to the address service (same socket, BOX_ADDRESS). A Pocket Box has its own, under
// /box/system/tailscale.
var boxRemoteRoutes = map[string]string{
	"GET ":       "GET /v1/tailscale",
	"POST /up":   "POST /v1/tailscale/up",
	"POST /down": "POST /v1/tailscale/down",
}

func (s *Server) handleBoxRemote(w http.ResponseWriter, r *http.Request) {
	if s.boxAddr == nil || s.box != nil {
		writeErr(w, http.StatusNotFound, "no Tailscale here (a Pocket Box has it under Settings → System)")
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/api/v1/box/remote")
	target, ok := boxRemoteRoutes[r.Method+" "+sub]
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.passToBox(w, r, s.boxAddr, target, "remote"+sub)
}

// boxDownloadsRoutes maps Core's admin routes onto the box downloads
// service's: the Usenet provider (SABnzbd), the indexers (Prowlarr) and
// subtitles (Bazarr) behind Pocket Watch, Requests and Subtitles, and
// torrents (qBittorrent, off until switched on) with an optional WireGuard VPN.
// Removing an indexer (DELETE /indexers/{id}) is matched in handleBoxDownloads.
var boxDownloadsRoutes = map[string]string{
	"GET ":           "GET /v1/downloads",
	"PUT /usenet":    "PUT /v1/downloads/usenet",
	"DELETE /usenet": "DELETE /v1/downloads/usenet",
	"POST /indexers": "POST /v1/downloads/indexers",
	"PUT /subtitles": "PUT /v1/downloads/subtitles",
	"PUT /torrents":  "PUT /v1/downloads/torrents",
	"PUT /vpn":       "PUT /v1/downloads/vpn",
	"DELETE /vpn":    "DELETE /v1/downloads/vpn",
}

func (s *Server) handleBoxDownloads(w http.ResponseWriter, r *http.Request) {
	if s.boxDL == nil {
		writeErr(w, http.StatusNotFound, "this Core is not on a Pocket Box")
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/api/v1/box/downloads")
	target, ok := boxDownloadsRoutes[r.Method+" "+sub]
	if id, isIndexer := strings.CutPrefix(sub, "/indexers/"); !ok && isIndexer && r.Method == http.MethodDelete && allDigits(id) {
		target, ok = "DELETE /v1/downloads/indexers/"+id, true
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.passToBox(w, r, s.boxDL, target, "downloads"+sub)
}

// allDigits: an indexer id, nothing that could steer the path elsewhere.
func allDigits(s string) bool {
	if s == "" || len(s) > 18 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// passToBox sends the request to one of the box's root services and its
// answer back, as it is.
func (s *Server) passToBox(w http.ResponseWriter, r *http.Request, svc *boxAgent, target, change string) {
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
	res, err := svc.call(r.Context(), method, path, body)
	if err != nil {
		s.log.Warn("box service unreachable", "path", path, "err", err)
		writeErr(w, http.StatusBadGateway, "the box agent did not answer")
		return
	}
	defer res.Body.Close()
	if path == "/v1/wizard" && res.StatusCode == http.StatusOK {
		s.box.forget()
	}
	if method != http.MethodGet && res.StatusCode == http.StatusOK {
		s.log.Info("box settings changed", "change", change, "by", s.actorEmail(r))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
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
