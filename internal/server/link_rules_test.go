package server

import (
	"context"
	"errors"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// An app that asks for a scope in every app ("*") gets a link to each app
// that offers it, new apps included, under one switch for the admin.
func TestLinkRules(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@b.c", "password": "password123"}, nil)
	var settings struct {
		RegistrationKey string `json:"registration_key"`
	}
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	register := func(auth string, m map[string]any) string {
		var reg struct {
			Token string `json:"token"`
		}
		if auth == "" {
			auth = settings.RegistrationKey
		}
		if c := e.do("POST", "/api/v1/apps/register", auth, m, &reg); c != 201 && c != 200 {
			t.Fatalf("register %v: %d", m["slug"], c)
		}
		return reg.Token
	}
	searchable := []map[string]any{{"scope": sdk.ScopeSearch, "name": "Find your things"}}
	notesM := map[string]any{"slug": "notes", "name": "Pocket Notes", "url": "http://notes", "public_url": "https://notes.example", "icon": "/favicon.svg",
		"category": "Everyday", "offers": searchable, "search": map[string]any{"what": "Your notes", "bangs": []string{"notes"}}}
	notesTok := register("", notesM)
	register("", map[string]any{"slug": "kitchen", "name": "Pocket Kitchen", "url": "http://kitchen"}) // offers nothing

	// Search enrolls asking for pocket:search everywhere: enrollment approves the rule.
	searchM := map[string]any{"slug": "search", "name": "Pocket Search", "url": "http://search",
		"offers": searchable, // its own offer must not make it link to itself
		"uses":   []map[string]any{{"app": "*", "scopes": []string{sdk.ScopeSearch}, "reason": "Find your things in every app"}}}
	searchTok := register("", searchM)
	core := sdk.New(e.srv.URL, sdk.Options{Token: searchTok})
	ctx := context.Background()

	links := func() map[string]map[string]any {
		var all []map[string]any
		e.do("GET", "/api/v1/links", "", nil, &all)
		out := map[string]map[string]any{}
		for _, l := range all {
			out[l["app_slug"].(string)+">"+l["target_slug"].(string)+" "+l["scope"].(string)] = l
		}
		return out
	}
	rules := func() []map[string]any {
		var out []map[string]any
		if c := e.do("GET", "/api/v1/link-rules", "", nil, &out); c != 200 {
			t.Fatalf("rules %d", c)
		}
		return out
	}
	all := links()
	l := all["search>notes pocket:search"]
	if len(all) != 1 || l == nil || l["enabled"] != true || l["state"] != linkOn || l["rule_id"].(float64) == 0 || l["reason"] != "Find your things in every app" {
		t.Fatalf("the rule should reach notes and nothing else: %+v", all)
	}
	rs := rules()
	if len(rs) != 1 || rs[0]["app_slug"] != "search" || rs[0]["scope"] != sdk.ScopeSearch || rs[0]["enabled"] != true || rs[0]["links"].(float64) != 1 || rs[0]["links_on"].(float64) != 1 || rs[0]["scope_name"] != "Find your things" {
		t.Fatalf("rules %+v", rs)
	}
	ruleID := itoa(int64(rs[0]["id"].(float64)))

	// The asking app learns who offers it, and who they are.
	apps, err := core.Capable(ctx, "owner@b.c", sdk.ScopeSearch)
	if err != nil || len(apps) != 1 {
		t.Fatalf("capable %+v %v", apps, err)
	}
	if a := apps[0]; a.App != "notes" || !a.On || a.Name != "Pocket Notes" || a.Icon != "https://notes.example/favicon.svg" || a.PublicURL != "https://notes.example" ||
		a.Category != "Everyday" || a.Search == nil || a.Search.What != "Your notes" || a.Search.Bangs[0] != "notes" {
		t.Fatalf("capable app %+v", a)
	}
	if tok, err := core.ActAs(ctx, "owner@b.c", "notes", sdk.ScopeSearch); err != nil || tok.URL != "http://notes" {
		t.Fatalf("token through a rule: %+v %v", tok, err)
	}
	// "*" is not a target: no token for it, and nothing else in notes opens.
	var le *sdk.LinkError
	if _, err := core.ActAs(ctx, "owner@b.c", "*", sdk.ScopeSearch); err == nil {
		t.Fatal("a token for every app at once")
	}
	if _, err := core.ActAs(ctx, "owner@b.c", "notes", "notes:write"); !errors.As(err, &le) || le.Reason != linkNotDeclared {
		t.Fatalf("a rule must only cover its scope: %v", err)
	}

	// A new app that offers it is reached at once, on like the rule.
	tasksM := map[string]any{"slug": "tasks", "name": "Pocket Tasks", "url": "http://tasks", "offers": searchable}
	tasksTok := register("", tasksM)
	if l := links()["search>tasks pocket:search"]; l == nil || l["enabled"] != true || l["state"] != linkOn {
		t.Fatalf("a new app should be linked: %+v", links())
	}

	// One link switched off stays off, whatever registers later.
	tasksLink := itoa(int64(links()["search>tasks pocket:search"]["id"].(float64)))
	e.do("PUT", "/api/v1/links/"+tasksLink, "", map[string]bool{"enabled": false}, nil)
	register(tasksTok, tasksM)
	register(searchTok, searchM)
	register(notesTok, notesM)
	if l := links()["search>tasks pocket:search"]; l["enabled"] != false || l["state"] != linkOffAdmin {
		t.Fatalf("re-registering switched a link back on: %+v", l)
	}
	if _, err := core.ActAs(ctx, "owner@b.c", "tasks", sdk.ScopeSearch); !errors.As(err, &le) || le.Reason != linkOffAdmin {
		t.Fatalf("switched off: %v", err)
	}
	if len(links()) != 2 {
		t.Fatalf("links %+v", links())
	}

	// The rule's switch: off for every app, and for apps that come later.
	if c := e.do("PUT", "/api/v1/link-rules/"+ruleID, "", map[string]bool{"enabled": false}, nil); c != 200 {
		t.Fatalf("rule off %d", c)
	}
	if _, err := core.ActAs(ctx, "owner@b.c", "notes", sdk.ScopeSearch); !errors.As(err, &le) || le.Reason != linkOffAdmin {
		t.Fatalf("rule off: %v", err)
	}
	register("", map[string]any{"slug": "mail", "name": "Pocket Mail", "url": "http://mail", "offers": searchable})
	if l := links()["search>mail pocket:search"]; l == nil || l["enabled"] != false {
		t.Fatalf("an app arriving under a rule that is off must start off: %+v", l)
	}
	e.do("PUT", "/api/v1/link-rules/"+ruleID, "", map[string]bool{"enabled": true}, nil)
	if rs := rules(); rs[0]["links"].(float64) != 3 || rs[0]["links_on"].(float64) != 3 {
		t.Fatalf("rule on: %+v", rs)
	}

	// An app that stops offering it drops out (no "can't work" link left behind), and comes back.
	delete(tasksM, "offers")
	register(tasksTok, tasksM)
	if _, ok := links()["search>tasks pocket:search"]; ok {
		t.Fatalf("a link to an app that no longer offers the scope: %+v", links())
	}
	if apps, _ := core.Capable(ctx, "owner@b.c", sdk.ScopeSearch); len(apps) != 2 {
		t.Fatalf("capable after tasks left: %+v", apps)
	}
	tasksM["offers"] = searchable
	register(tasksTok, tasksM)
	if l := links()["search>tasks pocket:search"]; l == nil || l["enabled"] != true {
		t.Fatalf("offered again: %+v", l)
	}

	// A person's own switch is per app, as for any link.
	var mine []map[string]any
	e.do("GET", "/api/v1/me/links", "", nil, &mine)
	if len(mine) != 3 {
		t.Fatalf("my links %+v", mine)
	}
	for _, l := range mine {
		if l["target_slug"] == "mail" {
			e.do("PUT", "/api/v1/me/links/"+itoa(int64(l["id"].(float64))), "", map[string]bool{"enabled": false}, nil)
		}
	}
	apps, _ = core.Capable(ctx, "owner@b.c", sdk.ScopeSearch)
	on := map[string]bool{}
	for _, a := range apps {
		on[a.App] = a.On
	}
	if len(apps) != 3 || !on["notes"] || !on["tasks"] || on["mail"] {
		t.Fatalf("after the person's switch: %+v", on)
	}

	// A rule added by an update (not at enrollment) waits for the admin, links and all.
	dayM := map[string]any{"slug": "day", "name": "Pocket Day", "url": "http://day"}
	dayTok := register("", dayM)
	dayM["uses"] = []map[string]any{{"app": "*", "scopes": []string{sdk.ScopeSearch}, "reason": "later"}}
	register(dayTok, dayM)
	if l := links()["day>notes pocket:search"]; l == nil || l["enabled"] != false || l["state"] != linkOffAdmin {
		t.Fatalf("a rule from an update must start off: %+v", l)
	}

	// An app that names a target itself owns that link: the rule's switch leaves it alone.
	searchM["uses"] = []map[string]any{{"app": "*", "scopes": []string{sdk.ScopeSearch}, "reason": "Find your things in every app"},
		{"app": "notes", "scopes": []string{sdk.ScopeSearch}, "reason": "Named"}}
	register(searchTok, searchM)
	if l := links()["search>notes pocket:search"]; l["rule_id"].(float64) != 0 || l["reason"] != "Named" || l["enabled"] != true {
		t.Fatalf("a named link: %+v", l)
	}
	e.do("PUT", "/api/v1/link-rules/"+ruleID, "", map[string]bool{"enabled": false}, nil)
	if l := links()["search>notes pocket:search"]; l["enabled"] != true {
		t.Fatalf("the rule switched a named link: %+v", l)
	}
	e.do("PUT", "/api/v1/link-rules/"+ruleID, "", map[string]bool{"enabled": true}, nil)

	// Dropping the rule from the manifest removes its links; the named one stays.
	searchM["uses"] = []map[string]any{{"app": "notes", "scopes": []string{sdk.ScopeSearch}, "reason": "Named"}}
	register(searchTok, searchM)
	all = links()
	if _, ok := all["search>tasks pocket:search"]; ok || all["search>notes pocket:search"] == nil {
		t.Fatalf("after dropping the rule: %+v", all)
	}
	for _, r := range rules() {
		if r["app_slug"] == "search" {
			t.Fatalf("an undeclared rule is still listed: %+v", r)
		}
	}

	// Members can't see or flip rules.
	var member struct {
		ID int64 `json:"id"`
	}
	e.do("POST", "/api/v1/users", "", map[string]string{"email": "kid@b.c", "password": "password123", "role": "member"}, &member)
	var pair struct {
		Code string `json:"code"`
	}
	e.do("POST", "/api/v1/pair", "", map[string]int64{"user_id": member.ID}, &pair)
	var prof Profile
	e.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": "kid phone"}, &prof)
	e.cj = nil
	if c := e.do("GET", "/api/v1/link-rules", prof.Token, nil, nil); c != 403 {
		t.Fatalf("member lists rules %d", c)
	}
	if c := e.do("PUT", "/api/v1/link-rules/1", prof.Token, map[string]bool{"enabled": true}, nil); c != 403 {
		t.Fatalf("member flips a rule %d", c)
	}
}

// An app Core has never heard of needs nothing in Core's own catalog: its
// manifest carries its name, icon, shelf and env prefix.
func TestUnknownAppRegistersFromItsManifest(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "owner@b.c", "password": "password123"}, nil)
	var settings struct {
		RegistrationKey string `json:"registration_key"`
	}
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	m := map[string]any{"slug": "brand-new", "name": "Pocket Brand New", "description": "Not in any list", "icon": "/favicon.svg", "category": "Home",
		"url": "http://brand-new:8080", "public_url": "https://new.example", "version": "0.1.0",
		"portable": map[string]any{"exports": []map[string]any{{"path": "/api/v1/export.json", "file": "new.json", "kind": "pocket"}}},
		"widget":   map[string]any{"title": "New things"}}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, m, nil); c != 201 {
		t.Fatalf("register %d", c)
	}
	var app struct {
		App struct {
			Name, Icon string
			EnvPrefix  string `json:"env_prefix"`
			Manifest   struct {
				Category string         `json:"category"`
				Portable map[string]any `json:"portable"`
				Widget   map[string]any `json:"widget"`
			} `json:"manifest"`
		} `json:"app"`
	}
	e.do("GET", "/api/v1/apps/brand-new", "", nil, &app)
	a := app.App
	if a.Name != "Pocket Brand New" || a.Icon != "/favicon.svg" || a.EnvPrefix != "BRAND_NEW" || a.Manifest.Category != "Home" || a.Manifest.Portable == nil || a.Manifest.Widget["title"] != "New things" {
		t.Fatalf("stored app %+v", a)
	}
	var svcs []struct {
		Type, Name string
		Catalog    struct {
			Prefix, Group string
			Pocket        bool
		} `json:"catalog"`
	}
	e.do("GET", "/api/v1/services", "", nil, &svcs)
	found := false
	for _, sv := range svcs {
		if sv.Type == "brand-new" {
			found = sv.Name == "Pocket Brand New" && sv.Catalog.Prefix == "BRAND_NEW" && sv.Catalog.Group == "Pocket" && sv.Catalog.Pocket
		}
	}
	if !found {
		t.Fatalf("the app should show up as a Pocket service for paired devices: %+v", svcs)
	}
	var res struct {
		Name, Category string
	}
	e.do("GET", "/api/v1/resolve?slug=brand-new", "", nil, &res)
	if res.Name != "Pocket Brand New" || res.Category != "Home" {
		t.Fatalf("resolve %+v", res)
	}
}
