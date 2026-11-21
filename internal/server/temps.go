package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Temperature is one sensor reading for GET /system's host.temperatures. Core in a container still
// sees the host's /sys (read-only), so these are the machine's own sensors.
type Temperature struct {
	Group    string  `json:"group"`              // cpu | drive | gpu | board
	Name     string  `json:"name"`               // "CPU", the drive's model, "Chipset"…
	Detail   string  `json:"detail,omitempty"`   // "Core 3", "Package id 0"; empty for the main reading
	Main     bool    `json:"main"`               // the one reading to show for this part
	Celsius  float64 `json:"celsius"`            //
	High     float64 `json:"high,omitempty"`     // the sensor's own "too warm" mark, when it has one
	Critical float64 `json:"critical,omitempty"` // and its shut-down mark
}

var hwmonIndex = regexp.MustCompile(`(\d+)$`)

// readTemperatures reads hwmon sensors under root (normally /sys) and falls back to thermal zones
// for anything hwmon doesn't cover (a Raspberry Pi's cpu-thermal, x86_pkg_temp). Readings outside
// -20…150 °C are dropped as broken sensors.
func readTemperatures(root string) []Temperature {
	var out []Temperature
	haveCPU := false

	mons, _ := filepath.Glob(filepath.Join(root, "class", "hwmon", "hwmon*"))
	sort.Slice(mons, func(i, j int) bool { return monNum(mons[i]) < monNum(mons[j]) })
	drives := 0
	for _, h := range mons {
		name := strings.TrimSpace(readStr(filepath.Join(h, "name")))
		inputs, _ := filepath.Glob(filepath.Join(h, "temp*_input"))
		sort.Strings(inputs)
		if len(inputs) == 0 {
			continue
		}
		group, part := sensorGroup(name)
		if group == "" {
			continue
		}
		if group == "drive" {
			drives++
			part = driveName(h, drives)
		}
		var readings []Temperature
		for _, in := range inputs {
			c, ok := milli(readStr(in))
			if !ok {
				continue
			}
			base := strings.TrimSuffix(in, "_input")
			label := strings.TrimSpace(readStr(base + "_label"))
			t := Temperature{Group: group, Name: part, Detail: label, Celsius: c}
			if v, ok := milli(readStr(base + "_max")); ok && v > 0 && v < 150 {
				t.High = v
			}
			if v, ok := milli(readStr(base + "_crit")); ok && v > 0 && v < 150 {
				t.Critical = v
			}
			// NVMe "Sensor N" readings repeat the composite one with odd limits: the composite is the drive.
			if group == "drive" && strings.HasPrefix(label, "Sensor") {
				continue
			}
			readings = append(readings, t)
		}
		if len(readings) == 0 {
			continue
		}
		pickMain(readings)
		if group == "cpu" {
			haveCPU = true
		}
		out = append(out, readings...)
	}

	zones, _ := filepath.Glob(filepath.Join(root, "class", "thermal", "thermal_zone*"))
	sort.Strings(zones)
	for _, z := range zones {
		typ := strings.ToLower(strings.TrimSpace(readStr(filepath.Join(z, "type"))))
		isCPU := strings.Contains(typ, "cpu") || typ == "x86_pkg_temp" || strings.Contains(typ, "soc")
		if !isCPU || haveCPU {
			continue // boards and Wi-Fi come from hwmon already; a CPU only when hwmon had none
		}
		c, ok := milli(readStr(filepath.Join(z, "temp")))
		if !ok {
			continue
		}
		t := Temperature{Group: "cpu", Name: "CPU", Main: true, Celsius: c}
		// Trip points: "passive" is where it starts slowing down, "critical" where it shuts off.
		trips, _ := filepath.Glob(filepath.Join(z, "trip_point_*_type"))
		for _, tp := range trips {
			kind := strings.TrimSpace(readStr(tp))
			v, ok := milli(readStr(strings.TrimSuffix(tp, "_type") + "_temp"))
			if !ok || v <= 0 || v >= 150 {
				continue
			}
			switch kind {
			case "passive", "hot":
				if t.High == 0 || v < t.High {
					t.High = v
				}
			case "critical":
				t.Critical = v
			}
		}
		out = append(out, t)
		haveCPU = true
	}
	return out
}

// sensorGroup: what a hwmon chip measures, and its name for people. Empty group = skip.
func sensorGroup(name string) (group, part string) {
	n := strings.ToLower(name)
	switch {
	case n == "coretemp" || n == "k10temp" || n == "zenpower" || strings.Contains(n, "cpu_thermal") || strings.Contains(n, "cpu-thermal") || n == "cpu":
		return "cpu", "CPU"
	case n == "nvme" || n == "drivetemp":
		return "drive", "Drive"
	case n == "amdgpu" || n == "nouveau" || n == "radeon" || strings.Contains(n, "gpu"):
		return "gpu", "Graphics"
	case strings.HasPrefix(n, "pch_"):
		return "board", "Chipset"
	case n == "acpitz":
		return "board", "Motherboard"
	case strings.HasPrefix(n, "iwlwifi") || strings.Contains(n, "wifi") || strings.HasPrefix(n, "mt7") || strings.HasPrefix(n, "ath"):
		return "board", "Wi-Fi"
	case strings.HasPrefix(n, "ucsi") || strings.Contains(n, "bat") || strings.Contains(n, "psy"):
		return "", "" // USB-C ports and batteries report nothing useful here
	}
	return "board", name
}

// pickMain marks the reading that stands for the part: a CPU's package (or Tctl/Tdie), else the
// first unlabelled or composite one, else the hottest.
func pickMain(rs []Temperature) {
	best := -1
	for i, r := range rs {
		l := strings.ToLower(r.Detail)
		if strings.HasPrefix(l, "package") || l == "tctl" || l == "tdie" || l == "composite" || l == "" {
			best = i
			break
		}
	}
	if best < 0 {
		best = 0
		for i, r := range rs {
			if r.Celsius > rs[best].Celsius {
				best = i
			}
		}
	}
	rs[best].Main = true
	if l := strings.ToLower(rs[best].Detail); l == "composite" || l == "" || strings.HasPrefix(l, "package") || l == "tctl" || l == "tdie" {
		rs[best].Detail = ""
	}
}

// driveName: the drive's model ("KIOXIA KBG50ZNV512G"), else "Drive 2".
func driveName(mon string, n int) string {
	m := strings.Join(strings.Fields(readStr(filepath.Join(mon, "device", "model"))), " ")
	if m != "" {
		return m
	}
	return "Drive " + strconv.Itoa(n)
}

func monNum(p string) int {
	m := hwmonIndex.FindString(p)
	n, _ := strconv.Atoi(m)
	return n
}

func readStr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// milli: sysfs gives millidegrees ("49000" = 49 °C).
func milli(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	c := v / 1000
	if c < -20 || c > 150 {
		return 0, false
	}
	return float64(int(c*10+0.5)) / 10, true
}
