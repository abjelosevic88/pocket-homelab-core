package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

type batteryList struct {
	Batteries []store.DeviceBattery `json:"batteries"`
}

func TestDeviceBatteries(t *testing.T) {
	e := newEnv(t)
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var b store.User
	if c := e.do("POST", "/api/v1/users", "", map[string]any{"email": "b@b.c", "name": "B", "password": "password123", "role": "member"}, &b); c != 201 && c != 200 {
		t.Fatalf("user %d", c)
	}
	mac := newInstall(t, e, 0, "Mac")
	phone := newInstall(t, e, 0, "iPhone")
	other := newInstall(t, e, b.ID, "B's iPhone")
	var devs []store.Device
	e.do("GET", "/api/v1/devices", "", nil, &devs)
	ids := map[string]int64{}
	for _, d := range devs {
		ids[d.Name] = d.ID
	}

	now := time.Now().UTC().Truncate(time.Second)
	body := map[string]any{"batteries": []map[string]any{
		{"key": "self", "name": "", "kind": "iphone", "level": 150, "charging": true, "measured_at": now.Format(time.RFC3339)},
		{"key": "watch-1", "name": "Apple Watch", "kind": "watch", "level": -5, "charging": false, "measured_at": now.Add(-time.Hour).Format(time.RFC3339)},
		{"key": "pencil", "name": "Pencil", "kind": "toaster", "level": 42, "measured_at": "yesterday"},
		{"key": "old", "name": "Old AirPods", "kind": "other", "level": 10, "measured_at": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)},
		{"key": "self", "kind": "iphone", "level": 1}, // repeated key: dropped
	}}

	// No token → 401; a person's session or an app can't report → 403; apps can't read → 403.
	if c := e.tokenOnly("PUT", "/api/v1/devices/me/battery", "", body, nil); c != 401 {
		t.Fatalf("anonymous put: %d", c)
	}
	if c := e.tokenOnly("GET", "/api/v1/devices/batteries", "", nil, nil); c != 401 {
		t.Fatalf("anonymous get: %d", c)
	}
	if c := e.do("PUT", "/api/v1/devices/me/battery", "", body, nil); c != 403 {
		t.Fatalf("session put: %d", c)
	}
	appTok, _ := registerTestApp(t, e, "habits")
	if c := e.tokenOnly("GET", "/api/v1/devices/batteries", appTok, nil, nil); c != 403 {
		t.Fatalf("app get: %d", c)
	}

	var saved struct{ Saved int }
	if c := e.tokenOnly("PUT", "/api/v1/devices/me/battery", phone.device, body, &saved); c != 200 || saved.Saved != 4 {
		t.Fatalf("put: %d %+v", c, saved)
	}
	if c := e.tokenOnly("PUT", "/api/v1/devices/me/battery", other.device, map[string]any{"batteries": []map[string]any{
		{"key": "self", "kind": "iphone", "level": 77, "measured_at": now.Format(time.RFC3339)}}}, &saved); c != 200 || saved.Saved != 1 {
		t.Fatalf("other put: %d %+v", c, saved)
	}

	// The Mac (device token) and the person's session see the phone's batteries, not B's
	// and not the 10-day-old reading; levels clamped, unknown kind = other.
	for _, who := range []string{mac.device, ""} {
		var got batteryList
		var c int
		if who == "" {
			c = e.do("GET", "/api/v1/devices/batteries", "", nil, &got)
		} else {
			c = e.tokenOnly("GET", "/api/v1/devices/batteries", who, nil, &got)
		}
		if c != 200 || len(got.Batteries) != 3 {
			t.Fatalf("get (%q): %d %+v", who, c, got)
		}
		by := map[string]store.DeviceBattery{}
		for _, x := range got.Batteries {
			if x.DeviceID != ids["iPhone"] || x.DeviceName != "iPhone" || x.Platform != "macos" {
				t.Fatalf("wrong device %+v", x)
			}
			by[x.Key] = x
		}
		if s := by["self"]; s.Level != 100 || !s.Charging || s.Kind != "iphone" || s.MeasuredAt != now.Format(time.RFC3339) {
			t.Fatalf("self %+v", s)
		}
		if w := by["watch-1"]; w.Level != 0 || w.Charging || w.Kind != "watch" || w.Name != "Apple Watch" {
			t.Fatalf("watch %+v", w)
		}
		if p := by["pencil"]; p.Kind != "other" || p.Level != 42 || p.MeasuredAt == "" {
			t.Fatalf("pencil %+v", p)
		}
	}

	// B sees only B's own device.
	var gotB batteryList
	if c := e.tokenOnly("GET", "/api/v1/devices/batteries", other.device, nil, &gotB); c != 200 || len(gotB.Batteries) != 1 ||
		gotB.Batteries[0].DeviceID != ids["B's iPhone"] || gotB.Batteries[0].Level != 77 {
		t.Fatalf("B: %d %+v", c, gotB)
	}

	// A new report replaces the old one; the list is capped and names are cut short.
	many := []map[string]any{}
	for i := range 20 {
		many = append(many, map[string]any{"key": "k" + strconv.Itoa(i), "name": strings.Repeat("x", 200), "kind": "other", "level": i})
	}
	if c := e.tokenOnly("PUT", "/api/v1/devices/me/battery", phone.device, map[string]any{"batteries": many}, &saved); c != 200 || saved.Saved != batteryMaxEntries {
		t.Fatalf("many: %d %+v", c, saved)
	}
	var got batteryList
	e.tokenOnly("GET", "/api/v1/devices/batteries", mac.device, nil, &got)
	if len(got.Batteries) != batteryMaxEntries || len([]rune(got.Batteries[0].Name)) != batteryMaxName {
		t.Fatalf("after many: %d %+v", len(got.Batteries), got.Batteries[0])
	}
	for _, x := range got.Batteries {
		if x.Key == "self" {
			t.Fatal("old entry survived the replace")
		}
	}

	// Revoking the phone removes its batteries.
	if c := e.do("DELETE", "/api/v1/devices/"+itoa(ids["iPhone"]), "", nil, nil); c != 200 && c != 204 {
		t.Fatalf("revoke %d", c)
	}
	got = batteryList{}
	if c := e.tokenOnly("GET", "/api/v1/devices/batteries", mac.device, nil, &got); c != 200 || len(got.Batteries) != 0 {
		t.Fatalf("after revoke: %d %+v", c, got)
	}
	var n int
	if err := e.st.DB.QueryRow(`SELECT COUNT(*) FROM device_batteries WHERE device_id = ?`, ids["iPhone"]).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left %d %v", n, err)
	}
}
