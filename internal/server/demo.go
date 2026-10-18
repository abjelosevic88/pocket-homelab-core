package server

import (
	"context"
	"encoding/json"

	"github.com/abjelosevic88/pocket-homelab-core/internal/catalog"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// seedDemo registers a few fake apps and services so the UI has something to
// show. Only runs on an empty registry.
func (s *Server) seedDemo(ctx context.Context) error {
	apps, err := s.store.ListApps(ctx)
	if err != nil || len(apps) > 0 {
		return err
	}
	demo := []Manifest{
		{Slug: "habits", Name: "Pocket Habits", Description: "Seven habits in green and red.", URL: "http://pocket-habits:8080", PublicURL: "https://habits.example.com", Version: "1.4.0", Publishes: []string{"habits.marked"}},
		{Slug: "kitchen", Name: "Pocket Kitchen", Description: "Recipes, meal plan, shopping, pantry.", URL: "http://pocket-kitchen:8080", PublicURL: "https://kitchen.example.com", Version: "0.9.2", Publishes: []string{"kitchen.cooked", "kitchen.checkout"}},
		{Slug: "inventory", Name: "Pocket Inventory", Description: "What you own and where it is.", URL: "http://pocket-inventory:8080", PublicURL: "https://inventory.example.com", Version: "2.1.0", Publishes: []string{"inventory.adjusted"}},
	}
	for _, m := range demo {
		e := catalog.Resolve(m.Slug, m.Name, "", true)
		raw, _ := json.Marshal(m)
		a := &store.App{Slug: m.Slug, Name: m.Name, Description: m.Description, Icon: "/favicon.svg", Version: m.Version, URL: m.URL, PublicURL: m.PublicURL, EnvPrefix: e.Prefix, Manifest: raw}
		if _, err := s.store.CreateApp(ctx, a); err != nil {
			return err
		}
		sv := &store.Service{Type: e.Type, Name: m.Name, URL: m.URL, PublicURL: m.PublicURL, AppID: a.ID, Enabled: true, Fields: map[string]string{}}
		if err := s.store.SaveService(ctx, sv, e.IsSecret); err != nil {
			return err
		}
	}
	for _, t := range []struct{ typ, url, key, val string }{
		{"jellyfin", "https://jellyfin.example.com", "API_KEY", "demo-jellyfin-key"},
		{"immich", "https://photos.example.com", "API_KEY", "demo-immich-key"},
		{"nextcloud", "https://cloud.example.com", "APP_PASSWORD", "demo-app-password"},
	} {
		e, _ := catalog.Get(t.typ)
		sv := &store.Service{Type: t.typ, Name: e.Name, URL: t.url, Enabled: true, Fields: map[string]string{t.key: t.val}}
		if t.typ == "nextcloud" {
			sv.Fields["USERNAME"] = "demo"
		}
		if err := s.store.SaveService(ctx, sv, e.IsSecret); err != nil {
			return err
		}
	}
	for _, ev := range []store.Event{
		{Topic: "kitchen.cooked", Source: "kitchen", Subject: "recipe:12", Payload: mustJSON(map[string]any{"recipe": "Pancakes", "servings": 4})},
		{Topic: "habits.marked", Source: "habits", Subject: "habit:6", Payload: mustJSON(map[string]any{"habit": "Cooked at home", "date": store.Now()[:10]})},
		{Topic: "inventory.adjusted", Source: "inventory", Subject: "item:88", Payload: mustJSON(map[string]any{"item": "Flour", "delta": -200, "unit": "g"})},
	} {
		e := ev
		if err := s.hub.Publish(ctx, &e); err != nil {
			return err
		}
	}
	s.log.Info("demo data seeded")
	return nil
}
