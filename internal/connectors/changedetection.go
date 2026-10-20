package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Changedetection.io (0.50+) connector: events only, polled, read-only.
// Changedetection has one password and no accounts, so there is nothing to
// provision or sign anyone into. Endpoints (header x-api-key):
//
//	GET /api/v1/systeminfo   Test: version, watch count, overdue checks   read
//	GET /api/v1/watch        every watch's last change, check and error  read (never with recheck_all: that queues a recheck of everything)
//	GET /api/v1/tags         group names, and which groups are muted     read
//
// Watches are household things: no user_email. A watch whose groups are all
// muted stays quiet here too, as it does in Changedetection's own alerts.
type changedetection struct{}

func init() { Register(changedetection{}) }

// changedetectionMaxEvents caps one poll; the rest wait for the next one.
const changedetectionMaxEvents = 50

func (changedetection) Type() string                 { return "changedetection" }
func (changedetection) Capabilities() Capabilities   { return Capabilities{Events: true} }
func (changedetection) SSORedirects(string) []string { return nil }

func (changedetection) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Changedetection.io has no per-person accounts")
}

func (changedetection) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Changedetection.io has no OpenID sign-in")
	}
	return nil
}

func (changedetection) get(ctx context.Context, t Target, path string, out any) error {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return errors.New("the Changedetection.io service in Core needs an API_KEY: copy it from Changedetection's Settings → API")
	}
	if _, err := doJSON(ctx, t, "Changedetection.io", http.MethodGet, path, map[string]string{"x-api-key": k}, nil, out, k); err != nil {
		if s := appStatus(err); s == http.StatusForbidden || s == http.StatusUnauthorized {
			return fmt.Errorf("Changedetection.io rejected the stored API key (HTTP %d): copy it again from Settings → API, and check API access is on", s)
		}
		return err
	}
	return nil
}

type cdWatch struct {
	LastChanged int64    `json:"last_changed"`
	LastChecked int64    `json:"last_checked"`
	LastError   any      `json:"last_error"` // false, or the message
	PageTitle   string   `json:"page_title"`
	Tags        []string `json:"tags"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Viewed      bool     `json:"viewed"`
}

func (w cdWatch) name() string {
	for _, s := range []string{w.Title, w.PageTitle, w.URL} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return "a watch"
}

func (w cdWatch) errText() string {
	switch v := w.LastError.(type) {
	case string:
		return strings.TrimSpace(v)
	case nil, bool:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

type cdTag struct {
	Title string `json:"title"`
	Muted bool   `json:"notification_muted"`
}

func (c changedetection) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	var si struct {
		Version string   `json:"version"`
		Watches int      `json:"watch_count"`
		Overdue []string `json:"overdue_watches"`
		Queue   int      `json:"queue_size"`
		UptimeS float64  `json:"uptime"`
	}
	if err := c.get(ctx, t, "/api/v1/systeminfo", &si); err != nil {
		return info, fmt.Errorf("cannot reach Changedetection.io at %s: %w", t.BaseURL, err)
	}
	info.Version, info.Admin = si.Version, true
	info.Detail = fmt.Sprintf("%d watches, %d overdue for a check; no accounts or sign-in here", si.Watches, len(si.Overdue))
	return info, nil
}

type cdState struct {
	Init    bool             `json:"init"`
	Changed map[string]int64 `json:"changed"`           // uuid → last_changed announced
	Failing map[string]int64 `json:"failing,omitempty"` // uuid → since when the check fails
}

func (c changedetection) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	var watches map[string]cdWatch
	if err := c.get(ctx, t, "/api/v1/watch", &watches); err != nil {
		return nil, state, err
	}
	tags := map[string]cdTag{}
	if err := c.get(ctx, t, "/api/v1/tags", &tags); err != nil {
		tags = map[string]cdTag{} // names and muting are a bonus
	}
	var prev cdState
	_ = json.Unmarshal(state, &prev)
	cur := cdState{Init: true, Changed: map[string]int64{}, Failing: map[string]int64{}}
	now := t.Clock()
	ids := make([]string, 0, len(watches))
	for id := range watches {
		ids = append(ids, id)
	}
	// oldest change first, so a capped poll leaves the newest for next time in order
	sort.Slice(ids, func(i, j int) bool {
		a, b := watches[ids[i]], watches[ids[j]]
		if a.LastChanged != b.LastChanged {
			return a.LastChanged < b.LastChanged
		}
		return ids[i] < ids[j]
	})
	pub := strings.TrimRight(t.PublicURL, "/")
	var out []Event
	for _, id := range ids {
		w := watches[id]
		cur.Changed[id] = w.LastChanged
		errMsg := w.errText()
		if errMsg != "" {
			cur.Failing[id] = now.Unix()
			if s, ok := prev.Failing[id]; ok {
				cur.Failing[id] = s
			}
		}
		if !prev.Init {
			continue // baseline
		}
		if len(out) >= changedetectionMaxEvents {
			// not announced yet: keep the old marks so the next poll sees it
			if v, ok := prev.Changed[id]; ok {
				cur.Changed[id] = v
			}
			if _, ok := prev.Failing[id]; !ok {
				delete(cur.Failing, id)
			}
			continue
		}
		names, muted := []string{}, len(w.Tags) > 0
		for _, tid := range w.Tags {
			tg, ok := tags[tid]
			if !ok {
				muted = false
				continue
			}
			names = append(names, tg.Title)
			muted = muted && tg.Muted
		}
		if muted {
			continue
		}
		p := map[string]any{"watch": w.name(), "watch_id": id, "target": w.URL}
		if len(names) > 0 {
			p["groups"] = names
		}
		if pub != "" {
			p["url"] = pub + "/diff/" + id
		}
		was, known := prev.Changed[id]
		if w.LastChanged > 0 && (!known || w.LastChanged > was) {
			q := clone(p)
			q["title"] = w.name() + " changed"
			q["changed_at"] = time.Unix(w.LastChanged, 0).UTC().Format(time.RFC3339)
			out = append(out, Event{Name: "change_detected", Subject: fmt.Sprintf("watch:%s:%d", id, w.LastChanged), Payload: q})
		}
		_, wasFailing := prev.Failing[id]
		switch {
		case errMsg != "" && !wasFailing:
			q := clone(p)
			q["title"] = w.name() + ": check failed"
			q["message"] = truncate(errMsg, 300)
			out = append(out, Event{Name: "check_failed", Subject: "watch:" + id, Payload: q})
		case errMsg == "" && wasFailing:
			q := clone(p)
			q["title"] = w.name() + ": checks work again"
			q["failing_since"] = time.Unix(prev.Failing[id], 0).UTC().Format(time.RFC3339)
			out = append(out, Event{Name: "check_recovered", Subject: "watch:" + id, Payload: q})
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}

func clone(p map[string]any) map[string]any {
	q := make(map[string]any, len(p)+2)
	for k, v := range p {
		q[k] = v
	}
	return q
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
