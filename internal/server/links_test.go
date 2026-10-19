package server

import (
	"context"
	"errors"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func TestAppLinks(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@b.c", "password": "password123"}, nil)
	var settings struct {
		RegistrationKey string `json:"registration_key"`
	}
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	var member struct {
		ID int64 `json:"id"`
	}
	e.do("POST", "/api/v1/users", "", map[string]string{"email": "kid@b.c", "password": "password123", "role": "member"}, &member)

	type regT struct {
		Token string `json:"token"`
	}
	register := func(m map[string]any) string {
		var reg regT
		key := settings.RegistrationKey
		if c := e.do("POST", "/api/v1/apps/register", key, m, &reg); c != 201 && c != 200 {
			t.Fatalf("register %v: %d", m["slug"], c)
		}
		return reg.Token
	}
	garage := register(map[string]any{"slug": "garage", "name": "Pocket Garage", "url": "http://garage",
		"uses": []map[string]any{{"app": "money", "scopes": []string{"transactions:write", "accounts:read"}, "reason": "Book car costs"}}})

	// The target is not registered yet: no token.
	core := sdk.New(e.srv.URL, sdk.Options{Token: garage})
	ctx := context.Background()
	var le *sdk.LinkError
	if _, err := core.ActAs(ctx, "owner@b.c", "money", "transactions:write"); !errors.As(err, &le) || le.Reason != linkNoTarget {
		t.Fatalf("want target_missing, got %v", err)
	}
	money := register(map[string]any{"slug": "money", "name": "Pocket Money", "url": "http://money",
		"offers": []map[string]any{{"scope": "transactions:write", "name": "Create transactions"}, {"scope": "accounts:read", "name": "See accounts"}}})
	kitchen := register(map[string]any{"slug": "kitchen", "name": "Pocket Kitchen", "url": "http://kitchen"})

	if on, why := core.LinkOn(ctx, "owner@b.c", "money", "transactions:write", "accounts:read"); !on {
		t.Fatalf("link should be on by default: %s", why)
	}
	tok, err := core.ActAs(ctx, "owner@b.c", "money", "accounts:read", "transactions:write")
	if err != nil || tok.URL != "http://money" || !sdk.IsLinkToken(tok.AccessToken) {
		t.Fatalf("act as: %+v %v", tok, err)
	}
	// Scopes Garage never declared are refused.
	if _, err := core.ActAs(ctx, "owner@b.c", "money", "transactions:delete_all"); !errors.As(err, &le) || le.Reason != linkNotDeclared {
		t.Fatalf("undeclared scope: %v", err)
	}
	if _, err := core.ActAs(ctx, "nobody@b.c", "money", "accounts:read"); !errors.As(err, &le) || le.Reason != "unknown_user" {
		t.Fatalf("unknown person: %v", err)
	}

	// Only the target app learns who the token is for.
	introspect := func(as string) map[string]any {
		var out map[string]any
		e.do("POST", "/api/v1/auth/introspect", as, map[string]string{"token": tok.AccessToken}, &out)
		return out
	}
	in := introspect(money)
	if in["active"] != true || in["kind"] != "delegated" || in["audience"] != "money" || in["scope"] != "accounts:read transactions:write" ||
		in["user"].(map[string]any)["email"] != "owner@b.c" || in["app"].(map[string]any)["slug"] != "garage" {
		t.Fatalf("money introspection %+v", in)
	}
	if in := introspect(kitchen); in["active"] != false || in["reason"] != "wrong_audience" {
		t.Fatalf("another app must not accept it: %+v", in)
	}
	// A link token is useless against Core itself.
	if c := e.do("GET", "/api/v1/auth/me", tok.AccessToken, nil, nil); c != 401 {
		t.Fatalf("pcx_ on Core API: %d", c)
	}

	// The person switches the link off: the live token dies at once.
	ownerCJ := e.cj
	var mine []map[string]any
	e.do("GET", "/api/v1/me/links", "", nil, &mine)
	if len(mine) != 2 {
		t.Fatalf("my links %+v", mine)
	}
	var writeID int64
	for _, l := range mine {
		if l["scope"] == "transactions:write" {
			writeID = int64(l["id"].(float64))
			if l["scope_name"] != "Create transactions" || l["target_name"] != "Pocket Money" || l["reason"] != "Book car costs" {
				t.Fatalf("link view %+v", l)
			}
		}
	}
	if c := e.do("PUT", "/api/v1/me/links/"+itoa(writeID), "", map[string]bool{"enabled": false}, nil); c != 200 {
		t.Fatalf("opt out %d", c)
	}
	if in := introspect(money); in["active"] != false || in["reason"] != linkOffPerson {
		t.Fatalf("opted-out token still active: %+v", in)
	}
	if on, why := core.LinkOn(ctx, "owner@b.c", "money", "transactions:write"); on || why != linkOffPerson {
		t.Fatalf("status after opt out: %v %s", on, why)
	}
	// The member is unaffected by the owner's switch.
	if _, err := core.ActAs(ctx, "kid@b.c", "money", "transactions:write"); err != nil {
		t.Fatalf("member link: %v", err)
	}
	e.do("PUT", "/api/v1/me/links/"+itoa(writeID), "", map[string]bool{"enabled": true}, nil)

	// The admin switches it off for the whole box.
	if c := e.do("PUT", "/api/v1/links/"+itoa(writeID), "", map[string]bool{"enabled": false}, nil); c != 200 {
		t.Fatalf("admin off %d", c)
	}
	if _, err := sdk.New(e.srv.URL, sdk.Options{Token: garage}).ActAs(ctx, "kid@b.c", "money", "transactions:write"); !errors.As(err, &le) || le.Reason != linkOffAdmin {
		t.Fatalf("admin switch: %v", err)
	}
	e.do("PUT", "/api/v1/links/"+itoa(writeID), "", map[string]bool{"enabled": true}, nil)

	// Members cannot see or flip the box-wide list.
	var pair struct {
		Code string `json:"code"`
	}
	e.do("POST", "/api/v1/pair", "", map[string]int64{"user_id": member.ID}, &pair)
	var prof Profile
	e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": "kid phone"}, &prof)
	e.cj = nil
	if c := e.do("GET", "/api/v1/links", prof.Token, nil, nil); c != 403 {
		t.Fatalf("member lists links %d", c)
	}
	var kidLinks []map[string]any
	if c := e.do("GET", "/api/v1/me/links", prof.Token, nil, &kidLinks); c != 200 || len(kidLinks) != 2 || kidLinks[0]["opt_outs"] != nil {
		t.Fatalf("member's own links %d %+v", c, kidLinks)
	}
	e.cj = ownerCJ

	// Re-registering without the use removes the links.
	register(map[string]any{"slug": "garage", "name": "Pocket Garage", "url": "http://garage"})
	var all []map[string]any
	e.do("GET", "/api/v1/links", "", nil, &all)
	if len(all) != 0 {
		t.Fatalf("links should follow the manifest: %+v", all)
	}
}
