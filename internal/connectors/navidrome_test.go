package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeNavidrome answers /auth/login and /api/user like Navidrome 0.64.
type fakeNavidrome struct {
	mu      sync.Mutex
	users   []map[string]any
	created []map[string]any
}

func (f *fakeNavidrome) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/auth/login":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case in["username"] == "boxadmin" && in["password"] == "admin-pass":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok-admin", "isAdmin": true})
		case in["username"] == "plain" && in["password"] == "plain-pass":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok-plain", "isAdmin": false})
		default:
			http.Error(w, `{"error":"Invalid username or password"}`, http.StatusUnauthorized)
		}
	case r.URL.Path == "/api/user":
		if r.Header.Get("X-ND-Authorization") != "Bearer tok-admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(f.users)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		in["id"] = "nd-" + in["userName"].(string)
		f.created = append(f.created, in)
		f.users = append(f.users, map[string]any{"id": in["id"], "userName": in["userName"], "email": in["email"], "isAdmin": in["isAdmin"]})
		_ = json.NewEncoder(w).Encode(map[string]any{"id": in["id"]})
	default:
		http.NotFound(w, r)
	}
}

func ndTarget(f *fakeNavidrome, user, pass string) (Target, func()) {
	srv := httptest.NewServer(f)
	return Target{BaseURL: srv.URL, Fields: map[string]string{"USER": user, "PASSWORD": pass}, HTTP: srv.Client()}, srv.Close
}

func TestNavidromeTest(t *testing.T) {
	f := &fakeNavidrome{users: []map[string]any{{"id": "a", "userName": "boxadmin", "isAdmin": true}}}
	tg, done := ndTarget(f, "boxadmin", "admin-pass")
	defer done()
	info, err := navidrome{}.Test(context.Background(), tg)
	if err != nil || !info.Admin || info.Users != 1 {
		t.Fatalf("Test = %+v, %v", info, err)
	}

	bad, done2 := ndTarget(f, "boxadmin", "wrong")
	defer done2()
	if _, err := (navidrome{}).Test(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong password: %v", err)
	}
	plain, done3 := ndTarget(f, "plain", "plain-pass")
	defer done3()
	if _, err := (navidrome{}).Test(context.Background(), plain); err == nil || !strings.Contains(err.Error(), "not an administrator") {
		t.Fatalf("non-admin: %v", err)
	}
}

func TestNavidromeProvisionCreates(t *testing.T) {
	f := &fakeNavidrome{users: []map[string]any{
		{"id": "a", "userName": "boxadmin", "isAdmin": true},
		{"id": "b", "userName": "ana", "email": "ana@other.example"},
	}}
	tg, done := ndTarget(f, "boxadmin", "admin-pass")
	defer done()
	acc, err := navidrome{}.Provision(context.Background(), tg, Person{Email: "Ana@Example.com", Name: "Ana"})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Status != StatusOK || !acc.Created || acc.RemoteID != "nd-ana2" {
		t.Fatalf("account = %+v", acc)
	}
	if acc.Fields["USER"] != "ana2" || len(acc.Fields["PASSWORD"]) != 32 {
		t.Fatalf("credential = %q / %d chars", acc.Fields["USER"], len(acc.Fields["PASSWORD"]))
	}
	got := f.created[0]
	if got["isAdmin"] != false || got["email"] != "ana@example.com" || got["name"] != "Ana" || got["password"] != acc.Fields["PASSWORD"] {
		t.Fatalf("created = %+v", got)
	}
}

func TestNavidromeProvisionLinksExisting(t *testing.T) {
	f := &fakeNavidrome{users: []map[string]any{
		{"id": "a", "userName": "boxadmin", "email": "owner@example.com", "isAdmin": true},
		{"id": "b", "userName": "sani", "email": "sani@example.com"},
	}}
	tg, done := ndTarget(f, "boxadmin", "admin-pass")
	defer done()
	acc, err := navidrome{}.Provision(context.Background(), tg, Person{Email: "sani@example.com"})
	if err != nil || acc.Status != StatusLinked || acc.RemoteID != "b" || acc.Fields != nil {
		t.Fatalf("link = %+v, %v", acc, err)
	}
	acc, err = navidrome{}.Provision(context.Background(), tg, Person{Email: "owner@example.com"})
	if err != nil || acc.Status != StatusLinked || !strings.Contains(acc.Note, "administrator") {
		t.Fatalf("admin link = %+v, %v", acc, err)
	}
	if len(f.created) != 0 {
		t.Fatalf("created %d accounts", len(f.created))
	}
}

func TestNavidromeKeepOwnCredential(t *testing.T) {
	f := &fakeNavidrome{users: []map[string]any{{"id": "a", "userName": "boxadmin", "isAdmin": true}}}
	tg, done := ndTarget(f, "boxadmin", "admin-pass")
	defer done()
	if _, err := (navidrome{}).Provision(context.Background(), tg, Person{Email: "new@example.com", KeepOwnCredential: true}); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.created) != 0 {
		t.Fatal("made an account for a person who keeps their own credential")
	}
}
