package server

import (
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Battery levels: a paired device (the Pocket Homelab iPhone app) reports its own battery and
// the ones it carries (a paired Watch); the person's other clients (Pocket Launcher on the Mac)
// show them all.

const (
	batteryMaxEntries = 16
	batteryMaxKey     = 64
	batteryMaxName    = 80
	batteryKeepFor    = 7 * 24 * time.Hour // older readings are left out of GET /devices/batteries
)

var batteryKinds = map[string]bool{"iphone": true, "ipad": true, "mac": true, "watch": true, "other": true}

// PUT /api/v1/devices/me/battery {batteries: [{key, name, kind, level, charging, measured_at}]}
// replaces all of the calling device's battery entries → {saved: N}.
func (s *Server) handlePutDeviceBattery(w http.ResponseWriter, r *http.Request) {
	p := pushDevice(w, r)
	if p == nil {
		return
	}
	var in struct {
		Batteries []struct {
			Key        string  `json:"key"`
			Name       string  `json:"name"`
			Kind       string  `json:"kind"`
			Level      float64 `json:"level"`
			Charging   bool    `json:"charging"`
			MeasuredAt string  `json:"measured_at"`
		} `json:"batteries"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	now := time.Now().UTC()
	seen := map[string]bool{}
	out := []store.DeviceBattery{}
	for _, b := range in.Batteries {
		if len(out) == batteryMaxEntries {
			break
		}
		key := clipText(b.Key, batteryMaxKey)
		if key == "" {
			key = "self"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		kind := strings.ToLower(strings.TrimSpace(b.Kind))
		if !batteryKinds[kind] {
			kind = "other"
		}
		level := 0
		if !math.IsNaN(b.Level) {
			level = int(math.Round(math.Max(0, math.Min(100, b.Level))))
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(b.MeasuredAt))
		if err != nil || at.After(now.Add(5*time.Minute)) {
			at = now // missing, unreadable or from the future: now
		}
		out = append(out, store.DeviceBattery{Key: key, Name: clipText(b.Name, batteryMaxName), Kind: kind, Level: level,
			Charging: b.Charging, MeasuredAt: at.UTC().Truncate(time.Second).Format(time.RFC3339)})
	}
	if err := s.store.ReplaceDeviceBatteries(r.Context(), p.Device.ID, out); err != nil {
		s.fail(w, err, "save batteries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": len(out)})
}

// GET /api/v1/devices/batteries → {batteries: [...]}: the recent battery readings of all of the
// caller's devices. Any credential of the person: a paired device or their own session/token.
func (s *Server) handleListDeviceBatteries(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || (p.Kind != "device" && p.Kind != "user") || p.User == nil || p.User.Disabled {
		writeErr(w, http.StatusForbidden, "a person's devices or sessions only")
		return
	}
	since := time.Now().UTC().Add(-batteryKeepFor).Truncate(time.Second).Format(time.RFC3339)
	bs, err := s.store.UserDeviceBatteries(r.Context(), p.User.ID, since)
	if err != nil {
		s.fail(w, err, "list batteries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batteries": bs})
}

// clipText trims s, drops control characters and keeps at most n characters.
func clipText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if rs := []rune(s); len(rs) > n {
		s = strings.TrimSpace(string(rs[:n]))
	}
	return s
}
