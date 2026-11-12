package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// tinyNavidrome makes accounts and remembers them.
type tinyNavidrome struct {
	mu    sync.Mutex
	users []map[string]any
}

func (f *tinyNavidrome) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/auth/login":
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "isAdmin": true})
	case r.URL.Path == "/api/user" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(f.users)
	case r.URL.Path == "/api/user":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		in["id"] = "nd-1"
		f.users = append(f.users, in)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "nd-1"})
	default:
		http.NotFound(w, r)
	}
}

func writeServicesFile(t *testing.T, path, url string) {
	t.Helper()
	b, _ := json.Marshal([]map[string]any{{
		"type": "navidrome", "url": url, "public_url": "https://navidrome.pocket.local",
		"fields":    map[string]string{"USER": "pocket-box", "PASSWORD": "box-pass"},
		"connector": map[string]bool{"enabled": true, "provision_on_create": true},
	}})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestServicesFileAndSetupProvisioning(t *testing.T) {
	nd := &tinyNavidrome{}
	app := httptest.NewServer(nd)
	defer app.Close()
	file := filepath.Join(t.TempDir(), "services.json")
	writeServicesFile(t, file, app.URL)
	t.Setenv("SERVICES_FILE", file)
	e := newEnv(t)
	ctx := context.Background()

	sv, err := e.st.GetServiceByType(ctx, "navidrome")
	if err != nil {
		t.Fatal(err)
	}
	if sv.URL != app.URL || sv.PublicURL != "https://navidrome.pocket.local" || sv.Fields["USER"] != "pocket-box" || sv.Audience != "all" || sv.Name != "Navidrome" {
		t.Fatalf("service = %+v", sv)
	}
	row, err := e.st.GetConnector(ctx, sv.ID)
	if err != nil || !row.Enabled || !row.ProvisionOnCreate {
		t.Fatalf("connector = %+v, %v", row, err)
	}

	// The owner's first-run setup makes their Navidrome account.
	if code := e.do(http.MethodPost, "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "long-enough-1", "name": "Owner"}, nil); code != http.StatusOK {
		t.Fatalf("setup = %d", code)
	}
	var acct *struct{ status, user string }
	for i := 0; i < 50 && acct == nil; i++ {
		time.Sleep(50 * time.Millisecond)
		u, err := e.st.GetUserByEmail(ctx, "owner@example.com")
		if err != nil {
			continue
		}
		a, err := e.st.GetConnectorAccount(ctx, sv.ID, u.ID)
		if err != nil || a.Status != connectors.StatusOK {
			continue
		}
		ovs, _ := e.st.ListOverrides(ctx, u.ID)
		acct = &struct{ status, user string }{a.Status, ovs[sv.ID].Fields["USER"]}
	}
	if acct == nil || acct.user != "owner" {
		t.Fatalf("owner's account = %+v", acct)
	}

	// At the next start the box's values win, an admin's connector switches stay.
	row.Enabled = false
	if err := e.st.SaveConnector(ctx, row); err != nil {
		t.Fatal(err)
	}
	writeServicesFile(t, file, "http://127.0.0.1:4533")
	if err := e.s.applyServicesFile(ctx); err != nil {
		t.Fatal(err)
	}
	sv, _ = e.st.GetServiceByType(ctx, "navidrome")
	row, _ = e.st.GetConnector(ctx, sv.ID)
	if sv.URL != "http://127.0.0.1:4533" || row.Enabled {
		t.Fatalf("after restart: url %q, connector enabled %v", sv.URL, row.Enabled)
	}
}

func TestServicesFileBroken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "services.json")
	if err := os.WriteFile(file, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVICES_FILE", file)
	e := newEnv(t)
	if err := os.WriteFile(file, []byte(`[{"url":"http://x"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.s.applyServicesFile(context.Background()); err == nil {
		t.Fatal("an entry without a type must fail")
	}
}

func writeOwnerServices(t *testing.T, path, radarrKey string) {
	t.Helper()
	b, _ := json.Marshal([]map[string]any{
		{"type": "radarr", "url": "http://127.0.0.1:7878", "audience": "admins", "fields": map[string]string{"API_KEY": radarrKey}, "owner_credential": true, "connector": map[string]bool{"enabled": false}},
		{"type": "sonarr", "url": "http://127.0.0.1:8989", "audience": "admins", "fields": map[string]string{"API_KEY": "sonarr-key"}},
	})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Services without accounts of their own (owner_credential): every admin gets
// the box's key as their own credential, members nothing, a person's own
// entry is never replaced, and a copied key follows the box's rotation.
func TestServicesFileOwnerCredential(t *testing.T) {
	file := filepath.Join(t.TempDir(), "services.json")
	writeOwnerServices(t, file, "radarr-1")
	t.Setenv("SERVICES_FILE", file)
	e := newEnv(t)
	ctx := context.Background()
	radarr, err := e.st.GetServiceByType(ctx, "radarr")
	if err != nil {
		t.Fatal(err)
	}
	sonarr, _ := e.st.GetServiceByType(ctx, "sonarr")
	key := func(email string, svID int64) (string, string) {
		t.Helper()
		u, err := e.st.GetUserByEmail(ctx, email)
		if err != nil {
			t.Fatal(err)
		}
		ovs, err := e.st.ListOverrides(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ovs[svID].Fields["API_KEY"], ovs[svID].Source
	}

	// The owner made in the box's first-run setup.
	if c := e.do(http.MethodPost, "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "long-enough-1"}, nil); c != http.StatusOK {
		t.Fatalf("setup %d", c)
	}
	if k, src := key("owner@example.com", radarr.ID); k != "radarr-1" || src != "box" {
		t.Fatalf("owner's radarr = %q (%s)", k, src)
	}
	if k, _ := key("owner@example.com", sonarr.ID); k != "" {
		t.Fatalf("sonarr has no owner_credential, got %q", k)
	}

	// A member gets nothing; a new admin does; a member made admin does.
	var kid, admin2 struct {
		ID int64 `json:"id"`
	}
	e.do(http.MethodPost, "/api/v1/users", "", map[string]any{"email": "kid@example.com", "password": "password123", "role": "member"}, &kid)
	e.do(http.MethodPost, "/api/v1/users", "", map[string]any{"email": "two@example.com", "password": "password123", "role": "admin"}, &admin2)
	if k, _ := key("kid@example.com", radarr.ID); k != "" {
		t.Fatalf("a member got the box key: %q", k)
	}
	if k, _ := key("two@example.com", radarr.ID); k != "radarr-1" {
		t.Fatalf("new admin's radarr = %q", k)
	}
	e.do(http.MethodPost, "/api/v1/users", "", map[string]any{"email": "three@example.com", "password": "password123", "role": "member"}, nil)
	three, _ := e.st.GetUserByEmail(ctx, "three@example.com")
	if c := e.do(http.MethodPut, "/api/v1/users/"+strconv.FormatInt(three.ID, 10), "", map[string]string{"role": "admin"}, nil); c != http.StatusOK {
		t.Fatalf("promote %d", c)
	}
	if k, _ := key("three@example.com", radarr.ID); k != "radarr-1" {
		t.Fatalf("promoted admin's radarr = %q", k)
	}

	// The second admin puts in a key of their own; the box rotates its key.
	cat := catalog.Resolve("radarr", "", "", false)
	if err := e.st.SaveOverride(ctx, &store.Override{ServiceID: radarr.ID, UserID: admin2.ID, Fields: map[string]string{"API_KEY": "mine"}}, cat.IsSecret); err != nil {
		t.Fatal(err)
	}
	writeOwnerServices(t, file, "radarr-2")
	if err := e.s.applyServicesFile(ctx); err != nil {
		t.Fatal(err)
	}
	if k, src := key("owner@example.com", radarr.ID); k != "radarr-2" || src != "box" {
		t.Fatalf("owner's copied key after rotation = %q (%s)", k, src)
	}
	if k, src := key("two@example.com", radarr.ID); k != "mine" || src != "user" {
		t.Fatalf("own key replaced: %q (%s)", k, src)
	}
	if k, _ := key("kid@example.com", radarr.ID); k != "" {
		t.Fatalf("a member got the box key at start: %q", k)
	}
}
