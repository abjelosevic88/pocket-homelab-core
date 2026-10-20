package server

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func TestServiceLinks(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@b.c", "password": "password123"}, nil)
	var settings struct {
		RegistrationKey string `json:"registration_key"`
	}
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var kid, guest struct {
		ID int64 `json:"id"`
	}
	e.do("POST", "/api/v1/users", "", map[string]string{"email": "kid@b.c", "password": "password123", "role": "member"}, &kid)
	e.do("POST", "/api/v1/users", "", map[string]string{"email": "guest@b.c", "password": "password123", "role": "member"}, &guest)

	var reg struct {
		Token string `json:"token"`
	}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "day", "name": "Pocket Day", "url": "http://day",
		"services": []map[string]any{{"type": "immich", "reason": "Photos of the day"}, {"type": "maloja", "reason": "What played"}, {"type": "dawarich"}}}, &reg); c != 201 && c != 200 {
		t.Fatalf("register: %d", c)
	}
	core := sdk.New(e.srv.URL, sdk.Options{Token: reg.Token})
	ctx := context.Background()
	var le *sdk.LinkError

	// No Immich in Core yet.
	if _, err := core.ServiceFor(ctx, "owner@b.c", "immich"); !errors.As(err, &le) || le.Reason != linkNoTarget {
		t.Fatalf("want target_missing, got %v", err)
	}
	var immich, maloja struct {
		ID int64 `json:"id"`
	}
	e.do("POST", "/api/v1/services", "", map[string]any{"type": "immich", "url": "http://immich:2283", "public_url": "https://photos.example", "fields": map[string]string{"API_KEY": "owner-key"}}, &immich)
	e.do("POST", "/api/v1/services", "", map[string]any{"type": "maloja", "url": "http://maloja:42010"}, &maloja)
	// Kid has their own Immich key (as a connector would write it).
	if c := e.do("PUT", "/api/v1/users/"+strconv.FormatInt(kid.ID, 10)+"/credentials/"+strconv.FormatInt(immich.ID, 10), "", map[string]any{"fields": map[string]string{"API_KEY": "kid-key"}}, nil); c != 200 {
		t.Fatalf("kid credential: %d", c)
	}

	// Nobody gets the box key, the owner (admin) included…
	if _, err := core.ServiceFor(ctx, "owner@b.c", "immich"); !errors.As(err, &le) || le.Reason != linkNoAccount {
		t.Fatalf("the box key must not go out, even for an admin: %v", err)
	}
	// …until it is claimed as the owner's own (pocket-core credentials claim-box).
	owner, _ := e.st.GetUserByEmail(ctx, "owner@b.c")
	if done, err := ClaimBoxCredentials(ctx, e.st, owner, false); err != nil || strings.Join(done, ",") != "immich,maloja" {
		t.Fatalf("claim: %v %v", done, err)
	}
	if again, _ := ClaimBoxCredentials(ctx, e.st, owner, false); len(again) != 0 {
		t.Fatalf("claim twice: %v", again)
	}
	got, err := core.ServiceFor(ctx, "owner@b.c", "immich")
	if err != nil || got.Key != "owner-key" || got.Source != "personal" || got.URL != "http://immich:2283" || got.PublicURL != "https://photos.example" {
		t.Fatalf("owner immich: %+v %v", got, err)
	}
	if got, err := core.ServiceFor(ctx, "kid@b.c", "immich"); err != nil || got.Key != "kid-key" || got.Source != "personal" {
		t.Fatalf("kid immich: %+v %v", got, err)
	}
	if _, err := core.ServiceFor(ctx, "guest@b.c", "immich"); !errors.As(err, &le) || le.Reason != linkNoAccount {
		t.Fatalf("guest must not get the owner's key: %v", err)
	}
	// Maloja has no key: the owner gets the address, a member who isn't its listener does not.
	if got, err := core.ServiceFor(ctx, "owner@b.c", "maloja"); err != nil || got.URL != "http://maloja:42010" || got.Key != "" {
		t.Fatalf("owner maloja: %+v %v", got, err)
	}
	if _, err := core.ServiceFor(ctx, "kid@b.c", "maloja"); !errors.As(err, &le) || le.Reason != linkNoAccount {
		t.Fatalf("kid maloja: %v", err)
	}
	// Not declared, and services never go through the app-link token.
	if _, err := core.ServiceFor(ctx, "owner@b.c", "jellyfin"); !errors.As(err, &le) || le.Reason != linkNotDeclared {
		t.Fatalf("undeclared service: %v", err)
	}
	if _, err := core.ActAs(ctx, "owner@b.c", "service:immich", "account"); err == nil {
		t.Fatal("a service must not get a pcx_ token")
	}

	// They are links: listed with names, switchable for the box and per person.
	var links []map[string]any
	e.do("GET", "/api/v1/links", "", nil, &links)
	var immichLink map[string]any
	for _, l := range links {
		if l["target_slug"] == "service:immich" {
			immichLink = l
		}
	}
	if immichLink == nil || immichLink["target_name"] != "Immich" || immichLink["target_kind"] != "service" || immichLink["scope_name"] != "Use your Immich" || immichLink["reason"] != "Photos of the day" {
		t.Fatalf("immich link view %+v", immichLink)
	}
	id := strconv.FormatInt(int64(immichLink["id"].(float64)), 10)
	e.do("PUT", "/api/v1/links/"+id, "", map[string]any{"enabled": false}, nil)
	if _, err := core.ServiceFor(ctx, "owner@b.c", "immich"); !errors.As(err, &le) || le.Reason != linkOffAdmin {
		t.Fatalf("switched off for the box: %v", err)
	}
	e.do("PUT", "/api/v1/links/"+id, "", map[string]any{"enabled": true}, nil)
	// The owner switches it off for themselves (Settings → Apps acting for me); the kid is unaffected.
	if c := e.do("PUT", "/api/v1/me/links/"+id, "", map[string]bool{"enabled": false}, nil); c != 200 {
		t.Fatalf("opt out: %d", c)
	}
	if _, err := core.ServiceFor(ctx, "owner@b.c", "immich"); !errors.As(err, &le) || le.Reason != linkOffPerson {
		t.Fatalf("switched off by the owner: %v", err)
	}
	if _, err := core.ServiceFor(ctx, "kid@b.c", "immich"); err != nil {
		t.Fatalf("kid unaffected by the owner's switch: %v", err)
	}
	// Status shows the reason without handing anything out.
	st, err := core.LinkStatus(ctx, "guest@b.c", "service:immich")
	if err != nil || len(st) != 1 || st[0].State != linkNoAccount {
		t.Fatalf("guest status %+v %v", st, err)
	}
}
