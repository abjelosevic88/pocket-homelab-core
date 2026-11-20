package server

// What apps need in memory, for the setup's meter ("your picks need about 4.5 GB, this machine has
// 3.7 GB"). Measured on a Raspberry Pi 5 with 80 apps and on a home server (2026-11): a Pocket app
// idles at about 14 MB and every container costs about 9 MB of Docker's own (its shim and port
// proxy). What weighs is the services some apps bring along; the catalog names them
// (requires.services: "name (image)"), and the sizes below are theirs at rest. Rough on purpose:
// the meter is there to stop someone putting 80 apps with Immich and ONLYOFFICE on a 4 GB machine,
// not to promise a number.

const (
	appMemoryMB     = 23  // the app (14) + its container's overhead (9)
	helperOverMB    = 9   // per helper container, on top of the helper's own
	unknownHelperMB = 100 // a helper not in the list
	systemMemoryMB  = 500 // the system, Docker, Pocket Core and Caddy before any app
)

// helperSize is one service an app brings, found by a piece of its image name.
type helperSize struct {
	Match string `json:"match"`
	Name  string `json:"name"`
	MB    int    `json:"mb"`
}

var helperSizes = []helperSize{
	{"immich-server", "Immich", 620},
	{"immich-machine-learning", "Immich's face and object recognition", 350},
	{"immich-app/postgres", "Immich's database", 150},
	{"onlyoffice/documentserver", "ONLYOFFICE", 550},
	{"libretranslate", "LibreTranslate", 1200},
	{"translate-nllb", "the NLLB translation model", 1400},
	{"searxng", "SearXNG", 350},
	{"sockpuppetbrowser", "a headless browser", 250},
	{"jellyfin", "Jellyfin", 200},
	{"bazarr", "Bazarr", 190},
	{"gotenberg", "Gotenberg", 150},
	{"drawio", "draw.io", 130},
	{"navidrome", "Navidrome", 100},
	{"postgres", "a database", 80},
	{"valkey", "a cache", 15},
	{"redis", "a cache", 15},
	{"guacd", "guacd", 15},
}

// machineSize is this machine for the meter: memory, swap and processors.
func machineSize() map[string]any {
	_, total := readMemory()
	_, swap := readSwap()
	return map[string]any{"memory_mb": total >> 20, "swap_mb": swap >> 20, "cores": numCPU()}
}

func memoryGuide() map[string]any {
	return map[string]any{"app_mb": appMemoryMB, "helper_over_mb": helperOverMB, "unknown_helper_mb": unknownHelperMB,
		"system_mb": systemMemoryMB, "helpers": helperSizes}
}
