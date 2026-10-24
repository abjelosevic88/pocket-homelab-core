package server

import "testing"

func TestProfileAudiobooksUsesExistingClientKeys(t *testing.T) {
	for _, tc := range []struct {
		name, choice, token, wantURL, wantToken string
		visible                                 bool
	}{
		{"automatic", "", "pcd_device", "https://audio.example.com", "pcd_device", true},
		{"explicit pocket", "pocket", "pcd_device", "https://audio.example.com", "pcd_device", true},
		{"explicit direct", "audiobookshelf", "pcd_device", "https://abs.example.com", "abs-key", true},
		{"preview", "", "", "https://abs.example.com", "abs-key", true},
		{"not visible", "pocket", "pcd_device", "https://abs.example.com", "abs-key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Profile{Services: []ProfileService{{Type: "core", Env: map[string]string{}}, {Type: "audiobookshelf", URL: "https://abs.example.com", Env: map[string]string{"ABS_URL": "https://abs.example.com", "ABS_TOKEN": "abs-key"}}}}
			types := map[string]bool{"audiobookshelf": true}
			if tc.visible {
				types["audiobooks"] = true
				p.Services = append(p.Services, ProfileService{Type: "audiobooks", URL: "https://audio.example.com", Env: map[string]string{"AUDIOBOOKS_API_KEY": tc.token}})
			}
			profileAudiobooks(p, tc.choice, types)
			values := map[string]string{}
			for _, service := range p.Services {
				for key, value := range service.Env {
					if _, exists := values[key]; exists {
						t.Fatalf("duplicate key %s", key)
					}
					values[key] = value
				}
			}
			if values["ABS_URL"] != tc.wantURL || values["ABS_TOKEN"] != tc.wantToken {
				t.Fatalf("wrong selection: %+v", values)
			}
			if tc.wantToken == "pcd_device" {
				for _, service := range p.Services {
					if service.Type == "audiobookshelf" {
						t.Fatal("direct ABS credentials still exported")
					}
				}
			}
		})
	}
}
