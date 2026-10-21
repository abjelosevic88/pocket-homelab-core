# API

Base path `/api/v1`. JSON in and out. Authenticate with the session cookie (browser), `Authorization: Bearer <token>` or `X-API-Key: <token>`; see [configuration.md](configuration.md#tokens-at-a-glance) for the token kinds. Errors are `{"error": "…"}`.

Who may call what: **anyone authenticated** = users, apps and devices; **user** = a person (session, `pcu_`, `pco_`) or a device acting for its owner; **admin** = a user with the admin role; **app** = a registered app's `pca_` token. The registry (`/apps`, `/resolve`), the service list and the bus (`/events`, `/webhooks`) are **admin or app** only; a household member sees their own devices, profile and credentials.

## Auth & account
| Method | Path | Who | Notes |
|---|---|---|---|
| GET | `/auth/status` | – | `needs_setup`, `auth_mode`, `authenticated`, `user`, `version`, `title`, `box_name` |
| POST | `/auth/setup` · `/auth/login` · `/auth/logout` | – | `{email, password, name, box_name}` for setup |
| GET/PUT | `/auth/me` | anyone | the calling principal (`kind`, `user`, `app`, `device`); PUT `{name, password, current_password}` |
| POST | `/auth/introspect` | app, admin | `{token}` → `{active, kind, scope, user, device, app}`; when an app asks about a person outside the audience of the service mirroring it, `{active: false, reason: "not_in_audience"}` |
| GET/POST/DELETE | `/tokens`, `/tokens/{id}` | user | personal access tokens; POST returns `{token, secret}` once |
| GET/POST/PUT/DELETE | `/users`, `/users/{id}` | admin | `{email, name, password, role, disabled}`; creating a person runs every connector set to *create accounts for new people* in the background |

## Registry
| Method | Path | Who | Notes |
|---|---|---|---|
| POST | `/apps/register` | registration key or app token | the [manifest](manifest.md) |
| GET | `/apps` | admin, app | |
| GET | `/apps/{slug}` | admin, app | `{app, webhooks, oidc_client}` |
| GET | `/resolve?slug=` | admin, app | `{url, public_url, status, version}`; without `slug` a map of every app |
| GET | `/people` | admin, app | `[{email, name}]` of every active account, for share pickers (nothing else about anyone) |
| PUT/DELETE | `/apps/{slug}` | admin | edit descriptive fields / remove (cascades to service, webhooks, OIDC client) |
| POST | `/apps/{slug}/rotate` · `/apps/{slug}/check` | admin | new token (shown once) / health check now |

## App links

See [links](links.md): `POST /links/token` and `GET /links/status` (app), `GET /links` · `PUT /links/{id}` (admin), `GET /me/links` · `PUT /me/links/{id}` (person). For a `pcx_` token, `/auth/introspect` answers `{active, kind: "delegated", scope, audience, user, app}` only to the target app.

## Services & catalog
| Method | Path | Who | Notes |
|---|---|---|---|
| GET | `/catalog` | – | every service type with its env prefix and fields |
| GET | `/services` · `/services/{id}` | admin, app | secrets masked as `••••••••`; a masked value sent back in a PUT means "keep" |
| POST/PUT/DELETE | `/services`, `/services/{id}` | admin | `{type, name, url, public_url, fields:{API_KEY:…}, enabled, notes, audience: all\|admins\|users, audience_users:[ids]}`; services that mirror a registered app cannot be deleted or re-typed. For a registered app the audience is enforced, not just hidden: `/oidc/authorize` answers `access_denied` and introspection reports the token inactive for anyone outside it |
| GET | `/me/services` | user | the services in the caller's profile as launcher links: `{id, type, name, group, url, open_url, icon, pocket}`; `open_url` is the app's `…/auth/oidc/start` (derived from its registered callback) so a click lands signed in, else `url`; their URL override if set, else the public URL; no credentials |
| GET | `/users/{id}/credentials` | owner, admin | every service with that person's overrides (masked); members do not see box-wide values |
| PUT/DELETE | `/users/{id}/credentials/{sid}` | owner, admin | `{url?, fields?}` per-person override merged over the service in that person's profile |
| POST | `/services/{id}/test` | admin | plain reachability of `url` |

## Connectors
Core acting on a third-party service: accounts, sign-in, events. See [connectors.md](connectors.md). All **admin** only; members and apps get `403 admin only`, like the other admin service endpoints.

| Method | Path | Who | Notes |
|---|---|---|---|
| GET | `/connectors` | admin | every connector Core has: `{type, name, capabilities:{provision, sso, events}, service_id?, connector?}` |
| GET | `/services/{id}/connector` | admin | `{enabled, provision_on_create, sso_enabled, events_enabled, poll_interval_s, last_poll_at, last_error, type, capabilities, configured, redirects, issuer, oidc_client, polling, push?}`; 404 when the service type has no connector. `push` (connectors whose events arrive by push) = `{per_user, triggers:[{trigger, name}], url, checked_at, received, published, dropped, last_received_at, people:[{user_id, email, name, state: ok\|missing\|needs_key\|error\|off, detail, workflows:[{trigger, name, remote_workflow_id, state, error}], received, published, dropped, last_drop, last_received_at}]}` |
| PUT | `/services/{id}/connector` | admin | any of `{enabled, provision_on_create, sso_enabled, events_enabled, poll_interval_s}`. Flipping `sso_enabled` configures the app immediately and answers `502 {error}` (switch unchanged) when the app refused; turning it on needs `enabled`. Turning events on (`enabled` and `events_enabled` both true) creates the push subscriptions in the app right away and starts a new baseline for the periodic check; turning either off deletes them. Problems are reported in `last_error` (`events: …`) and per person in `push.people` |
| POST | `/services/{id}/connector/test` | admin | read-only check → `{ok, info:{version, admin, users, permissions, detail}, ms}` or `{ok:false, error}` |
| POST | `/services/{id}/connector/poll` | admin | check now: poll (`immich.user_created`) and, with events on, re-create any push subscription deleted in the app → `{ok, events, baseline}` / `{ok:false, error}`; `409` while a poll runs; needs `enabled` |
| POST | `/services/{id}/connector/hooks/recreate` | admin | delete and create every push subscription again (new hook secrets) → `{ok, error?, connector}`; needs events on |
| POST | `/services/{id}/connector/provision` | admin | `{user_id}` or `{all:true}` → `{results:[{user_id, email, status: ok\|linked\|error\|skipped, remote_id, error, note, credential: connector\|own\|none}]}`; needs `enabled` |
| GET | `/services/{id}/connector/accounts` | admin | everyone in the audience (and anyone with an account row): `{user_id, email, name, in_audience, status: ok\|linked\|error\|"", remote_id, error, credential: connector\|own\|""}` |
| GET | `/services/{id}/connector/remote-accounts` | admin | connectors that link by hand (`capabilities.link`, Jellyfin): the app's accounts `[{id, name, admin, disabled}]` |
| PUT | `/services/{id}/connector/accounts/{user_id}` | admin | `{remote_id}` links the person to that account by hand (`manual: true`, status `linked`, the connector's non-secret fields such as `USER_ID` saved as their credential unless they set their own); `{remote_id: ""}` unlinks. `409` when the account is linked to someone else |
| GET | `/users/{id}/connectors` | admin | that person's status in every connected service |

`GET /users/{id}/credentials` items carry `source: user|connector` for overrides that are set.

### Push deliveries (public)

| Method | Path | Who | Notes |
|---|---|---|---|
| POST | `/connectors/hooks/{hookID}` | the app, with header `X-Pocket-Hook: <secret>` | where a connected app (Immich workflow webhook step) delivers one thing that happened. No session or token: the random hook id plus its secret (stored hashed, compared in constant time) authenticate. Unknown hook or wrong/missing secret → `404` and nothing is recorded. Body up to 256 KiB (`413` above), at most 1200 deliveries per hook per minute (`429`). `202 {published: bool, dropped: "<reason>"}` once read — also for deliveries that are dropped (owner not a Core person in the audience, duplicate, …); `400` for a body the connector cannot read |

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
Outside `/api/v1`: `/.well-known/openid-configuration`, `/oidc/authorize`, `/oidc/token`, `/oidc/userinfo`, `/oidc/jwks`, `/oidc/logout`. Clients: `GET/POST /api/v1/oidc/clients`, `PUT/DELETE /api/v1/oidc/clients/{id}` (admin; PUT `{rotate_secret:true}` mints a new secret). A client a connector made carries `service_id`; that service's audience is enforced at `/oidc/authorize` and `/oidc/token`. See [oidc.md](oidc.md).

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
