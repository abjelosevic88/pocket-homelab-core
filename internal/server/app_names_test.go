package server

import (
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// An admin's name for an app and the family word survive the app re-registering its own name.
func TestAppNames(t *testing.T) {
	e := newEnv(t)
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123", "name": "A", "box_name": "Box"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	m := map[string]any{"slug": "notes", "name": "Pocket Notes", "url": e.srv.URL, "version": "v0.8.3"}
	var reg struct {
		Token string `json:"token"`
	}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, m, &reg); c != 201 {
		t.Fatalf("register %d", c)
	}
	name := func() (string, string) {
		var got struct {
			App store.App `json:"app"`
		}
		e.do("GET", "/api/v1/apps/notes", "", nil, &got)
		a := got.App
		sv, err := e.st.GetServiceByApp(t.Context(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		return a.Name, sv.Name
	}

	if c := e.do("PUT", "/api/v1/settings", "", map[string]string{"family_name": "Home"}, nil); c != 200 {
		t.Fatalf("family %d", c)
	}
	if a, sv := name(); a != "Home Notes" || sv != "Home Notes" {
		t.Fatalf("family rename: app %q service %q", a, sv)
	}

	if c := e.do("PUT", "/api/v1/apps/notes", "", map[string]string{"name": "Scratchpad"}, nil); c != 200 {
		t.Fatalf("rename %d", c)
	}
	if c := e.do("POST", "/api/v1/apps/register", reg.Token, m, nil); c != 200 {
		t.Fatalf("re-register %d", c)
	}
	if a, sv := name(); a != "Scratchpad" || sv != "Scratchpad" {
		t.Fatalf("after re-register: app %q service %q", a, sv)
	}

	// Clearing the name falls back to the family name.
	e.do("PUT", "/api/v1/apps/notes", "", map[string]string{"name": ""}, nil)
	if a, _ := name(); a != "Home Notes" {
		t.Fatalf("cleared: %q", a)
	}
}

func TestDisplayName(t *testing.T) {
	for _, c := range []struct{ own, custom, family, want string }{
		{"Pocket Notes", "", "Pocket", "Pocket Notes"},
		{"Pocket Notes", "", "Home", "Home Notes"},
		{"Pocket Notes", "Jotter", "Home", "Jotter"},
		{"Immich", "", "Home", "Immich"},
		{"Pocketbase", "", "Home", "Pocketbase"},
	} {
		if got := store.DisplayName(c.own, c.custom, c.family); got != c.want {
			t.Errorf("DisplayName(%q, %q, %q) = %q, want %q", c.own, c.custom, c.family, got, c.want)
		}
	}
}
