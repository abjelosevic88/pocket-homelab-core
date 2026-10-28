package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
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
