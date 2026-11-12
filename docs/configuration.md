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
| `SERVICES_FILE` | — | A JSON list of third-party services a Pocket Box runs, applied at every start (see below). |
| `BOX_AGENT` | — | The Pocket Box agent's unix socket. Set on a Pocket Box only: adds Settings → System and the first-run box wizard (see below). |
| `BOX_ADDRESS` | — | The Pocket Box address service's unix socket. Set on a Pocket Box only: adds *Secure address* to Settings → System and the wizard (see below). |
| `BOX_DOWNLOADS` | — | The Pocket Box downloads service's unix socket. Set on a Pocket Box only: adds *Downloads* (Usenet provider, indexers, subtitles) to Settings → System and the wizard when Pocket Watch, Requests or Subtitles are installed (see below). |
| `INSTALLER_SOCKET` / `INSTALLER_TOKEN` | — | The installer agent's unix socket and its token: adds **Install app** and a full **Remove app** to the Apps page. See [installer.md](installer.md). |
| `INSTALLER_ENABLED` | `true` | `false` keeps Install/Remove off on this Core whatever the admin switch says (apps here are set up by hand). |
| `TZ` | – | Container timezone |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `text` | `debug`, `warn`, `error`; `json` |
| `TRUST_PROXY` | `true` | Honour `X-Forwarded-For` |
| `AUTH_MODE` | `local` | `local` (built-in accounts), `proxy` (trust `AUTH_PROXY_HEADER`), `none` (single implicit admin, LAN only) |
| `AUTH_PROXY_HEADER` / `AUTH_PROXY_EMAIL_HEADER` | `Remote-User` / `Remote-Email` | Headers for proxy mode |
| `SESSION_TTL` | `720h` | Browser session lifetime |
| `SECURE_COOKIES` | `false` | Set `true` behind HTTPS. On plain http it makes every sign-in bounce back to the login page (Core warns at startup and on the login page) |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` / `ADMIN_NAME` | – | Create the first admin on an empty database (otherwise the UI asks). Ignored afterwards; Core warns when they no longer match. Change a password with `pocket-core reset-password` |
| `REGISTRATION_KEY` | generated | The key apps present on first registration. Generated and shown under Settings when empty. |
| `PAIR_TTL` | `10m` | How long a pairing code stays valid |
| `HEALTH_INTERVAL` | `60s` | How often registered apps are polled on their health path |
| `EVENT_RETENTION` | `720h` | Events older than this are purged hourly |
| `WEBHOOK_ATTEMPTS` | `8` | Delivery attempts before a webhook delivery is marked failed (10 s, 20 s, 40 s… capped at 1 h) |
| `DEMO_DATA` | `false` | Seed three fake apps, three services and a few events on an empty database |
| `MCP_OAUTH_ENABLED` | `false` | Enable Core account sign-in for status-only MCP; requires `AUTH_MODE=local` and HTTPS `BASE_URL` origin (HTTP loopback for development) |
| `MCP_APP_TOOLS_ENABLED` | `false` | Enable curated per-account read/create/update tools. Requires `MCP_OAUTH_ENABLED`; each connection must approve content scopes. |
| `MCP_TOKEN_HASH` | disabled | Hex SHA-256 digest of a dedicated status-only MCP bearer token; see [MCP setup](mcp.md) |
| `MCP_APPS` | none | Required with `MCP_TOKEN_HASH`: comma-separated registered app slugs, or `*` for all |
| `MCP_CLIENTS_FILE` | disabled | JSON file of named clients with token hashes, app grants and expiry; reloaded each request. Use instead of `MCP_TOKEN_HASH`/`MCP_APPS`. See [MCP setup](mcp.md). |
| `PUSH_RELAY_URL` | `https://push.pockethomelab.com` | The Pocket Push relay that forwards Pocket Hub notifications to Apple; see [push.md](push.md) |
| `PUSH_RELAY_TOKEN` | none | The relay's `pps_` sender token for this box. Without it (and without `PUSH_NAMES_ACCOUNT`) notifications report `no_relay` |
| `PUSH_NAMES_ACCOUNT` | none | Path to a Pocket Names account file `{"name","token"}`; with no `PUSH_RELAY_TOKEN`, Core enrolls at the relay with it and keeps the sender token in `DATA_DIR/push-relay.token` |
| `PUSH_TOPICS` | `org.bjelke.pockethub,org.bjelke.pockethub.mac` | APNs topics (app IDs) a device may register for |

## Auth modes

- **local** — accounts with email + password. The first account created becomes admin. This is the mode a Pocket Box ships with: the same account signs in to every Pocket app through OpenID Connect.
- **proxy** — trust a forward-auth proxy (Authelia, Authentik, Tinyauth…). Users are created on first visit. Keep `/api/*`, `/oidc/*` and `/.well-known/*` outside the forward-auth rule: apps, phones and relying parties call them with tokens.
- **none** — no login at all; one implicit admin. Only for a trusted LAN or `make dev`.

### Signing in through PocketID

Core is the OpenID provider for the Pocket apps; it can itself sign people in through another provider. Set `SSO_*` and the login page gets a “Sign in with PocketID” button. When an app sends someone to Core's `/oidc/authorize` without a Core session, they go to Core's login, then to PocketID (passkey), back to Core, and on to the app — one hop each way, with no password stored anywhere. Core checks the upstream ID token's RS256 signature against the provider's keys, and its issuer, audience, expiry and nonce, then matches the person to a Core user by **verified** email (disabled users are refused). Once it works, `PASSWORD_LOGIN=false` turns passwords off; app, device and API tokens are unaffected.

**Set it up in Settings → Single sign-on → How people sign in to Core** (admins). It takes over from `SSO_*` and
`PASSWORD_LOGIN` the moment it is saved, without a restart; *Use the server's settings* hands control back to the
environment. The client secret is stored sealed with the box key and never shown again. Passwords can only be switched
off while a provider is on and answers its discovery document, and the provider cannot be switched off while passwords
are off — the box cannot be locked.

In PocketID, step by step:

1. *Settings → OIDC Clients → Add OIDC client*. Name: Pocket Core.
2. **Callback URLs:** `BASE_URL/api/v1/auth/sso/callback` (e.g. `https://core.example.com/api/v1/auth/sso/callback`;
   the Settings page shows yours with a Copy button).
3. **Public client: off** — Core keeps a client secret (a confidential client).
4. **PKCE: on** — Core always sends a challenge.
5. **Group restriction: off**, or pick the groups whose members may sign in. PocketID v2 starts new clients restricted
   to no groups, which refuses everyone with "You are not allowed to access this service".
6. Save; copy the **Client ID** and the **Client secret** (shown once) into Core.
7. **Issuer:** PocketID's own address (`https://auth.example.com`). *Test* checks Core can read its configuration.
8. For each person, the PocketID email must equal their Core email and **Email verified** must be on.
9. Save, try *Continue with PocketID* in a private window, and only then switch passwords off if you want to.

"New people from PocketID" (`SSO_AUTO_CREATE`) is off by default: only people who already have a Core account can sign in.
Authentik, Keycloak, Authelia and other OpenID Connect providers work the same way (confidential client, this callback,
scopes `openid email profile`).

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

## Services a box sets up (`SERVICES_FILE`)

A Pocket Box makes the third-party services it ships with ready before anyone signs in. It creates an admin account and key in each, then hands them to Core in a file:

```json
[
  {
    "type": "navidrome",
    "name": "Navidrome",
    "url": "http://127.0.0.1:4533",
    "public_url": "https://navidrome.pocket.local",
    "fields": { "USER": "pocket-box", "PASSWORD": "…" },
    "audience": "all",
    "connector": { "enabled": true, "provision_on_create": true }
  }
]
```

| Part | Applied |
|---|---|
| `url`, `public_url`, `fields` | at every start. The box owns these and may have replaced a key |
| `name`, `audience` | only when Core creates the service; an admin's later edits stand |
| `connector` | only when Core creates the connector; an admin's later switches stand. A new connector set to make accounts provisions everyone already in Core |

A type a registered Pocket app owns is skipped. An entry without a type stops Core from starting. The first account made on Core's setup screen gets its accounts in every connector set to make them, like people added later under Users.

## The box itself (`BOX_AGENT`)

On a Pocket Box, Core also sets up the machine it runs on. Core runs unprivileged, so a small root service, the **box agent**, makes the changes. It answers JSON on a unix socket that only Core's group can open. Core passes an admin's requests through and checks nothing but who is asking; the agent checks the values.

| Core (admins) | Agent | What |
|---|---|---|
| `GET /api/v1/box/system/status` | `GET /v1/status` | name, time zone, addresses, Wi-Fi, Tailscale, console password set, disk encryption |
| `GET …/timezones` · `PUT …/timezone {timezone}` | same | the zone; the Pocket apps restart a moment later to pick it up |
| `GET …/wifi` · `POST …/wifi {ssid, password}` | same | nearby networks (`available: false` without a Wi-Fi card), join one |
| `POST …/tailscale {auth_key?}` · `DELETE …/tailscale` | same | join the tailnet (without a key the answer carries `auth_url`), leave it |
| `PUT …/password {password}` | same | the console account `pocket`, locked until set here |
| `POST …/wizard` | same | the setup wizard is finished |
| `GET …/updates` · `POST …/updates` · `PUT …/updates {auto}` | same | this release, the last update run (updated, rolled back, …), check now, nightly updates on or off |
| `POST …/restart` | same | restart the box (to finish a kernel update) |

`GET /api/v1/auth/status` carries `box_system` (there is an agent) and `box_wizard`. `box_wizard` is true after the first account exists and until the wizard is finished. While it's true, an admin who opens Core lands on **/welcome**, which walks through time zone, network, Tailscale, the console password and the disk; *Do this later* finishes it too. An agent that doesn't answer never traps anyone in the wizard. Without `BOX_AGENT` none of this exists: the routes answer 404 and Settings has no System tab.

### The box's secure address (`BOX_ADDRESS`)

How phones and computers get a padlock for the box, chosen first on the box's landing page (`http://pocket.local`, before anyone has an account) and changeable here by an admin at any time:

- **Pocket's free address** (recommended): `<random>.box.pockethomelab.com` from Pocket Names, with a Let's Encrypt certificate.
- **The owner's own domain on Cloudflare**: `<name>.<their zone>`. *Sign in with Cloudflare* (OAuth with PKCE as a public client, scopes `zone.read dns.write offline_access`; Cloudflare sends the browser back through `names.pockethomelab.com/cloudflare/back` to the page that started it) or a pasted API token (a prefilled *Create token* link: Zone · Read, DNS · Edit). The box keeps two A records pointed at its home address and gets its certificate over DNS-01 itself.
- **The box's own certificate**: no public name; each device installs the box's CA.

| Core (admins) | Address service | What |
|---|---|---|
| `GET /api/v1/box/address` | `GET /v1/address` | the mode, the address in use, how far getting it got, the last problem, whether *Sign in with Cloudflare* is on offer |
| `PUT /api/v1/box/address {mode, zone_id?, sub?, token? \| pending?}` | same | choose; the box moves over once the new address works, then lets the old one go |
| `POST …/address/cloudflare/zones {token}` | same | the zones a pasted token can change |
| `POST …/address/cloudflare/start {return_url}` · `POST …/cloudflare/finish {code, state}` | same | *Sign in with Cloudflare*: the URL to open, then the zones and a pending sign-in |

`GET /api/v1/auth/status` carries `box_address`.

### Downloads on the box (`BOX_DOWNLOADS`)

On a box with Pocket Watch, Requests or Subtitles, the box runs Radarr, Sonarr, Prowlarr, SABnzbd (and Lidarr with Pocket Music, Bazarr with Subtitles) on loopback and wires them together itself. The owner only types in a Usenet provider and an indexer's API key (and, for subtitles, the languages and an optional OpenSubtitles.com login) under Settings → System → **Downloads** (deep link `/settings/system#downloads`, which Pocket Requests uses) or the wizard's optional *Downloads* step. Both appear only when the service's `installed` is true.

| Core (admins) | Downloads service | What |
|---|---|---|
| `GET /api/v1/box/downloads` | `GET /v1/downloads` | installed, ready, the services, the Usenet server (never its password), the indexers with their status, subtitles, the provider and indexer presets |
| `PUT …/downloads/usenet {host, port, ssl, username, password?, connections}` · `DELETE …/usenet` | same | save the Usenet server in SABnzbd and test it; remove it |
| `POST …/downloads/indexers {preset?, name?, url?, api_key}` · `DELETE …/indexers/{id}` | same | add an indexer in Prowlarr and test it; remove one |
| `PUT …/downloads/subtitles {languages, opensubtitles?: {username, password}}` | same | Bazarr's languages and OpenSubtitles.com login |

A failed test still answers 200 with `status: "error"` and a plain message; 400 is for bad input (`{"error": "…"}`). `GET /api/v1/auth/status` carries `box_downloads`.
