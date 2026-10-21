package config

import (
	"strings"
	"testing"
)

func TestMCPConfigRequiresExplicitAccess(t *testing.T) {
	for _, tc := range []struct {
		name, hash, apps string
		valid            bool
	}{
		{"disabled", "", "", true},
		{"invalid hash", "secret", "notes", false},
		{"missing allowlist", strings.Repeat("a", 64), "", false},
		{"selected apps", strings.Repeat("a", 64), "notes, tasks", true},
		{"explicit all", strings.Repeat("a", 64), "*", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_TOKEN_HASH", tc.hash)
			t.Setenv("MCP_APPS", tc.apps)
			_, err := Load()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestMCPOAuthConfiguration(t *testing.T) {
	for _, tc := range []struct {
		origin, mode string
		valid        bool
	}{
		{"https://core.example.com", "local", true},
		{"http://127.0.0.1:8080", "local", true},
		{"http://core.example.com", "local", false},
		{"https://core.example.com/path", "local", false},
		{"https://user:pass@core.example.com", "local", false},
		{"https://core.example.com", "none", false},
		{"https://core.example.com", "proxy", false},
	} {
		t.Run(tc.origin+tc.mode, func(t *testing.T) {
			t.Setenv("MCP_OAUTH_ENABLED", "true")
			t.Setenv("BASE_URL", tc.origin)
			t.Setenv("AUTH_MODE", tc.mode)
			_, err := Load()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
