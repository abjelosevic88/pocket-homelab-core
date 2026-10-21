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
