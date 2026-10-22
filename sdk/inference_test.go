package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAIProviderSelection(t *testing.T) {
	for _, tc := range []struct {
		name, provider, state          string
		configured, key, pocket, ready bool
	}{
		{"standalone", "auto", "", false, true, false, true},
		{"standalone no key", "", "", false, false, false, false},
		{"explicit pocket standalone", "pocket", "", false, true, true, false},
		{"prefer pocket over saved key", "auto", "on", true, true, true, true},
		{"pocket without key", "", "on", true, false, true, true},
		{"explicit openai", "openai", "on", true, true, false, true},
		{"disabled link", "auto", "off_person", true, true, false, true},
		{"missing app", "auto", "target_missing", true, true, false, true},
		{"missing scope", "auto", "empty", true, true, false, true},
		{"explicit pocket disabled", "pocket", "off_person", true, true, true, false},
		{"Core outage is not fallback", "auto", "error", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("user") != "member@test" || r.URL.Query().Get("app") != "ai" {
					t.Error("wrong person or app")
				}
				if tc.state == "error" {
					w.WriteHeader(503)
					return
				}
				links := []LinkState{}
				if tc.state != "empty" {
					links = append(links, LinkState{Scope: InferenceScope, On: tc.state == "on", State: tc.state})
				}
				json.NewEncoder(w).Encode(links)
			}))
			defer upstream.Close()
			url := ""
			if tc.configured {
				url = upstream.URL
			}
			c := New(url, Options{Token: "app"})
			pocket, status := c.SelectAI(t.Context(), "member@test", tc.provider, tc.key)
			if (pocket != nil) != tc.pocket || status.Ready != tc.ready {
				t.Fatalf("pocket=%v status=%+v", pocket != nil, status)
			}
			if pocket != nil && !tc.ready {
				if _, err := pocket.Complete(t.Context(), "", "private", false); err == nil {
					t.Fatal("unavailable provider ran")
				}
			}
		})
	}
}

func TestInferenceDelegationAndRevocation(t *testing.T) {
	var calls atomic.Int32
	var allowed atomic.Bool
	allowed.Store(true)
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer pcx_member" || r.URL.Path != "/api/v1/inference" {
			t.Error("wrong credential or path")
		}
		var in InferenceRequest
		json.NewDecoder(r.Body).Decode(&in)
		if in.Prompt != "private prompt" || !in.JSON {
			t.Error("prompt lost")
		}
		json.NewEncoder(w).Encode(InferenceReply{Text: `{"ok":true}`, Model: "pocket-ai/sonnet"})
	}))
	defer ai.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/links/status" {
			json.NewEncoder(w).Encode([]LinkState{{Scope: InferenceScope, On: true}})
			return
		}
		var in struct {
			User, App string
			Scopes    []string
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.User != "member@test" || in.App != "ai" || len(in.Scopes) != 1 || in.Scopes[0] != InferenceScope {
			t.Error("delegation is not scoped to caller")
		}
		if !allowed.Load() {
			w.WriteHeader(403)
			w.Write([]byte(`{"reason":"off_person","error":"disabled"}`))
			return
		}
		json.NewEncoder(w).Encode(LinkToken{AccessToken: "pcx_member", URL: ai.URL})
	}))
	defer core.Close()
	c, _ := New(core.URL, Options{Token: "app"}).SelectAI(t.Context(), "member@test", "auto", true)
	if out, err := c.Complete(t.Context(), "system", "private prompt", true); err != nil || out.Model != "pocket-ai/sonnet" {
		t.Fatal(out, err)
	}
	allowed.Store(false)
	if _, err := c.Complete(t.Context(), "system", "private prompt", true); err == nil {
		t.Fatal("revoked link reused")
	}
	if calls.Load() != 1 {
		t.Fatal("generation ran after revocation")
	}
}
