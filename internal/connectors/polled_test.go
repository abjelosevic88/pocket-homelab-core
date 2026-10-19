package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Dawarich: the phone going quiet is noticed as time passes, once.
func TestDawarichTrackingStopped(t *testing.T) {
	pointAt := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": 7, "email": "me@x"}})
		case "/api/v1/points":
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"timestamp": pointAt.Unix()}})
		case "/api/v1/visits":
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()
	now := pointAt.Add(time.Hour)
	tg := Target{BaseURL: srv.URL, Fields: map[string]string{"API_KEY": "k"}, HTTP: srv.Client(), Now: func() time.Time { return now }}
	c := dawarich{}
	var state json.RawMessage
	poll := func() []Event {
		evs, st, err := c.Poll(context.Background(), tg, state)
		if err != nil {
			t.Fatal(err)
		}
		state = st
		return evs
	}
	if evs := poll(); len(evs) != 0 {
		t.Fatalf("baseline %+v", evs)
	}
	now = pointAt.Add(11 * time.Hour)
	if evs := poll(); len(evs) != 0 {
		t.Fatalf("11 h is not yet quiet: %+v", evs)
	}
	now = pointAt.Add(13 * time.Hour)
	evs := poll()
	if len(evs) != 1 || evs[0].Name != "tracking_stopped" || evs[0].RemoteUserID != "7" || evs[0].Payload["silent_hours"] != int64(13) {
		t.Fatalf("stopped %+v", evs)
	}
	now = pointAt.Add(20 * time.Hour)
	if evs := poll(); len(evs) != 0 {
		t.Fatalf("announced once: %+v", evs)
	}
}

// Prometheus lines with escaped quotes and commas inside label values.
func TestParsePromLine(t *testing.T) {
	name, l, v, ok := parsePromLine(`monitor_status{monitor_name="Say \"hi\", ok",monitor_type="http",monitor_url="https://a.b/c?x=1,2"} 0`)
	if !ok || name != "monitor_status" || v != 0 || l["monitor_name"] != `Say "hi", ok` || l["monitor_url"] != "https://a.b/c?x=1,2" {
		t.Fatalf("%q %+v %v %v", name, l, v, ok)
	}
	if _, _, _, ok := parsePromLine("# HELP x"); ok {
		t.Fatal("comment parsed")
	}
	if n, _, v, ok := parsePromLine("process_cpu_seconds_total 12.5"); !ok || n != "process_cpu_seconds_total" || v != 12.5 {
		t.Fatal("plain line")
	}
}
