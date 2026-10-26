// Package catalog describes every service a paired Pocket Homelab client
// understands: its env-key prefix and the credential fields it takes. The
// list mirrors the client's env template (pockethomelab.com/downloads/pocket-homelab.env).
package catalog

import (
	"sort"
	"strings"
)

// Field is one setting of a service.
type Field struct {
	Key    string `json:"key"`    // suffix after the prefix: API_KEY, USERNAME…
	Label  string `json:"label"`  // human label
	Secret bool   `json:"secret"` // encrypted at rest, masked in the UI
	Help   string `json:"help,omitempty"`
	// ConnectorOnly: box-wide, used by Core's connector alone; never in a
	// device profile or env file, never set per person.
	ConnectorOnly bool `json:"connector_only,omitempty"`
}

// Entry is a service type.
type Entry struct {
	Type   string  `json:"type"`   // catalog key: jellyfin
	Name   string  `json:"name"`   // display: Jellyfin
	Prefix string  `json:"prefix"` // env prefix: JELLYFIN → JELLYFIN_URL
	Group  string  `json:"group"`  // Media, Downloads, Pocket…
	URLKey string  `json:"url_key"`
	Pocket bool    `json:"pocket"` // a Pocket app: credential is the device token
	Fields []Field `json:"fields"`
}

// FullKey returns PREFIX_KEY.
func (e Entry) FullKey(suffix string) string { return e.Prefix + "_" + suffix }

// IsSecret reports whether a field suffix is a secret for this entry.
// IsConnectorOnly reports whether a field stays inside Core.
func (e Entry) IsConnectorOnly(suffix string) bool {
	for _, f := range e.Fields {
		if f.Key == suffix {
			return f.ConnectorOnly
		}
	}
	return false
}

func (e Entry) IsSecret(suffix string) bool {
	for _, f := range e.Fields {
		if f.Key == suffix {
			return f.Secret
		}
	}
	return looksSecret(suffix)
}

func looksSecret(k string) bool {
	k = strings.ToUpper(k)
	return strings.Contains(k, "KEY") || strings.Contains(k, "TOKEN") || strings.Contains(k, "PASSWORD") || strings.Contains(k, "SECRET")
}

func apiKey(help string) []Field {
	return []Field{{Key: "API_KEY", Label: "API key", Secret: true, Help: help}}
}
func token(help string) []Field {
	return []Field{{Key: "TOKEN", Label: "Token", Secret: true, Help: help}}
}
func userPass(ukey, pkey string) []Field {
	return []Field{{Key: ukey, Label: "Username"}, {Key: pkey, Label: "Password", Secret: true}}
}

var entries = []Entry{
	// ---- Pocket apps (credential = Core device token, filled automatically) ----
	{Type: "habits", Name: "Pocket Habits", Prefix: "HABITS", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "inventory", Name: "Pocket Inventory", Prefix: "INVENTORY", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "bookshelf", Name: "Pocket Bookshelf", Prefix: "BOOKSHELF", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "clips", Name: "Pocket Clips", Prefix: "CLIPS", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "travel", Name: "Pocket Travel", Prefix: "TRAVEL", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "invoicing", Name: "Pocket Invoicing", Prefix: "INVOICING", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "split", Name: "Pocket Split", Prefix: "SPLIT", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "kitchen", Name: "Pocket Kitchen", Prefix: "KITCHEN", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "day", Name: "Pocket Day", Prefix: "DAY", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "garage", Name: "Pocket Garage", Prefix: "GARAGE", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "subtitles", Name: "Pocket Subtitles", Prefix: "SUBSYNC", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "money", Name: "Pocket Money", Prefix: "MONEY", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "collaborator", Name: "Pocket Collaborator", Prefix: "COLLABORATOR", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "projects", Name: "Pocket Projects", Prefix: "PROJECTS", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "movement", Name: "Pocket Movement", Prefix: "MOVEMENT", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "wishlist", Name: "Pocket Wishlist", Prefix: "WISHLIST", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "health", Name: "Pocket Health", Prefix: "HEALTH", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "music", Name: "Pocket Music", Prefix: "MUSIC", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "watch", Name: "Pocket Watch", Prefix: "WATCH", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "audiobooks", Name: "Pocket Audiobooks", Prefix: "AUDIOBOOKS", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "backups", Name: "Pocket Backups", Prefix: "BACKUPS", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "library", Name: "Pocket Library", Prefix: "LIBRARY", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "still", Name: "Pocket Still", Prefix: "STILL", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "network", Name: "Pocket Network", Prefix: "NETWORK", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "mail", Name: "Pocket Mail", Prefix: "MAIL", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "database", Name: "Pocket Database", Prefix: "DATABASE", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "podcasts", Name: "Pocket Podcasts", Prefix: "PODCASTS", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "notes", Name: "Pocket Notes", Prefix: "NOTES", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "office", Name: "Pocket Office", Prefix: "OFFICE", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "files", Name: "Pocket Files", Prefix: "FILES", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "calendar", Name: "Pocket Calendar", Prefix: "CALENDAR", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "contacts", Name: "Pocket Contacts", Prefix: "CONTACTS", Group: "Pocket", Pocket: true, Fields: apiKey("")},
	{Type: "core", Name: "Pocket Core", Prefix: "CORE", Group: "Pocket", Pocket: true, Fields: apiKey("")},

	// ---- Server ----
	{Type: "glances", Name: "Glances", Prefix: "GLANCES", Group: "Server", Fields: []Field{{Key: "STORAGE_MOUNT", Label: "Extra mount point"}, {Key: "TEMPS_URL", Label: "Temperatures JSON URL"}}},
	{Type: "homepage", Name: "Dashboard link", Prefix: "HOMEPAGE", Group: "Server"},
	{Type: "truenas", Name: "TrueNAS", Prefix: "TRUENAS", Group: "Server", Fields: apiKey("Read-only key is enough")},
	{Type: "kuma", Name: "Uptime Kuma", Prefix: "KUMA", Group: "Server", Fields: []Field{{Key: "STATUS_SLUG", Label: "Status page slug"}, {Key: "API_KEY", Label: "API key", Secret: true, Help: "Settings → API Keys (lets Core read every monitor's status from /metrics)"}}},
	{Type: "komodo", Name: "Komodo", Prefix: "KOMODO", Group: "Server", Fields: []Field{{Key: "API_KEY", Label: "API key", Secret: true}, {Key: "API_SECRET", Label: "API secret", Secret: true}}},
	{Type: "backrest", Name: "Backrest", Prefix: "BACKREST", Group: "Server", Fields: append(userPass("USERNAME", "PASSWORD"), Field{Key: "GRACE_HOURS", Label: "Grace hours", Help: "hours past a plan's schedule before a missing backup is announced (empty: a quarter of the interval, at least 2)"})},
	{Type: "scrutiny", Name: "Scrutiny", Prefix: "SCRUTINY", Group: "Server", Fields: []Field{{Key: "TEMP_WARN", Label: "Hot disk (°C)", Help: "Core's connector announces a disk at or above this; default 55"}, {Key: "STALE_HOURS", Label: "Silent host (hours)", Help: "a host with no disk data for this long is announced; default 48"}}},
	{Type: "speedtest", Name: "Speedtest Tracker", Prefix: "SPEEDTEST", Group: "Server", Fields: token("Settings → API Tokens, with Read results (without one Core sees only the newest result)")},
	{Type: "changedetection", Name: "Changedetection.io", Prefix: "CHANGEDETECTION", Group: "Server", Fields: apiKey("Settings → API")},
	{Type: "beszel", Name: "Beszel", Prefix: "BESZEL", Group: "Server", Fields: []Field{{Key: "EMAIL", Label: "Email", Help: "a Beszel user's login; alert events come from that user's alerts"}, {Key: "PASSWORD", Label: "Password", Secret: true}, {Key: "GLANCES", Label: "Glances system name"}}},
	{Type: "adguard", Name: "AdGuard Home", Prefix: "ADGUARD", Group: "Server", Fields: userPass("USERNAME", "PASSWORD")},
	{Type: "ntfy", Name: "ntfy", Prefix: "NTFY", Group: "Server", Fields: []Field{{Key: "TOPICS", Label: "Topics (comma list)"}}},
	{Type: "ssh", Name: "Services & jobs (SSH, Mac)", Prefix: "SERVICES_SSH", Group: "Server", URLKey: "HOST", Fields: []Field{{Key: "PORT", Label: "Port"}, {Key: "IDENTITY_FILE", Label: "Identity file"}}},

	// ---- Downloads ----
	{Type: "qbittorrent", Name: "qBittorrent", Prefix: "QBIT", Group: "Downloads", Fields: userPass("USERNAME", "PASSWORD")},
	{Type: "sabnzbd", Name: "SABnzbd", Prefix: "SABNZBD", Group: "Downloads", Fields: apiKey("Config → General → API Key")},
	{Type: "nzbget", Name: "NZBget", Prefix: "NZBGET", Group: "Downloads", Fields: userPass("USERNAME", "PASSWORD")},
	{Type: "slskd", Name: "slskd", Prefix: "SLSKD", Group: "Downloads", Fields: apiKey("Options → Web → API keys")},
	{Type: "metube", Name: "MeTube", Prefix: "METUBE", Group: "Downloads", Fields: []Field{{Key: "FOLDERS", Label: "Folders (comma list)"}}},

	// ---- Media ----
	{Type: "jellyfin", Name: "Jellyfin", Prefix: "JELLYFIN", Group: "Media", Fields: []Field{{Key: "API_KEY", Label: "API key", Secret: true, Help: "Dashboard → API Keys"}, {Key: "USER_ID", Label: "User id (optional)"}}},
	{Type: "jellyseerr", Name: "Jellyseerr", Prefix: "JELLYSEERR", Group: "Media", Fields: apiKey("Settings → General")},
	{Type: "jellystat", Name: "Jellystat", Prefix: "JELLYSTAT", Group: "Media", Fields: apiKey("")},
	{Type: "radarr", Name: "Radarr", Prefix: "RADARR", Group: "Media", Fields: apiKey("Settings → General")},
	{Type: "sonarr", Name: "Sonarr", Prefix: "SONARR", Group: "Media", Fields: apiKey("Settings → General")},
	{Type: "maintainerr", Name: "Maintainerr", Prefix: "MAINTAINERR", Group: "Media"},
	{Type: "lidarr", Name: "Lidarr", Prefix: "LIDARR", Group: "Media", Fields: apiKey("Settings → General")},
	{Type: "chaptarr", Name: "Chaptarr / Readarr", Prefix: "CHAPTARR", Group: "Media", Fields: apiKey("Settings → General")},
	{Type: "prowlarr", Name: "Prowlarr", Prefix: "PROWLARR", Group: "Media", Fields: apiKey("Settings → General")},
	{Type: "bazarr", Name: "Bazarr", Prefix: "BAZARR", Group: "Media", Fields: apiKey("Settings → General")},
	{Type: "audiobookshelf", Name: "Audiobookshelf", Prefix: "ABS", Group: "Media", Fields: []Field{{Key: "TOKEN", Label: "Token", Secret: true, Help: "Settings → API Keys (an administrator's key; root's to manage the root user)"}, {Key: "USER_ID", Label: "User id (optional)"}}},
	{Type: "navidrome", Name: "Navidrome", Prefix: "NAVIDROME", Group: "Media", Fields: userPass("USER", "PASSWORD")},
	{Type: "immich", Name: "Immich", Prefix: "IMMICH", Group: "Media", Fields: apiKey("Account → API keys")},
	{Type: "calibre", Name: "Calibre-Web", Prefix: "CALIBRE", Group: "Media", Fields: userPass("USER", "PASSWORD")},
	{Type: "pinepods", Name: "PinePods", Prefix: "PINEPODS", Group: "Media", Fields: apiKey("")},

	// ---- Files, notes, code ----
	{Type: "paperless", Name: "Paperless-ngx", Prefix: "PAPERLESS", Group: "Files", Fields: token("A superuser's token: My Profile → API Auth Token")},
	{Type: "nextcloud", Name: "Nextcloud", Prefix: "NEXTCLOUD", Group: "Files", Fields: []Field{{Key: "USERNAME", Label: "Username"}, {Key: "APP_PASSWORD", Label: "App password", Secret: true},
		{Key: "ADMIN_PASSWORD", Label: "Login password (connector only)", Secret: true, ConnectorOnly: true, Help: "Only for the connector: Nextcloud wants the admin's real password, not an app password, to add accounts and change sign-in. Stays in Core, never sent to apps or devices."}}},
	{Type: "forgejo", Name: "Forgejo / Gitea", Prefix: "FORGEJO", Group: "Files", Fields: token("Settings → Applications (a site administrator's token: read:admin, write:admin, read:user)")},
	{Type: "searxng", Name: "SearXNG", Prefix: "SEARXNG", Group: "Files"},

	// ---- Money & life ----
	{Type: "firefly", Name: "Firefly III", Prefix: "FIREFLY", Group: "Money", Fields: []Field{{Key: "TOKEN", Label: "Personal access token", Secret: true}}},
	{Type: "pico", Name: "Firefly Pico", Prefix: "PICO", Group: "Money"},
	{Type: "subscriptions", Name: "Subscriptions page", Prefix: "SUBSCRIPTIONS", Group: "Money"},
	{Type: "lubelogger", Name: "LubeLogger", Prefix: "LUBELOGGER", Group: "Life", Fields: append(userPass("USERNAME", "PASSWORD"), Field{Key: "CURRENCY", Label: "Currency"}, Field{Key: "DISTANCE_UNIT", Label: "Distance unit"}, Field{Key: "VOLUME_UNIT", Label: "Volume unit"}, Field{Key: "VEHICLE_ID", Label: "Vehicle id"}, Field{Key: "DECIMAL", Label: "Decimal separator"})},
	{Type: "dawarich", Name: "Dawarich", Prefix: "DAWARICH", Group: "Life", Fields: apiKey("Account → API key")},
	{Type: "kavita", Name: "Kavita", Prefix: "KAVITA", Group: "Media", Fields: []Field{{Key: "API_KEY", Label: "API key", Secret: true, Help: "an admin's auth key: User Settings → Auth Keys (signed in as a Kavita admin)"}, {Key: "USER_ID", Label: "User id (optional)"}}},
	{Type: "maloja", Name: "Maloja", Prefix: "MALOJA", Group: "Media", Fields: apiKey("Settings → API keys (for scrobbling devices; Core itself only reads)")},
}

var byType = map[string]Entry{}

func init() {
	for i := range entries {
		if entries[i].URLKey == "" {
			entries[i].URLKey = "URL"
		}
		if entries[i].Fields == nil {
			entries[i].Fields = []Field{}
		}
		byType[entries[i].Type] = entries[i]
	}
}

// All returns the catalog sorted by group then name.
func All() []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return groupOrder(out[i].Group) < groupOrder(out[j].Group)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func groupOrder(g string) int {
	switch g {
	case "Pocket":
		return 0
	case "Server":
		return 1
	case "Downloads":
		return 2
	case "Media":
		return 3
	case "Files":
		return 4
	case "Money":
		return 5
	case "Life":
		return 6
	}
	return 9
}

// Get looks up a type; ok is false for unknown types.
func Get(typ string) (Entry, bool) {
	e, ok := byType[strings.ToLower(strings.TrimSpace(typ))]
	return e, ok
}

// Custom builds an entry for a type that is not in the catalog (a new Pocket
// app that registered with its own env_prefix, or an admin-added service).
func Custom(typ, name, prefix string, pocket bool) Entry {
	if prefix == "" {
		prefix = strings.ToUpper(strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(typ))
	}
	if name == "" {
		name = typ
	}
	e := Entry{Type: typ, Name: name, Prefix: prefix, Group: "Custom", URLKey: "URL", Pocket: pocket, Fields: []Field{{Key: "API_KEY", Label: "API key", Secret: true}}}
	if pocket {
		e.Group = "Pocket"
	}
	return e
}

// Resolve returns the catalog entry or a custom one.
func Resolve(typ, name, prefix string, pocket bool) Entry {
	if e, ok := Get(typ); ok {
		if prefix != "" && prefix != e.Prefix {
			e.Prefix = prefix
		}
		return e
	}
	return Custom(typ, name, prefix, pocket)
}
