package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAskSessionSourceAndRevocation(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var calls int
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		secret, err := f.st.GetMCPSecret(r.Context(), token, "app", "")
		if err != nil || secret.UserID != f.user.ID {
			t.Error("wrong delegated person")
		}
		var binding mcpAuthorization
		_ = json.Unmarshal(secret.Data, &binding)
		if binding.Target != "ai" {
			t.Error("credential not AI-bound")
		}
		if r.URL.Path == "/api/v1/ask/connect" {
			var in struct{ Token string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			if !strings.HasPrefix(in.Token, "pcma_") {
				t.Error("missing server-side connection")
			}
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000001"}`)
	}))
	defer ai.Close()
	f.app("ai", ai.URL, "all")
	f.app("notes", "https://notes.test", "all")
	for _, tc := range []struct {
		method, path, body, origin string
		cookie                     bool
		want                       int
	}{
		{"GET", "/api/v1/ask/status", "", "", false, 401},
		{"POST", "/api/v1/ask/connect", `{"approved":true}`, "https://evil.test", true, 403},
		{"POST", "/api/v1/ask/connect", `{"approved":false}`, oauthTestOrigin, true, 400},
		{"POST", "/api/v1/ask/connect", `{"approved":true}`, oauthTestOrigin, true, 204},
		{"POST", "/api/v1/ask/threads", `{"text":"hello","source":{"app":"notes","url":"https://evil.test/notes/1"}}`, oauthTestOrigin, true, 400},
		{"POST", "/api/v1/ask/threads", `{"text":"hello","source":{"app":"unknown","url":"https://unknown.test/"}}`, oauthTestOrigin, true, 403},
		{"POST", "/api/v1/ask/threads", `{"text":"hello","source":{"app":"notes","url":"https://notes.test/notes/1"}}`, oauthTestOrigin, true, 200},
		{"POST", "/api/v1/ask/threads/00000000-0000-0000-0000-000000000001/fork", `{}`, oauthTestOrigin, true, 404},
	} {
		res, body := f.request(tc.method, tc.path, tc.body, tc.origin, tc.cookie)
		if res.StatusCode != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, res.StatusCode, body)
		}
	}
	if calls != 2 {
		t.Fatalf("unexpected upstream calls %d", calls)
	}
	_, body := f.request("GET", "/api/v1/ask/status", "", "", true)
	if strings.Contains(body, "pcma_") || strings.Contains(body, "pcu_") {
		t.Fatal("credential leaked")
	}
	grant, err := f.s.askGrant(context.Background(), f.user)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.st.DeleteMCPGrant(context.Background(), grant, f.user.ID)
	res, _ := f.request("GET", "/api/v1/ask/threads", "", "", true)
	if res.StatusCode != 409 {
		t.Fatal("revoked connection still usable")
	}
	res, err = http.Get(f.srv.URL + "/ask")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "https://notes.test") {
		t.Fatal("registered frame origin missing")
	}
}
