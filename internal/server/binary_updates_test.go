package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/appstore"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

func binaryCatalogServer(t *testing.T, version string) (url, pub string) {
	t.Helper()
	priv, pub, err := appstore.GenerateKey("test")
	if err != nil {
		t.Fatal(err)
	}
	c := &appstore.Catalog{Generated: time.Now().UTC(), Source: "test", Binaries: []appstore.Binary{{
		Slug: "ai", Name: "Pocket AI", Repo: "pocket-homelab-ai", Version: version, Tag: "v" + version, Program: "pocket-ai",
		Files: map[string]appstore.BinaryFile{"linux/amd64": {URL: "https://example.test/pocket-ai.tar.gz", SHA256: strings.Repeat("a", 64), Size: 10}},
	}}}
	b, err := appstore.Sign(c, priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
	t.Cleanup(srv.Close)
	return srv.URL, pub
}

// fakeAI answers /healthz with the version in *v (Core reads it on checks).
func fakeAI(t *testing.T, v *atomic.Value) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","version":"` + v.Load().(string) + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestBinarySelfUpdate(t *testing.T) {
	url, pub := binaryCatalogServer(t, "0.103.2")
	t.Setenv("INSTALLER_CATALOG_URL", url)
	t.Setenv("INSTALLER_CATALOG_KEYS", pub)
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil)
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var running atomic.Value
	running.Store("0.76.0")
	appURL := fakeAI(t, &running)
	register := func(version string) string {
		running.Store(version)
		var reg struct {
			Token string `json:"token"`
		}
		if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "ai", "name": "Pocket AI", "url": appURL, "version": version}, &reg); c != 201 && c != 200 {
			t.Fatalf("register %d", c)
		}
		return reg.Token
	}
	tok := register("0.76.0")
	ctx := context.Background()

	rels := e.s.binaryReleases(ctx, true, nil)
	if len(rels) != 1 || rels[0].Kind != "binary" || rels[0].Running != "0.76.0" || rels[0].Updatable || !strings.Contains(rels[0].UpdateNote, "by hand once") {
		t.Fatalf("before any check: %+v", rels)
	}
	// A Pocket Box carries Pocket AI itself: not listed twice.
	if got := e.s.binaryReleases(ctx, false, map[string]bool{"ai": true}); len(got) != 0 {
		t.Fatalf("covered slug listed: %+v", got)
	}

	var chk map[string]any
	check := func() {
		chk = nil
		if c := e.do("GET", "/api/v1/apps/self/update?os=linux&arch=amd64&version=0.76.0", tok, nil, &chk); c != 200 {
			t.Fatalf("check %d", c)
		}
	}
	check()
	if chk["latest"] != "0.103.2" || chk["apply"] != false || chk["file"] == nil || chk["program"] != "pocket-ai" {
		t.Fatalf("check: %v", chk)
	}
	if c := e.do("GET", "/api/v1/apps/self/update", "", nil, nil); c != 403 {
		t.Fatalf("person's session: %d", c)
	}

	a, _ := e.st.GetAppBySlug(ctx, "ai")
	if ok, note := e.s.binaryUpdatable(ctx, a, e.s.findBinaryApp(ctx, "ai")); !ok {
		t.Fatalf("not updatable after a check: %q version=%q seen=%+v", note, a.Version, e.s.getBinarySeen(ctx, "ai"))
	}
	j, err := e.s.startUpgrade(ctx, a, "a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	check()
	if chk["apply"] != true || chk["target"] != "0.103.2" {
		t.Fatalf("after Update: %v", chk)
	}
	if c := e.do("POST", "/api/v1/apps/self/update", tok, map[string]string{"state": "installed", "message": "Installed; restarting now"}, nil); c != 200 {
		t.Fatalf("report %d", c)
	}
	register("0.103.2") // what a restart does
	deadline := time.Now().Add(10 * time.Second)
	for j.snapshot()["state"] == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	snap := j.snapshot()
	if snap["state"] != "done" {
		t.Fatalf("job: %v", snap)
	}
	if log := strings.Join(snap["log"].([]string), "\n"); !strings.Contains(log, "restarting now") || !strings.Contains(log, "runs v0.103.2") {
		t.Fatalf("log: %s", log)
	}
	if u := e.s.getBinaryUpdate(ctx, "ai"); u == nil || u.State != "done" {
		t.Fatalf("state: %+v", u)
	}
	// Up to date now: no second update.
	a, _ = e.st.GetAppBySlug(ctx, "ai")
	if _, err := e.s.startUpgrade(ctx, a, "a@b.c"); err == nil {
		t.Fatal("updated an app that is up to date")
	}
}

func TestBinaryFailureReport(t *testing.T) {
	url, pub := binaryCatalogServer(t, "0.2.0")
	t.Setenv("INSTALLER_CATALOG_URL", url)
	t.Setenv("INSTALLER_CATALOG_KEYS", pub)
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123"}, nil)
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var reg struct {
		Token string `json:"token"`
	}
	var running atomic.Value
	running.Store("0.1.0")
	e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "ai", "name": "Pocket AI", "url": fakeAI(t, &running), "version": "0.1.0"}, &reg)
	ctx := context.Background()
	// A copy that cannot replace itself says why; Update stays off.
	e.do("GET", "/api/v1/apps/self/update?os=linux&arch=amd64&version=0.1.0&managed=updated+by+Pocket+Box", reg.Token, nil, nil)
	a, _ := e.st.GetAppBySlug(ctx, "ai")
	if ok, note := e.s.binaryUpdatable(ctx, a, e.s.findBinaryApp(ctx, "ai")); ok || note != "updated by Pocket Box" {
		t.Fatalf("managed: %v %q", ok, note)
	}
	e.do("GET", "/api/v1/apps/self/update?os=linux&arch=amd64&version=0.1.0", reg.Token, nil, nil)
	j, err := e.s.startUpgrade(ctx, a, "")
	if err != nil {
		t.Fatal(err)
	}
	e.do("POST", "/api/v1/apps/self/update", reg.Token, map[string]string{"state": "failed", "message": "checksum mismatch"}, nil)
	deadline := time.Now().Add(10 * time.Second)
	for j.snapshot()["state"] == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if s := j.snapshot(); s["state"] != "failed" || s["error"] != "checksum mismatch" {
		t.Fatalf("job: %v", s)
	}
}
