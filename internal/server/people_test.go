package server

import (
	"context"
	"testing"
)

func TestPeopleForApps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@example.com", "password": "password123", "name": "Owner"}, nil)
	e.do("POST", "/api/v1/users", "", map[string]string{"email": "kid@example.com", "password": "password123", "role": "member", "name": "Kid"}, nil)
	settings, err := e.st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reg struct{ Token string }
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "files", "name": "Pocket Files", "url": "http://files"}, &reg); c != 201 {
		t.Fatalf("register: %d", c)
	}
	var people []struct{ Email, Name, PasswordHash, Role string }
	if c := e.do("GET", "/api/v1/people", reg.Token, nil, &people); c != 200 || len(people) != 2 {
		t.Fatalf("people for an app: %d %v", c, people)
	}
	for _, p := range people {
		if p.Role != "" || p.PasswordHash != "" {
			t.Fatalf("people leaks more than name and email: %+v", p)
		}
	}
	if c := e.do("GET", "/api/v1/people", "pca_wrong", nil, nil); c != 401 {
		t.Fatalf("bad token: %d", c)
	}
}
