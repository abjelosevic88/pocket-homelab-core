package config

import (
	"strings"
	"testing"
)

func TestWarnings(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		expect []string // substrings, one per warning, in order
	}{
		{"https and secure", Config{AuthMode: "local", BaseURL: "https://core.home.lan", SecureCookies: true}, nil},
		{"lan http without secure", Config{AuthMode: "local", BaseURL: "http://192.168.1.10:8179"}, nil},
		{"secure cookies on lan http", Config{AuthMode: "local", BaseURL: "http://192.168.1.10:8179", SecureCookies: true}, []string{"SECURE_COOKIES=true but BASE_URL is plain http"}},
		{"secure cookies on loopback http", Config{AuthMode: "local", BaseURL: "http://localhost:8080", SecureCookies: true}, []string{"cannot reach it"}},
		{"https without secure", Config{AuthMode: "local", BaseURL: "https://core.home.lan"}, []string{"SECURE_COOKIES=false"}},
		{"placeholder", Config{AuthMode: "local", BaseURL: "https://core.example.com", SecureCookies: true}, []string{"placeholder"}},
		{"proxy mode ignores cookies", Config{AuthMode: "proxy", BaseURL: "http://core.lan", SecureCookies: true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.cfg.Warnings()
			if len(got) != len(c.expect) {
				t.Fatalf("got %d warnings %q, want %d", len(got), got, len(c.expect))
			}
			for i, want := range c.expect {
				if !strings.Contains(got[i], want) {
					t.Fatalf("warning %d = %q, want it to mention %q", i, got[i], want)
				}
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST", "app.localhost", "127.0.0.1", "127.1.2.3", "::1", "[::1]"} {
		if !IsLoopback(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"192.168.1.10", "core.lan", "localhost.example.com", "10.0.0.1"} {
		if IsLoopback(h) {
			t.Errorf("%q should not be loopback", h)
		}
	}
}
