package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
)

// Speedtest Tracker (1.x) connector: events only, polled, read-only.
// Speedtest Tracker has admin accounts only and no OpenID sign-in.
// Endpoints:
//
//	GET /api/v1/results?sort=-created_at&page[size]=N   with an API token (results:read): every result, failed ones too   read
//	GET /api/speedtest/latest                           without one: only the newest finished result (public)           read
//
// With a TOKEN Core sees every test and Speedtest Tracker's own thresholds
// (benchmarks, healthy); without one, the newest result at each poll. Each
// finished test is a household event; so is the line falling below the
// thresholds and coming back. The WAN address in a result is never copied.
type speedtest struct{}

func init() { Register(speedtest{}) }

// speedtestPage: results read per poll (tests run hourly at most, usually).
const speedtestPage = 25

func (speedtest) Type() string                 { return "speedtest" }
func (speedtest) Capabilities() Capabilities   { return Capabilities{Events: true} }
func (speedtest) SSORedirects(string) []string { return nil }

func (speedtest) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Speedtest Tracker has no per-person accounts")
}

func (speedtest) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Speedtest Tracker has no OpenID sign-in")
	}
	return nil
}

// stResult is one test, in Mbit/s and ms whichever endpoint it came from.
type stResult struct {
	ID        int64
	Status    string // completed | failed (others are still running)
	Down, Up  *float64
	Ping      *float64
	Jitter    *float64
	Loss      *float64
	Healthy   *bool
	Scheduled bool
	Server    string
	ISP       string
	ResultURL string
	Message   string
	At        string
	Breached  []string // benchmarks that failed
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func (speedtest) results(ctx context.Context, t Target) ([]stResult, bool, error) {
	tok := strings.TrimSpace(t.Fields["TOKEN"])
	if tok == "" {
		var res struct {
			Data struct {
				ID         int64    `json:"id"`
				Ping       *float64 `json:"ping"`
				Download   *float64 `json:"download"` // Mbit/s here
				Upload     *float64 `json:"upload"`
				ServerName string   `json:"server_name"`
				URL        string   `json:"url"`
				Scheduled  bool     `json:"scheduled"`
				Failed     bool     `json:"failed"`
				CreatedAt  string   `json:"created_at"`
			} `json:"data"`
		}
		if _, err := doJSON(ctx, t, "Speedtest Tracker", http.MethodGet, "/api/speedtest/latest", nil, nil, &res); err != nil {
			if appStatus(err) == http.StatusNotFound {
				return nil, false, nil // no results yet
			}
			return nil, false, err
		}
		d := res.Data
		r := stResult{ID: d.ID, Status: "completed", Down: d.Download, Up: d.Upload, Ping: d.Ping, Scheduled: d.Scheduled, Server: d.ServerName, ResultURL: d.URL, At: d.CreatedAt}
		if d.Failed {
			r.Status = "failed"
		}
		return []stResult{r}, false, nil
	}
	var res struct {
		Data []struct {
			ID         int64    `json:"id"`
			Ping       *float64 `json:"ping"`
			DownBits   *float64 `json:"download_bits"`
			UpBits     *float64 `json:"upload_bits"`
			Healthy    *bool    `json:"healthy"`
			Status     string   `json:"status"`
			Scheduled  bool     `json:"scheduled"`
			CreatedAt  string   `json:"created_at"`
			Benchmarks map[string]struct {
				Passed bool `json:"passed"`
			} `json:"benchmarks"`
			Data struct {
				ISP        string   `json:"isp"`
				Message    string   `json:"message"`
				PacketLoss *float64 `json:"packetLoss"`
				Ping       struct {
					Jitter *float64 `json:"jitter"`
				} `json:"ping"`
				Server struct {
					Name     string `json:"name"`
					Location string `json:"location"`
				} `json:"server"`
				Result struct {
					URL string `json:"url"`
				} `json:"result"`
			} `json:"data"`
		} `json:"data"`
	}
	path := fmt.Sprintf("/api/v1/results?sort=-created_at&page%%5Bsize%%5D=%d", speedtestPage)
	if _, err := doJSON(ctx, t, "Speedtest Tracker", http.MethodGet, path, map[string]string{"Authorization": "Bearer " + tok}, nil, &res, tok); err != nil {
		switch appStatus(err) {
		case http.StatusUnauthorized:
			return nil, true, errors.New("Speedtest Tracker rejected the stored API token (HTTP 401): make one under Settings → API Tokens with \"Read results\"")
		case http.StatusForbidden:
			return nil, true, errors.New("the stored Speedtest Tracker API token may not read results: give it \"Read results\" (results:read)")
		}
		return nil, true, err
	}
	out := make([]stResult, 0, len(res.Data))
	for _, d := range res.Data {
		r := stResult{ID: d.ID, Status: d.Status, Ping: d.Ping, Healthy: d.Healthy, Scheduled: d.Scheduled, At: d.CreatedAt,
			ISP: d.Data.ISP, Message: d.Data.Message, Loss: d.Data.PacketLoss, Jitter: d.Data.Ping.Jitter, ResultURL: d.Data.Result.URL, Server: d.Data.Server.Name}
		if d.Data.Server.Location != "" && r.Server != "" {
			r.Server += " (" + d.Data.Server.Location + ")"
		}
		if d.DownBits != nil {
			v := round2(*d.DownBits / 1e6)
			r.Down = &v
		}
		if d.UpBits != nil {
			v := round2(*d.UpBits / 1e6)
			r.Up = &v
		}
		for k, b := range d.Benchmarks {
			if !b.Passed {
				r.Breached = append(r.Breached, k)
			}
		}
		sort.Strings(r.Breached)
		out = append(out, r)
	}
	return out, true, nil
}

func (c speedtest) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	rs, full, err := c.results(ctx, t)
	if err != nil {
		return info, fmt.Errorf("cannot read Speedtest Tracker at %s: %w", t.BaseURL, err)
	}
	info.Admin = true
	last := "no results yet"
	for _, r := range rs {
		if r.Status == "completed" && r.Down != nil && r.Up != nil {
			last = fmt.Sprintf("last test %.0f↓ / %.0f↑ Mbit/s", *r.Down, *r.Up)
			break
		}
	}
	if full {
		info.Detail = last + "; reading every result with the API token; no accounts or sign-in here"
	} else {
		info.Detail = last + "; without an API token Core sees only the newest result at each check and no thresholds: add one (Settings → API Tokens, Read results) for failed tests and slow-line alerts"
	}
	return info, nil
}

type speedtestState struct {
	Init     bool  `json:"init"`
	LastID   int64 `json:"last_id"`
	Degraded bool  `json:"degraded,omitempty"`
}

func (c speedtest) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	rs, _, err := c.results(ctx, t)
	if err != nil {
		return nil, state, err
	}
	var prev speedtestState
	_ = json.Unmarshal(state, &prev)
	cur := prev
	cur.Init = true
	// oldest first; only finished tests; a running one is read again next time
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	pub := strings.TrimRight(t.PublicURL, "/")
	final := func(st string) bool { return st == "completed" || st == "failed" || st == "skipped" }
	lastFinal := -1
	for i, r := range rs {
		if final(r.Status) {
			lastFinal = i
		}
	}
	var out []Event
	for i, r := range rs {
		if r.ID <= prev.LastID {
			continue
		}
		if !final(r.Status) && i > lastFinal {
			break // still running: it and everything after it wait for the next poll
		}
		cur.LastID = r.ID
		if !final(r.Status) || r.Status == "skipped" {
			continue // skipped, or stuck while later tests finished
		}
		if !prev.Init {
			if r.Healthy != nil {
				cur.Degraded = !*r.Healthy
			}
			continue // baseline
		}
		p := map[string]any{"result_id": r.ID, "scheduled": r.Scheduled}
		setNonEmpty(p, "server", r.Server)
		setNonEmpty(p, "isp", r.ISP)
		setNonEmpty(p, "tested_at", r.At)
		setNonEmpty(p, "result_url", r.ResultURL)
		if pub != "" {
			p["url"] = pub + "/admin/results"
		}
		if r.Status == "failed" {
			p["title"] = "Speedtest failed"
			if r.Message != "" {
				p["message"] = truncate(r.Message, 300)
				p["title"] = "Speedtest failed: " + truncate(r.Message, 120)
			}
			out = append(out, Event{Name: "failed", Subject: fmt.Sprintf("result:%d", r.ID), Payload: p})
			continue
		}
		for k, v := range map[string]*float64{"download_mbps": r.Down, "upload_mbps": r.Up, "ping_ms": r.Ping, "jitter_ms": r.Jitter, "packet_loss": r.Loss} {
			if v != nil {
				p[k] = *v
			}
		}
		if r.Healthy != nil {
			p["healthy"] = *r.Healthy
		}
		if len(r.Breached) > 0 {
			p["below_threshold"] = r.Breached
		}
		title := "Speedtest"
		if r.Down != nil && r.Up != nil {
			title = fmt.Sprintf("Speedtest: %.0f↓ / %.0f↑ Mbit/s", *r.Down, *r.Up)
			if r.Ping != nil {
				title += fmt.Sprintf(", %.0f ms", *r.Ping)
			}
		}
		p["title"] = title
		out = append(out, Event{Name: "finished", Subject: fmt.Sprintf("result:%d", r.ID), Payload: p})
		if r.Healthy == nil {
			continue
		}
		switch {
		case !*r.Healthy && !cur.Degraded:
			cur.Degraded = true
			q := clone(p)
			q["title"] = "The internet line is below Speedtest Tracker's thresholds: " + strings.TrimPrefix(title, "Speedtest: ")
			out = append(out, Event{Name: "degraded", Subject: fmt.Sprintf("result:%d", r.ID), Payload: q})
		case *r.Healthy && cur.Degraded:
			cur.Degraded = false
			q := clone(p)
			q["title"] = "The internet line is back above Speedtest Tracker's thresholds: " + strings.TrimPrefix(title, "Speedtest: ")
			out = append(out, Event{Name: "recovered", Subject: fmt.Sprintf("result:%d", r.ID), Payload: q})
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}
