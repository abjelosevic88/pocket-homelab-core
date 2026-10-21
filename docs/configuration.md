# Configuration

Everything is read from environment variables; every value has a default so a bare `docker run -v ./data:/data` works.

| Variable | Default | What it does |
|---|---|---|
| `PORT` / `HOST` | `8080` / `0.0.0.0` | Listen address |
| `BASE_URL` | `http://localhost:8080` | **The URL phones and apps reach Core at.** Goes into every pairing QR code and is the default OIDC issuer. Set it before pairing anything. |
| `OIDC_ISSUER` | `BASE_URL` | Override the issuer if Core is reached under a second name |
| `SSO_ISSUER` / `SSO_CLIENT_ID` / `SSO_CLIENT_SECRET` | – | Sign in **to Core** through an upstream OpenID Connect provider (PocketID, Authelia…). The callback is `BASE_URL/api/v1/auth/sso/callback` |
| `SSO_NAME` | `single sign-on` | Button label: “Sign in with …” |
| `SSO_AUTO_CREATE` | `false` | Give an unknown (verified) email a member account instead of refusing it |
| `PASSWORD_LOGIN` | `true` | `false` = only the upstream provider. Stays on without `SSO_ISSUER`, so nobody gets locked out |
| `DATA_DIR` | `./data` (`/data` in the image) | SQLite database, secret key, backups |
| `DB_PATH` / `SECRET_PATH` | `$DATA_DIR/pocket-core.db` / `$DATA_DIR/secret.key` | Override locations. The secret key encrypts stored service credentials; back it up with the database. |
| `TZ` | – | Container timezone |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `text` | `debug`, `warn`, `error`; `json` |
| `TRUST_PROXY` | `true` | Honour `X-Forwarded-For` |
| `AUTH_MODE` | `local` | `local` (built-in accounts), `proxy` (trust `AUTH_PROXY_HEADER`), `none` (single implicit admin, LAN only) |
| `AUTH_PROXY_HEADER` / `AUTH_PROXY_EMAIL_HEADER` | `Remote-User` / `Remote-Email` | Headers for proxy mode |
| `SESSION_TTL` | `720h` | Browser session lifetime |
| `SECURE_COOKIES` | `false` | Set `true` behind HTTPS |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` / `ADMIN_NAME` | – | Create the first admin on an empty database (otherwise the UI asks) |
| `REGISTRATION_KEY` | generated | The key apps present on first registration. Generated and shown under Settings when empty. |
| `PAIR_TTL` | `10m` | How long a pairing code stays valid |
| `HEALTH_INTERVAL` | `60s` | How often registered apps are polled on their health path |
| `EVENT_RETENTION` | `720h` | Events older than this are purged hourly |
| `WEBHOOK_ATTEMPTS` | `8` | Delivery attempts before a webhook delivery is marked failed (10 s, 20 s, 40 s… capped at 1 h) |
| `DEMO_DATA` | `false` | Seed three fake apps, three services and a few events on an empty database |
| `MCP_OAUTH_ENABLED` | `false` | Enable Core account sign-in for status-only MCP; requires `AUTH_MODE=local` and HTTPS `BASE_URL` origin (HTTP loopback for development) |
| `MCP_TOKEN_HASH` | disabled | Hex SHA-256 digest of a dedicated status-only MCP bearer token; see [MCP setup](mcp.md) |
| `MCP_APPS` | none | Required with `MCP_TOKEN_HASH`: comma-separated registered app slugs, or `*` for all |
| `MCP_CLIENTS_FILE` | disabled | JSON file of named clients with token hashes, app grants and expiry; reloaded each request. Use instead of `MCP_TOKEN_HASH`/`MCP_APPS`. See [MCP setup](mcp.md). |

## Auth modes

- **local** — accounts with email + password. The first account created becomes admin. This is the mode a Pocket Box ships with: the same account signs in to every Pocket app through OpenID Connect.
- **proxy** — trust a forward-auth proxy (Authelia, Authentik, Tinyauth…). Users are created on first visit. Keep `/api/*`, `/oidc/*` and `/.well-known/*` outside the forward-auth rule: apps, phones and relying parties call them with tokens.
- **none** — no login at all; one implicit admin. Only for a trusted LAN or `make dev`.

### Signing in through PocketID

Core is the OpenID provider for the Pocket apps; it can itself sign people in through another provider. Set `SSO_*` and the login page gets a “Sign in with PocketID” button. When an app sends someone to Core's `/oidc/authorize` without a Core session, they go to Core's login, then to PocketID (passkey), back to Core, and on to the app — one hop each way, with no password stored anywhere. Core checks the upstream ID token's RS256 signature against the provider's keys, and its issuer, audience, expiry and nonce, then matches the person to a Core user by **verified** email (disabled users are refused). Once it works, `PASSWORD_LOGIN=false` turns passwords off; app, device and API tokens are unaffected.

In PocketID: *OIDC Clients → Add*, callback `BASE_URL/api/v1/auth/sso/callback`, PKCE on, group restriction off (or add yourself to an allowed group).

## Tokens at a glance

| Prefix | Who holds it | Issued by |
|---|---|---|
| `pcreg_` | nobody permanently: apps present it once | Settings → App registration key |
| `pca_` | a registered Pocket app | the registration response |
| `pcd_` | a paired phone or Mac | pairing claim |
| `pcu_` | a person's script, Raycast, Homepage | Users → My API tokens |
| `pco_` / `pcr_` | an OIDC relying party | `/oidc/token` |
| `pcs_` | an OIDC client secret | Settings → Single sign-on |
| `pcma_` / `pcmr_` | an approved MCP assistant | OAuth access/refresh tokens, isolated from ordinary API credentials |
| `mcps_` | a confidential MCP OAuth client | Dynamic registration; not a user access token |
| `pcm_` | an MCP status client | `pocket-core mcp setup`; dedicated status-only credential |

Tokens go in `Authorization: Bearer …` (or `X-API-Key` for the ordinary API).
MCP accepts only the Authorization bearer header and its own credential; it does
not accept household/app tokens. Core stores only hashes.

## Lockouts

Ten failed sign-ins from one IP or against one account, ten bad pairing codes from one IP, or ten bad registration keys from one IP block that key for 15 minutes (in memory; a restart clears it). Cookie-authenticated POST/PUT/DELETE requests must carry an `Origin` or `Referer` on Core's own host; bearer-token calls are not subject to this.

## CLI

```
pocket-core                        start the server (default)
pocket-core backup [-o FILE]       write a consistent SQLite backup
pocket-core reset-password [-email USER] [-password NEW]
pocket-core registration-key       print the key apps use to register
pocket-core pair [-email USER]     print a pairing code from the console
pocket-core connector [-url URL] [-token TOKEN] <type> <action>
                                   drive a connector through the running server (see docs/connectors.md)
pocket-core version
pocket-core mcp setup [-apps notes,tasks] [-name codex] [-dir ./mcp-secrets] [-url URL] [-ttl 2160h]
                                   generate private client configuration without opening the database
pocket-core mcp check [-url URL] [-app notes]
                                   call MCP using POCKET_MCP_TOKEN; omit -app to list allowed apps
```

Inside the container: `docker exec pocket-core pocket-core pair`. `connector` talks to the running server's admin API with an admin's personal token (`-token` or `POCKET_CORE_TOKEN`), so it never writes the database itself.

`DATA_DIR/connectors/` holds the backups connectors write before they change an app's settings (e.g. Immich's system config); they contain that app's secrets.

## What to back up

`DATA_DIR` as a whole: the database, `secret.key` (without it stored credentials cannot be decrypted) and `backups/`. `GET /api/v1/backup` or `pocket-core backup` produce a consistent copy of the database while it runs.
