package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

func TestRegistrationEventOnlyOnEnrollment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
	settings, err := e.st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer health.Close()
	manifest := map[string]any{"slug": "tasks", "name": "Pocket Tasks", "url": health.URL, "version": "1.0.0"}
	var registration struct {
		Token string
	}
	if code := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, manifest, &registration); code != 201 || registration.Token == "" {
		t.Fatalf("initial enrollment: status=%d token present=%v", code, registration.Token != "")
	}
	for _, step := range []struct {
		name, token, version string
	}{
		{"restart", registration.Token, "1.0.0"},
		{"upgrade", registration.Token, "1.1.0"},
		{"token recovery", settings.RegistrationKey, "1.1.0"},
	} {
		t.Run(step.name, func(t *testing.T) {
			manifest["version"] = step.version
			if code := e.do("POST", "/api/v1/apps/register", step.token, manifest, nil); code != 200 {
				t.Fatalf("re-register: %d", code)
			}
			events, err := e.st.ListEvents(ctx, store.EventFilter{Topic: "core.app.registered"})
			if err != nil || len(events) != 1 {
				t.Fatalf("registration events: %v, %v", events, err)
			}
			var payload struct {
				New bool
			}
			if err := json.Unmarshal(events[0].Payload, &payload); err != nil || !payload.New || events[0].Subject != "app:tasks" {
				t.Fatalf("initial event: %+v, %v", events[0], err)
			}
		})
	}
	app, err := e.st.GetAppBySlug(ctx, "tasks")
	if err != nil || app.Version != "1.1.0" {
		t.Fatalf("manifest update: %+v, %v", app, err)
	}
}

func TestEventLogExcludesRegistrationBeforePagination(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
	insert := func(topic, source string) {
		t.Helper()
		if err := e.st.InsertEvent(ctx, &store.Event{Topic: topic, Source: source, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	insert("core.app.down", "core")
	insert("tasks.completed", "tasks")
	// More than a full internal page of registration noise must not bury older activity.
	for i := 0; i < 510; i++ {
		insert("core.app.registered", "core")
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"limit=2&exclude_topic=core.app.registered", []string{"tasks.completed", "core.app.down"}},
		{"limit=2&exclude_topic=core.app.registered&source=core", []string{"core.app.down"}},
		{"limit=2&exclude_topic=core.app.registered&topic=core.*", []string{"core.app.down"}},
		{"limit=2&exclude_topic=core.app.registered&after=1", []string{"tasks.completed"}},
		{"limit=2&topic=core.app.registered", []string{"core.app.registered", "core.app.registered"}},
		{"limit=2", []string{"core.app.registered", "core.app.registered"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var got []store.Event
			if code := e.do("GET", "/api/v1/events?"+tc.query, "", nil, &got); code != 200 || len(got) != len(tc.want) {
				t.Fatalf("events: status=%d got=%+v want=%v", code, got, tc.want)
			}
			for i, topic := range tc.want {
				if got[i].Topic != topic {
					t.Fatalf("event %d: %s, want %s", i, got[i].Topic, topic)
				}
			}
		})
	}
	for _, tc := range []struct {
		query string
		core  int
	}{
		{"?exclude_topic=core.app.registered", 1},
		{"", 511},
	} {
		var counts []store.SourceCount
		if code := e.do("GET", "/api/v1/events/sources"+tc.query, "", nil, &counts); code != 200 {
			t.Fatal(code)
		}
		if len(counts) != 2 || counts[0].Source != "core" || counts[0].Count != tc.core || counts[1].Source != "tasks" || counts[1].Count != 1 {
			t.Fatalf("source counts %s: %+v", tc.query, counts)
		}
	}
}
