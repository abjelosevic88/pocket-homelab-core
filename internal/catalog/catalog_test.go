package catalog

import "testing"

func TestCatalogKeysMatchClientTemplate(t *testing.T) {
	// A few keys the Pocket Homelab client's env template expects verbatim.
	cases := map[string][2]string{
		"jellyfin":   {"JELLYFIN_URL", "JELLYFIN_API_KEY"},
		"habits":     {"HABITS_URL", "HABITS_API_KEY"},
		"nextcloud":  {"NEXTCLOUD_URL", "NEXTCLOUD_APP_PASSWORD"},
		"ssh":        {"SERVICES_SSH_HOST", "SERVICES_SSH_PORT"},
		"navidrome":  {"NAVIDROME_URL", "NAVIDROME_PASSWORD"},
		"docker":     {"DOCKER_URL", "DOCKER_TOKEN"},
		"dozzle":     {"DOZZLE_URL", "DOZZLE_PASSWORD"},
		"gotify":     {"GOTIFY_URL", "GOTIFY_TOKEN"},
		"duplicati":  {"DUPLICATI_URL", "DUPLICATI_PASSWORD"},
		"crowdsec":   {"CROWDSEC_URL", "CROWDSEC_MACHINE_ID"},
		"cloudflare": {"CLOUDFLARE_URL", "CLOUDFLARE_API_TOKEN"},
		"autobrr":    {"AUTOBRR_URL", "AUTOBRR_API_KEY"},
		"cleanuparr": {"CLEANUPARR_URL", "CLEANUPARR_API_KEY"},
		"profilarr":  {"PROFILARR_URL", "PROFILARR_API_KEY"},
		"wizarr":     {"WIZARR_URL", "WIZARR_API_KEY"},
	}
	for _, typ := range []string{"docker", "crowdsec", "cloudflare"} {
		if e, _ := Get(typ); e.Audience != "admins" {
			t.Errorf("%s should default to admins only", typ)
		}
	}
	for typ, want := range cases {
		e, ok := Get(typ)
		if !ok {
			t.Fatalf("missing %s", typ)
		}
		if got := e.FullKey(e.URLKey); got != want[0] {
			t.Errorf("%s url key = %s, want %s", typ, got, want[0])
		}
		found := false
		for _, f := range e.Fields {
			if e.FullKey(f.Key) == want[1] {
				found = true
			}
		}
		if !found {
			t.Errorf("%s lacks %s", typ, want[1])
		}
	}
	if !All()[0].Pocket {
		t.Errorf("Pocket group should sort first")
	}
	if c := Custom("my-app", "", "", true); c.Prefix != "MY_APP" || !c.IsSecret("API_KEY") {
		t.Errorf("custom entry: %+v", c)
	}
}

// Every slot on Devices → Apps on your devices appears once.
func TestChoicesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Choices {
		if seen[c.Slot] {
			t.Fatalf("slot %q listed twice", c.Slot)
		}
		seen[c.Slot] = true
	}
}
