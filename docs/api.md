# API

Base path `/api/v1`. JSON in and out. Authenticate with the session cookie (browser), `Authorization: Bearer <token>` or `X-API-Key: <token>`; see [configuration.md](configuration.md#tokens-at-a-glance) for the token kinds. Errors are `{"error": "…"}`.

Who may call what: **anyone authenticated** = users, apps and devices; **user** = a person (session, `pcu_`, `pco_`) or a device acting for its owner; **admin** = a user with the admin role; **app** = a registered app's `pca_` token. The registry (`/apps`, `/resolve`), the service list and the bus (`/events`, `/webhooks`) are **admin or app** only; a household member sees their own devices, profile and credentials.

## Auth & account
| Method | Path | Who | Notes |
|---|---|---|---|
| GET | `/auth/status` | – | `needs_setup`, `auth_mode`, `authenticated`, `user`, `version`, `title`, `box_name` |
| POST | `/auth/setup` · `/auth/login` · `/auth/logout` | – | `{email, password, name, box_name}` for setup |
| GET/PUT | `/auth/me` | anyone | the calling principal (`kind`, `user`, `app`, `device`); PUT `{name, password, current_password}` |
| POST | `/auth/introspect` | app, admin | `{token}` → `{active, kind, scope, user, device, app}` |
| GET/POST/DELETE | `/tokens`, `/tokens/{id}` | user | personal access tokens; POST returns `{token, secret}` once |
| GET/POST/PUT/DELETE | `/users`, `/users/{id}` | admin | `{email, name, password, role, disabled}` |

## Registry
| Method | Path | Who | Notes |
|---|---|---|---|
| POST | `/apps/register` | registration key or app token | the [manifest](manifest.md) |
| GET | `/apps` | admin, app | |
| GET | `/apps/{slug}` | admin, app | `{app, webhooks, oidc_client}` |
| GET | `/resolve?slug=` | admin, app | `{url, public_url, status, version}`; without `slug` a map of every app |
| PUT/DELETE | `/apps/{slug}` | admin | edit descriptive fields / remove (cascades to service, webhooks, OIDC client) |
| POST | `/apps/{slug}/rotate` · `/apps/{slug}/check` | admin | new token (shown once) / health check now |

## Services & catalog
| Method | Path | Who | Notes |
|---|---|---|---|
| GET | `/catalog` | – | every service type with its env prefix and fields |
| GET | `/services` · `/services/{id}` | admin, app | secrets masked as `••••••••`; a masked value sent back in a PUT means "keep" |
| POST/PUT/DELETE | `/services`, `/services/{id}` | admin | `{type, name, url, public_url, fields:{API_KEY:…}, enabled, notes, audience: all\|admins\|users, audience_users:[ids]}`; services that mirror a registered app cannot be deleted or re-typed |
| GET | `/me/services` | user | the services in the caller's profile as launcher links: `{id, type, name, group, url, icon, pocket}`; their URL override if set, else the public URL; no credentials |
| GET | `/users/{id}/credentials` | owner, admin | every service with that person's overrides (masked); members do not see box-wide values |
| PUT/DELETE | `/users/{id}/credentials/{sid}` | owner, admin | `{url?, fields?}` per-person override merged over the service in that person's profile |
| POST | `/services/{id}/test` | admin | plain reachability of `url` |

## Events
See [events.md](events.md). `POST /events`, `GET /events`, `/events/{id}`, `/events/topics`, `/events/stream`; `GET/POST /webhooks`, `PUT/DELETE /webhooks/{id}`, `POST /webhooks/{id}/test`, `GET /webhooks/{id}/deliveries` (admin, or the app that owns the webhook).

## Pairing & devices
| Method | Path | Who | Notes |
|---|---|---|---|
| POST | `/pair` | user | `{user_id?}` (admin may pair for someone else) → `{code, url, qr_png, qr_svg, expires_at, ttl_s}` |
| GET | `/pair/{code}` | owner/admin | `{claimed, device}` for the UI to poll |
| GET | `/pair/{code}/qr.png` · `.svg` | – | the QR image |
| POST | `/pair/claim` | – | `{code, name, platform}` → the [profile](pairing.md) with `token` |
| GET | `/pair/profile?format=env` | user/device | the profile again (device tokens get their `pcd_` filled in; users get a preview) |
| GET | `/devices` · DELETE `/devices/{id}` | user (own) / admin (all) | |

## Box
| Method | Path | Who | Notes |
|---|---|---|---|
| GET | `/overview` | anyone | the home page in one call |
| GET | `/system` | anyone | version, uptime, counts, config summary |
| GET/PUT | `/settings` | admin | `{title, box_name, registration_key}` |
| POST | `/settings/registration-key` | admin | rotate |
| GET | `/backup` | admin | SQLite copy |
| GET | `/healthz` · `/readyz` · `/metrics` | – | liveness, readiness, Prometheus |

## OpenID Connect
Outside `/api/v1`: `/.well-known/openid-configuration`, `/oidc/authorize`, `/oidc/token`, `/oidc/userinfo`, `/oidc/jwks`, `/oidc/logout`. Clients: `GET/POST /api/v1/oidc/clients`, `PUT/DELETE /api/v1/oidc/clients/{id}` (admin; PUT `{rotate_secret:true}` mints a new secret). See [oidc.md](oidc.md).

## Homepage widget

```yaml
- Pocket Core:
    icon: https://core.example.com/favicon.svg
    href: https://core.example.com
    widget:
      type: customapi
      url: https://core.example.com/api/v1/overview
      headers: { Authorization: "Bearer pcu_…" }
      mappings:
        - { field: apps_up, label: Apps up }
        - { field: services_enabled, label: Services }
        - { field: listeners, label: Listeners }
```
