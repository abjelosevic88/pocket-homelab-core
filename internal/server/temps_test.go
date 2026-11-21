package server

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSys(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, v := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The owner's main box: two NVMe drives, the chipset, USB-C, Wi-Fi and an Intel CPU.
func TestReadTemperaturesPC(t *testing.T) {
	root := t.TempDir()
	writeSys(t, root, map[string]string{
		"class/hwmon/hwmon0/name":          "nvme",
		"class/hwmon/hwmon0/device/model":  "KBG50ZNV512G KIOXIA   ",
		"class/hwmon/hwmon0/temp1_input":   "33850",
		"class/hwmon/hwmon0/temp1_label":   "Composite",
		"class/hwmon/hwmon0/temp1_max":     "78850",
		"class/hwmon/hwmon0/temp1_crit":    "83850",
		"class/hwmon/hwmon0/temp2_input":   "33850",
		"class/hwmon/hwmon0/temp2_label":   "Sensor 1",
		"class/hwmon/hwmon0/temp2_max":     "65261850",
		"class/hwmon/hwmon1/name":          "nvme",
		"class/hwmon/hwmon1/temp1_input":   "37850",
		"class/hwmon/hwmon1/temp1_label":   "Composite",
		"class/hwmon/hwmon2/name":          "ucsi_source_psy_USBC000:001",
		"class/hwmon/hwmon2/temp1_input":   "20000",
		"class/hwmon/hwmon3/name":          "pch_cometlake",
		"class/hwmon/hwmon3/temp1_input":   "35000",
		"class/hwmon/hwmon5/name":          "iwlwifi_1",
		"class/hwmon/hwmon5/temp1_input":   "33000",
		"class/hwmon/hwmon6/name":          "coretemp",
		"class/hwmon/hwmon6/temp1_input":   "49000",
		"class/hwmon/hwmon6/temp1_label":   "Package id 0",
		"class/hwmon/hwmon6/temp1_max":     "80000",
		"class/hwmon/hwmon6/temp1_crit":    "100000",
		"class/hwmon/hwmon6/temp2_input":   "51000",
		"class/hwmon/hwmon6/temp2_label":   "Core 0",
		"class/thermal/thermal_zone0/type": "x86_pkg_temp",
		"class/thermal/thermal_zone0/temp": "49000",
	})
	got := readTemperatures(root)
	byKey := map[string]Temperature{}
	for _, r := range got {
		byKey[r.Name+"|"+r.Detail] = r
	}
	if len(got) != 6 {
		t.Fatalf("want 6 readings (2 drives, chipset, wifi, CPU package + core), got %d: %+v", len(got), got)
	}
	cpu := byKey["CPU|"]
	if !cpu.Main || cpu.Celsius != 49 || cpu.High != 80 || cpu.Critical != 100 || cpu.Group != "cpu" {
		t.Errorf("CPU package: %+v", cpu)
	}
	if c := byKey["CPU|Core 0"]; c.Main || c.Celsius != 51 {
		t.Errorf("core 0: %+v", c)
	}
	d := byKey["KBG50ZNV512G KIOXIA|"]
	if !d.Main || d.Group != "drive" || d.Celsius != 33.9 || d.High != 78.9 || d.Critical != 83.9 {
		t.Errorf("drive: %+v", d)
	}
	if d2, ok := byKey["Drive 2|"]; !ok || d2.Celsius != 37.9 {
		t.Errorf("second drive without a model: %+v", got)
	}
	if b := byKey["Chipset|"]; b.Group != "board" || !b.Main {
		t.Errorf("chipset: %+v", b)
	}
	if w := byKey["Wi-Fi|"]; w.Celsius != 33 {
		t.Errorf("wifi: %+v", w)
	}
}

// A Raspberry Pi: only a thermal zone, with trip points.
func TestReadTemperaturesPi(t *testing.T) {
	root := t.TempDir()
	writeSys(t, root, map[string]string{
		"class/thermal/thermal_zone0/type":              "cpu-thermal",
		"class/thermal/thermal_zone0/temp":              "61335",
		"class/thermal/thermal_zone0/trip_point_0_type": "critical",
		"class/thermal/thermal_zone0/trip_point_0_temp": "110000",
		"class/thermal/thermal_zone0/trip_point_1_type": "passive",
		"class/thermal/thermal_zone0/trip_point_1_temp": "80000",
	})
	got := readTemperatures(root)
	if len(got) != 1 || got[0].Group != "cpu" || got[0].Celsius != 61.3 || got[0].High != 80 || got[0].Critical != 110 || !got[0].Main {
		t.Fatalf("pi: %+v", got)
	}
}

func TestReadTemperaturesNothing(t *testing.T) {
	if got := readTemperatures(t.TempDir()); len(got) != 0 {
		t.Fatalf("want none, got %+v", got)
	}
}
