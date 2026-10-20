package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Scrutiny (0.8+) connector: events only, polled, read-only. Scrutiny has no
// accounts and no API authentication. Endpoints:
//
//	GET /api/health     Test                                               read
//	GET /api/summary    every disk: status, temperature, last collection  read
//
// Disks are household things: no user_email. device_status is a bit set:
// 1 = the drive's own SMART check failed, 2 = Scrutiny's failure model says
// it is failing. Archived disks are ignored.
//
// Optional service fields: TEMP_WARN (°C; default 55) and STALE_HOURS (a
// host whose collector sent nothing for this long is announced; default 48).
type scrutiny struct{}

func init() { Register(scrutiny{}) }

const (
	scrutinyTempWarn   = 55
	scrutinyTempRearm  = 5 // a hot disk is announced again only after cooling this far below the limit
	scrutinyStaleHours = 48
)

func (scrutiny) Type() string                 { return "scrutiny" }
func (scrutiny) Capabilities() Capabilities   { return Capabilities{Events: true} }
func (scrutiny) SSORedirects(string) []string { return nil }

func (scrutiny) Provision(context.Context, Target, Person) (Account, error) {
	return Account{}, errors.New("Scrutiny has no accounts")
}

func (scrutiny) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Scrutiny has no sign-in")
	}
	return nil
}

type scrutinyDisk struct {
	Device struct {
		WWN      string `json:"wwn"`
		Name     string `json:"device_name"`
		Model    string `json:"model_name"`
		Serial   string `json:"serial_number"`
		Label    string `json:"label"`
		Host     string `json:"host_id"`
		Capacity int64  `json:"capacity"`
		Protocol string `json:"device_protocol"`
		Status   int    `json:"device_status"`
		Archived bool   `json:"archived"`
	} `json:"device"`
	Smart struct {
		Collected  time.Time `json:"collector_date"`
		Temp       int       `json:"temp"`
		PowerOnHrs int       `json:"power_on_hours"`
	} `json:"smart"`
}

func (d scrutinyDisk) name() string {
	n := d.Device.Name
	if d.Device.Label != "" {
		n = d.Device.Label
	}
	if d.Device.Host != "" {
		n = d.Device.Host + " " + n
	}
	return n
}

func (scrutiny) summary(ctx context.Context, t Target) (map[string]scrutinyDisk, error) {
	var res struct {
		Success bool `json:"success"`
		Data    struct {
			Summary map[string]scrutinyDisk `json:"summary"`
		} `json:"data"`
	}
	if _, err := doJSON(ctx, t, "Scrutiny", http.MethodGet, "/api/summary", nil, nil, &res); err != nil {
		return nil, err
	}
	out := map[string]scrutinyDisk{}
	for wwn, d := range res.Data.Summary {
		if d.Device.Archived {
			continue
		}
		if d.Device.WWN == "" {
			d.Device.WWN = wwn
		}
		out[wwn] = d
	}
	return out, nil
}

func fieldInt(t Target, key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(t.Fields[key])); err == nil && n > 0 {
		return n
	}
	return def
}

func (c scrutiny) Test(ctx context.Context, t Target) (Info, error) {
	var info Info
	if _, err := doJSON(ctx, t, "Scrutiny", http.MethodGet, "/api/health", nil, nil, nil); err != nil {
		return info, fmt.Errorf("cannot reach Scrutiny at %s: %w", t.BaseURL, err)
	}
	disks, err := c.summary(ctx, t)
	if err != nil {
		return info, err
	}
	info.Admin = true
	failing, hosts := 0, map[string]bool{}
	for _, d := range disks {
		if d.Device.Status != 0 {
			failing++
		}
		hosts[d.Device.Host] = true
	}
	info.Detail = fmt.Sprintf("%d disks on %d hosts, %d failing now; hot over %d °C; no accounts or sign-in here", len(disks), len(hosts), failing, fieldInt(t, "TEMP_WARN", scrutinyTempWarn))
	return info, nil
}

type scrutinyState struct {
	Init   bool              `json:"init"`
	Status map[string]int    `json:"status"`
	Hot    map[string]bool   `json:"hot,omitempty"`
	Silent map[string]int64  `json:"silent,omitempty"` // host → newest collection when announced silent
	Names  map[string]string `json:"names,omitempty"`  // wwn → "host sda (model)", to name a disk that left
}

func scrutinyStatus(s int) string {
	switch {
	case s&1 != 0 && s&2 != 0:
		return "failed SMART and Scrutiny's checks"
	case s&1 != 0:
		return "failed its SMART self-assessment"
	case s&2 != 0:
		return "is failing by Scrutiny's checks"
	}
	return "passes"
}

func (c scrutiny) Poll(ctx context.Context, t Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	disks, err := c.summary(ctx, t)
	if err != nil {
		return nil, state, err
	}
	var prev scrutinyState
	_ = json.Unmarshal(state, &prev)
	cur := scrutinyState{Init: true, Status: map[string]int{}, Hot: map[string]bool{}, Silent: map[string]int64{}, Names: map[string]string{}}
	warn := fieldInt(t, "TEMP_WARN", scrutinyTempWarn)
	stale := time.Duration(fieldInt(t, "STALE_HOURS", scrutinyStaleHours)) * time.Hour
	now := t.Clock()
	pub := strings.TrimRight(t.PublicURL, "/")
	ids := make([]string, 0, len(disks))
	for id := range disks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Event
	newest := map[string]time.Time{} // host → newest collection
	for _, id := range ids {
		d := disks[id]
		cur.Status[id] = d.Device.Status
		cur.Names[id] = fmt.Sprintf("%s (%s)", d.name(), d.Device.Model)
		if d.Smart.Collected.After(newest[d.Device.Host]) {
			newest[d.Device.Host] = d.Smart.Collected
		}
		hot := d.Smart.Temp >= warn || (prev.Hot[id] && d.Smart.Temp > warn-scrutinyTempRearm)
		if hot {
			cur.Hot[id] = true
		}
		if !prev.Init {
			continue // baseline
		}
		p := map[string]any{"disk": d.name(), "device": d.Device.Name, "host": d.Device.Host, "model": d.Device.Model, "serial": d.Device.Serial, "wwn": id, "temperature": d.Smart.Temp}
		if d.Device.Capacity > 0 {
			p["capacity"] = d.Device.Capacity
		}
		if d.Smart.PowerOnHrs > 0 {
			p["power_on_hours"] = d.Smart.PowerOnHrs
		}
		if pub != "" {
			p["url"] = pub + "/web/device/" + id
		}
		was, known := prev.Status[id]
		switch {
		case !known:
			q := clone(p)
			q["title"] = fmt.Sprintf("New disk on %s: %s %s", d.Device.Host, d.Device.Model, d.Device.Name)
			q["status"] = scrutinyStatus(d.Device.Status)
			out = append(out, Event{Name: "disk_added", Subject: "disk:" + id, Payload: q})
			if d.Device.Status == 0 {
				break
			}
			fallthrough
		case d.Device.Status != 0 && d.Device.Status != was:
			q := clone(p)
			q["title"] = fmt.Sprintf("%s (%s) %s", d.name(), d.Device.Model, scrutinyStatus(d.Device.Status))
			q["smart_failed"], q["scrutiny_failed"] = d.Device.Status&1 != 0, d.Device.Status&2 != 0
			out = append(out, Event{Name: "disk_failing", Subject: "disk:" + id, Payload: q})
		case d.Device.Status == 0 && was != 0:
			q := clone(p)
			q["title"] = fmt.Sprintf("%s (%s) passes again", d.name(), d.Device.Model)
			out = append(out, Event{Name: "disk_passing", Subject: "disk:" + id, Payload: q})
		}
		if hot && !prev.Hot[id] {
			q := clone(p)
			q["title"] = fmt.Sprintf("%s is at %d °C", d.name(), d.Smart.Temp)
			q["limit"] = warn
			out = append(out, Event{Name: "disk_hot", Subject: "disk:" + id, Payload: q})
		}
	}
	for id := range prev.Status {
		if _, ok := disks[id]; !ok && prev.Init {
			n := prev.Names[id]
			if n == "" {
				n = "A disk"
			}
			p := map[string]any{"wwn": id, "disk": n, "title": n + " left Scrutiny (removed or archived)"}
			out = append(out, Event{Name: "disk_removed", Subject: "disk:" + id, Payload: p})
		}
	}
	hosts := make([]string, 0, len(newest))
	for h := range newest {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		last := newest[h]
		silent := !last.IsZero() && now.Sub(last) > stale
		if silent {
			cur.Silent[h] = last.Unix()
		}
		_, was := prev.Silent[h]
		if !prev.Init || silent == was {
			continue
		}
		p := map[string]any{"host": h, "last_collected": last.UTC().Format(time.RFC3339)}
		if pub != "" {
			p["url"] = pub + "/web/dashboard"
		}
		if silent {
			p["title"] = fmt.Sprintf("Scrutiny has had no disk data from %s for %d hours", h, int(now.Sub(last).Hours()))
			out = append(out, Event{Name: "host_silent", Subject: "host:" + h, Payload: p})
		} else {
			p["title"] = fmt.Sprintf("Scrutiny gets disk data from %s again", h)
			out = append(out, Event{Name: "host_reporting", Subject: "host:" + h, Payload: p})
		}
	}
	next, _ := json.Marshal(cur)
	return out, next, nil
}
