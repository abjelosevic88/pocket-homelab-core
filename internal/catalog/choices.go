package catalog

// Choice is a place in Pocket Homelab that one of two apps fills: Money is
// Pocket Money or Firefly III, Music is Pocket Music or Navidrome, Watch is
// Pocket Watch or Jellyfin, Requests is Pocket Requests or Jellyseerr. When a
// person's profile carries both, the choice they made in Core goes to their
// devices as EnvKey (MONEY_BACKEND=firefly); without one the client takes the
// Pocket app.
type Choice struct {
	Slot    string         `json:"slot"`
	Title   string         `json:"title"`
	EnvKey  string         `json:"env_key"`
	Options []ChoiceOption `json:"options"`
}

// ChoiceOption is one app for a slot: the value the client understands and
// the service type that supplies it.
type ChoiceOption struct {
	Value string `json:"value"`
	Name  string `json:"name"`
	Type  string `json:"type"`
}

// Choices in the order Core shows them. The Pocket app is always first.
var Choices = []Choice{
	{Slot: "audiobooks", Title: "Audiobooks", EnvKey: "AUDIOBOOKS_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Audiobooks", "audiobooks"}, {"audiobookshelf", "Audiobookshelf", "audiobookshelf"}}},
	{Slot: "money", Title: "Money", EnvKey: "MONEY_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Money", "money"}, {"firefly", "Firefly III", "firefly"}}},
	{Slot: "watch", Title: "Watch", EnvKey: "WATCH_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Watch", "watch"}, {"jellyfin", "Jellyfin", "jellyfin"}}},
	{Slot: "music", Title: "Music", EnvKey: "MUSIC_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Music", "music"}, {"navidrome", "Navidrome", "navidrome"}}},
	{Slot: "podcasts", Title: "Podcasts", EnvKey: "PODCASTS_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Podcasts", "podcasts"}, {"pinepods", "PinePods", "pinepods"}}},
	{Slot: "documents", Title: "Documents", EnvKey: "DOCUMENTS_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Documents", "documents"}, {"paperless", "Paperless-ngx", "paperless"}}},
	{Slot: "notes", Title: "Notes", EnvKey: "NOTES_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Notes", "notes"}, {"nextcloud", "Nextcloud Notes", "nextcloud"}}},
	{Slot: "garage", Title: "Garage", EnvKey: "GARAGE_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Garage", "garage"}, {"lubelogger", "LubeLogger", "lubelogger"}}},
	{Slot: "requests", Title: "Requests", EnvKey: "REQUESTS_BACKEND", Options: []ChoiceOption{{"pocket", "Pocket Requests", "requests"}, {"jellyseerr", "Jellyseerr", "jellyseerr"}}},
}

// ChoiceFor returns a slot's choice.
func ChoiceFor(slot string) (Choice, bool) {
	for _, c := range Choices {
		if c.Slot == slot {
			return c, true
		}
	}
	return Choice{}, false
}

// Option returns the option with a value.
func (c Choice) Option(value string) (ChoiceOption, bool) {
	for _, o := range c.Options {
		if o.Value == value {
			return o, true
		}
	}
	return ChoiceOption{}, false
}

// Resolve is the app a profile with these service types uses: the stored
// value when its app is there, else the first option that is (the Pocket
// app). explicit says the stored value decided.
func (c Choice) Resolve(stored string, types map[string]bool) (value string, explicit bool) {
	if o, ok := c.Option(stored); ok && types[o.Type] {
		return o.Value, true
	}
	for _, o := range c.Options {
		if types[o.Type] {
			return o.Value, false
		}
	}
	return "", false
}
