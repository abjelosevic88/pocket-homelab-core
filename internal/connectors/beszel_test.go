package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBeszel mimics Beszel 0.21 on PocketBase 0.40: password sign-in for
// users and _superusers, the raw token in Authorization, list rules
// (systems shared with the user, the user's own alerts and history; a
// superuser sees everything) and PocketBase's list answers.
type fakeBeszel struct {
	mu      sync.Mutex
	users   map[string][3]string // email → id, password, role
	supers  map[string]string    // email → password
	tokens  map[string]string    // token → user id ("*" = superuser)
	mfa     bool
	systems []map[string]any // + "users": []string
	alerts  []map[string]any // id, user
	history []map[string]any // id, user, system, name, monitor_name, value, created, resolved
}

func newFakeBeszel() *fakeBeszel {
	return &fakeBeszel{
		users:  map[string][3]string{"owner@home.lan": {"u1owner00000001", "s3cret-pw", "admin"}, "kid@home.lan": {"u2kid0000000002", "kidpw", "user"}},
		supers: map[string]string{"owner@home.lan": "s3cret-pw", "root@home.lan": "rootpw"},
		tokens: map[string]string{},
	}
}

func pbErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}, "message": msg, "status": code})
}

var (
	fbCreatedRe = regexp.MustCompile(`^created >= "([^"]+)"$`)
	fbIDRe      = regexp.MustCompile(`id = "([^"]+)"`)
)

func (f *fakeBeszel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/auth-with-password") && r.Method == http.MethodPost {
		var body struct{ Identity, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		var id, role string
		switch r.URL.Path {
		case "/api/collections/users/auth-with-password":
			u, ok := f.users[body.Identity]
			if !ok || u[1] != body.Password {
				pbErr(w, 400, "Failed to authenticate.")
				return
			}
			id, role = u[0], u[2]
		case "/api/collections/_superusers/auth-with-password":
			if pw, ok := f.supers[body.Identity]; !ok || pw != body.Password {
				pbErr(w, 400, "Failed to authenticate.")
				return
			}
			id = "*"
		default:
			pbErr(w, 404, "Missing collection context.")
			return
		}
		if f.mfa {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"mfaId":"mfa123"}`))
			return
		}
		tok := fmt.Sprintf("tok%d", len(f.tokens)+1)
		f.tokens[tok] = id
		rec := map[string]any{"id": id, "email": body.Identity, "collectionName": "users"}
		if role != "" {
			rec["role"] = role
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "record": rec})
		return
	}
	if r.Method != http.MethodGet {
		pbErr(w, 405, "nope")
		return
	}
	who, authed := f.tokens[r.Header.Get("Authorization")] // raw token, no "Bearer"
	if r.URL.Path == "/api/beszel/info" {
		if !authed {
			pbErr(w, 401, "The request requires valid record authorization token.")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"key": "ssh-ed25519 AAAAfake", "v": "0.21.0", "cu": false})
		return
	}
	coll, ok := strings.CutPrefix(r.URL.Path, "/api/collections/")
	coll, ok2 := strings.CutSuffix(coll, "/records")
	if !ok || !ok2 {
		pbErr(w, 404, "not found")
		return
	}
	q := r.URL.Query()
	var src []map[string]any
	switch coll {
	case "systems":
		src = f.systems
	case "alerts":
		src = f.alerts
	case "alerts_history":
		src = f.history
	default:
		pbErr(w, 403, "Only superusers can perform this action.")
		return
	}
	visible := func(rec map[string]any) bool {
		if !authed {
			return false // list rules need @request.auth.id
		}
		if who == "*" {
			return true
		}
		if coll == "systems" {
			for _, u := range rec["users"].([]string) {
				if u == who {
					return true
				}
			}
			return false
		}
		return rec["user"] == who
	}
	filter := q.Get("filter")
	match := func(rec map[string]any) bool {
		switch {
		case filter == "":
			return true
		case filter == "resolved = null":
			return rec["resolved"] == ""
		case fbCreatedRe.MatchString(filter):
			return rec["created"].(string) >= fbCreatedRe.FindStringSubmatch(filter)[1]
		case strings.HasPrefix(filter, "(id = "):
			for _, m := range fbIDRe.FindAllStringSubmatch(filter, -1) {
				if rec["id"] == m[1] {
					return true
				}
			}
			return false
		}
		panic("fake Beszel: unexpected filter " + filter)
	}
	var items []map[string]any
	for _, rec := range src {
		if visible(rec) && match(rec) {
			cp := map[string]any{}
			for k, v := range rec {
				if k != "users" && k != "user" {
					cp[k] = v
				}
			}
			if coll == "alerts_history" && q.Get("expand") == "system" {
				for _, s := range f.systems {
					if s["id"] == rec["system"] && visible(s) {
						cp["expand"] = map[string]any{"system": map[string]any{"name": s["name"]}}
					}
				}
			}
			items = append(items, cp)
		}
	}
	if s := q.Get("sort"); s != "" {
		keys := strings.Split(s, ",")
		sort.SliceStable(items, func(i, j int) bool {
			for _, k := range keys {
				desc := strings.HasPrefix(k, "-")
				k = strings.TrimPrefix(k, "-")
				a, b := fmt.Sprint(items[i][k]), fmt.Sprint(items[j][k])
				if a != b {
					return (a < b) != desc
				}
			}
			return false
		})
	}
	page, _ := strconv.Atoi(q.Get("page"))
	per, _ := strconv.Atoi(q.Get("perPage"))
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = 30
	}
	total := len(items)
	lo, hi := min((page-1)*per, total), min(page*per, total)
	pages := (total + per - 1) / per
	_ = json.NewEncoder(w).Encode(map[string]any{"page": page, "perPage": per, "totalItems": total, "totalPages": pages, "items": append([]map[string]any{}, items[lo:hi]...)})
}

func (f *fakeBeszel) sys(id string) map[string]any {
	for _, s := range f.systems {
		if s["id"] == id {
			return s
		}
	}
	panic(id)
}

func (f *fakeBeszel) hist(id string) map[string]any {
	for _, s := range f.history {
		if s["id"] == id {
			return s
		}
	}
	panic(id)
}

func pbTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000Z") }

func fbSystem(id, name, status string, updated time.Time, info map[string]any, users ...string) map[string]any {
	return map[string]any{"id": id, "name": name, "status": status, "host": name + ".lan", "port": "45876", "updated": pbTime(updated), "created": pbTime(updated), "info": info, "users": users}
}

func fbHist(id, user, system, name string, value float64, created time.Time, resolved string) map[string]any {
	return map[string]any{"id": id, "user": user, "system": system, "alert_id": "al" + id, "name": name, "monitor_name": "", "value": value, "created": pbTime(created), "resolved": resolved}
}

type beszelHarness struct {
	t     *testing.T
	f     *fakeBeszel
	tg    Target
	now   time.Time
	state json.RawMessage
}

func newBeszelHarness(t *testing.T, email, pw string) *beszelHarness {
	h := &beszelHarness{t: t, f: newFakeBeszel(), now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(h.f)
	t.Cleanup(srv.Close)
	h.tg = Target{BaseURL: srv.URL, PublicURL: "https://beszel.example/", Fields: map[string]string{"EMAIL": email, "PASSWORD": pw, "GLANCES": "home-server"}, HTTP: srv.Client(), Now: func() time.Time { return h.now }}
	return h
}

func (h *beszelHarness) poll() []Event {
	h.t.Helper()
	evs, st, err := beszel{}.Poll(context.Background(), h.tg, h.state)
	if err != nil {
		h.t.Fatal(err)
	}
	h.state = st
	return evs
}

func evNames(evs []Event) string {
	var n []string
	for _, e := range evs {
		n = append(n, e.Name+"@"+e.Subject)
	}
	return strings.Join(n, " ")
}

func findEv(t *testing.T, evs []Event, name, subject string) Event {
	t.Helper()
	for _, e := range evs {
		if e.Name == name && e.Subject == subject {
			return e
		}
	}
	t.Fatalf("no %s %s in %s", name, subject, evNames(evs))
	return Event{}
}

func TestBeszelTest(t *testing.T) {
	h := newBeszelHarness(t, "owner@home.lan", "s3cret-pw")
	u := "u1owner00000001"
	h.f.systems = []map[string]any{
		fbSystem("sysa00000000001", "home-server", "up", h.now, map[string]any{"v": "0.21.0"}, u),
		fbSystem("sysb00000000002", "nas", "down", h.now, map[string]any{}, u),
		fbSystem("sysc00000000003", "pi", "paused", h.now, nil, u),
		fbSystem("sysd00000000004", "kids-pc", "down", h.now, nil, "u2kid0000000002"), // not shared with the owner
	}
	h.f.alerts = []map[string]any{{"id": "a1", "user": u}, {"id": "a2", "user": u}, {"id": "a3", "user": "u2kid0000000002"}}
	info, err := beszel{}.Test(context.Background(), h.tg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "0.21.0" || !info.Admin || !strings.Contains(info.Detail, "3 systems, 1 down now, 1 paused") || !strings.Contains(info.Detail, "2 alerts set") || !strings.Contains(info.Detail, "role admin") {
		t.Fatalf("%+v", info)
	}
	// a plain user, nothing shared
	h.tg.Fields = map[string]string{"EMAIL": "kid@home.lan", "PASSWORD": "kidpw"}
	h.f.systems = h.f.systems[:3]
	info, err = beszel{}.Test(context.Background(), h.tg)
	if err != nil || info.Admin || !strings.Contains(info.Detail, "0 systems") || !strings.Contains(info.Detail, "share them with it") {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestBeszelSignInErrors(t *testing.T) {
	h := newBeszelHarness(t, "owner@home.lan", "wrong-pw-xyz")
	_, err := beszel{}.Test(context.Background(), h.tg)
	if err == nil || !strings.Contains(err.Error(), "rejected EMAIL and PASSWORD") || !strings.Contains(err.Error(), "https://beszel.example") || strings.Contains(err.Error(), "wrong-pw-xyz") {
		t.Fatalf("bad credentials: %v", err)
	}
	if _, _, err := (beszel{}).Poll(context.Background(), h.tg, nil); err == nil {
		t.Fatal("poll with bad credentials")
	}
	h.tg.Fields = map[string]string{"EMAIL": "owner@home.lan"}
	if _, err := (beszel{}).Test(context.Background(), h.tg); err == nil || !strings.Contains(err.Error(), "needs EMAIL and PASSWORD") {
		t.Fatalf("missing: %v", err)
	}
	h.tg.Fields = map[string]string{"EMAIL": "owner@home.lan", "PASSWORD": "s3cret-pw"}
	h.f.mfa = true
	if _, err := (beszel{}).Test(context.Background(), h.tg); err == nil || !strings.Contains(err.Error(), "one-time code") {
		t.Fatalf("mfa: %v", err)
	}
	// provisioning and sign-in are not Core's here
	if _, err := (beszel{}).Provision(context.Background(), h.tg, Person{Email: "x@y"}); err == nil {
		t.Fatal("provision")
	}
	if err := (beszel{}).ConfigureSSO(context.Background(), h.tg, SSOConfig{Enabled: true}); err == nil {
		t.Fatal("sso on")
	}
	if err := (beszel{}).ConfigureSSO(context.Background(), h.tg, SSOConfig{}); err != nil {
		t.Fatal(err)
	}
}

func TestBeszelPoll(t *testing.T) {
	old := beszelPerPage
	beszelPerPage = 2 // every list spans pages
	defer func() { beszelPerPage = old }()

	h := newBeszelHarness(t, "owner@home.lan", "s3cret-pw")
	f, u, kid := h.f, "u1owner00000001", "u2kid0000000002"
	t0 := h.now
	f.systems = []map[string]any{
		fbSystem("sysa00000000001", "home-server", "up", t0, map[string]any{"v": "0.20.0", "cpu": 12.5, "mp": 40.0}, u),
		fbSystem("sysb00000000002", "nas", "down", t0.Add(-30*time.Minute), map[string]any{"v": "0.20.0"}, u),
		fbSystem("sysc00000000003", "pi", "pending", t0, map[string]any{}, u),
		fbSystem("sysd00000000004", "laptop", "up", t0, map[string]any{"v": "0.20.0"}, u),
		fbSystem("syse00000000005", "kids-pc", "up", t0, map[string]any{"v": "0.20.0"}, kid),
	}
	f.history = []map[string]any{
		fbHist("hold00000000001", u, "sysa00000000001", "CPU", 80, t0.Add(-2*time.Hour), pbTime(t0.Add(-time.Hour))),
		fbHist("hopen0000000002", u, "sysa00000000001", "CPU", 80, t0.Add(-10*time.Minute), ""),
		fbHist("hkid00000000003", kid, "syse00000000005", "Memory", 50, t0.Add(-5*time.Minute), ""),
	}
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("baseline %s", evNames(evs))
	}
	h.now = t0.Add(5 * time.Minute)
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("nothing changed: %s", evNames(evs))
	}

	// home-server goes down, laptop is paused, the nas comes back with a new
	// agent, the open CPU alert ends, a memory alert fires on the nas, a disk
	// alert fires and ends between two polls, the kid's alert (not ours) fires
	t1 := t0.Add(10 * time.Minute)
	h.now = t1
	f.sys("sysa00000000001")["status"] = "down"
	f.sys("sysa00000000001")["updated"] = pbTime(t1.Add(-2 * time.Minute))
	f.sys("sysd00000000004")["status"] = "paused"
	f.sys("sysd00000000004")["info"] = map[string]any{}
	f.sys("sysb00000000002")["status"] = "up"
	f.sys("sysb00000000002")["info"] = map[string]any{"v": "0.21.0", "mp": 91.234, "dp": 50.0, "efs": map[string]any{"tank": 97.0}}
	f.hist("hopen0000000002")["resolved"] = pbTime(t1.Add(-time.Minute))
	f.history = append(f.history,
		fbHist("hmem00000000004", u, "sysb00000000002", "Memory", 90, t1.Add(-3*time.Minute), ""),
		fbHist("hdisk0000000005", u, "sysb00000000002", "Disk", 95, t1.Add(-4*time.Minute), pbTime(t1.Add(-time.Minute))),
		fbHist("hkid00000000006", kid, "syse00000000005", "CPU", 50, t1.Add(-3*time.Minute), ""),
	)
	evs := h.poll()
	if len(evs) != 7 {
		t.Fatalf("got %s", evNames(evs))
	}
	down := findEv(t, evs, "system_down", "system:sysa00000000001").Payload
	if down["title"] != "home-server is down" || down["down_since"] != t1.Add(-2*time.Minute).Format(time.RFC3339) || down["url"] != "https://beszel.example/system/sysa00000000001" || down["previous_status"] != "up" || down["host"] != "home-server.lan" {
		t.Fatalf("down %+v", down)
	}
	up := findEv(t, evs, "system_up", "system:sysb00000000002").Payload
	if up["title"] != "nas is back up" || up["down_for_seconds"] != int64(40*60) || up["down_since"] != t0.Add(-30*time.Minute).Format(time.RFC3339) {
		t.Fatalf("up %+v", up)
	}
	ag := findEv(t, evs, "agent_updated", "system:sysb00000000002").Payload
	if ag["from_version"] != "0.20.0" || ag["to_version"] != "0.21.0" || ag["title"] != "nas: Beszel agent 0.20.0 → 0.21.0" {
		t.Fatalf("agent %+v", ag)
	}
	res := findEv(t, evs, "alert_resolved", "alert:sysa00000000001/CPU").Payload
	if res["title"] != "home-server: CPU usage resolved" || res["duration_seconds"] != int64(19*60) || res["threshold"] != 80.0 || res["alert_label"] != "CPU usage" {
		t.Fatalf("resolved %+v", res)
	}
	mem := findEv(t, evs, "alert_triggered", "alert:sysb00000000002/Memory").Payload
	if mem["title"] != "nas: Memory usage above 90%" || mem["threshold"] != 90.0 || mem["unit"] != "%" || mem["current_value"] != 91.23 || mem["system"] != "nas" || mem["url"] != "https://beszel.example/system/sysb00000000002" || mem["triggered_at"] != t1.Add(-3*time.Minute).Format(time.RFC3339) {
		t.Fatalf("memory %+v", mem)
	}
	disk := findEv(t, evs, "alert_triggered", "alert:sysb00000000002/Disk").Payload
	if disk["current_value"] != 97.0 || disk["title"] != "nas: Disk usage above 95%" {
		t.Fatalf("disk %+v", disk)
	}
	findEv(t, evs, "alert_resolved", "alert:sysb00000000002/Disk")
	for _, e := range evs {
		if e.UserEmail != "" || e.RemoteUserID != "" || e.Personal || e.Payload["title"] == nil {
			t.Fatalf("household events only: %+v", e)
		}
		if strings.Contains(e.Subject, "syse") || strings.Contains(e.Subject, "sysd") {
			t.Fatalf("paused laptop or the kid's system: %+v", e)
		}
	}

	// no repeats
	h.now = t1.Add(5 * time.Minute)
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("repeat: %s", evNames(evs))
	}

	// home-server reconnects through pending (still one outage), then is up;
	// the paused laptop resuming is not news
	f.sys("sysa00000000001")["status"] = "pending"
	f.sys("sysd00000000004")["status"] = "pending"
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("pending: %s", evNames(evs))
	}
	f.sys("sysa00000000001")["status"] = "down"
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("down again within the same outage: %s", evNames(evs))
	}
	h.now = t1.Add(20 * time.Minute)
	f.sys("sysa00000000001")["status"] = "up"
	f.sys("sysd00000000004")["status"] = "up"
	f.sys("sysd00000000004")["info"] = map[string]any{"v": "0.20.0"}
	evs = h.poll()
	if len(evs) != 1 || evs[0].Name != "system_up" || evs[0].Payload["down_for_seconds"] != int64(22*60) {
		t.Fatalf("back up: %s %+v", evNames(evs), evs)
	}

	// the memory alert ends; the state stays small
	f.hist("hmem00000000004")["resolved"] = pbTime(h.now)
	evs = h.poll()
	if len(evs) != 1 || evs[0].Name != "alert_resolved" || evs[0].Payload["duration_seconds"] != int64(23*60) {
		t.Fatalf("memory resolved: %s", evNames(evs))
	}
	var st beszelState
	if err := json.Unmarshal(h.state, &st); err != nil || len(st.Open) != 0 || len(st.Systems) != 4 {
		t.Fatalf("state %s", h.state)
	}
	// a system removed from Beszel is forgotten
	f.systems = f.systems[1:]
	h.poll()
	st = beszelState{}
	_ = json.Unmarshal(h.state, &st)
	if _, ok := st.Systems["sysa00000000001"]; ok {
		t.Fatal("removed system kept")
	}
}

// A superuser login works when EMAIL is not a Beszel user; it sees every
// user's history, so one incident recorded for two users is one event.
func TestBeszelSuperuserMergesUsers(t *testing.T) {
	h := newBeszelHarness(t, "root@home.lan", "rootpw")
	f, u, kid := h.f, "u1owner00000001", "u2kid0000000002"
	t0 := h.now
	f.systems = []map[string]any{fbSystem("sysa00000000001", "home-server", "up", t0, map[string]any{"v": "0.21.0", "la": []float64{3.5, 2, 1}}, u)}
	info, err := beszel{}.Test(context.Background(), h.tg)
	if err != nil || !info.Admin || !strings.Contains(info.Detail, "superuser") || !strings.Contains(info.Detail, "1 systems") {
		t.Fatalf("%+v %v", info, err)
	}
	if evs := h.poll(); len(evs) != 0 {
		t.Fatal("baseline")
	}
	t1 := t0.Add(5 * time.Minute)
	h.now = t1
	f.history = append(f.history,
		fbHist("hu0000000000001", u, "sysa00000000001", "LoadAvg1", 3, t1.Add(-time.Minute), ""),
		fbHist("hk0000000000001", kid, "sysa00000000001", "LoadAvg1", 3, t1.Add(-time.Minute), ""),
	)
	nm := fbHist("hn0000000000001", u, "sysa00000000001", "NetworkMonitorLoss", 12.5, t1.Add(-time.Minute), "")
	nm["monitor_name"] = "1.1.1.1"
	f.history = append(f.history, nm)
	evs := h.poll()
	if len(evs) != 2 {
		t.Fatalf("merged: %s", evNames(evs))
	}
	la := findEv(t, evs, "alert_triggered", "alert:sysa00000000001/LoadAvg1").Payload
	if la["title"] != "home-server: 1m load average above 3" || la["current_value"] != 3.5 || la["unit"] != "" {
		t.Fatalf("load %+v", la)
	}
	loss := findEv(t, evs, "alert_triggered", "alert:sysa00000000001/NetworkMonitorLoss/1.1.1.1").Payload
	if loss["value"] != 12.5 || loss["threshold"] != nil || loss["monitor"] != "1.1.1.1" || loss["title"] != "home-server: Network monitor loss (1.1.1.1) at 12.5%" {
		t.Fatalf("loss %+v", loss)
	}
	h.now = t1.Add(5 * time.Minute)
	f.hist("hu0000000000001")["resolved"] = pbTime(h.now)
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("the kid's copy is still open: %s", evNames(evs))
	}
	f.hist("hk0000000000001")["resolved"] = pbTime(h.now)
	evs = h.poll()
	if len(evs) != 1 || evs[0].Name != "alert_resolved" || evs[0].Subject != "alert:sysa00000000001/LoadAvg1" {
		t.Fatalf("resolved once: %s", evNames(evs))
	}
}

// More new history than one poll handles: the rest comes next time, with
// records sharing a timestamp neither lost nor repeated.
func TestBeszelHistoryCap(t *testing.T) {
	oldMax, oldPer := beszelMaxNew, beszelPerPage
	beszelMaxNew, beszelPerPage = 3, 2
	defer func() { beszelMaxNew, beszelPerPage = oldMax, oldPer }()
	h := newBeszelHarness(t, "owner@home.lan", "s3cret-pw")
	u := "u1owner00000001"
	h.f.systems = []map[string]any{fbSystem("sysa00000000001", "home-server", "up", h.now, nil, u)}
	same := h.now.Add(-time.Hour)
	h.f.history = []map[string]any{fbHist("hbase0000000000", u, "sysa00000000001", "Status", 0, same, pbTime(same))}
	h.poll()
	names := []string{"CPU", "Memory", "Disk", "Temperature", "Bandwidth", "GPU", "Battery"}
	for i, n := range names {
		at := same // the first three share the baseline's timestamp
		if i >= 3 {
			at = h.now.Add(time.Duration(i) * time.Minute)
		}
		h.f.history = append(h.f.history, fbHist(fmt.Sprintf("hnew%011d", i), u, "sysa00000000001", n, 50, at, ""))
	}
	seen := map[string]int{}
	var counts []int
	for range 4 {
		evs := h.poll()
		counts = append(counts, len(evs))
		for _, e := range evs {
			seen[e.Subject]++
		}
	}
	if fmt.Sprint(counts) != "[3 3 1 0]" || len(seen) != 7 {
		t.Fatalf("counts %v seen %v", counts, seen)
	}
	for s, n := range seen {
		if n != 1 {
			t.Fatalf("%s announced %d times", s, n)
		}
	}
}
