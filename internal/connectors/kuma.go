package connectors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Uptime Kuma (1.23+) connector: events only, polled. Kuma has one admin and
// no OpenID sign-in, and adding a notification needs its socket.io login,
// so Core reads instead of subscribing:
//
//	GET /metrics                              every monitor's status and certificate (API key, Basic auth password)
//	GET /api/status-page/:slug                the status page's monitors (public; STATUS_SLUG)
//	GET /api/status-page/heartbeat/:slug      their last checks, with Kuma's message (public)
//
// With an API key Core watches every monitor; with only a status page slug,
// the monitors on that page. With both, down events carry the message of
// the last check. Monitors are household things: no user_email.
type kuma struct{}

func init() { Register(kuma{}) }

// kumaCertWarn: days before a certificate expires that each get one event.
var kumaCertWarn = []int{14, 7, 3, 1}

func (kuma) Type() string               { return "kuma" }
func (kuma) Capabilities() Capabilities { return Capabilities{Events: true} }
func (kuma) SSORedirects(string) []string {
	return nil
}

func (kuma) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Uptime Kuma has no per-person accounts")
}

func (kuma) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Uptime Kuma has no OpenID sign-in")
	}
	return nil
}

type kumaMonitor struct {
	Name     string
	Type     string
	URL      string
	Status   int // 0 down, 1 up, 2 pending, 3 maintenance
	CertDays *int
	Message  string
	At       string
}

// metrics reads every monitor from Prometheus metrics; nil when no API key.
func (kuma) metrics(ctx context.Context, t Target) (map[string]*kumaMonitor, error) {
	k := strings.TrimSpace(t.Fields["API_KEY"])
	if k == "" {
		return nil, nil
	}
	var raw []byte
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+k))
	if _, err := doJSON(ctx, t, "Uptime Kuma", http.MethodGet, "/metrics", map[string]string{"Authorization": auth, "Accept": "text/plain"}, nil, &raw, k); err != nil {
		if appStatus(err) == http.StatusUnauthorized {
			return nil, errors.New("Uptime Kuma rejected the stored API key (HTTP 401): make one under Settings → API Keys")
		}
		return nil, err
	}
	out := map[string]*kumaMonitor{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, val, ok := parsePromLine(line)
		if !ok || (name != "monitor_status" && name != "monitor_cert_days_remaining") {
			continue
		}
		mn := labels["monitor_name"]
		if mn == "" {
			continue
		}
		m := out[mn]
		if m == nil {
			m = &kumaMonitor{Name: mn, Type: labels["monitor_type"], Status: -1}
			if u := labels["monitor_url"]; u != "" && u != "https://" && u != "null" {
				m.URL = u
			} else if h := labels["monitor_hostname"]; h != "" && h != "null" {
				m.URL = h
				if p := labels["monitor_port"]; p != "" && p != "null" {
					m.URL += ":" + p
				}
			}
			out[mn] = m
		}
		switch name {
		case "monitor_status":
			m.Status = int(val)
		case "monitor_cert_days_remaining":
			d := int(val)
			m.CertDays = &d
		}
	}
	for n, m := range out {
		if m.Status < 0 {
			delete(out, n) // only a certificate gauge left behind
		}
	}
	return out, nil
}

// parsePromLine reads `name{a="x",b="y"} 1`.
func parsePromLine(line string) (string, map[string]string, float64, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil, 0, false
	}
	labels := map[string]string{}
	name, rest := line, ""
	if i := strings.IndexByte(line, '{'); i >= 0 {
		name = line[:i]
		j := i + 1
		for j < len(line) && line[j] != '}' {
			eq := strings.IndexByte(line[j:], '=')
			if eq < 0 || j+eq+1 >= len(line) || line[j+eq+1] != '"' {
				return "", nil, 0, false
			}
			key := strings.TrimSpace(line[j : j+eq])
			k := j + eq + 2
			var b strings.Builder
			for k < len(line) && line[k] != '"' {
				if line[k] == '\\' && k+1 < len(line) {
					k++
					switch line[k] {
					case 'n':
						b.WriteByte('\n')
					default:
						b.WriteByte(line[k])
					}
				} else {
					b.WriteByte(line[k])
				}
				k++
			}
			labels[key] = b.String()
			j = k + 1
			if j < len(line) && line[j] == ',' {
				j++
			}
		}
		rest = strings.TrimSpace(line[min(j+1, len(line)):])
	} else if sp := strings.IndexByte(line, ' '); sp > 0 {
		name, rest = line[:sp], strings.TrimSpace(line[sp:])
	}
	if f := strings.Fields(rest); len(f) > 0 {
		v, err := strconv.ParseFloat(f[0], 64)
		return name, labels, v, err == nil
	}
	return "", nil, 0, false
}

// statusPage reads the monitors on the status page and their last checks.
func (kuma) statusPage(ctx context.Context, t Target) (string, map[string]*kumaMonitor, error) {
	slug := strings.TrimSpace(t.Fields["STATUS_SLUG"])
	if slug == "" {
		return "", nil, nil
	}
	var page struct {
		Config struct {
			Title string `json:"title"`
		} `json:"config"`
		Groups []struct {
			Monitors []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"monitorList"`
		} `json:"publicGroupList"`
	}
	if _, err := doJSON(ctx, t, "Uptime Kuma", http.MethodGet, "/api/status-page/"+url.PathEscape(slug), nil, nil, &page); err != nil {
		if appStatus(err) == http.StatusNotFound {
			return "", nil, fmt.Errorf("Uptime Kuma has no status page %q (STATUS_SLUG)", slug)
		}
		return "", nil, err
	}
	var beats struct {
		List map[string][]struct {
			Status int    `json:"status"`
			Time   string `json:"time"`
			Msg    string `json:"msg"`
		} `json:"heartbeatList"`
	}
	if _, err := doJSON(ctx, t, "Uptime Kuma", http.MethodGet, "/api/status-page/heartbeat/"+url.PathEscape(slug), nil, nil, &beats); err != nil {
		return "", nil, err
	}
	out := map[string]*kumaMonitor{}
	for _, g := range page.Groups {
		for _, m := range g.Monitors {
			km := &kumaMonitor{Name: m.Name, Type: m.Type, URL: m.URL, Status: -1}
			if l := beats.List[strconv.FormatInt(m.ID, 10)]; len(l) > 0 {
				last := l[len(l)-1]
				km.Status, km.Message, km.At = last.Status, strings.TrimSpace(last.Msg), last.Time
			}
			out[m.Name] = km
		}
	}
	return page.Config.Title, out, nil
}

// monitors merges what Core can read: metrics win, the status page adds messages.
func (c kuma) monitors(ctx context.Context, t Target) (map[string]*kumaMonitor, string, error) {
	if strings.TrimSpace(t.Fields["API_KEY"]) == "" && strings.TrimSpace(t.Fields["STATUS_SLUG"]) == "" {
		return nil, "", errors.New("the Uptime Kuma service in Core needs an API_KEY (Settings → API Keys: every monitor) or a STATUS_SLUG (a status page's monitors)")
	}
	all, err := c.metrics(ctx, t)
	if err != nil {
		return nil, "", err
	}
	title, page, err := c.statusPage(ctx, t)
	if err != nil {
		if all == nil {
			return nil, "", err
		}
		page = nil // the messages are a bonus
	}
	if all == nil {
		all = map[string]*kumaMonitor{}
		for n, m := range page {
			if m.Status >= 0 {
				all[n] = m
			}
		}
		return all, title, nil
	}
	for n, m := range all {
		if p, ok := page[n]; ok {
			m.Message, m.At = p.Message, p.At
		}
	}
	return all, title, nil
}

func (c kuma) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	all, title, err := c.monitors(ctx, t)
	if err != nil {
		return info, err
	}
	info.Admin = true
	down := 0
	for _, m := range all {
		if m.Status == 0 {
			down++
		}
	}
	src := "every monitor (API key)"
	if strings.TrimSpace(t.Fields["API_KEY"]) == "" {
		src = fmt.Sprintf("the monitors on status page %q only; add an API key to watch every monitor", title)
	}
	info.Detail = fmt.Sprintf("%d monitors, %d down now; watching %s; no accounts or sign-in here", len(all), down, src)
	return info, nil
}

type kumaState struct {
	Init       bool             `json:"init"`
	Status     map[string]int   `json:"status"`
	DownSince  map[string]int64 `json:"down_since,omitempty"`
	CertWarned map[string]int   `json:"cert_warned,omitempty"` // smallest threshold already announced
}

func (c kuma) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	all, _, err := c.monitors(ctx, t)
	if err != nil {
		return nil, state, err
	}
	var prev kumaState
	_ = json.Unmarshal(state, &prev)
	cur := kumaState{Init: true, Status: map[string]int{}, DownSince: map[string]int64{}, CertWarned: map[string]int{}}
	now := t.Clock()
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Event
	for _, n := range names {
		m := all[n]
		cur.Status[n] = m.Status
		if m.Status == 0 {
			cur.DownSince[n] = now.Unix()
			if s, ok := prev.DownSince[n]; ok {
				cur.DownSince[n] = s
			}
		}
		// the smallest warning threshold the certificate is under; one event per step down
		certStep := 0
		if m.CertDays != nil {
			for _, th := range kumaCertWarn {
				if *m.CertDays <= th {
					certStep = th
				}
			}
		}
		announced := prev.CertWarned[n]
		if certStep > 0 {
			cur.CertWarned[n] = certStep
		}
		if !prev.Init {
			continue // baseline: what is down or expiring now is not news
		}
		p := map[string]any{"monitor": m.Name, "monitor_type": m.Type}
		if m.URL != "" {
			p["target"] = m.URL
		}
		if m.Message != "" {
			p["message"] = m.Message
		}
		if m.At != "" {
			p["checked_at"] = m.At
		}
		if pub := strings.TrimRight(t.PublicURL, "/"); pub != "" {
			p["url"] = pub + "/dashboard"
		}
		was, known := prev.Status[n]
		switch {
		case m.Status == 0 && (!known || was != 0):
			p["title"] = m.Name + " is down"
			out = append(out, Event{Name: "monitor_down", Subject: "monitor:" + n, Payload: p})
		case m.Status == 1 && known && was == 0:
			p["title"] = m.Name + " is back up"
			if s, ok := prev.DownSince[n]; ok {
				p["down_for_seconds"] = now.Unix() - s
				p["down_since"] = time.Unix(s, 0).UTC().Format(time.RFC3339)
			}
			out = append(out, Event{Name: "monitor_up", Subject: "monitor:" + n, Payload: p})
		}
		if certStep > 0 && (announced == 0 || certStep < announced) {
			q := map[string]any{}
			for k, v := range p {
				q[k] = v
			}
			q["days_remaining"] = *m.CertDays
			q["title"] = fmt.Sprintf("%s: certificate expires in %d days", m.Name, *m.CertDays)
			out = append(out, Event{Name: "cert_expiring", Subject: "monitor:" + n, Payload: q})
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}
