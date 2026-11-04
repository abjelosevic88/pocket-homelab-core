package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/abjelosevic88/pocket-homelab-core/internal/installer"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

func TestSemverNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.9.1", "0.10.0", true}, {"v0.4.5", "0.4.6", true}, {"0.4.6", "0.4.6", false}, {"1.0.0", "0.9.9", false}, {"", "0.1.0", false}, {"dev", "0.1.0", false}} {
		if got := semverNewer(c.a, c.b); got != c.want {
			t.Errorf("semverNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestNotifyNtfyAndApprise(t *testing.T) {
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		user, pw, _ := r.BasicAuth()
		got = append(got, map[string]string{"path": r.URL.Path, "body": string(b), "title": r.Header.Get("Title"),
			"prio": r.Header.Get("Priority"), "tags": r.Header.Get("Tags"), "ct": r.Header.Get("Content-Type"), "auth": user + ":" + pw})
	}))
	defer srv.Close()
	s := &Server{cfg: &config.Config{NotifyURL: srv.URL + "/fallback"}}
	ctx := context.Background()

	// Settings empty → NOTIFY_URL; ntfy style with Basic auth from the URL.
	if err := s.notify(ctx, store.Settings{}, "Updated Pocket Notes", "Notes is now on 0.12.0.", 2, "arrow_up"); err != nil {
		t.Fatal(err)
	}
	st := store.Settings{NotifyURL: "http://me:secret@" + srv.Listener.Addr().String() + "/alerts"}
	if err := s.notify(ctx, st, "Update failed: Pocket Notes", "back on 0.11.0", 4, "warning"); err != nil {
		t.Fatal(err)
	}
	// Apprise API.
	st.NotifyURL = srv.URL + "/notify/updates"
	if err := s.notify(ctx, st, "2 app updates available", "a\nb", 3, "package"); err != nil {
		t.Fatal(err)
	}
	if got[0]["path"] != "/fallback" || got[0]["title"] != "Updated Pocket Notes" || got[0]["prio"] != "2" || got[0]["body"] != "Notes is now on 0.12.0." {
		t.Fatalf("ntfy: %+v", got[0])
	}
	if got[1]["path"] != "/alerts" || got[1]["auth"] != "me:secret" || got[1]["tags"] != "warning" {
		t.Fatalf("ntfy with auth: %+v", got[1])
	}
	var ap map[string]string
	json.Unmarshal([]byte(got[2]["body"]), &ap)
	if got[2]["ct"] != "application/json" || ap["title"] != "2 app updates available" || ap["body"] != "a\nb" || ap["type"] != "info" {
		t.Fatalf("apprise: %+v %v", got[2], ap)
	}
	if err := (&Server{cfg: &config.Config{}}).notify(ctx, store.Settings{}, "x", "y", 3, ""); err == nil {
		t.Fatal("no address must be an error")
	}
	if r := redactURL("http://me:secret@host/alerts"); r != "http://me@host/alerts" {
		t.Fatal(r)
	}
}

// fakeInstallerAgent answers the installer agent's API on a unix socket: a catalog
// with notes (updatable), tasks (built here) and habits (current).
func fakeInstallerAgent(t *testing.T, updates *[]string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"apps": []map[string]any{
			{"slug": "notes", "version": "0.12.0", "updatable": true, "present": true, "available": true},
			{"slug": "tasks", "version": "0.5.0", "update_note": "built from source on this server; update it by hand", "present": true, "available": true},
			{"slug": "habits", "version": "0.4.6", "updatable": true, "present": true, "available": true},
		}})
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Slug string }
		json.NewDecoder(r.Body).Decode(&req)
		*updates = append(*updates, req.Slug)
		json.NewEncoder(w).Encode(map[string]any{"id": "j-" + req.Slug, "state": "running"})
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "state": "done", "log": []string{"ok"}})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestRunUpdates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var updates []string
	e.s.inst = installer.NewClient(fakeInstallerAgent(t, &updates), strings.Repeat("t", 32))
	var sent []string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent = append(sent, r.Header.Get("Title")) }))
	defer ntfy.Close()
	st, _ := e.st.GetSettings(ctx)
	st.NotifyURL = ntfy.URL + "/alerts"
	e.st.SaveSettings(ctx, st)
	for _, a := range []store.App{{Slug: "notes", Name: "Pocket Notes", Version: "0.11.0"}, {Slug: "tasks", Name: "Pocket Tasks", Version: "v0.4.0"}, {Slug: "habits", Name: "Pocket Habits", Version: "0.4.6"}} {
		a.URL, a.AppName, a.HealthPath, a.EnvPrefix, a.Manifest = "http://127.0.0.1:1", a.Name, "/healthz", strings.ToUpper(a.Slug), json.RawMessage(`{}`)
		if _, err := e.st.CreateApp(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}

	// Automatic updates off: both new versions are only announced, once.
	run, err := e.s.runUpdates(ctx, false)
	if err != nil || len(run.Available) != 2 || len(updates) != 0 || len(sent) != 1 || sent[0] != "2 app updates available" {
		t.Fatalf("announce: %+v %v updates=%v sent=%v", run, err, updates, sent)
	}
	if run, _ := e.s.runUpdates(ctx, false); len(run.Available) != 0 || len(run.Skipped) != 2 || len(sent) != 1 {
		t.Fatalf("second pass must not repeat itself: %+v sent=%v", run, sent)
	}

	// On: notes is updated (and announced), tasks stays announced only.
	e.st.SetKV(ctx, "update_announced:notes", "")
	run, err = e.s.runUpdates(ctx, true)
	if err != nil || len(run.Updated) != 1 || len(updates) != 1 || updates[0] != "notes" || len(sent) != 2 || sent[1] != "Updated Pocket Notes" {
		t.Fatalf("apply: %+v %v updates=%v sent=%v", run, err, updates, sent)
	}
	events, _ := e.st.ListEvents(ctx, store.EventFilter{Limit: 10})
	topics := map[string]bool{}
	for _, ev := range events {
		topics[ev.Topic] = true
	}
	if !topics["core.app.updated"] || !topics["core.apps.updates_available"] {
		t.Fatalf("events: %v", topics)
	}

	// Notifications off: still on the bus, nothing sent.
	st, _ = e.st.GetSettings(ctx)
	st.NotifyUpdates = false
	e.st.SaveSettings(ctx, st)
	e.st.SetKV(ctx, "update_announced:tasks", "")
	e.s.runUpdates(ctx, true)
	if len(sent) != 2 {
		t.Fatalf("sent with notifications off: %v", sent)
	}
}
