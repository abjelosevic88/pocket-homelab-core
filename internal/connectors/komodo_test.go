package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKomodo mimics Komodo Core 2.3.3: API key headers, GET /user, and
// POST /read {"type","params"} with Mongo ids as {"$oid": …} on whole
// documents and plain "id" on update list items.
type fakeKomodo struct {
	mu      sync.Mutex
	me      string // user id the key belongs to
	users   []map[string]any
	updates []fkUpdate
	alerts  []fkAlert
	calls   map[string]int
	stacks  map[string]string
	servers map[string]string
}

type fkUpdate struct {
	ID, Op, Operator, Username, Status string
	TargetType, TargetID               string
	Start, End                         int64
	Success                            bool
	Commit                             string
	FailedStage                        string
}

type fkAlert struct {
	ID, Type, Level      string
	TargetType, TargetID string
	TS                   int64
	ResolvedTS           int64 // 0 = open
	Data                 map[string]any
}

const fkKey, fkSecret = "kkey-123", "ssecret-456"

func oid(id string) map[string]any { return map[string]any{"$oid": id} }

func (f *fakeKomodo) user(id, name string, admin, enabled bool, typ string) {
	f.users = append(f.users, map[string]any{"_id": oid(id), "username": name, "admin": admin, "super_admin": false, "enabled": enabled,
		"config": map[string]any{"type": typ, "data": map[string]any{}}})
}

func (f *fakeKomodo) find(id string) map[string]any {
	for _, u := range f.users {
		if u["_id"].(map[string]any)["$oid"] == id {
			return u
		}
	}
	return nil
}

func (f *fakeKomodo) updateJSON(u fkUpdate, full bool) map[string]any {
	m := map[string]any{"operation": u.Op, "start_ts": u.Start, "success": u.Success, "operator": u.Operator,
		"target": map[string]any{"type": u.TargetType, "id": u.TargetID}, "status": u.Status}
	if full {
		m["_id"] = oid(u.ID)
		var logs []map[string]any
		logs = append(logs, map[string]any{"stage": "prepare", "success": true, "stdout": "SECRET_ENV=xyz"})
		if u.FailedStage != "" {
			logs = append(logs, map[string]any{"stage": u.FailedStage, "success": false, "stderr": "boom"})
		}
		m["logs"] = logs
		if u.Status == "Complete" {
			m["end_ts"] = u.End
		} else {
			m["end_ts"] = nil
		}
		if u.Commit != "" {
			m["commit_hash"] = u.Commit
		}
		if u.Op == "RunBuild" {
			m["version"] = map[string]any{"major": 1, "minor": 4, "patch": 2}
		}
	} else {
		m["id"] = u.ID
		m["username"] = u.Username
		if u.Op == "RunBuild" {
			m["version"] = map[string]any{"major": 1, "minor": 4, "patch": 2}
		}
	}
	return m
}

func (f *fakeKomodo) alertJSON(a fkAlert) map[string]any {
	m := map[string]any{"_id": oid(a.ID), "ts": a.TS, "resolved": a.ResolvedTS != 0, "level": a.Level,
		"target": map[string]any{"type": a.TargetType, "id": a.TargetID}, "data": map[string]any{"type": a.Type, "data": a.Data}, "resolved_ts": nil}
	if a.ResolvedTS != 0 {
		m["resolved_ts"] = a.ResolvedTS
	}
	return m
}

func kerr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "trace": []string{}})
}

func (f *fakeKomodo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Api-Key") != fkKey || r.Header.Get("X-Api-Secret") != fkSecret {
		kerr(w, http.StatusUnauthorized, "Invalid client credentials")
		return
	}
	me := f.find(f.me)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		_ = json.NewEncoder(w).Encode(me)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/read":
	default:
		kerr(w, http.StatusNotFound, "not found")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		kerr(w, http.StatusUnsupportedMediaType, "Expected request with `Content-Type: application/json`")
		return
	}
	var req struct {
		Type   string          `json:"type"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Type == "" || !strings.HasPrefix(strings.TrimSpace(string(req.Params)), "{") {
		kerr(w, http.StatusUnprocessableEntity, "bad request shape")
		return
	}
	var p struct {
		Page         int            `json:"page"`
		ID           string         `json:"id"`
		Query        map[string]any `json:"query"`
		ServiceUsers string         `json:"service_users"`
		Limit        *int           `json:"limit"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[req.Type]++
	enc := json.NewEncoder(w)
	admin := me["admin"] == true
	switch req.Type {
	case "GetVersion":
		_ = enc.Encode(map[string]any{"version": "2.3.3"})
	case "ListUsers":
		if !admin {
			kerr(w, http.StatusInternalServerError, "this route is only accessable by admins")
			return
		}
		var out []map[string]any
		for _, u := range f.users {
			svc := u["config"].(map[string]any)["type"] == "Service"
			if (p.ServiceUsers == "Exclude" && svc) || (p.ServiceUsers == "Only" && !svc) {
				continue
			}
			out = append(out, u)
		}
		_ = enc.Encode(out)
	case "GetServersSummary":
		_ = enc.Encode(map[string]any{"total": 2, "healthy": 1, "warning": 0, "unhealthy": 1, "disabled": 0})
	case "GetStacksSummary":
		_ = enc.Encode(map[string]any{"total": 40, "running": 38, "stopped": 1, "down": 1, "unhealthy": 0, "unknown": 0})
	case "GetDeploymentsSummary":
		_ = enc.Encode(map[string]any{"total": 0, "running": 0, "stopped": 0, "not_deployed": 0, "unhealthy": 0, "unknown": 0})
	case "ListStacks", "ListServers":
		if p.Limit == nil || *p.Limit != 0 {
			kerr(w, http.StatusBadRequest, "want limit 0")
			return
		}
		src, typ := f.stacks, "Stack"
		if req.Type == "ListServers" {
			src, typ = f.servers, "Server"
		}
		out := []map[string]any{}
		for id, n := range src {
			out = append(out, map[string]any{"id": id, "type": typ, "name": n, "template": false, "tags": []string{}, "info": map[string]any{}})
		}
		_ = enc.Encode(out)
	case "ListUpdates":
		ups := append([]fkUpdate(nil), f.updates...)
		sort.SliceStable(ups, func(i, j int) bool { return ups[i].Start > ups[j].Start })
		lo, hi := min(p.Page*100, len(ups)), min(p.Page*100+100, len(ups))
		items := []map[string]any{}
		for _, u := range ups[lo:hi] {
			items = append(items, f.updateJSON(u, false))
		}
		var next any
		if len(items) == 100 {
			next = p.Page + 1
		}
		_ = enc.Encode(map[string]any{"updates": items, "next_page": next})
	case "GetUpdate":
		for _, u := range f.updates {
			if u.ID == p.ID {
				_ = enc.Encode(f.updateJSON(u, true))
				return
			}
		}
		kerr(w, http.StatusInternalServerError, "no update exists with given id")
	case "ListAlerts":
		al := []fkAlert{}
		for _, a := range f.alerts {
			if v, ok := p.Query["resolved"]; ok && v != (a.ResolvedTS != 0) {
				continue
			}
			al = append(al, a)
		}
		sort.SliceStable(al, func(i, j int) bool { return al[i].TS > al[j].TS })
		lo, hi := min(p.Page*100, len(al)), min(p.Page*100+100, len(al))
		items := []map[string]any{}
		for _, a := range al[lo:hi] {
			items = append(items, f.alertJSON(a))
		}
		var next any
		if len(items) == 100 {
			next = p.Page + 1
		}
		_ = enc.Encode(map[string]any{"alerts": items, "next_page": next})
	case "GetAlert":
		for _, a := range f.alerts {
			if a.ID == p.ID {
				_ = enc.Encode(f.alertJSON(a))
				return
			}
		}
		kerr(w, http.StatusInternalServerError, "no alert found with given id")
	default:
		kerr(w, http.StatusUnprocessableEntity, "unknown variant "+req.Type)
	}
}

const (
	kAlice = "64b000000000000000000a11"
	kBob   = "64b000000000000000000b0b"
	kBot   = "64b000000000000000000b07"
	kAdmin = "64b0000000000000000000ad"
)

func newFakeKomodo() *fakeKomodo {
	f := &fakeKomodo{me: kAdmin,
		stacks:  map[string]string{"st1": "immich", "st2": "jellyfin"},
		servers: map[string]string{"sv1": "nas"}}
	f.user(kAdmin, "admin", true, true, "Local")
	f.user(kAlice, "alice", false, true, "Oidc")
	f.user(kBob, "bob", false, false, "Local")
	f.user(kBot, "pocket-core", true, true, "Service")
	return f
}

func komodoTarget(srv *httptest.Server) Target {
	return Target{BaseURL: srv.URL, PublicURL: "https://komodo.example/", HTTP: srv.Client(),
		Fields: map[string]string{"API_KEY": fkKey, "API_SECRET": fkSecret}}
}

func TestKomodoTest(t *testing.T) {
	f := newFakeKomodo()
	srv := httptest.NewServer(f)
	defer srv.Close()
	tg := komodoTarget(srv)
	c := komodo{}
	info, err := c.Test(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "2.3.3" || !info.Admin || info.Users != 3 || len(info.Permissions) != 1 || info.Permissions[0] != "admin" {
		t.Fatalf("%+v", info)
	}
	for _, want := range []string{`user "admin"`, "3 users and 1 service users", "2 servers (1 healthy)", "40 stacks (38 running)", "0 deployments"} {
		if !strings.Contains(info.Detail, want) {
			t.Fatalf("detail %q lacks %q", info.Detail, want)
		}
	}

	// a service user's key, not an admin: works, says what it can't do
	f.me = kBot
	f.users[3]["admin"] = false
	info, err = c.Test(context.Background(), tg)
	if err != nil || info.Admin || info.Users != 0 || !strings.Contains(info.Detail, `service user "pocket-core"`) || !strings.Contains(info.Detail, "not an admin") {
		t.Fatalf("%+v %v", info, err)
	}

	// a disabled user's key
	f.me = kBob
	if _, err := c.Test(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled: %v", err)
	}

	// wrong secret: 401 explained, secret never echoed
	bad := tg
	bad.Fields = map[string]string{"API_KEY": fkKey, "API_SECRET": "wrong-secret-789"}
	_, err = c.Test(context.Background(), bad)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "wrong-secret-789") {
		t.Fatalf("401: %v", err)
	}
	none := tg
	none.Fields = map[string]string{"API_KEY": fkKey}
	if _, err := c.Test(context.Background(), none); err == nil || !strings.Contains(err.Error(), "API_SECRET") {
		t.Fatalf("missing: %v", err)
	}
}

func TestKomodoLinker(t *testing.T) {
	f := newFakeKomodo()
	srv := httptest.NewServer(f)
	defer srv.Close()
	tg := komodoTarget(srv)
	c := komodo{}
	var _ Linker = c
	accs, err := c.RemoteAccounts(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(accs)
	want := fmt.Sprint([]RemoteAccount{{ID: kAdmin, Name: "admin", Admin: true}, {ID: kAlice, Name: "alice"}, {ID: kBob, Name: "bob", Disabled: true}})
	if got != want {
		t.Fatalf("accounts\n got %s\nwant %s", got, want)
	}
	acc, err := c.Link(context.Background(), tg, kAlice)
	if err != nil || acc.RemoteID != kAlice || acc.Status != StatusLinked || acc.Fields["USER_ID"] != kAlice || !strings.Contains(acc.Note, "alice") {
		t.Fatalf("%+v %v", acc, err)
	}
	if _, err := c.Link(context.Background(), tg, "nope"); err == nil {
		t.Fatal("unknown id linked")
	}
	if _, err := c.Link(context.Background(), tg, kBot); err == nil || !strings.Contains(err.Error(), "service user") {
		t.Fatalf("service user: %v", err)
	}
	f.me = kAlice
	if _, err := c.RemoteAccounts(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "not an admin") {
		t.Fatalf("non-admin: %v", err)
	}
}

func TestKomodoSSO(t *testing.T) {
	c := komodo{}
	if r := c.SSORedirects("https://komodo.example/"); len(r) != 1 || r[0] != "https://komodo.example/auth/oidc/callback" {
		t.Fatal(r)
	}
	tg := Target{PublicURL: "https://komodo.example"}
	err := c.ConfigureSSO(context.Background(), tg, SSOConfig{Enabled: true, Issuer: "https://core.example", ClientID: "cid", ClientSecret: "csecret"})
	if err == nil || !strings.Contains(err.Error(), "KOMODO_OIDC_PROVIDER=https://core.example") || !strings.Contains(err.Error(), "KOMODO_HOST=https://komodo.example") ||
		!strings.Contains(err.Error(), "/auth/oidc/callback") || strings.Contains(err.Error(), "csecret") {
		t.Fatalf("%v", err)
	}
	if err := c.ConfigureSSO(context.Background(), tg, SSOConfig{}); err != nil {
		t.Fatal(err)
	}
	if c.Capabilities() != (Capabilities{Events: true, Link: true}) {
		t.Fatal(c.Capabilities())
	}
}

type kmPoller struct {
	t     *testing.T
	tg    Target
	state json.RawMessage
}

func (p *kmPoller) poll() []Event {
	p.t.Helper()
	evs, st, err := komodo{}.Poll(context.Background(), p.tg, p.state)
	if err != nil {
		p.t.Fatal(err)
	}
	p.state = st
	return evs
}

func kmEventNames(evs []Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Name)
	}
	return strings.Join(s, ",")
}

func TestKomodoPollUpdates(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).UnixMilli()
	f := newFakeKomodo()
	f.updates = []fkUpdate{
		{ID: "u0", Op: "DeployStack", Operator: kAlice, Username: "alice", Status: "Complete", TargetType: "Stack", TargetID: "st1", Start: base - 60000, End: base - 50000, Success: true},
		{ID: "u1", Op: "Deploy", Operator: kAlice, Username: "alice", Status: "InProgress", TargetType: "Deployment", TargetID: "dp1", Start: base - 30000},
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := &kmPoller{t: t, tg: komodoTarget(srv)}
	p.tg.Now = func() time.Time { return time.UnixMilli(base + 600000) }
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("baseline: %+v", evs)
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("nothing new: %+v", evs)
	}

	f.mu.Lock()
	f.updates = append(f.updates,
		fkUpdate{ID: "u2", Op: "DeployStack", Operator: kAlice, Username: "alice", Status: "Complete", TargetType: "Stack", TargetID: "st1", Start: base + 1000, End: base + 43000, Success: true, Commit: "abc1234"},
		fkUpdate{ID: "u3", Op: "RunProcedure", Operator: "000000000000000000000001", Username: "000000000000000000000001", Status: "Complete", TargetType: "Procedure", TargetID: "pr1", Start: base + 2000, End: base + 3000, Success: true},
		fkUpdate{ID: "u4", Op: "RunBuild", Operator: "000000000000000000000003", Username: "000000000000000000000003", Status: "Complete", TargetType: "Build", TargetID: "bd1", Start: base + 3000, End: base + 9000, Success: false, FailedStage: "docker build"},
		fkUpdate{ID: "u5", Op: "PullStack", Operator: kBob, Username: "bob", Status: "InProgress", TargetType: "Stack", TargetID: "st2", Start: base + 4000},
		fkUpdate{ID: "u6", Op: "DeleteStack", Operator: kAlice, Username: "alice", Status: "Complete", TargetType: "Stack", TargetID: "gone", Start: base + 5000, End: base + 5100, Success: true},
		fkUpdate{ID: "u7", Op: "PruneImages", Operator: kAlice, Username: "alice", Status: "Complete", TargetType: "Server", TargetID: "sv1", Start: base + 6000, End: base + 7000, Success: true},
	)
	f.updates[1].Status, f.updates[1].End, f.updates[1].Success = "Complete", base+20000, true // the baseline's running deploy finished
	f.mu.Unlock()

	evs := p.poll()
	if got := kmEventNames(evs); got != "deployment_deployed,stack_deployed,procedure_run,operation_failed,resource_deleted,prune_images" {
		t.Fatalf("events %s", got)
	}
	if f.calls["ListStacks"] != 1 || f.calls["ListServers"] != 1 {
		t.Fatalf("names are read once per poll and type: %v", f.calls)
	}
	by := map[string]Event{}
	for _, e := range evs {
		by[e.Subject] = e
	}
	// the deploy that was running at the baseline: alice's, attributed
	if e := by["update:u1"]; e.RemoteUserID != kAlice || e.Payload["duration_seconds"] != int64(50) || e.Payload["operator"] != "alice" {
		t.Fatalf("u1 %+v", e)
	}
	e := by["update:u2"]
	pl := e.Payload
	if e.RemoteUserID != kAlice || e.Personal || e.UserEmail != "" || pl["target"] != "immich" || pl["target_type"] != "Stack" || pl["operation"] != "DeployStack" ||
		pl["success"] != true || pl["duration_seconds"] != int64(42) || pl["commit"] != "abc1234" || pl["operator"] != "alice" || pl["operator_kind"] != "user" ||
		pl["title"] != "Stack immich deployed by alice" || pl["url"] != "https://komodo.example/updates/u2" || pl["target_url"] != "https://komodo.example/stacks/st1" {
		t.Fatalf("u2 %+v", e)
	}
	// a scheduled procedure: household, no person
	if e := by["update:u3"]; e.RemoteUserID != "" || e.Payload["operator"] != "Procedure" || e.Payload["operator_kind"] != "system" || e.Payload["title"] != "Procedure ran (Procedure)" {
		t.Fatalf("u3 %+v", e)
	}
	// a failed build from a git webhook
	if e := by["update:u4"]; e.RemoteUserID != "" || e.Payload["event"] != "build_finished" || e.Payload["failed_stage"] != "docker build" || e.Payload["success"] != false ||
		e.Payload["version"] != "1.4.2" || e.Payload["operator"] != "Git Webhook" || !strings.HasPrefix(e.Payload["title"].(string), "Run Build failed") {
		t.Fatalf("u4 %+v", e)
	}
	if e := by["update:u6"]; e.Payload["resource_type"] != "Stack" || e.Payload["title"] != "Stack deleted by alice" {
		t.Fatalf("u6 %+v", e)
	}
	if e := by["update:u7"]; e.Payload["target"] != "nas" || e.Payload["title"] != "Prune Images: nas by alice" {
		t.Fatalf("u7 %+v", e)
	}
	for _, e := range evs {
		b, _ := json.Marshal(e.Payload)
		if strings.Contains(string(b), "SECRET_ENV") || strings.Contains(string(b), "boom") {
			t.Fatalf("logs leaked: %s", b)
		}
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("re-poll: %s", kmEventNames(evs))
	}

	// bob's pull, running since the last poll, finishes
	f.mu.Lock()
	f.updates[5].Status, f.updates[5].End, f.updates[5].Success = "Complete", base+700000, true
	f.mu.Unlock()
	evs = p.poll()
	if len(evs) != 1 || evs[0].Subject != "update:u5" || evs[0].Name != "stack_pulled" || evs[0].RemoteUserID != kBob || evs[0].Payload["operator"] != "bob" || evs[0].Payload["target"] != "jellyfin" {
		t.Fatalf("u5 %+v", evs)
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("re-poll 2: %s", kmEventNames(evs))
	}
	var st komodoState
	_ = json.Unmarshal(p.state, &st)
	if len(st.Pending) != 0 {
		t.Fatalf("pending left %+v", st.Pending)
	}
}

// A pending update is found by GetUpdate once it left the newest pages.
func TestKomodoPendingOffPage(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).UnixMilli()
	f := newFakeKomodo()
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := &kmPoller{t: t, tg: komodoTarget(srv)}
	p.tg.Now = func() time.Time { return time.UnixMilli(base) }
	p.poll() // baseline of an empty Komodo
	f.mu.Lock()
	f.updates = append(f.updates, fkUpdate{ID: "long", Op: "RunBuild", Operator: kAlice, Username: "alice", Status: "InProgress", TargetType: "Build", TargetID: "bd1", Start: base + 1000})
	f.mu.Unlock()
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("running: %s", kmEventNames(evs))
	}
	f.mu.Lock()
	for i := 0; i < 120; i++ {
		f.updates = append(f.updates, fkUpdate{ID: fmt.Sprintf("n%03d", i), Op: "None", Status: "Complete", TargetType: "System", TargetID: "system", Start: base + 2000 + int64(i)})
	}
	f.updates[0].Status, f.updates[0].End, f.updates[0].Success = "Complete", base+90000, true
	f.mu.Unlock()
	evs := p.poll()
	if len(evs) != 1 || evs[0].Name != "build_finished" || evs[0].RemoteUserID != kAlice || evs[0].Payload["operator"] != "alice" || evs[0].Payload["title"] != "Build built (1.4.2) by alice" {
		t.Fatalf("%+v", evs)
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("again: %s", kmEventNames(evs))
	}
}

// More than the cap: the rest arrive next poll, nothing twice, nothing lost.
func TestKomodoUpdateCap(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).UnixMilli()
	f := newFakeKomodo()
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := &kmPoller{t: t, tg: komodoTarget(srv)}
	p.poll()
	f.mu.Lock()
	for i := 0; i < 130; i++ {
		f.updates = append(f.updates, fkUpdate{ID: fmt.Sprintf("b%03d", i), Op: "RestartStack", Operator: kAlice, Username: "alice", Status: "Complete", TargetType: "Stack", TargetID: "st1", Start: base + int64(i)*1000, End: base + int64(i)*1000 + 500, Success: true})
	}
	f.mu.Unlock()
	seen := map[string]int{}
	total := 0
	for i := 0; i < 4; i++ {
		evs := p.poll()
		if len(evs) > kmMaxEvents {
			t.Fatalf("poll %d: %d events", i, len(evs))
		}
		for _, e := range evs {
			seen[e.Subject]++
		}
		total += len(evs)
	}
	if total != 130 || len(seen) != 130 {
		t.Fatalf("total %d distinct %d", total, len(seen))
	}
}

func TestKomodoPollAlerts(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).UnixMilli()
	f := newFakeKomodo()
	f.alerts = []fkAlert{
		{ID: "a0", Type: "ServerUnreachable", Level: "CRITICAL", TargetType: "Server", TargetID: "sv1", TS: base - 3600000,
			Data: map[string]any{"id": "sv1", "name": "nas", "region": nil, "err": map[string]any{"error": "connection refused", "trace": []string{}}}},
		{ID: "a1", Type: "StackStateChange", Level: "WARNING", TargetType: "Stack", TargetID: "st1", TS: base - 1000, ResolvedTS: base - 1000,
			Data: map[string]any{"id": "st1", "name": "immich", "from": "running", "to": "down"}},
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := &kmPoller{t: t, tg: komodoTarget(srv)}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("baseline: %+v", evs)
	}

	f.mu.Lock()
	f.alerts[0].ResolvedTS = base + 1000 // the baseline's outage ends
	f.alerts = append(f.alerts,
		fkAlert{ID: "a2", Type: "ServerDisk", Level: "WARNING", TargetType: "Server", TargetID: "sv1", TS: base + 2000,
			Data: map[string]any{"id": "sv1", "name": "nas", "region": nil, "path": "/mnt/storage", "used_gb": 900.0, "total_gb": 1000.0}},
		fkAlert{ID: "a3", Type: "ContainerStateChange", Level: "WARNING", TargetType: "Deployment", TargetID: "dp1", TS: base + 3000, ResolvedTS: base + 3000,
			Data: map[string]any{"id": "dp1", "name": "web", "server_id": "sv1", "server_name": "nas", "from": "running", "to": "exited"}},
		fkAlert{ID: "a4", Type: "ServerCpu", Level: "WARNING", TargetType: "Server", TargetID: "sv1", TS: base + 4000, ResolvedTS: base + 64000,
			Data: map[string]any{"id": "sv1", "name": "nas", "region": nil, "percentage": 97.3}},
	)
	f.mu.Unlock()
	evs := p.poll()
	if got := kmEventNames(evs); got != "alert_opened,alert_opened,alert_opened,alert_resolved,alert_resolved" {
		t.Fatalf("events %s", got)
	}
	for _, e := range evs {
		if e.RemoteUserID != "" || e.UserEmail != "" || e.Personal {
			t.Fatalf("household %+v", e)
		}
	}
	if e := evs[0]; e.Subject != "alert:a2" || e.Payload["title"] != "nas: disk /mnt/storage at 90.0%" || e.Payload["level"] != "warning" || e.Payload["percentage"] != 90.0 ||
		e.Payload["url"] != "https://komodo.example/alerts/a2" || e.Payload["target_url"] != "https://komodo.example/servers/sv1" || e.Payload["resolved"] != false {
		t.Fatalf("disk %+v", e)
	}
	if e := evs[1]; e.Subject != "alert:a3" || e.Payload["one_shot"] != true || e.Payload["title"] != "Deployment web is now exited on nas" || e.Payload["to"] != "exited" || e.Payload["server"] != "nas" {
		t.Fatalf("container %+v", e)
	}
	if e := evs[2]; e.Subject != "alert:a4" || e.Payload["title"] != "nas: CPU at 97.3%" {
		t.Fatalf("cpu opened %+v", e)
	}
	if e := evs[3]; e.Subject != "alert:a4" || e.Payload["open_seconds"] != int64(60) || e.Payload["title"] != "nas: CPU usage back to normal" {
		t.Fatalf("cpu resolved %+v", e)
	}
	if e := evs[4]; e.Subject != "alert:a0" || e.Payload["title"] != "nas is reachable again" || e.Payload["open_seconds"] != int64(3601) || e.Payload["alert_type"] != "ServerUnreachable" {
		t.Fatalf("unreachable resolved %+v", e)
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("re-poll: %s", kmEventNames(evs))
	}

	f.mu.Lock()
	f.alerts[2].ResolvedTS = base + 900000
	f.mu.Unlock()
	evs = p.poll()
	if len(evs) != 1 || evs[0].Name != "alert_resolved" || evs[0].Subject != "alert:a2" || evs[0].Payload["title"] != "nas: disk /mnt/storage back to normal" {
		t.Fatalf("disk resolved %+v", evs)
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("re-poll 2: %s", kmEventNames(evs))
	}
}

// Errors keep the old state; a poll before a baseline needs credentials.
func TestKomodoPollErrors(t *testing.T) {
	f := newFakeKomodo()
	srv := httptest.NewServer(f)
	defer srv.Close()
	tg := komodoTarget(srv)
	tg.Fields["API_SECRET"] = "nope"
	old := json.RawMessage(`{"init":true,"upd_ts":5}`)
	evs, st, err := komodo{}.Poll(context.Background(), tg, old)
	if err == nil || len(evs) != 0 || string(st) != string(old) || strings.Contains(err.Error(), "nope") {
		t.Fatalf("%v %s %v", evs, st, err)
	}
}
