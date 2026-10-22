package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// authStatusAs fetches /auth/status as a browser that reached Core at host.
func (e *env) authStatusAs(host, forwardedProto string) map[string]any {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/auth/status", nil)
	req.Host = host
	if forwardedProto != "" {
		req.Header.Set("X-Forwarded-Proto", forwardedProto)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var st map[string]any
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

func TestCookieWarningOnPlainHTTP(t *testing.T) {
	t.Setenv("SECURE_COOKIES", "true")
	e := newEnv(t)

	if w, _ := e.authStatusAs("192.168.1.10:8179", "")["cookie_warning"].(string); !strings.Contains(w, "SECURE_COOKIES=true") {
		t.Fatalf("plain http on the LAN: cookie_warning = %q", w)
	}
	for _, c := range []struct{ host, proto string }{
		{"core.home.lan", "https"}, // behind an HTTPS reverse proxy
		{"localhost:8179", ""},     // browsers keep Secure cookies on loopback
		{"127.0.0.1:8179", ""},
	} {
		if w := e.authStatusAs(c.host, c.proto)["cookie_warning"]; w != "" {
			t.Fatalf("%s %s: unexpected cookie_warning %q", c.host, c.proto, w)
		}
	}
}

func TestNoCookieWarningWithoutSecureCookies(t *testing.T) {
	e := newEnv(t)
	if w := e.authStatusAs("192.168.1.10:8179", "")["cookie_warning"]; w != "" {
		t.Fatalf("unexpected cookie_warning %q", w)
	}
}

func TestStaleAdminEnvIsLogged(t *testing.T) {
	t.Setenv("ADMIN_EMAIL", "owner@box.test")
	t.Setenv("ADMIN_PASSWORD", "first-password")
	var logs bytes.Buffer
	e := newEnvLog(t, &logs)
	if strings.Contains(logs.String(), "ADMIN_") {
		t.Fatalf("fresh database should not warn: %s", logs.String())
	}

	e.s.warnStaleAdminEnv(context.Background())
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("matching env should not warn: %s", logs.String())
	}

	e.s.cfg.AdminPassword = "edited-later"
	e.s.warnStaleAdminEnv(context.Background())
	if !strings.Contains(logs.String(), "ADMIN_PASSWORD no longer matches") {
		t.Fatalf("edited password not reported: %s", logs.String())
	}

	logs.Reset()
	e.s.cfg.AdminEmail = "someone-else@box.test"
	e.s.warnStaleAdminEnv(context.Background())
	if !strings.Contains(logs.String(), "ADMIN_EMAIL has no account here") {
		t.Fatalf("unknown admin email not reported: %s", logs.String())
	}
	if strings.Contains(logs.String(), "edited-later") {
		t.Fatal("the password must never be logged")
	}
}
