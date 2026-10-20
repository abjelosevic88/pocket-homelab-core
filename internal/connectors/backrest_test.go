package connectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackrest mimics Backrest 1.14.1: Connect RPC unary calls as
// POST /v1.Backrest/<Method> with JSON, protojson answers (int64 quoted,
// enums by name, zero values left out), the auth middleware (Basic auth
// checked per request; 401 text/plain otherwise; nothing checked when auth
// is disabled) and the public web UI carrying the build version.
type fakeBackrest struct {
	mu        sync.Mutex
	authOff   bool
	user      string
	pass      string
	modno     int64
	ops       map[int64]*fakeBrOp
	plans     []map[string]any
	forbidden []string
	lastReq   map[string]any
}

type fakeBrOp struct {
	ID, Modno, Start, End int64
	Plan, Repo, Snap      string
	Status, Msg, Kind     string // Kind: backup restore hook check prune forget index
	DryRun                bool
	Added, FilesNew       int64
	Errors                []map[string]string
	Hook, Cond            string
	Instance              string
}

func newFakeBackrest() *fakeBackrest {
	return &fakeBackrest{user: "admin", pass: "s3cret-pw", ops: map[int64]*fakeBrOp{}, plans: []map[string]any{
		{"id": "nightly", "repo": "nas", "paths": []string{"/home"}, "schedule": map[string]any{"cron": "0 3 * * *", "clock": "CLOCK_LOCAL"}},
		{"id": "manual", "repo": "nas", "schedule": map[string]any{"disabled": true}},
	}}
}

// put stores an operation; like the oplog, every write takes a new modno.
func (f *fakeBackrest) put(o fakeBrOp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modno++
	o.Modno = f.modno
	if o.Instance == "" {
		o.Instance = "box"
	}
	f.ops[o.ID] = &o
}

func (o *fakeBrOp) json() map[string]any {
	s := func(n int64) string { return strconv.FormatInt(n, 10) }
	m := map[string]any{"id": s(o.ID), "modno": s(o.Modno), "flowId": s(o.ID), "repoId": o.Repo, "repoGuid": "guid-" + o.Repo,
		"planId": o.Plan, "instanceId": o.Instance, "status": o.Status, "unixTimeStartMs": s(o.Start)}
	if o.End > 0 {
		m["unixTimeEndMs"] = s(o.End)
	}
	if o.Snap != "" {
		m["snapshotId"] = o.Snap
	}
	if o.Msg != "" {
		m["displayMessage"] = o.Msg
	}
	switch o.Kind {
	case "backup":
		b := map[string]any{}
		if o.Status == "STATUS_INPROGRESS" {
			b["lastStatus"] = map[string]any{"status": map[string]any{"percentDone": 0.4, "totalFiles": "10"}}
		} else if o.Status != "STATUS_ERROR" {
			b["lastStatus"] = map[string]any{"summary": map[string]any{"filesNew": s(o.FilesNew), "filesChanged": "2", "dataAdded": s(o.Added),
				"totalFilesProcessed": "1000", "totalBytesProcessed": "5000000", "totalDuration": 61.5, "snapshotId": o.Snap}}
		}
		if len(o.Errors) > 0 {
			b["errors"] = o.Errors
		}
		if o.DryRun {
			b["dryRun"] = true
		}
		m["operationBackup"] = b
	case "restore":
		m["operationRestore"] = map[string]any{"path": "/home/x", "target": "/tmp/restore", "lastStatus": map[string]any{"messageType": "summary", "bytesRestored": "2048", "filesRestored": "3"}}
	case "hook":
		m["operationRunHook"] = map[string]any{"name": o.Hook, "condition": o.Cond, "parentOp": "1"}
	case "check":
		m["operationCheck"] = map[string]any{"outputLogref": "x"}
	case "prune":
		m["operationPrune"] = map[string]any{}
	case "forget":
		m["operationForget"] = map[string]any{"policy": map[string]any{}}
	case "index":
		m["operationIndexSnapshot"] = map[string]any{"snapshot": map[string]any{"id": o.Snap}}
	}
	return m
}

func (f *fakeBackrest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		_, _ = io.WriteString(w, `<!doctype html><html><head><script type="module" crossorigin src="./assets/index-AAA.js"></script><link rel="modulepreload" crossorigin href="./assets/runtime-BBB.js"></head></html>`)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/assets/index-AAA.js":
		_, _ = io.WriteString(w, `import{a as b}from"./runtime-BBB.js";b();`)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/assets/runtime-BBB.js":
		_, _ = io.WriteString(w, "var Ao=\"unix\".trim().toLowerCase(),Fu=Ao===\"windows\",Bu=\"1.14.1\".trim(),gg=Fu?\"\\\\\":\"/\";console.log(`UI OS: ${Ao}, Build Version: ${Bu}, Backend URL: ./`);")
		return
	}
	// auth middleware
	if !f.authOff {
		u, p, ok := r.BasicAuth()
		if !ok || u != f.user || p != f.pass {
			http.Error(w, "Unauthorized (No Authorization Header)", http.StatusUnauthorized)
			return
		}
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.lastReq = req
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1.Backrest/GetConfig":
		auth := map[string]any{"users": []any{map[string]any{"name": f.user, "passwordBcrypt": "********"}}}
		if f.authOff {
			auth = map[string]any{"disabled": true}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"modno": 7, "version": 4, "instance": "box",
			"repos": []any{map[string]any{"id": "nas", "uri": "rest:https://u:repo-pw@nas/r", "guid": "guid-nas", "password": "repo-secret", "env": []string{"B2_KEY=cloud-secret"}},
				map[string]any{"id": "cloud", "uri": "b2:bucket", "guid": "guid-cloud", "password": "repo-secret"}},
			"plans": f.plans, "auth": auth})
	case "/v1.Backrest/GetSummaryDashboard":
		var plans []any
		for _, p := range f.plans {
			id := p["id"].(string)
			var ts, du []string
			var st []string
			list := f.sorted(func(o *fakeBrOp) bool { return o.Plan == id && o.Kind == "backup" && o.Status != "STATUS_PENDING" })
			for i := len(list) - 1; i >= 0; i-- { // newest first
				o := list[i]
				ts = append(ts, strconv.FormatInt(o.Start, 10))
				du = append(du, strconv.FormatInt(max(o.End-o.Start, 1000), 10))
				st = append(st, o.Status)
			}
			plans = append(plans, map[string]any{"id": id, "recentBackups": map[string]any{"flowId": ts, "timestampMs": ts, "durationMs": du, "status": st}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"planSummaries": plans, "repoSummaries": []any{map[string]any{"id": "nas"}}, "configPath": "/x/config.json"})
	case "/v1.Backrest/GetOperations":
		sel, _ := req["selector"].(map[string]any)
		if sel == nil {
			http.Error(w, `{"code":"unknown","message":"empty selector"}`, http.StatusInternalServerError)
			return
		}
		inst, _ := sel["instanceId"].(string)
		gte, _ := strconv.ParseInt(brToStr(sel["modnoGte"]), 10, 64)
		lastN, _ := strconv.Atoi(brToStr(req["lastN"]))
		list := f.sorted(func(o *fakeBrOp) bool { return (inst == "" || o.Instance == inst) && o.Modno >= gte })
		if lastN > 0 { // reversed by start time, limited, then sorted by id
			sort.SliceStable(list, func(i, j int) bool { return list[i].Start > list[j].Start })
			list = list[:min(lastN, len(list))]
			sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		}
		out := []any{}
		for _, o := range list {
			out = append(out, o.json())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"operations": out})
	default:
		f.forbidden = append(f.forbidden, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"unimplemented"}`)
	}
}

func brToStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return ""
}

func (f *fakeBackrest) sorted(keep func(*fakeBrOp) bool) []*fakeBrOp {
	var out []*fakeBrOp
	for _, o := range f.ops {
		if keep(o) {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type brHarness struct {
	t     *testing.T
	f     *fakeBackrest
	tg    Target
	now   time.Time
	state json.RawMessage
	srv   *httptest.Server
}

func newBrHarness(t *testing.T, now time.Time) *brHarness {
	h := &brHarness{t: t, f: newFakeBackrest(), now: now}
	h.srv = httptest.NewServer(h.f)
	t.Cleanup(h.srv.Close)
	h.tg = Target{BaseURL: h.srv.URL, PublicURL: "https://backrest.example", HTTP: h.srv.Client(),
		Fields: map[string]string{"USERNAME": "admin", "PASSWORD": "s3cret-pw"}, Now: func() time.Time { return h.now }}
	return h
}

func (h *brHarness) poll() []Event {
	h.t.Helper()
	evs, st, err := backrest{}.Poll(context.Background(), h.tg, h.state)
	if err != nil {
		h.t.Fatal(err)
	}
	h.state = st
	if len(h.f.forbidden) > 0 {
		h.t.Fatalf("called %v", h.f.forbidden)
	}
	for _, s := range []string{"s3cret-pw", "repo-secret", "cloud-secret", "repo-pw"} {
		if strings.Contains(string(st), s) {
			h.t.Fatalf("secret %q in state", s)
		}
		for _, e := range evs {
			b, _ := json.Marshal(e.Payload)
			if strings.Contains(string(b), s) {
				h.t.Fatalf("secret %q in %s: %s", s, e.Name, b)
			}
		}
	}
	return evs
}

func brMs(t time.Time) int64 { return t.UnixMilli() }

func brByName(evs []Event) map[string]Event {
	m := map[string]Event{}
	for _, e := range evs {
		m[e.Name] = e
	}
	return m
}

func TestBackrestTest(t *testing.T) {
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	h := newBrHarness(t, now)
	h.f.put(fakeBrOp{ID: 1, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_WARNING", Start: brMs(now.Add(-6 * time.Hour)), End: brMs(now.Add(-5 * time.Hour)), Snap: "abc"})
	info, err := backrest{}.Test(context.Background(), h.tg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "1.14.1" || !info.Admin {
		t.Fatalf("%+v", info)
	}
	for _, want := range []string{"2 repos, 2 plans", "nightly: last backup 2026-10-20 04:00 warning", "manual: no backup in the last 30 days (not scheduled)"} {
		if !strings.Contains(info.Detail, want) {
			t.Fatalf("detail %q lacks %q", info.Detail, want)
		}
	}
	if strings.Contains(info.Detail, "OFF") || strings.Contains(info.Detail, "secret") {
		t.Fatal(info.Detail)
	}
	if len(h.f.forbidden) > 0 {
		t.Fatal(h.f.forbidden)
	}
	// overdue shows in Test too
	h.now = now.Add(40 * time.Hour)
	info, _ = backrest{}.Test(context.Background(), h.tg)
	if !strings.Contains(info.Detail, "(overdue)") {
		t.Fatal(info.Detail)
	}
	// bad GRACE_HOURS
	h.tg.Fields["GRACE_HOURS"] = "soon"
	if _, err := (backrest{}).Test(context.Background(), h.tg); err == nil || !strings.Contains(err.Error(), "GRACE_HOURS") {
		t.Fatal(err)
	}
}

func TestBackrestAuth(t *testing.T) {
	h := newBrHarness(t, time.Now())
	h.tg.Fields["PASSWORD"] = "wrong-pw"
	_, err := backrest{}.Test(context.Background(), h.tg)
	if err == nil || !strings.Contains(err.Error(), "rejected the stored USERNAME and PASSWORD") || strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("%v", err)
	}
	h.tg.Fields = map[string]string{}
	_, err = backrest{}.Test(context.Background(), h.tg)
	if err == nil || !strings.Contains(err.Error(), "needs USERNAME and PASSWORD") {
		t.Fatalf("%v", err)
	}
	if _, _, err := (backrest{}).Poll(context.Background(), h.tg, nil); err == nil {
		t.Fatal("poll without credentials")
	}
	// authentication turned off in Backrest: no credentials needed, and Test says so
	h.f.authOff = true
	info, err := backrest{}.Test(context.Background(), h.tg)
	if err != nil || !strings.Contains(info.Detail, "authentication is OFF") {
		t.Fatalf("%v %+v", err, info)
	}
	if _, err := (backrest{}).Provision(context.Background(), h.tg, Person{}); err == nil {
		t.Fatal("provision")
	}
	if err := (backrest{}).ConfigureSSO(context.Background(), h.tg, SSOConfig{Enabled: true}); err == nil {
		t.Fatal("sso")
	}
}

func TestBackrestEvents(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	h := newBrHarness(t, t0)
	f := h.f
	// history before Core: a good backup, a failed check, a failed backup
	f.put(fakeBrOp{ID: 1, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(-24 * time.Hour)), End: brMs(t0.Add(-23 * time.Hour)), Snap: "s1", Added: 10})
	f.put(fakeBrOp{ID: 2, Plan: "_system_", Repo: "nas", Kind: "check", Status: "STATUS_ERROR", Start: brMs(t0.Add(-22 * time.Hour)), End: brMs(t0.Add(-22 * time.Hour)), Msg: "check failed"})
	f.put(fakeBrOp{ID: 3, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_ERROR", Start: brMs(t0.Add(-2 * time.Hour)), End: brMs(t0.Add(-2 * time.Hour)), Msg: "boom"})
	f.put(fakeBrOp{ID: 4, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_PENDING", Start: brMs(t0.Add(24 * time.Hour))})
	f.put(fakeBrOp{ID: 5, Plan: "other", Repo: "nas", Kind: "backup", Status: "STATUS_ERROR", Instance: "peer", Start: brMs(t0), End: brMs(t0)})
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	if req := f.lastReq; req["lastN"] != "200" {
		t.Fatalf("baseline request %v", req)
	}

	h.now = t0.Add(10 * time.Minute)
	// a backup starts: in progress is not news
	f.put(fakeBrOp{ID: 10, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_INPROGRESS", Start: brMs(t0.Add(time.Minute))})
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("in progress %+v", evs)
	}
	sel := f.lastReq["selector"].(map[string]any)
	if sel["instanceId"] != "box" || sel["modnoGte"] == nil || f.lastReq["lastN"] != "500" {
		t.Fatalf("request %v", f.lastReq)
	}

	h.now = t0.Add(time.Hour)
	f.put(fakeBrOp{ID: 10, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(time.Minute)), End: brMs(t0.Add(31 * time.Minute)), Snap: "s10", Added: 3 << 20, FilesNew: 4})
	f.put(fakeBrOp{ID: 11, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_WARNING", Start: brMs(t0.Add(32 * time.Minute)), End: brMs(t0.Add(40 * time.Minute)), Snap: "s11",
		Msg:    "Partial backup, some files may not have been read completely.",
		Errors: []map[string]string{{"item": "/home/a/locked.db", "during": "archival", "message": "open /home/a/locked.db: permission denied"}, {"item": "/x", "message": "read error"}}})
	f.put(fakeBrOp{ID: 12, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_ERROR", Start: brMs(t0.Add(41 * time.Minute)), End: brMs(t0.Add(42 * time.Minute)),
		Msg: "repo rest:https://u:repo-pw@nas/r unreachable; password s3cret-pw " + strings.Repeat("x", 900)})
	f.put(fakeBrOp{ID: 13, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_SUCCESS", DryRun: true, Start: brMs(t0.Add(43 * time.Minute)), End: brMs(t0.Add(44 * time.Minute))})
	f.put(fakeBrOp{ID: 14, Plan: "nightly", Repo: "nas", Kind: "index", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(44 * time.Minute)), End: brMs(t0.Add(44 * time.Minute)), Snap: "s10"})
	f.put(fakeBrOp{ID: 15, Plan: "_system_", Repo: "nas", Kind: "check", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(45 * time.Minute)), End: brMs(t0.Add(50 * time.Minute))})
	f.put(fakeBrOp{ID: 16, Plan: "_system_", Repo: "cloud", Kind: "prune", Status: "STATUS_ERROR", Start: brMs(t0.Add(45 * time.Minute)), End: brMs(t0.Add(46 * time.Minute)), Msg: "repo locked"})
	f.put(fakeBrOp{ID: 17, Plan: "nightly", Repo: "nas", Kind: "forget", Status: "STATUS_ERROR", Start: brMs(t0.Add(46 * time.Minute)), End: brMs(t0.Add(47 * time.Minute)), Msg: "forget failed"})
	f.put(fakeBrOp{ID: 18, Plan: "nightly", Repo: "nas", Kind: "restore", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(47 * time.Minute)), End: brMs(t0.Add(48 * time.Minute)), Snap: "s10"})
	f.put(fakeBrOp{ID: 19, Plan: "nightly", Repo: "nas", Kind: "hook", Status: "STATUS_ERROR", Start: brMs(t0.Add(48 * time.Minute)), End: brMs(t0.Add(48 * time.Minute)), Hook: "command hook plan/nightly/hook/0", Cond: "CONDITION_SNAPSHOT_START", Msg: "exit status 1"})
	f.put(fakeBrOp{ID: 20, Plan: "nightly", Repo: "nas", Kind: "hook", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(48 * time.Minute)), End: brMs(t0.Add(48 * time.Minute)), Hook: "ok hook"})
	f.put(fakeBrOp{ID: 21, Plan: "nightly", Repo: "cloud", Kind: "restore", Status: "STATUS_ERROR", Start: brMs(t0.Add(49 * time.Minute)), End: brMs(t0.Add(49 * time.Minute)), Msg: "no space"})
	f.put(fakeBrOp{ID: 22, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_USER_CANCELLED", Start: brMs(t0.Add(50 * time.Minute)), End: brMs(t0.Add(50 * time.Minute))})
	evs := h.poll()
	names := []string{}
	for _, e := range evs {
		names = append(names, e.Name)
	}
	want := "backup_finished backup_warning backup_failed check_recovered prune_failed forget_failed restore_finished hook_failed restore_failed"
	if strings.Join(names, " ") != want {
		t.Fatalf("got %v", names)
	}
	m := brByName(evs)
	fin := m["backup_finished"]
	if fin.Subject != "plan:nightly" || fin.UserEmail != "" || fin.Payload["snapshot_id"] != "s10" || fin.Payload["bytes_added"] != int64(3<<20) ||
		fin.Payload["files_new"] != int64(4) || fin.Payload["duration_seconds"] != int64(1800) || fin.Payload["repo"] != "nas" ||
		fin.Payload["url"] != "https://backrest.example/#/plan/nightly" || fin.Payload["title"] != "Backup of nightly finished: 3.0 MiB added" {
		t.Fatalf("finished %+v", fin.Payload)
	}
	warn := m["backup_warning"].Payload
	if warn["error_count"] != 2 || !strings.Contains(warn["message"].(string), "Partial backup") || warn["snapshot_id"] != "s11" {
		t.Fatalf("warning %+v", warn)
	}
	if errs := warn["errors"].([]string); len(errs) != 2 || errs[0] != "archival open /home/a/locked.db: permission denied" || errs[1] != "/x: read error" {
		t.Fatalf("warning errors %q", errs)
	}
	failMsg := m["backup_failed"].Payload["message"].(string)
	if !strings.Contains(failMsg, "https://[redacted]@nas") || !strings.Contains(failMsg, "password [redacted]") || len([]rune(failMsg)) > brMsgMax+1 {
		t.Fatalf("failed message %q", failMsg)
	}
	if rec := m["check_recovered"]; rec.Subject != "repo:nas" || rec.Payload["url"] != "https://backrest.example/#/repo/nas" || rec.Payload["plan"] != nil || rec.Payload["failing_since"] == nil {
		t.Fatalf("check recovered %+v", rec)
	}
	if p := m["prune_failed"].Payload; p["repo"] != "cloud" || p["message"] != "repo locked" {
		t.Fatalf("prune %+v", p)
	}
	if p := m["hook_failed"].Payload; p["hook"] != "command hook plan/nightly/hook/0" || p["condition"] != "CONDITION_SNAPSHOT_START" {
		t.Fatalf("hook %+v", p)
	}
	if p := m["restore_finished"].Payload; p["bytes_restored"] != int64(2048) || p["target"] != nil || p["path"] != nil {
		t.Fatalf("restore %+v", p)
	}

	// nothing new, an announced operation modified again: no duplicates
	h.now = t0.Add(2 * time.Hour)
	f.put(*f.ops[10])
	f.put(*f.ops[12])
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("duplicates %+v", evs)
	}
	// prune fails again (each failure is news), then recovers once
	f.put(fakeBrOp{ID: 30, Plan: "_system_", Repo: "cloud", Kind: "prune", Status: "STATUS_ERROR", Start: brMs(h.now), End: brMs(h.now)})
	f.put(fakeBrOp{ID: 31, Plan: "_system_", Repo: "cloud", Kind: "prune", Status: "STATUS_SUCCESS", Start: brMs(h.now), End: brMs(h.now.Add(time.Minute))})
	f.put(fakeBrOp{ID: 32, Plan: "_system_", Repo: "cloud", Kind: "prune", Status: "STATUS_SUCCESS", Start: brMs(h.now), End: brMs(h.now.Add(2 * time.Minute))})
	evs = h.poll()
	if len(evs) != 2 || evs[0].Name != "prune_failed" || evs[1].Name != "prune_recovered" || evs[1].Payload["failing_for_seconds"] != int64(4500) {
		t.Fatalf("prune %+v", evs)
	}
}

func TestBackrestOverdue(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 3, 30, 0, 0, time.UTC)
	h := newBrHarness(t, t0)
	h.f.put(fakeBrOp{ID: 1, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(-30 * time.Minute)), End: brMs(t0), Snap: "s1"})
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	// daily cron: 24 h + default grace 6 h
	h.now = t0.Add(29 * time.Hour)
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("not yet %+v", evs)
	}
	h.now = t0.Add(31 * time.Hour)
	evs := h.poll()
	if len(evs) != 1 || evs[0].Name != "backup_overdue" || evs[0].Subject != "plan:nightly" {
		t.Fatalf("overdue %+v", evs)
	}
	p := evs[0].Payload
	if p["hours_since"] != int64(31) || p["expected_every_hours"] != 24.0 || p["grace_hours"] != 6.0 || p["schedule"] != "cron 0 3 * * *" ||
		p["last_success_at"] != "2026-10-20T03:30:00Z" || p["url"] != "https://backrest.example/#/plan/nightly" || p["title"] != "No backup of nightly for 31 hours" {
		t.Fatalf("overdue payload %+v", p)
	}
	h.now = t0.Add(40 * time.Hour)
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("one event per stretch %+v", evs)
	}
	// a failed run does not end it
	h.f.put(fakeBrOp{ID: 5, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_ERROR", Start: brMs(h.now), End: brMs(h.now)})
	if evs := h.poll(); len(evs) != 1 || evs[0].Name != "backup_failed" {
		t.Fatalf("failed %+v", evs)
	}
	// a good one does (a warning counts: a snapshot was made)
	h.now = t0.Add(41 * time.Hour)
	h.f.put(fakeBrOp{ID: 6, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_WARNING", Start: brMs(h.now.Add(-10 * time.Minute)), End: brMs(h.now), Snap: "s6"})
	evs = h.poll()
	if len(evs) != 2 || evs[0].Name != "backup_warning" || evs[1].Name != "backup_resumed" || evs[1].Payload["overdue_for_seconds"] != int64(10*3600) {
		t.Fatalf("resumed %+v", evs)
	}
	// GRACE_HOURS overrides the default
	h.tg.Fields["GRACE_HOURS"] = "1"
	h.now = t0.Add(41*time.Hour + 25*time.Hour + 30*time.Minute)
	if evs := h.poll(); len(evs) != 1 || evs[0].Name != "backup_overdue" || evs[0].Payload["grace_hours"] != 1.0 {
		t.Fatalf("grace %+v", evs)
	}
	// raising the grace ends the stretch quietly: no backup ran
	h.tg.Fields["GRACE_HOURS"] = "48"
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("quiet %+v", evs)
	}
	var st backrestState
	_ = json.Unmarshal(h.state, &st)
	if len(st.Overdue) != 0 {
		t.Fatalf("state %+v", st)
	}
}

// A plan without any good backup is timed from when Core first saw it;
// one already overdue at the baseline is not announced, its recovery is.
func TestBackrestOverdueBaseline(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	h := newBrHarness(t, t0)
	h.f.plans = append(h.f.plans, map[string]any{"id": "photos", "repo": "cloud", "schedule": map[string]any{"maxFrequencyHours": 6}})
	h.f.put(fakeBrOp{ID: 1, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(-72 * time.Hour)), End: brMs(t0.Add(-71 * time.Hour))})
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	h.now = t0.Add(7 * time.Hour)
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("photos 6h + 2h grace not yet: %+v", evs)
	}
	h.now = t0.Add(8*time.Hour + time.Minute)
	evs := h.poll()
	if len(evs) != 1 || evs[0].Payload["plan"] != "photos" || evs[0].Payload["last_success_at"] != nil || !strings.Contains(evs[0].Payload["title"].(string), "since Core started watching") {
		t.Fatalf("photos %+v", evs)
	}
	h.f.put(fakeBrOp{ID: 9, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_SUCCESS", Start: brMs(h.now), End: brMs(h.now.Add(time.Minute))})
	h.now = h.now.Add(2 * time.Minute)
	m := brByName(h.poll())
	if _, ok := m["backup_resumed"]; !ok || m["backup_resumed"].Payload["plan"] != "nightly" {
		t.Fatalf("nightly resumed %+v", m)
	}
}

func TestBackrestEventCap(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	h := newBrHarness(t, t0)
	h.f.put(fakeBrOp{ID: 1, Plan: "nightly", Repo: "nas", Kind: "backup", Status: "STATUS_SUCCESS", Start: brMs(t0.Add(-time.Hour)), End: brMs(t0)})
	h.poll()
	h.now = t0.Add(time.Hour)
	for i := int64(0); i < 60; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		h.f.put(fakeBrOp{ID: 100 + i, Plan: "_system_", Repo: "nas", Kind: "check", Status: "STATUS_ERROR", Start: brMs(at), End: brMs(at)})
	}
	if evs := h.poll(); len(evs) != brMaxEvents {
		t.Fatalf("first page %d", len(evs))
	}
	if evs := h.poll(); len(evs) != 10 {
		t.Fatalf("rest %d", len(evs))
	}
	if evs := h.poll(); len(evs) != 0 {
		t.Fatalf("again %d", len(evs))
	}
}

func TestBackrestCronPeriod(t *testing.T) {
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	tg := Target{Fields: map[string]string{}}
	for expr, want := range map[string]time.Duration{
		"0 3 * * *":       24 * time.Hour,
		"0 */6 * * *":     6 * time.Hour,
		"30 2 * * 1":      7 * 24 * time.Hour,
		"0 3 * * MON-FRI": 72 * time.Hour,
		"@hourly":         time.Hour,
		"0 3 * * * *":     24 * time.Hour, // trailing year
		"0 4 1,15 * *":    16 * 24 * time.Hour,
		"0 3 * * 7":       7 * 24 * time.Hour,
	} {
		e, ok := brExpectation(tg, &brSchedule{Cron: expr}, now)
		if !ok || e.Period != want || e.Guessed {
			t.Errorf("%q: %v %v %v", expr, e.Period, ok, e.Guessed)
		}
	}
	if e, ok := brExpectation(tg, &brSchedule{Cron: "0 3 L * ?"}, now); !ok || !e.Guessed || e.Period != 24*time.Hour || e.Grace != 6*time.Hour {
		t.Errorf("unreadable cron: %+v %v", e, ok)
	}
	for _, s := range []*brSchedule{nil, {Disabled: true}, {Cron: "0 0 1 1 *"}} {
		if _, ok := brExpectation(tg, s, now); ok {
			t.Errorf("%+v expected no expectation", s)
		}
	}
	if e, _ := brExpectation(tg, &brSchedule{MaxFrequencyDays: 7}, now); e.Period != 7*24*time.Hour || e.Grace != 42*time.Hour {
		t.Errorf("days %+v", e)
	}
	if e, _ := brExpectation(tg, &brSchedule{MaxFrequencyHours: 1}, now); e.Grace != brMinGrace {
		t.Errorf("hours %+v", e)
	}
}
