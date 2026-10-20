package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// poller runs one connector's polls against a target, keeping the state.
type poller struct {
	t     *testing.T
	c     Connector
	tg    Target
	state json.RawMessage
}

func (p *poller) poll() []Event {
	p.t.Helper()
	evs, st, err := p.c.Poll(context.Background(), p.tg, p.state)
	if err != nil {
		p.t.Fatal(err)
	}
	p.state = st
	return evs
}

func names(evs []Event) string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Name)
	}
	return strings.Join(out, ",")
}

// Changedetection: changes and failing checks, muted groups quiet, never a recheck.
func TestChangedetectionPoll(t *testing.T) {
	var mu sync.Mutex
	watches := map[string]map[string]any{
		"w1": {"last_changed": 1000, "last_checked": 1100, "last_error": false, "title": "", "page_title": "Kindle deals", "url": "https://shop.example/kindle", "tags": []string{"t1"}, "viewed": true},
		"w2": {"last_changed": 0, "last_checked": 1100, "last_error": false, "title": "Router firmware", "url": "https://router.example/fw", "tags": []string{}, "viewed": true},
		"w3": {"last_changed": 500, "last_checked": 1100, "last_error": false, "title": "Noisy", "url": "https://noisy.example", "tags": []string{"t2"}, "viewed": true},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("x-api-key") != "cd-key" {
			w.WriteHeader(403)
			return
		}
		if r.URL.RawQuery != "" || r.Method != "GET" {
			t.Errorf("only plain reads: %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case "/api/v1/systeminfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.60.9", "watch_count": len(watches), "overdue_watches": []string{"w2"}, "queue_size": 0, "uptime": 10})
		case "/api/v1/watch":
			_ = json.NewEncoder(w).Encode(watches)
		case "/api/v1/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"t1": map[string]any{"title": "Shopping", "notification_muted": false}, "t2": map[string]any{"title": "Muted", "notification_muted": true}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	tg := Target{BaseURL: srv.URL, PublicURL: "https://cd.example/", Fields: map[string]string{"API_KEY": "cd-key"}, HTTP: srv.Client()}
	c := changedetection{}
	info, err := c.Test(context.Background(), tg)
	if err != nil || info.Version != "0.60.9" || !strings.Contains(info.Detail, "3 watches, 1 overdue") {
		t.Fatalf("test %+v %v", info, err)
	}
	bad := tg
	bad.Fields = map[string]string{"API_KEY": "nope"}
	if _, err := c.Test(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "rejected the stored API key") || strings.Contains(err.Error(), "nope") {
		t.Fatalf("bad key %v", err)
	}
	p := &poller{t: t, c: c, tg: tg}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	mu.Lock()
	watches["w1"]["last_changed"] = 2000
	watches["w2"]["last_error"] = "Connection refused"
	watches["w3"]["last_changed"] = 2500 // muted group
	watches["w4"] = map[string]any{"last_changed": 0, "last_checked": 0, "last_error": false, "title": "New", "url": "https://new.example", "tags": []string{}}
	mu.Unlock()
	evs := p.poll()
	if names(evs) != "check_failed,change_detected" {
		t.Fatalf("events %s %+v", names(evs), evs)
	}
	ch := evs[1]
	if ch.Payload["title"] != "Kindle deals changed" || ch.Payload["url"] != "https://cd.example/diff/w1" || ch.Payload["target"] != "https://shop.example/kindle" || ch.Subject != "watch:w1:2000" || ch.UserEmail != "" {
		t.Fatalf("change %+v", ch)
	}
	if g, _ := ch.Payload["groups"].([]string); len(g) != 1 || g[0] != "Shopping" {
		t.Fatalf("groups %+v", ch.Payload["groups"])
	}
	if evs[0].Payload["message"] != "Connection refused" || evs[0].Payload["title"] != "Router firmware: check failed" {
		t.Fatalf("failed %+v", evs[0])
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("no repeats %+v", evs)
	}
	mu.Lock()
	watches["w2"]["last_error"] = false
	mu.Unlock()
	if evs := p.poll(); names(evs) != "check_recovered" {
		t.Fatalf("recovered %+v", evs)
	}
}

// Scrutiny: failing and passing again, hot once until cooled, new disks, silent hosts.
func TestScrutinyPoll(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	disk := func(name, host string, status, temp int, collected time.Time) map[string]any {
		return map[string]any{
			"device": map[string]any{"archived": false, "wwn": "", "device_name": name, "model_name": "ST12000VN0008", "serial_number": "ZZ" + name, "label": "", "host_id": host, "capacity": 12000138625024, "device_protocol": "ATA", "device_status": status},
			"smart":  map[string]any{"collector_date": collected.Format(time.RFC3339), "temp": temp, "power_on_hours": 1939},
		}
	}
	sum := map[string]any{
		"0xa": disk("sda", "truenas", 0, 32, now.Add(-time.Hour)),
		"0xb": disk("nvme1", "home-server", 0, 36, now.Add(-12*time.Hour)),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/api/summary":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"summary": sum}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	tg := Target{BaseURL: srv.URL, PublicURL: "https://scrutiny.example", Fields: map[string]string{}, HTTP: srv.Client(), Now: func() time.Time { return now }}
	c := scrutiny{}
	if info, err := c.Test(context.Background(), tg); err != nil || !strings.Contains(info.Detail, "2 disks on 2 hosts, 0 failing") {
		t.Fatalf("test %+v %v", info, err)
	}
	p := &poller{t: t, c: c, tg: tg}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	mu.Lock()
	sum["0xa"] = disk("sda", "truenas", 2, 57, now.Add(-time.Hour))
	sum["0xc"] = disk("sdb", "truenas", 0, 30, now.Add(-time.Hour))
	mu.Unlock()
	evs := p.poll()
	if names(evs) != "disk_failing,disk_hot,disk_added" {
		t.Fatalf("events %s", names(evs))
	}
	f := evs[0]
	if f.Payload["title"] != "truenas sda (ST12000VN0008) is failing by Scrutiny's checks" || f.Payload["scrutiny_failed"] != true || f.Payload["smart_failed"] != false || f.Payload["url"] != "https://scrutiny.example/web/device/0xa" || f.UserEmail != "" {
		t.Fatalf("failing %+v", f)
	}
	mu.Lock()
	sum["0xa"] = disk("sda", "truenas", 2, 53, now.Add(-time.Hour)) // still within the re-arm band
	mu.Unlock()
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("no repeats %s", names(evs))
	}
	mu.Lock()
	sum["0xa"] = disk("sda", "truenas", 0, 49, now.Add(-time.Hour))
	mu.Unlock()
	if evs := p.poll(); names(evs) != "disk_passing" {
		t.Fatalf("passing %s", names(evs))
	}
	mu.Lock()
	sum["0xa"] = disk("sda", "truenas", 0, 56, now.Add(-time.Hour))
	mu.Unlock()
	if evs := p.poll(); names(evs) != "disk_hot" {
		t.Fatalf("hot again after cooling %s", names(evs))
	}
	now = now.Add(37 * time.Hour) // home-server's collector: 49 h without data
	evs = p.poll()
	if names(evs) != "host_silent" || evs[0].Payload["host"] != "home-server" || evs[0].Payload["title"] != "Scrutiny has had no disk data from home-server for 49 hours" {
		t.Fatalf("silent %s %+v", names(evs), evs)
	}
	mu.Lock()
	sum["0xb"] = disk("nvme1", "home-server", 0, 36, now)
	delete(sum, "0xc")
	mu.Unlock()
	evs = p.poll()
	if names(evs) != "disk_removed,host_reporting" || evs[0].Payload["disk"] != "truenas sdb (ST12000VN0008)" {
		t.Fatalf("back %s %+v", names(evs), evs)
	}
}

// Speedtest Tracker: every finished test once, failures, thresholds, running ones wait.
func TestSpeedtestPoll(t *testing.T) {
	var mu sync.Mutex
	result := func(id int64, status string, down, up float64, healthy bool) map[string]any {
		return map[string]any{"id": id, "ping": 2.24, "download": down / 8 * 1e6, "upload": up / 8 * 1e6, "download_bits": down * 1e6, "upload_bits": up * 1e6,
			"healthy": healthy, "status": status, "scheduled": true, "created_at": "2026-10-20 09:07:00",
			"benchmarks": map[string]any{"download": map[string]any{"bar": "min", "passed": down >= 100, "type": "absolute", "value": 100, "unit": "mbps"}},
			"data": map[string]any{"isp": "BH Telecom", "interface": map[string]any{"externalIp": "203.0.113.9"}, "ping": map[string]any{"jitter": 0.4},
				"server": map[string]any{"name": "BLIC.NET", "location": "Banja Luka"}, "result": map[string]any{"url": "https://www.speedtest.net/result/c/x"}}}
	}
	results := []map[string]any{result(10, "completed", 152.63, 74.67, true)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/results":
			if r.Header.Get("Authorization") != "Bearer st-token" {
				w.WriteHeader(401)
				return
			}
			if r.URL.Query().Get("sort") != "-created_at" || r.URL.Query().Get("page[size]") == "" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			out := []any{}
			for i := len(results) - 1; i >= 0; i-- {
				out = append(out, results[i])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
		case "/api/speedtest/latest":
			l := results[len(results)-1]
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "ok", "data": map[string]any{"id": l["id"], "ping": 2.24, "download": l["download_bits"].(float64) / 1e6, "upload": l["upload_bits"].(float64) / 1e6,
				"server_name": "BLIC.NET", "url": "https://www.speedtest.net/result/c/x", "scheduled": true, "failed": l["status"] == "failed", "created_at": "2026-10-20T09:07:00.000000+02:00"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	tg := Target{BaseURL: srv.URL, PublicURL: "https://speedtest.example", Fields: map[string]string{"TOKEN": "st-token"}, HTTP: srv.Client()}
	c := speedtest{}
	if info, err := c.Test(context.Background(), tg); err != nil || !strings.HasPrefix(info.Detail, "last test 153↓ / 75↑ Mbit/s; reading every result") {
		t.Fatalf("test %+v %v", info, err)
	}
	p := &poller{t: t, c: c, tg: tg}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	mu.Lock()
	results = append(results, result(11, "failed", 0, 0, true), result(12, "completed", 41.5, 20, false), result(13, "running", 0, 0, true))
	results[1]["data"] = map[string]any{"message": "Ookla server unreachable"}
	results[1]["download_bits"], results[1]["upload_bits"], results[1]["healthy"] = nil, nil, nil
	mu.Unlock()
	evs := p.poll()
	if names(evs) != "failed,finished,degraded" {
		t.Fatalf("events %s", names(evs))
	}
	if evs[0].Payload["message"] != "Ookla server unreachable" {
		t.Fatalf("failed %+v", evs[0])
	}
	fin := evs[1].Payload
	if fin["download_mbps"] != 41.5 || fin["upload_mbps"] != 20.0 || fin["healthy"] != false || fin["server"] != "BLIC.NET (Banja Luka)" || fin["title"] != "Speedtest: 42↓ / 20↑ Mbit/s, 2 ms" {
		t.Fatalf("finished %+v", fin)
	}
	if b, _ := fin["below_threshold"].([]string); len(b) != 1 || b[0] != "download" {
		t.Fatalf("thresholds %+v", fin["below_threshold"])
	}
	for _, e := range evs {
		raw, _ := json.Marshal(e.Payload)
		if strings.Contains(string(raw), "203.0.113.9") {
			t.Fatalf("the WAN address leaked: %s", raw)
		}
	}
	mu.Lock()
	results[3] = result(13, "completed", 150, 70, true)
	mu.Unlock()
	if evs := p.poll(); names(evs) != "finished,recovered" {
		t.Fatalf("running one later %s", names(evs))
	}
	if evs := p.poll(); len(evs) != 0 {
		t.Fatalf("no repeats %s", names(evs))
	}
	// A test stuck "running" does not hold back the ones after it.
	mu.Lock()
	results = append(results, result(14, "running", 0, 0, true), result(15, "completed", 151, 71, true))
	mu.Unlock()
	if evs := p.poll(); names(evs) != "finished" || evs[0].Payload["result_id"] != int64(15) {
		t.Fatalf("stuck %s", names(evs))
	}
	// Without a token: the newest result, from the public endpoint.
	pub := &poller{t: t, c: c, tg: Target{BaseURL: srv.URL, Fields: map[string]string{}, HTTP: srv.Client()}}
	pub.poll()
	mu.Lock()
	results = append(results, result(16, "completed", 149, 70, true))
	mu.Unlock()
	if evs := pub.poll(); names(evs) != "finished" || evs[0].Payload["download_mbps"] != 149.0 {
		t.Fatalf("public %+v", evs)
	}
}
