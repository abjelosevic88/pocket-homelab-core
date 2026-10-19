# Pocket Core contract, version 1

This is the part of Core other software builds against. Anything here changes only with a new major version; everything else in the API may grow freely.

## 1. Tokens

| Prefix | Meaning | Presented as |
|---|---|---|
| `pcreg_` | registration key, box-wide, rotatable | `Authorization: Bearer` on `POST /api/v1/apps/register`, once |
| `pca_` | app token, one per registered app | `Authorization: Bearer` on every app → Core call |
| `pcd_` | device token, one per paired phone or Mac | `Authorization: Bearer` on device → Core and device → app calls |
| `pcu_` | personal token | same |
| `pco_` / `pcr_` | OIDC access / refresh token | OAuth 2 bearer / refresh grant |

An app decides where a bearer token goes by prefix: `pcd_`, `pcu_`, `pco_` → ask Core (`POST /api/v1/auth/introspect` with the app's own `pca_`); anything else → the app's own table. Core returns `{"active": bool, "kind": "user|device|app", "user": {...}, "device": {...}, "scope": ""}`. An app maps `user.email` to its local account and may create one on first sight.

## 2. Manifest (`POST /api/v1/apps/register`)

Required: `slug` (`^[a-z0-9][a-z0-9-]{0,40}$`, stable forever), `name`, `url` (absolute). Optional: `description`, `icon`, `version`, `public_url`, `health_path` (default `/healthz`), `env_prefix` (default: catalog entry for the slug, else upper-cased slug), `publishes` (list of topics), `subscribes` (list of `{topics, url, secret}`), `oidc` (`{redirect_uris, public}`).

Response: `{app, token?, core: {base_url, events_url, stream_url, introspect_url, issuer}, oidc?: {client_id, client_secret?, issuer, discovery}}`. `token` and `client_secret` appear only when minted; persist them. Registering again with the app token updates the record; registering with the registration key for an existing slug re-keys it.

Health: Core GETs `url + health_path` and reads `{"status": "ok", "version": "…"}` (any 2xx counts as up).

## 3. Events

Topic: lowercase, dotted, `^[a-z0-9._-]{1,120}$`; an app publishes only under `<slug>.`. Body: `{"topic", "subject"?, "payload"?}` with `payload` any JSON ≤ 256 KiB. Stored event: `{id, topic, source, subject, payload, created_at}`; `id` is monotonically increasing per box.

Delivery:
- SSE `GET /api/v1/events/stream?topics=a.*,b.c[&typed=1]` — `id:` per message, `Last-Event-ID` or `?after=` replays missed events in order, `: ping` every 25 s.
- Webhook POST of the stored event as JSON with `X-Pocket-Event` (topic), `X-Pocket-Event-ID`, and `X-Pocket-Signature: sha256=<hex HMAC-SHA256(body, secret)>` when a secret is set. Any non-2xx is retried with exponential backoff (10 s doubling, cap 1 h, 8 attempts by default). Deliveries are at-least-once; consumers dedupe on the event id.
- Topic patterns: `*`, `prefix.*`, exact, comma-separated.

Core's own topics: `core.app.registered|removed|ok|down`, `core.device.paired|revoked`, `core.test`.

## 4. Profile (`POST /api/v1/pair/claim`, `GET /api/v1/pair/profile`)

```
{ box: {name, core_url, version, issuer}, user: {id, email, name, role}, device?: {...}, token?: "pcd_…",
  services: [{type, name, group, url, pocket, env: {KEY: value}}], values: {KEY: value}, env: "KEY=value\n…" }
```

`env` is the Pocket Homelab client's env-file format; keys follow the catalog (`GET /api/v1/catalog`): `<PREFIX>_<URL_KEY>` plus `<PREFIX>_<FIELD>`. For registered Pocket apps `<PREFIX>_API_KEY` is the device's own token unless the admin pinned a value. Pairing codes are 8 characters from `ABCDEFGHJKLMNPQRSTUVWXYZ23456789`, valid `PAIR_TTL`, single use; the QR carries `pockethomelab://pair?core=<BASE_URL>&code=<CODE>`.

## 5. OpenID Connect

Discovery at `/.well-known/openid-configuration`. Authorization code with PKCE (`S256`, `plain`), `client_secret_basic`, `client_secret_post` or `none` (public clients must use PKCE). Claims: `sub` (user id as string), `email`, `email_verified`, `name`, `preferred_username`, `groups` (`users`, `admin`). Redirect URIs match exactly. No consent screen.

## 6. Compatibility promise

Within major version 1: fields are only added, never removed or re-typed; token prefixes keep their meaning; topics Core emits keep their names; the env keys in the catalog only grow. Apps should ignore unknown fields.
