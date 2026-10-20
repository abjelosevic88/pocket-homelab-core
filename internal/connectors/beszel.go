package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Beszel (0.21, a PocketBase 0.40 app) connector: events only, polled. Core
// signs in with the service's EMAIL and PASSWORD and reads; nothing in Beszel
// is created or changed:
//
//	POST /api/collections/users/auth-with-password        sign in (identity = EMAIL) → token
//	POST /api/collections/_superusers/auth-with-password  the same, when EMAIL is only a PocketBase superuser (/_/ login)
//	GET  /api/beszel/info                                 hub version ("v"; signed in)
//	GET  /api/collections/systems/records                 systems the account sees: status, agent version, readings
//	GET  /api/collections/alerts/records                  how many alerts the account set (Test only)
//	GET  /api/collections/alerts_history/records          alerts that fired and their resolution
//
// The token goes in Authorization as is (PocketBase's convention), one
// sign-in per call. Signing in writes nothing Beszel shows, but PocketBase
// remembers the login origin (IP + User-Agent, kept stable here) and may
// mail a "new login" alert the very first time, if SMTP is set up.
//
// Beszel shows a user only the systems shared with them and only their own
// alert history (the alerts they set). A superuser sees every system and
// every user's history; the same alert set by two users then fires twice,
// so alert events are merged per system + alert (+ network monitor).
// Systems and alerts are household things: no user_email.
type beszel struct{}

func init() { Register(beszel{}) }

// beszelPerPage: records asked per page (PocketBase allows up to 1000).
var beszelPerPage = 200

// beszelMaxNew: new alert history records handled per poll; the rest wait
// for the next poll (nothing is skipped).
var beszelMaxNew = 50

// beszelMaxOpen: fired, unresolved alerts remembered to announce their end.
const beszelMaxOpen = 200

// beszelMaxPages bounds every list.
const beszelMaxPages = 10

const beszelUA = "Pocket Core (Beszel connector)"

var (
	beszelIDRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	beszelDateRe = regexp.MustCompile(`^[0-9][0-9 :.TZ+-]{9,40}$`)
)

func (beszel) Type() string                 { return "beszel" }
func (beszel) Capabilities() Capabilities   { return Capabilities{Events: true} }
func (beszel) SSORedirects(string) []string { return nil }

func (beszel) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Core does not make Beszel accounts: add people in Beszel's admin (/_/ → users) and share systems with them there")
}

func (beszel) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Core does not set up Beszel's OpenID sign-in: configure it in Beszel's admin (/_/ → users → Options → OAuth2)")
	}
	return nil
}

// ---- signing in ----

type beszelSession struct {
	token     string
	superuser bool
	email     string
	role      string // users only: admin, user or readonly
}

func (s beszelSession) hdr() map[string]string {
	return map[string]string{"Authorization": s.token, "User-Agent": beszelUA}
}

func beszelWho(t Target) string {
	if pub := strings.TrimRight(t.PublicURL, "/"); pub != "" {
		return pub
	}
	return "Beszel"
}

// beszelMFA: PocketBase answers a correct password with 401 {"mfaId"} when
// the account needs a one-time code as well.
func beszelMFA(err error) bool {
	var ae *appError
	return errors.As(err, &ae) && ae.Status == http.StatusUnauthorized && strings.Contains(ae.Msg, "mfaId")
}

func (beszel) signIn(ctx context.Context, t Target) (beszelSession, error) {
	email := strings.TrimSpace(t.Fields["EMAIL"])
	pw := t.Fields["PASSWORD"]
	if email == "" || pw == "" {
		return beszelSession{}, fmt.Errorf("the Beszel service in Core needs EMAIL and PASSWORD: the login you use at %s", beszelWho(t))
	}
	try := func(coll string) (beszelSession, error) {
		var res struct {
			Token  string `json:"token"`
			Record struct {
				Email string `json:"email"`
				Role  string `json:"role"`
			} `json:"record"`
		}
		body := map[string]string{"identity": email, "password": pw}
		if _, err := doJSON(ctx, t, "Beszel", http.MethodPost, "/api/collections/"+coll+"/auth-with-password", map[string]string{"User-Agent": beszelUA}, body, &res, pw); err != nil {
			return beszelSession{}, err
		}
		if res.Token == "" {
			return beszelSession{}, errors.New("Beszel signed in but returned no token")
		}
		s := beszelSession{token: res.Token, superuser: coll == "_superusers", email: res.Record.Email, role: res.Record.Role}
		if s.email == "" {
			s.email = email
		}
		return s, nil
	}
	s, uerr := try("users")
	if uerr == nil {
		return s, nil
	}
	if beszelMFA(uerr) {
		return beszelSession{}, errors.New("Beszel asks this account for a one-time code (MFA_OTP is on) and Core cannot answer it: use a Beszel user without MFA (MFA_OTP=superusers keeps it for superusers only)")
	}
	us := appStatus(uerr)
	if us != http.StatusBadRequest && us != http.StatusForbidden {
		return beszelSession{}, uerr // unreachable, 5xx…: no point trying another account type
	}
	// Not a Beszel user (or password sign-in is off for users): the same
	// login may be a PocketBase superuser, Beszel's /_/ admin.
	s, serr := try("_superusers")
	if serr == nil {
		return s, nil
	}
	if beszelMFA(serr) {
		return beszelSession{}, errors.New("EMAIL is a Beszel superuser that needs a one-time code (MFA_OTP) and Core cannot answer it: use a regular Beszel user without MFA instead")
	}
	ss := appStatus(serr)
	switch {
	case us == http.StatusBadRequest && ss == http.StatusBadRequest:
		return beszelSession{}, fmt.Errorf("Beszel rejected EMAIL and PASSWORD (HTTP 400, as a user and as a superuser): check them in the Beszel service in Core; they are the login you use at %s", beszelWho(t))
	case us == http.StatusForbidden && ss == http.StatusBadRequest:
		return beszelSession{}, errors.New("Beszel has password sign-in turned off for users (DISABLE_PASSWORD_AUTH=true) and EMAIL/PASSWORD is not a superuser either: turn password sign-in back on, or give Core a superuser login")
	case ss == http.StatusForbidden:
		return beszelSession{}, errors.New("Beszel refused the superuser sign-in (HTTP 403; its superuser IP allow-list?) and EMAIL/PASSWORD is not a Beszel user: give Core a regular Beszel user")
	}
	return beszelSession{}, serr
}

// ---- reading ----

type beszelPage[T any] struct {
	Page       int `json:"page"`
	PerPage    int `json:"perPage"`
	TotalItems int `json:"totalItems"`
	TotalPages int `json:"totalPages"`
	Items      []T `json:"items"`
}

// list reads a PocketBase collection page by page, at most limit records
// (0 = all, up to beszelMaxPages pages). It returns the total PocketBase counted.
func beszelList[T any](ctx context.Context, t Target, s beszelSession, coll string, q url.Values, limit int) ([]T, int, error) {
	per := beszelPerPage
	if limit > 0 && limit < per {
		per = limit
	}
	var out []T
	total := 0
	for page := 1; page <= beszelMaxPages; page++ {
		v := url.Values{}
		for k, vs := range q {
			v[k] = vs
		}
		v.Set("page", strconv.Itoa(page))
		v.Set("perPage", strconv.Itoa(per))
		var res beszelPage[T]
		if _, err := doJSON(ctx, t, "Beszel", http.MethodGet, "/api/collections/"+coll+"/records?"+v.Encode(), s.hdr(), nil, &res, s.token); err != nil {
			switch appStatus(err) {
			case http.StatusUnauthorized:
				return nil, 0, errors.New("Beszel did not accept Core's sign-in token (HTTP 401): sign in again with Test")
			case http.StatusForbidden:
				return nil, 0, fmt.Errorf("the Beszel account in Core may not read %s (HTTP 403)", coll)
			}
			return nil, 0, err
		}
		total = res.TotalItems
		out = append(out, res.Items...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], total, nil
		}
		if len(res.Items) < per || (res.TotalPages > 0 && page >= res.TotalPages) {
			break
		}
	}
	return out, total, nil
}

type beszelSystem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"` // up, down, paused, pending
	Host    string `json:"host"`
	Updated string `json:"updated"`
	Info    *struct {
		CPU       float64            `json:"cpu"`
		MemPct    float64            `json:"mp"`
		DiskPct   float64            `json:"dp"`
		ExtraFs   map[string]float64 `json:"efs"`
		Temp      float64            `json:"dt"`
		Bandwidth float64            `json:"bb"` // bytes/s
		LoadAvg   []float64          `json:"la"`
		GPU       float64            `json:"g"`
		Agent     string             `json:"v"`
	} `json:"info"`
}

func (c beszel) systems(ctx context.Context, t Target, s beszelSession) ([]beszelSystem, error) {
	q := url.Values{"sort": {"name,id"}, "fields": {"id,name,status,host,updated,info"}}
	l, _, err := beszelList[beszelSystem](ctx, t, s, "systems", q, 0)
	return l, err
}

type beszelHistory struct {
	ID       string  `json:"id"`
	System   string  `json:"system"`
	Name     string  `json:"name"`
	Monitor  string  `json:"monitor_name"`
	Value    float64 `json:"value"`
	Created  string  `json:"created"`
	Resolved string  `json:"resolved"`
	Expand   struct {
		System *struct {
			Name string `json:"name"`
		} `json:"system"`
	} `json:"expand"`
}

// key: one incident, whichever Beszel user's alert recorded it.
func (h beszelHistory) key() string { return h.System + "|" + h.Name + "|" + h.Monitor }

var beszelHistoryQ = url.Values{"expand": {"system"}, "fields": {"id,system,name,monitor_name,value,created,resolved,expand.system.name"}}

func beszelHistoryList(ctx context.Context, t Target, s beszelSession, filter, sort string, limit int) ([]beszelHistory, error) {
	q := url.Values{}
	for k, v := range beszelHistoryQ {
		q[k] = v
	}
	if filter != "" {
		q.Set("filter", filter)
	}
	q.Set("sort", sort)
	l, _, err := beszelList[beszelHistory](ctx, t, s, "alerts_history", q, limit)
	return l, err
}

func (c beszel) version(ctx context.Context, t Target, s beszelSession) string {
	var info struct {
		V string `json:"v"`
	}
	if _, err := doJSON(ctx, t, "Beszel", http.MethodGet, "/api/beszel/info", s.hdr(), nil, &info, s.token); err != nil {
		return ""
	}
	return info.V
}

func (c beszel) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	s, err := c.signIn(ctx, t)
	if err != nil {
		return info, err
	}
	info.Version = c.version(ctx, t, s)
	sys, err := c.systems(ctx, t, s)
	if err != nil {
		return info, err
	}
	down, paused := 0, 0
	for _, x := range sys {
		switch x.Status {
		case "down":
			down++
		case "paused":
			paused++
		}
	}
	_, alerts, err := beszelList[struct{}](ctx, t, s, "alerts", url.Values{"fields": {"id"}}, 1)
	if err != nil {
		return info, err
	}
	info.Admin = s.superuser || s.role == "admin"
	who := fmt.Sprintf("signed in as %s (Beszel user, role %s)", s.email, s.role)
	if s.superuser {
		who = fmt.Sprintf("signed in as %s (Beszel superuser)", s.email)
	}
	scope := fmt.Sprintf("%d alerts set on this account (alert events come from these)", alerts)
	if s.superuser {
		scope = "a superuser sees every system and every user's alert history (repeats are merged); a regular Beszel user with the household's alerts is enough"
	}
	if len(sys) == 0 {
		scope += "; this account sees no systems: share them with it in Beszel"
	}
	info.Detail = fmt.Sprintf("%s; %d systems, %d down now, %d paused; %s; no accounts or sign-in here", who, len(sys), down, paused, scope)
	return info, nil
}

// ---- polling ----

type beszelSysState struct {
	Status    string `json:"s"`
	DownSince int64  `json:"d,omitempty"` // still down (or reconnecting) since
	Agent     string `json:"v,omitempty"`
}

type beszelOpen struct {
	Key     string `json:"k"`
	Created string `json:"c"`
}

type beszelState struct {
	Init     bool                      `json:"init"`
	Systems  map[string]beszelSysState `json:"systems"`
	Since    string                    `json:"since,omitempty"`     // newest alert history record handled (created)
	SinceIDs []string                  `json:"since_ids,omitempty"` // the ones handled with exactly that time
	Open     map[string]beszelOpen     `json:"open,omitempty"`      // fired, unresolved: history id →
}

// beszelTime parses PocketBase's "2006-01-02 15:04:05.000Z".
func beszelTime(s string) (time.Time, bool) {
	for _, l := range []string{"2006-01-02 15:04:05.000Z", "2006-01-02 15:04:05Z", time.RFC3339Nano} {
		if tm, err := time.Parse(l, strings.TrimSpace(s)); err == nil {
			return tm, true
		}
	}
	return time.Time{}, false
}

func beszelRFC(s string) string {
	if tm, ok := beszelTime(s); ok {
		return tm.UTC().Format(time.RFC3339)
	}
	return s
}

func (c beszel) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	s, err := c.signIn(ctx, t)
	if err != nil {
		return nil, state, err
	}
	sys, err := c.systems(ctx, t, s)
	if err != nil {
		return nil, state, err
	}
	var prev beszelState
	_ = json.Unmarshal(state, &prev)
	now := t.Clock()
	pub := strings.TrimRight(t.PublicURL, "/")
	cur := beszelState{Init: true, Systems: map[string]beszelSysState{}, Open: map[string]beszelOpen{}}
	byID := map[string]beszelSystem{}
	var out []Event

	sysPayload := func(x beszelSystem) map[string]any {
		p := map[string]any{"system": x.Name, "system_id": x.ID, "status": x.Status}
		if x.Host != "" {
			p["host"] = x.Host
		}
		if pub != "" {
			p["url"] = pub + "/system/" + url.PathEscape(x.ID)
		}
		return p
	}

	// ---- systems ----
	for _, x := range sys {
		byID[x.ID] = x
		was, known := prev.Systems[x.ID]
		st := beszelSysState{Status: x.Status, Agent: was.Agent}
		if x.Info != nil && x.Info.Agent != "" {
			st.Agent = x.Info.Agent // a paused system's info is cleared: keep the last known
		}
		switch x.Status {
		case "down":
			st.DownSince = was.DownSince
			if st.DownSince == 0 {
				st.DownSince = now.Unix()
				// Beszel stops updating a down system: its last update is when it went down
				if tm, ok := beszelTime(x.Updated); ok && tm.Before(now) {
					st.DownSince = tm.Unix()
				}
			}
		case "pending":
			st.DownSince = was.DownSince // a down system reconnecting passes through pending
		}
		cur.Systems[x.ID] = st
		if !prev.Init {
			continue // baseline: what is down now is not news
		}
		p := sysPayload(x)
		switch {
		case x.Status == "down" && (!known || was.DownSince == 0): // down → pending → down is one outage
			p["title"] = x.Name + " is down"
			p["down_since"] = time.Unix(st.DownSince, 0).UTC().Format(time.RFC3339)
			if known {
				p["previous_status"] = was.Status
			}
			out = append(out, Event{Name: "system_down", Subject: "system:" + x.ID, Payload: p})
		case x.Status == "up" && known && was.DownSince != 0:
			p["title"] = x.Name + " is back up"
			p["down_since"] = time.Unix(was.DownSince, 0).UTC().Format(time.RFC3339)
			p["down_for_seconds"] = max(now.Unix()-was.DownSince, 0)
			out = append(out, Event{Name: "system_up", Subject: "system:" + x.ID, Payload: p})
		}
		if known && was.Agent != "" && st.Agent != "" && st.Agent != was.Agent {
			q := sysPayload(x)
			q["title"] = fmt.Sprintf("%s: Beszel agent %s → %s", x.Name, was.Agent, st.Agent)
			q["from_version"], q["to_version"] = was.Agent, st.Agent
			out = append(out, Event{Name: "agent_updated", Subject: "system:" + x.ID, Payload: q})
		}
	}

	// ---- alerts ----
	if !prev.Init {
		newest, err := beszelHistoryList(ctx, t, s, "", "-created,-id", 50)
		if err != nil {
			return nil, state, err
		}
		if len(newest) > 0 {
			cur.Since = newest[0].Created
			for _, h := range newest {
				if h.Created == cur.Since {
					cur.SinceIDs = append(cur.SinceIDs, h.ID)
				}
			}
		}
		open, err := beszelHistoryList(ctx, t, s, "resolved = null", "-created,-id", beszelMaxOpen)
		if err != nil {
			return nil, state, err
		}
		for _, h := range open {
			cur.Open[h.ID] = beszelOpen{Key: h.key(), Created: h.Created}
		}
		next, _ := json.Marshal(cur)
		return out, next, nil
	}

	openKeys := map[string]int{}
	for id, o := range prev.Open {
		if beszelIDRe.MatchString(id) {
			cur.Open[id] = o
			openKeys[o.Key]++
		}
	}
	resolve := func(h beszelHistory) {
		o, ok := cur.Open[h.ID]
		if !ok {
			return
		}
		delete(cur.Open, h.ID)
		if openKeys[o.Key]--; openKeys[o.Key] > 0 {
			return // another Beszel user's record of the same incident is still open
		}
		delete(openKeys, o.Key)
		out = append(out, c.alertEvent(h, false, byID, pub))
	}

	// what was open: resolved since, or gone (Beszel prunes old history)
	ids := make([]string, 0, len(cur.Open))
	for id := range cur.Open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	var resolved []beszelHistory
	for i := 0; i < len(ids); i += 40 {
		chunk := ids[i:min(i+40, len(ids))]
		parts := make([]string, len(chunk))
		for j, id := range chunk {
			parts[j] = `id = "` + id + `"`
		}
		l, err := beszelHistoryList(ctx, t, s, "("+strings.Join(parts, " || ")+")", "resolved,id", len(chunk))
		if err != nil {
			return nil, state, err
		}
		for _, h := range l {
			seen[h.ID] = true
			if h.Resolved != "" {
				resolved = append(resolved, h)
			}
		}
	}
	for _, id := range ids {
		if !seen[id] {
			if o := cur.Open[id]; openKeys[o.Key] > 0 {
				openKeys[o.Key]--
			}
			delete(cur.Open, id) // deleted in Beszel: nothing to announce
		}
	}
	sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].Resolved < resolved[j].Resolved })
	for _, h := range resolved {
		resolve(h)
	}

	// what fired since the last poll, oldest first
	filter := ""
	if beszelDateRe.MatchString(prev.Since) {
		filter = `created >= "` + prev.Since + `"`
	}
	fresh, err := beszelHistoryList(ctx, t, s, filter, "created,id", beszelMaxNew+len(prev.SinceIDs))
	if err != nil {
		return nil, state, err
	}
	cur.Since, cur.SinceIDs = prev.Since, prev.SinceIDs
	handled := map[string]bool{}
	for _, id := range prev.SinceIDs {
		handled[id] = true
	}
	n := 0
	for _, h := range fresh {
		if handled[h.ID] || (prev.Since != "" && h.Created < prev.Since) {
			continue
		}
		if n >= beszelMaxNew {
			break // the rest next time
		}
		n++
		if h.Created != cur.Since {
			cur.Since, cur.SinceIDs = h.Created, nil
		}
		cur.SinceIDs = append(cur.SinceIDs, h.ID)
		if !beszelIDRe.MatchString(h.ID) {
			continue
		}
		k := h.key()
		if openKeys[k] == 0 {
			out = append(out, c.alertEvent(h, true, byID, pub))
		}
		openKeys[k]++
		cur.Open[h.ID] = beszelOpen{Key: k, Created: h.Created}
		if h.Resolved != "" { // fired and ended between two polls
			resolve(h)
		}
	}
	if len(cur.SinceIDs) > 2*beszelMaxNew {
		cur.SinceIDs = cur.SinceIDs[len(cur.SinceIDs)-2*beszelMaxNew:]
	}
	// bounded: forget the oldest open alerts (their end goes unannounced)
	if len(cur.Open) > beszelMaxOpen {
		type kv struct {
			id string
			o  beszelOpen
		}
		all := make([]kv, 0, len(cur.Open))
		for id, o := range cur.Open {
			all = append(all, kv{id, o})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].o.Created > all[j].o.Created })
		for _, x := range all[beszelMaxOpen:] {
			delete(cur.Open, x.id)
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}

// ---- alert events ----

var beszelAlertLabel = map[string]string{
	"Status": "Status", "CPU": "CPU usage", "CPUIOWait": "CPU I/O wait", "CPUSteal": "CPU steal time",
	"Memory": "Memory usage", "Disk": "Disk usage", "Bandwidth": "Bandwidth", "GPU": "GPU usage",
	"Temperature": "Temperature", "LoadAvg1": "1m load average", "LoadAvg5": "5m load average",
	"LoadAvg15": "15m load average", "Battery": "Battery", "ContainerHealth": "Container health",
	"SystemdFailed": "Failed services", "NetworkMonitorLoss": "Network monitor loss",
}

var beszelAlertUnit = map[string]string{
	"CPU": "%", "CPUIOWait": "%", "CPUSteal": "%", "Memory": "%", "Disk": "%", "GPU": "%", "Battery": "%",
	"NetworkMonitorLoss": "%", "Bandwidth": " MB/s", "Temperature": "°C",
	"LoadAvg1": "", "LoadAvg5": "", "LoadAvg15": "",
}

func beszelNum(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }

// beszelReading: the system's reading for a metric alert at this poll (not
// the average over the alert's minutes that fired it).
func beszelReading(x beszelSystem, alert string) (float64, bool) {
	if x.Info == nil || x.Status != "up" {
		return 0, false
	}
	in := x.Info
	switch alert {
	case "CPU":
		return in.CPU, true
	case "Memory":
		return in.MemPct, true
	case "Disk":
		v := in.DiskPct
		for _, f := range in.ExtraFs {
			v = max(v, f)
		}
		return v, true
	case "Temperature":
		return in.Temp, in.Temp != 0
	case "Bandwidth":
		return in.Bandwidth / (1024 * 1024), true
	case "GPU":
		return in.GPU, in.GPU != 0
	case "LoadAvg1", "LoadAvg5", "LoadAvg15":
		i := map[string]int{"LoadAvg1": 0, "LoadAvg5": 1, "LoadAvg15": 2}[alert]
		if i < len(in.LoadAvg) {
			return in.LoadAvg[i], true
		}
	}
	return 0, false
}

func (beszel) alertEvent(h beszelHistory, fired bool, byID map[string]beszelSystem, pub string) Event {
	x, visible := byID[h.System]
	name := x.Name
	if name == "" && h.Expand.System != nil {
		name = h.Expand.System.Name
	}
	if name == "" {
		name = h.System
	}
	label := beszelAlertLabel[h.Name]
	if label == "" {
		label = h.Name // "Storage Pool: tank" and names from newer Beszel versions
	}
	unit, metric := beszelAlertUnit[h.Name]
	p := map[string]any{"system": name, "system_id": h.System, "alert": h.Name, "alert_label": label, "triggered_at": beszelRFC(h.Created)}
	if h.Monitor != "" {
		p["monitor"] = h.Monitor
	}
	if pub != "" {
		p["url"] = pub + "/system/" + url.PathEscape(h.System)
	}
	what := label
	if h.Monitor != "" {
		what += " (" + h.Monitor + ")"
	}
	// Beszel's history keeps the alert's threshold as "value", except for
	// network monitors, where it is the measured loss.
	var detail string
	switch {
	case h.Name == "NetworkMonitorLoss":
		p["value"], p["unit"] = h.Value, unit
		detail = " at " + beszelNum(h.Value) + unit
	case metric:
		p["threshold"], p["unit"] = h.Value, unit
		dir := "above"
		if h.Name == "Battery" {
			dir = "below"
		}
		detail = " " + dir + " " + beszelNum(h.Value) + unit
		if fired && visible {
			if v, ok := beszelReading(x, h.Name); ok {
				p["current_value"] = math.Round(v*100) / 100
			}
		}
	}
	ev := Event{Subject: "alert:" + h.System + "/" + h.Name, Payload: p}
	if h.Monitor != "" {
		ev.Subject += "/" + h.Monitor
	}
	if fired {
		ev.Name = "alert_triggered"
		switch h.Name {
		case "Status":
			p["title"] = name + ": Status alert, system down"
		case "ContainerHealth":
			p["title"] = name + ": containers unhealthy"
		case "SystemdFailed":
			p["title"] = name + ": systemd services failed"
		default:
			p["title"] = name + ": " + what + detail
		}
		return ev
	}
	ev.Name = "alert_resolved"
	p["resolved_at"] = beszelRFC(h.Resolved)
	if a, ok := beszelTime(h.Created); ok {
		if b, ok := beszelTime(h.Resolved); ok && !b.Before(a) {
			p["duration_seconds"] = int64(b.Sub(a) / time.Second)
		}
	}
	p["title"] = name + ": " + what + " resolved"
	return ev
}
