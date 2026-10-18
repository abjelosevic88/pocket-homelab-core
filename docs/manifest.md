# App manifest

A Pocket app joins the box with one request on every start:

```
POST {CORE_URL}/api/v1/apps/register
Authorization: Bearer <registration key, or the app's own token later>
Content-Type: application/json
```

```json
{
  "slug": "kitchen",
  "name": "Pocket Kitchen",
  "description": "Recipes, meal plan, shopping list, pantry.",
  "icon": "/favicon.svg",
  "version": "1.2.0",
  "url": "http://pocket-kitchen:8080",
  "public_url": "https://kitchen.example.com",
  "health_path": "/healthz",
  "env_prefix": "KITCHEN",
  "publishes": ["kitchen.cooked", "kitchen.checkout"],
  "subscribes": [
    { "topics": "habits.*,inventory.adjusted", "url": "/api/v1/core/events", "secret": "a-shared-hmac-key" }
  ],
  "oidc": { "redirect_uris": ["https://kitchen.example.com/auth/oidc/callback"] }
}
```

| Field | Required | Meaning |
|---|---|---|
| `slug` | yes | Lowercase id, stable across versions. Topics the app publishes must start with `<slug>.` |
| `name`, `description`, `icon`, `version` | name | Shown in Core and handed to paired devices. `icon` may be a path under `url`. |
| `url` | yes | Where Core and sibling apps reach the app (container name, LAN address). Health checks hit `url + health_path`. |
| `public_url` | no | What a phone opens; defaults to `url`. |
| `health_path` | no | Default `/healthz`. Core reads `{"status","version"}` from it every `HEALTH_INTERVAL`. |
| `env_prefix` | no | Key prefix in a device profile (`KITCHEN_URL`, `KITCHEN_API_KEY`). Defaults to the catalog entry for the slug, else the upper-cased slug. |
| `publishes` | no | Informational list of topics. |
| `subscribes` | no | Webhooks Core should create. `url` may be relative to `url`. Deliveries carry `X-Pocket-Signature: sha256=<hmac>` when `secret` is set. Idempotent on `url`. |
| `oidc` | no | Ask for an OpenID Connect client `pocket-<slug>`. `public: true` makes a PKCE-only client without a secret. |

## The response

```json
{
  "app": { "...": "the stored app" },
  "token": "pca_…",                         // only on first registration or after a re-key
  "core": {
    "base_url": "https://core.example.com",
    "events_url": "https://core.example.com/api/v1/events",
    "stream_url": "https://core.example.com/api/v1/events/stream",
    "introspect_url": "https://core.example.com/api/v1/auth/introspect",
    "issuer": "https://core.example.com"
  },
  "oidc": { "client_id": "pocket-kitchen", "client_secret": "pcs_…", "issuer": "…", "discovery": "…/.well-known/openid-configuration" }
}
```

Persist `token`. Afterwards register with it instead of the key; Core updates the record and never returns a new token unless an admin rotates it (then register with the key again and a fresh one comes back).

## What registration does

1. Creates or updates the app in the registry and starts health polling.
2. Mirrors it as a **service** so every paired device gets `<PREFIX>_URL` and `<PREFIX>_API_KEY` in its profile, the latter being the device's own Core token.
3. Creates the webhooks from `subscribes`.
4. Creates the OIDC client from `oidc`.
5. Publishes `core.app.registered`.

## Accepting device tokens

Paired devices call your app with `Authorization: Bearer pcd_…`. Validate it through Core instead of your own user table:

```
POST /api/v1/auth/introspect     Authorization: Bearer <your pca_ token>
{"token": "pcd_…"}
→ {"active": true, "kind": "device", "user": {"id": 1, "email": "…", "name": "…", "role": "admin"}, "device": {"id": 3, "name": "iPhone"}}
```

The Go SDK ships this as middleware: `r.Use(core.Middleware)` and read `sdk.FromContext(ctx)`. Tokens starting with `pcd_`, `pcu_` or `pco_` are Core's; anything else is yours.

## Go SDK

```go
import "github.com/abjelosevic88/pocket-homelab-core/sdk"

core := sdk.New(os.Getenv("CORE_URL"), sdk.Options{TokenFile: cfg.DataDir + "/core.token"})
if core.Configured() {
    reg, err := core.Register(ctx, os.Getenv("CORE_REGISTRATION_KEY"), sdk.Manifest{
        Slug: "kitchen", Name: "Pocket Kitchen", URL: cfg.InternalURL, PublicURL: cfg.BaseURL, Version: version,
        Publishes: []string{"kitchen.cooked"},
        Subscribes: []sdk.Subscription{{Topics: "habits.*", URL: "/api/v1/core/events", Secret: hookSecret}},
    })
    // reg.OIDC.ClientSecret is set on first registration — store it.
}
_ = core.Publish(ctx, "kitchen.cooked", "recipe:12", map[string]any{"recipe": "Pancakes"})
habits, _ := core.Resolve(ctx, "habits")       // habits.URL replaces HABITS_URL
go core.Stream(ctx, "inventory.*", func(e sdk.Event) { … })   // SSE, resumes after reconnects
```

Every call returns `sdk.ErrNotConfigured` when `CORE_URL` is empty, so an app keeps working on a lab without Core.
