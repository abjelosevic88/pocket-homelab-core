package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Backrest (1.14) connector: events only, polled, strictly read-only.
// Backrest is the web UI and scheduler for restic. Its API is Connect RPC:
// every call is POST /v1.Backrest/<Method> with a JSON body, answered in
// protojson (camelCase, int64 as quoted numbers, enums by name). Core sends
// USERNAME/PASSWORD as HTTP Basic auth, which Backrest's middleware checks
// on every request (with authentication turned off in Backrest nothing is
// checked and the fields may stay empty). Read:
//
//	POST /v1.Backrest/GetConfig            instance name, repos, plans and their schedules (Core decodes ids, schedules and whether auth is off; never repo passwords or env)
//	POST /v1.Backrest/GetSummaryDashboard  each plan's recent backups (Test; first poll: each plan's last good backup)
//	POST /v1.Backrest/GetOperations        operations changed since the last check: {selector: {instanceId, modnoGte}, lastN}
//	GET  /  and  /assets/*.js              the public web UI; Test reads the version from it (the API has none)
//
// Never called: Backup, DoRepoTask, Forget, Restore, Cancel, RunCommand,
// SetConfig, ClearHistory, Login or anything else that changes Backrest or
// starts an operation. Plans and repos are household things: no user_email.
type backrest struct{}

func init() { Register(backrest{}) }

const (
	brMaxEvents = 50  // events per poll; the rest follow on the next poll
	brPageOps   = 500 // operations asked for per poll
	brBaseOps   = 200 // newest operations read for the baseline
	brSeenKeep  = 300 // announced operation ids remembered
	brMsgMax    = 500 // characters of Backrest's message in a payload
	brMinGrace  = 2 * time.Hour
	brGuessed   = 24 * time.Hour // assumed period when a cron expression cannot be read
	brCronDays  = 62             // days of a cron schedule sampled for its longest gap
)

func (backrest) Type() string               { return "backrest" }
func (backrest) Capabilities() Capabilities { return Capabilities{Events: true} }
func (backrest) SSORedirects(string) []string {
	return nil
}

func (backrest) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Backrest has no per-person accounts")
}

func (backrest) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Backrest has no OpenID sign-in")
	}
	return nil
}

// ---- wire types (protojson) ----

// brInt is a protojson int64: a quoted number (bare numbers accepted too).
type brInt int64

func (n *brInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return err
		}
		v = int64(f)
	}
	*n = brInt(v)
	return nil
}

// brStatus is an OperationStatus by name; numbers are mapped to names.
type brStatus string

const (
	brPending  brStatus = "STATUS_PENDING"
	brRunning  brStatus = "STATUS_INPROGRESS"
	brSuccess  brStatus = "STATUS_SUCCESS"
	brWarning  brStatus = "STATUS_WARNING"
	brError    brStatus = "STATUS_ERROR"
	brSysCanc  brStatus = "STATUS_SYSTEM_CANCELLED"
	brUserCanc brStatus = "STATUS_USER_CANCELLED"
)

func (s *brStatus) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = brStatus(v)
		return nil
	}
	names := map[string]brStatus{"1": brPending, "2": brRunning, "3": brSuccess, "4": brError, "5": brSysCanc, "6": brUserCanc, "7": brWarning}
	*s = names[string(b)]
	return nil
}

func (s brStatus) ok() bool { return s == brSuccess || s == brWarning }

type brSchedule struct {
	Disabled          bool   `json:"disabled"`
	Cron              string `json:"cron"`
	MaxFrequencyDays  int    `json:"maxFrequencyDays"`
	MaxFrequencyHours int    `json:"maxFrequencyHours"`
}

type brPlan struct {
	ID       string      `json:"id"`
	Repo     string      `json:"repo"`
	Schedule *brSchedule `json:"schedule"`
}

// brConfig is the little of Backrest's config Core looks at. The rest
// (repo passwords, env, hooks) is never decoded.
type brConfig struct {
	Instance string `json:"instance"`
	Repos    []struct {
		ID string `json:"id"`
	} `json:"repos"`
	Plans []brPlan `json:"plans"`
	Auth  *struct {
		Disabled bool `json:"disabled"`
	} `json:"auth"`
}

func (c brConfig) authOff() bool { return c.Auth == nil || c.Auth.Disabled }

type brSummary struct {
	FilesNew     brInt   `json:"filesNew"`
	FilesChanged brInt   `json:"filesChanged"`
	DataAdded    brInt   `json:"dataAdded"`
	TotalFiles   brInt   `json:"totalFilesProcessed"`
	TotalBytes   brInt   `json:"totalBytesProcessed"`
	Duration     float64 `json:"totalDuration"`
	SnapshotID   string  `json:"snapshotId"`
}

type brOp struct {
	ID         brInt    `json:"id"`
	Modno      brInt    `json:"modno"`
	RepoID     string   `json:"repoId"`
	PlanID     string   `json:"planId"`
	SnapshotID string   `json:"snapshotId"`
	Status     brStatus `json:"status"`
	Start      brInt    `json:"unixTimeStartMs"`
	End        brInt    `json:"unixTimeEndMs"`
	Message    string   `json:"displayMessage"`
	Backup     *struct {
		LastStatus *struct {
			Summary *brSummary `json:"summary"`
		} `json:"lastStatus"`
		Errors []struct {
			Item    string `json:"item"`
			During  string `json:"during"`
			Message string `json:"message"`
		} `json:"errors"`
		DryRun bool `json:"dryRun"`
	} `json:"operationBackup"`
	Restore *struct {
		LastStatus *struct {
			TotalBytes    brInt   `json:"totalBytes"`
			BytesRestored brInt   `json:"bytesRestored"`
			FilesRestored brInt   `json:"filesRestored"`
			Seconds       float64 `json:"secondsElapsed"`
		} `json:"lastStatus"`
	} `json:"operationRestore"`
	Hook *struct {
		Name      string          `json:"name"`
		Condition json.RawMessage `json:"condition"`
	} `json:"operationRunHook"`
	Check  *struct{} `json:"operationCheck"`
	Prune  *struct{} `json:"operationPrune"`
	Forget *struct{} `json:"operationForget"`
}

func (o brOp) ended() int64 {
	if o.End > 0 {
		return int64(o.End)
	}
	return int64(o.Start)
}

type brDashboard struct {
	Plans []struct {
		ID     string `json:"id"`
		Recent struct {
			Time     []brInt    `json:"timestampMs"`
			Duration []brInt    `json:"durationMs"`
			Status   []brStatus `json:"status"`
		} `json:"recentBackups"`
	} `json:"planSummaries"`
}

// brRecent is a plan's newest backup and its newest good one (end, unix ms).
type brRecent struct {
	Last       int64
	LastStatus brStatus
	LastOK     int64
}

func (d brDashboard) recent() map[string]brRecent {
	out := map[string]brRecent{}
	for _, p := range d.Plans {
		var r brRecent
		for i, ts := range p.Recent.Time {
			end := int64(ts)
			if i < len(p.Recent.Duration) {
				end += int64(p.Recent.Duration[i])
			}
			var st brStatus
			if i < len(p.Recent.Status) {
				st = p.Recent.Status[i]
			}
			if end > r.Last {
				r.Last, r.LastStatus = end, st
			}
			if st.ok() && end > r.LastOK {
				r.LastOK = end
			}
		}
		out[p.ID] = r
	}
	return out
}

// ---- calls ----

func (backrest) rpc(ctx context.Context, t Target, method string, in, out any) error {
	user, pass := strings.TrimSpace(t.Fields["USERNAME"]), t.Fields["PASSWORD"]
	hdr := map[string]string{"Connect-Protocol-Version": "1"}
	basic := ""
	if user != "" || pass != "" {
		basic = base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		hdr["Authorization"] = "Basic " + basic
	}
	if in == nil {
		in = struct{}{}
	}
	if _, err := doJSON(ctx, t, "Backrest", http.MethodPost, "/v1.Backrest/"+method, hdr, in, out, pass, basic); err != nil {
		switch appStatus(err) {
		case http.StatusUnauthorized:
			if user == "" || pass == "" {
				return errors.New("the Backrest service in Core needs USERNAME and PASSWORD (a Backrest user, as set under Settings → Users in Backrest): Backrest answered HTTP 401 without them")
			}
			return errors.New("Backrest rejected the stored USERNAME and PASSWORD (HTTP 401): check the user under Settings → Users in Backrest and save its password again on the service in Core")
		case http.StatusNotFound:
			return fmt.Errorf("Backrest has no %s call at this URL (HTTP 404): the service's URL must be Backrest's own address (port 9898 unless changed), without a path", method)
		}
		return err
	}
	return nil
}

func (c backrest) config(ctx context.Context, t Target) (brConfig, error) {
	var cfg brConfig
	err := c.rpc(ctx, t, "GetConfig", nil, &cfg)
	return cfg, err
}

func (c backrest) dashboard(ctx context.Context, t Target) (brDashboard, error) {
	var d brDashboard
	err := c.rpc(ctx, t, "GetSummaryDashboard", nil, &d)
	return d, err
}

// ops returns this instance's operations changed at or after modno (0 =
// any), at most n of them (Backrest keeps the newest by start time).
func (c backrest) ops(ctx context.Context, t Target, instance string, modno int64, n int) ([]brOp, error) {
	sel := map[string]any{}
	if instance != "" {
		sel["instanceId"] = instance
	}
	if modno > 0 {
		sel["modnoGte"] = strconv.FormatInt(modno, 10)
	}
	var res struct {
		Operations []brOp `json:"operations"`
	}
	err := c.rpc(ctx, t, "GetOperations", map[string]any{"selector": sel, "lastN": strconv.Itoa(n)}, &res)
	return res.Operations, err
}

var (
	brAssetRe = regexp.MustCompile(`(?:src|href)="(\.?/?assets/[^"?#]+\.js)"`)
	brBuildRe = regexp.MustCompile(`Build Version: \$\{([A-Za-z_$][\w$]*)\}`)
)

// version reads the build version the web UI logs to the console; the API
// does not expose one. Best effort: "" when it cannot be found.
func (backrest) version(ctx context.Context, t Target) string {
	var page []byte
	if _, err := doJSON(ctx, t, "Backrest", http.MethodGet, "/", map[string]string{"Accept": "text/html"}, nil, &page); err != nil {
		return ""
	}
	for _, m := range brAssetRe.FindAllSubmatch(page, 12) {
		var js []byte
		p := "/" + strings.TrimLeft(string(m[1]), "./")
		if _, err := doJSON(ctx, t, "Backrest", http.MethodGet, p, map[string]string{"Accept": "*/*"}, nil, &js); err != nil {
			continue
		}
		v := brBuildRe.FindSubmatch(js)
		if v == nil {
			continue
		}
		re := regexp.MustCompile(`(?:^|[^\w$])` + regexp.QuoteMeta(string(v[1])) + `="([^"]{1,40})"`)
		if m := re.FindSubmatch(js); m != nil {
			return strings.TrimPrefix(string(m[1]), "v")
		}
	}
	return ""
}

// ---- schedules ----

// brGrace reads GRACE_HOURS; set=false means the default.
func brGrace(t Target) (time.Duration, bool, error) {
	s := strings.TrimSpace(t.Fields["GRACE_HOURS"])
	if s == "" {
		return 0, false, nil
	}
	h, err := strconv.ParseFloat(s, 64)
	if err != nil || h < 0 || h > 24*366 {
		return 0, false, fmt.Errorf("GRACE_HOURS on the Backrest service must be a number of hours (e.g. 6), or empty for the default (a quarter of the schedule's interval, at least %d hours)", int(brMinGrace.Hours()))
	}
	return time.Duration(h * float64(time.Hour)), true, nil
}

// brExpect is how long a plan may go without a good backup.
type brExpect struct {
	Period  time.Duration // longest gap between scheduled runs
	Grace   time.Duration
	Guessed bool   // the cron expression could not be read: a daily plan assumed
	Desc    string // the schedule, for payloads
}

// brExpectation returns false for plans Backrest never runs by itself
// (no schedule, disabled) or runs less than about monthly.
func brExpectation(t Target, s *brSchedule, now time.Time) (brExpect, bool) {
	var e brExpect
	switch {
	case s == nil || s.Disabled:
		return e, false
	case s.MaxFrequencyHours > 0:
		e.Period, e.Desc = time.Duration(s.MaxFrequencyHours)*time.Hour, fmt.Sprintf("every %d hours", s.MaxFrequencyHours)
	case s.MaxFrequencyDays > 0:
		e.Period, e.Desc = time.Duration(s.MaxFrequencyDays)*24*time.Hour, fmt.Sprintf("every %d days", s.MaxFrequencyDays)
	case strings.TrimSpace(s.Cron) != "":
		e.Desc = "cron " + strings.TrimSpace(s.Cron)
		cr, ok := brParseCron(s.Cron)
		if !ok {
			e.Period, e.Guessed = brGuessed, true
			break
		}
		gap, ok := cr.maxGap(now)
		if !ok {
			return e, false
		}
		e.Period = gap
	default:
		return e, false
	}
	if g, set, _ := brGrace(t); set {
		e.Grace = g
	} else {
		e.Grace = max(brMinGrace, e.Period/4)
	}
	return e, true
}

// brCron is a five-field cron expression (minute hour day month weekday).
type brCron struct {
	min, hour, dom, mon, dow uint64
	domAny, dowAny           bool
}

var (
	brMonths = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
	brDays   = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}
	brMacros = map[string]string{"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *", "@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *"}
)

func brParseCron(expr string) (brCron, bool) {
	var c brCron
	f := strings.Fields(strings.TrimSpace(expr))
	if len(f) == 1 {
		m, ok := brMacros[strings.ToLower(f[0])]
		if !ok {
			return c, false
		}
		f = strings.Fields(m)
	}
	switch {
	case len(f) == 6 && f[5] == "*": // trailing year
		f = f[:5]
	case len(f) == 7 && f[6] == "*": // seconds … year
		f = f[1:6]
	}
	if len(f) != 5 {
		return c, false
	}
	var ok [5]bool
	c.min, ok[0] = brField(f[0], 0, 59, nil)
	c.hour, ok[1] = brField(f[1], 0, 23, nil)
	c.dom, ok[2] = brField(f[2], 1, 31, nil)
	c.mon, ok[3] = brField(f[3], 1, 12, brMonths)
	c.dow, ok[4] = brField(f[4], 0, 7, brDays)
	for _, o := range ok {
		if !o {
			return c, false
		}
	}
	if c.dow&(1<<7) != 0 {
		c.dow |= 1 // 7 is Sunday too
	}
	c.domAny = f[2] == "*" || f[2] == "?"
	c.dowAny = f[4] == "*" || f[4] == "?"
	return c, true
}

func brField(s string, lo, hi int, names map[string]int) (uint64, bool) {
	val := func(x string) (int, bool) {
		if v, ok := names[strings.ToUpper(x)]; ok {
			return v, true
		}
		v, err := strconv.Atoi(x)
		return v, err == nil
	}
	var mask uint64
	for _, part := range strings.Split(s, ",") {
		step, slash := 1, false
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n < 1 {
				return 0, false
			}
			step, slash, part = n, true, part[:i]
		}
		var a, b int
		switch {
		case part == "*" || part == "?":
			a, b = lo, hi
		case strings.Contains(part, "-"):
			x, y, _ := strings.Cut(part, "-")
			var ok1, ok2 bool
			a, ok1 = val(x)
			b, ok2 = val(y)
			if !ok1 || !ok2 {
				return 0, false
			}
		default:
			var ok bool
			if a, ok = val(part); !ok {
				return 0, false
			}
			b = a
			if slash {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, false
		}
		for v := a; v <= b; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, mask != 0
}

func (c brCron) day(d time.Time) bool {
	if c.mon&(1<<uint(d.Month())) == 0 {
		return false
	}
	dom, dow := c.dom&(1<<uint(d.Day())) != 0, c.dow&(1<<uint(d.Weekday())) != 0
	if c.domAny || c.dowAny {
		return dom && dow
	}
	return dom || dow
}

// maxGap is the longest time between consecutive runs over the next
// brCronDays days; false when it runs less than twice in that time.
func (c brCron) maxGap(from time.Time) (time.Duration, bool) {
	y, m, d := from.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, from.Location())
	var prev time.Time
	var gap time.Duration
	runs := 0
	for i := 0; i < brCronDays; i++ {
		day := start.AddDate(0, 0, i)
		if !c.day(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if c.hour&(1<<uint(h)) == 0 {
				continue
			}
			for mi := 0; mi < 60; mi++ {
				if c.min&(1<<uint(mi)) == 0 {
					continue
				}
				at := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, from.Location())
				if runs > 0 {
					gap = max(gap, at.Sub(prev))
				}
				prev = at
				runs++
			}
		}
	}
	return gap, runs >= 2
}

// ---- Test ----

func (c backrest) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	if _, _, err := brGrace(t); err != nil {
		return info, err
	}
	cfg, err := c.config(ctx, t)
	if err != nil {
		return info, err
	}
	dash, err := c.dashboard(ctx, t)
	if err != nil {
		return info, err
	}
	info.Admin = true // Backrest users have no roles
	info.Version = c.version(ctx, t)
	now := t.Clock()
	rec := dash.recent()
	parts := []string{fmt.Sprintf("%d repos, %d plans", len(cfg.Repos), len(cfg.Plans))}
	plans := append([]brPlan(nil), cfg.Plans...)
	sort.Slice(plans, func(i, j int) bool { return plans[i].ID < plans[j].ID })
	for _, p := range plans {
		r := rec[p.ID]
		s := p.ID + ": "
		if r.Last == 0 {
			s += "no backup in the last 30 days"
		} else {
			s += "last backup " + time.UnixMilli(r.Last).In(now.Location()).Format("2006-01-02 15:04") + " " + strings.ToLower(strings.TrimPrefix(string(r.LastStatus), "STATUS_"))
		}
		if e, ok := brExpectation(t, p.Schedule, now); !ok {
			s += " (not scheduled)"
		} else if r.LastOK > 0 && now.Sub(time.UnixMilli(r.LastOK)) > e.Period+e.Grace {
			s += " (overdue)"
		}
		parts = append(parts, s)
	}
	if cfg.authOff() {
		parts = append(parts, "authentication is OFF in Backrest: anyone who reaches it can run, change and restore backups")
	}
	parts = append(parts, "no accounts or sign-in here; Core only reads")
	info.Detail = strings.Join(parts, "; ")
	return info, nil
}

// ---- Poll ----

type backrestState struct {
	Init    bool             `json:"init"`
	Since   int64            `json:"since"`             // first poll, unix ms: what ended before it is not news
	Modno   int64            `json:"modno"`             // ask for operations changed from this modno on
	Seen    []int64          `json:"seen,omitempty"`    // announced operation ids, newest brSeenKeep
	LastOK  map[string]int64 `json:"last_ok,omitempty"` // plan → end of its last good backup, unix ms
	Watched map[string]int64 `json:"watched,omitempty"` // plan → when Core first saw it (the overdue clock while no good backup is known)
	Overdue map[string]int64 `json:"overdue,omitempty"` // plan → when backup_overdue was published
	Failing map[string]int64 `json:"failing,omitempty"` // check:<repo>, prune:<repo>, forget:<repo>/<plan> → failing since, unix ms
}

// brFailKey is the state key for operations announced as failing and recovered.
func brFailKey(o brOp) (kind, key string) {
	switch {
	case o.Check != nil:
		return "check", "check:" + o.RepoID
	case o.Prune != nil:
		return "prune", "prune:" + o.RepoID
	case o.Forget != nil:
		return "forget", "forget:" + o.RepoID + "/" + o.PlanID
	}
	return "", ""
}

func brFinished(s brStatus) bool { return s == brSuccess || s == brWarning || s == brError }

func (c backrest) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	if _, _, err := brGrace(t); err != nil {
		return nil, state, err
	}
	cfg, err := c.config(ctx, t)
	if err != nil {
		return nil, state, err
	}
	var prev backrestState
	_ = json.Unmarshal(state, &prev)
	now := t.Clock()
	nowMs := now.UnixMilli()
	if !prev.Init {
		st, err := c.baseline(ctx, t, cfg, now)
		if err != nil {
			return nil, state, err
		}
		next, _ := json.Marshal(st)
		return nil, next, nil
	}

	ops, err := c.ops(ctx, t, cfg.Instance, prev.Modno, brPageOps)
	if err != nil {
		return nil, state, err
	}
	cur := backrestState{Init: true, Since: prev.Since, Modno: prev.Modno,
		LastOK: map[string]int64{}, Watched: map[string]int64{}, Overdue: map[string]int64{}, Failing: map[string]int64{}}
	for k, v := range prev.LastOK {
		cur.LastOK[k] = v
	}
	for k, v := range prev.Failing {
		cur.Failing[k] = v
	}
	seen := map[int64]bool{}
	for _, id := range prev.Seen {
		seen[id] = true
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Modno != ops[j].Modno {
			return ops[i].Modno < ops[j].Modno
		}
		return ops[i].ID < ops[j].ID
	})
	var out []Event
	okNow := map[string]bool{}
	capped := false
	for _, o := range ops {
		if len(out) >= brMaxEvents {
			capped, cur.Modno = true, int64(o.Modno) // the rest on the next poll
			break
		}
		cur.Modno = max(cur.Modno, int64(o.Modno)+1)
		if !brFinished(o.Status) || seen[int64(o.ID)] || o.ended() < prev.Since {
			continue
		}
		seen[int64(o.ID)] = true
		if o.Backup != nil && !o.Backup.DryRun && o.Status.ok() {
			if o.ended() > cur.LastOK[o.PlanID] {
				cur.LastOK[o.PlanID] = o.ended()
			}
			okNow[o.PlanID] = true
		}
		out = append(out, c.opEvents(t, o, cur.Failing)...)
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	cur.Seen = ids[:min(len(ids), brSeenKeep)]

	// overdue plans; plans and repos gone from the config are forgotten
	plans := map[string]bool{}
	repos := map[string]bool{}
	for _, r := range cfg.Repos {
		repos[r.ID] = true
	}
	sorted := append([]brPlan(nil), cfg.Plans...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, p := range sorted {
		plans[p.ID] = true
		cur.Watched[p.ID] = nowMs
		if w, ok := prev.Watched[p.ID]; ok {
			cur.Watched[p.ID] = w
		}
		if s, ok := prev.Overdue[p.ID]; ok {
			cur.Overdue[p.ID] = s
		}
		if capped {
			continue // good backups may still be waiting in the next page
		}
		e, ok := brExpectation(t, p.Schedule, now)
		if !ok {
			delete(cur.Overdue, p.ID)
			continue
		}
		anchor, hadOK := cur.LastOK[p.ID]
		if !hadOK {
			anchor = cur.Watched[p.ID]
		}
		late := now.Sub(time.UnixMilli(anchor)) > e.Period+e.Grace
		pl := c.planPayload(t, p.ID, p.Repo)
		pl["schedule"] = e.Desc
		pl["expected_every_hours"] = e.Period.Hours()
		pl["grace_hours"] = e.Grace.Hours()
		if hadOK {
			pl["last_success_at"] = time.UnixMilli(anchor).UTC().Format(time.RFC3339)
		}
		since, was := cur.Overdue[p.ID]
		switch {
		case late && !was:
			cur.Overdue[p.ID] = nowMs
			h := int64(now.Sub(time.UnixMilli(anchor)).Hours())
			pl["hours_since"] = h
			if e.Guessed {
				pl["schedule_guessed"] = true
			}
			if hadOK {
				pl["title"] = fmt.Sprintf("No backup of %s for %d hours", p.ID, h)
			} else {
				pl["title"] = fmt.Sprintf("No backup of %s since Core started watching %d hours ago", p.ID, h)
			}
			out = append(out, Event{Name: "backup_overdue", Subject: "plan:" + p.ID, Payload: pl})
		case !late && was:
			delete(cur.Overdue, p.ID)
			if okNow[p.ID] {
				pl["overdue_since"] = time.UnixMilli(since).UTC().Format(time.RFC3339)
				pl["overdue_for_seconds"] = (nowMs - since) / 1000
				pl["title"] = "Backups of " + p.ID + " are running again"
				out = append(out, Event{Name: "backup_resumed", Subject: "plan:" + p.ID, Payload: pl})
			}
		}
	}
	for k := range cur.LastOK {
		if !plans[k] {
			delete(cur.LastOK, k)
		}
	}
	for k := range cur.Failing {
		_, rest, _ := strings.Cut(k, ":")
		repo, _, _ := strings.Cut(rest, "/")
		if !repos[repo] {
			delete(cur.Failing, k)
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}

// baseline records where the operation log is, each plan's last good
// backup and what is already failing or overdue, without announcing it.
func (c backrest) baseline(ctx context.Context, t Target, cfg brConfig, now time.Time) (backrestState, error) {
	nowMs := now.UnixMilli()
	st := backrestState{Init: true, Since: nowMs, LastOK: map[string]int64{}, Watched: map[string]int64{}, Overdue: map[string]int64{}, Failing: map[string]int64{}}
	dash, err := c.dashboard(ctx, t)
	if err != nil {
		return st, err
	}
	ops, err := c.ops(ctx, t, cfg.Instance, 0, brBaseOps)
	if err != nil {
		return st, err
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })
	for _, o := range ops {
		st.Modno = max(st.Modno, int64(o.Modno)+1)
		if !brFinished(o.Status) {
			continue
		}
		if _, key := brFailKey(o); key != "" {
			if o.Status == brError {
				if _, ok := st.Failing[key]; !ok {
					st.Failing[key] = o.ended()
				}
			} else {
				delete(st.Failing, key)
			}
		}
	}
	rec := dash.recent()
	for _, p := range cfg.Plans {
		st.Watched[p.ID] = nowMs
		anchor := nowMs
		if r := rec[p.ID]; r.LastOK > 0 {
			st.LastOK[p.ID] = r.LastOK
			anchor = r.LastOK
		}
		if e, ok := brExpectation(t, p.Schedule, now); ok && now.Sub(time.UnixMilli(anchor)) > e.Period+e.Grace {
			st.Overdue[p.ID] = nowMs
		}
	}
	return st, nil
}

// ---- payloads ----

var brUserinfoRe = regexp.MustCompile(`(://)[^/\s@]+@`)

// brMessage is Backrest's message for a payload: credentials in URLs and
// the stored password removed, cut to brMsgMax characters.
func brMessage(t Target, s string) string {
	s = redact(s, t.Fields["PASSWORD"])
	s = brUserinfoRe.ReplaceAllString(s, "${1}[redacted]@")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > brMsgMax {
		s = string(r[:brMsgMax]) + "…"
	}
	return s
}

func brSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func brLink(t Target, kind, id string) string {
	pub := strings.TrimRight(t.PublicURL, "/")
	if pub == "" {
		return ""
	}
	if id == "" || strings.HasPrefix(id, "_") {
		return pub + "/"
	}
	return pub + "/#/" + kind + "/" + url.PathEscape(id)
}

func (backrest) planPayload(t Target, plan, repo string) map[string]any {
	p := map[string]any{"plan": plan}
	if repo != "" {
		p["repo"] = repo
	}
	if u := brLink(t, "plan", plan); u != "" {
		p["url"] = u
	}
	return p
}

// opEvents turns one finished operation into its events and keeps failing
// up to date.
func (c backrest) opEvents(t Target, o brOp, failing map[string]int64) []Event {
	realPlan := o.PlanID != "" && !strings.HasPrefix(o.PlanID, "_")
	p := map[string]any{"repo": o.RepoID, "operation_id": int64(o.ID)}
	subject := "repo:" + o.RepoID
	link := brLink(t, "repo", o.RepoID)
	if realPlan {
		p["plan"] = o.PlanID
		subject = "plan:" + o.PlanID
		link = brLink(t, "plan", o.PlanID)
	}
	if link != "" {
		p["url"] = link
	}
	if o.Start > 0 {
		p["started_at"] = time.UnixMilli(int64(o.Start)).UTC().Format(time.RFC3339)
	}
	if o.End > 0 {
		p["finished_at"] = time.UnixMilli(int64(o.End)).UTC().Format(time.RFC3339)
		if o.Start > 0 && o.End >= o.Start {
			p["duration_seconds"] = int64(o.End-o.Start) / 1000
		}
	}
	msg := brMessage(t, o.Message)
	if msg != "" {
		p["message"] = msg
	}
	what := o.PlanID
	if !realPlan {
		what = o.RepoID
	}
	ev := func(name, title string) []Event {
		p["title"] = title
		return []Event{{Name: name, Subject: subject, Payload: p}}
	}

	switch {
	case o.Backup != nil:
		if o.Backup.DryRun {
			return nil
		}
		if o.Status != brError {
			var s *brSummary
			if o.Backup.LastStatus != nil {
				s = o.Backup.LastStatus.Summary
			}
			snap := o.SnapshotID
			if s != nil {
				if snap == "" {
					snap = s.SnapshotID
				}
				p["files_new"] = int64(s.FilesNew)
				p["files_changed"] = int64(s.FilesChanged)
				p["files_processed"] = int64(s.TotalFiles)
				p["bytes_added"] = int64(s.DataAdded)
				p["bytes_processed"] = int64(s.TotalBytes)
			}
			if snap != "" {
				p["snapshot_id"] = snap
			}
		}
		switch o.Status {
		case brSuccess:
			title := "Backup of " + what + " finished"
			if b, ok := p["bytes_added"].(int64); ok {
				title += ": " + brSize(b) + " added"
			}
			return ev("backup_finished", title)
		case brWarning:
			n := len(o.Backup.Errors)
			p["error_count"] = n
			var errs []string
			for _, e := range o.Backup.Errors {
				if len(errs) == 3 {
					break
				}
				s := e.Message
				if e.Item != "" && !strings.Contains(s, e.Item) {
					s = e.Item + ": " + s
				}
				if e.During != "" {
					s = e.During + " " + s
				}
				errs = append(errs, brMessage(t, s))
			}
			if len(errs) > 0 {
				p["errors"] = errs
			}
			title := "Backup of " + what + " finished with warnings"
			if n > 0 {
				title += fmt.Sprintf(" (%d unreadable)", n)
			}
			return ev("backup_warning", title)
		default:
			return ev("backup_failed", "Backup of "+what+" failed")
		}
	case o.Restore != nil:
		if ls := o.Restore.LastStatus; ls != nil {
			p["bytes_restored"] = int64(ls.BytesRestored)
			p["files_restored"] = int64(ls.FilesRestored)
		}
		if o.SnapshotID != "" {
			p["snapshot_id"] = o.SnapshotID
		}
		if o.Status == brError {
			return ev("restore_failed", "Restore from "+o.RepoID+" failed")
		}
		title := "Restore from " + o.RepoID + " finished"
		if b, _ := p["bytes_restored"].(int64); b > 0 {
			title += " (" + brSize(b) + ")"
		}
		return ev("restore_finished", title)
	case o.Hook != nil:
		if o.Status != brError {
			return nil
		}
		p["hook"] = o.Hook.Name
		if cond := strings.Trim(string(o.Hook.Condition), `"`); cond != "" {
			p["condition"] = cond
		}
		return ev("hook_failed", "Backrest hook failed: "+o.Hook.Name)
	}
	kind, key := brFailKey(o)
	if key == "" {
		return nil // index, stats, run command: not news
	}
	label := map[string]string{"check": "Repository check of " + o.RepoID, "prune": "Prune of " + o.RepoID, "forget": "Forget (retention) for " + what}[kind]
	if o.Status == brError {
		if _, ok := failing[key]; !ok {
			failing[key] = o.ended()
		}
		p["failing_since"] = time.UnixMilli(failing[key]).UTC().Format(time.RFC3339)
		return ev(kind+"_failed", label+" failed")
	}
	since, was := failing[key]
	if !was {
		return nil // routine success
	}
	delete(failing, key)
	p["failing_since"] = time.UnixMilli(since).UTC().Format(time.RFC3339)
	p["failing_for_seconds"] = (o.ended() - since) / 1000
	return ev(kind+"_recovered", label+" works again")
}
