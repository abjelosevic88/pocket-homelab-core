<p align="center">
  <img src="web/public/favicon.svg" width="72" alt="">
</p>
<h1 align="center">Pocket Core</h1>
<p align="center">
  The hub of a <b>Pocket Box</b>: the registry Pocket apps announce themselves to, the event bus between them, the OpenID Connect provider they all log in with, and the pairing service that hands the <a href="https://pockethomelab.com">Pocket Homelab</a> app every service on the box in one QR code.<br>
  <b>One binary · one SQLite file · one volume.</b>
</p>
<p align="center">
  <a href="https://github.com/abjelosevic88/pocket-homelab-core/actions"><img src="https://github.com/abjelosevic88/pocket-homelab-core/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/abjelosevic88/pocket-homelab-core/pkgs/container/pocket-homelab-core"><img src="https://img.shields.io/badge/ghcr.io-amd64%20%7C%20arm64-blue" alt="image"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT"></a>
</p>

---

Every Pocket app is a small Go server with its own SQLite file. They already talk to each other: Kitchen keeps its pantry in Inventory, posts receipts to Split and marks a Habits habit. Until now that meant an `INVENTORY_URL` and an `INVENTORY_TOKEN` in every app's environment, an API key copied from six admin pages into the phone, and a separate login everywhere. Pocket Core is the piece in the middle that makes all of that one thing:

- **A registry.** An app starts, POSTs its manifest, and is known: name, icon, version, where it lives, what it publishes, what it wants to hear. Core polls its health and tells the others where it is (`GET /resolve?slug=habits`). No more `*_URL` variables.
- **An event bus.** `POST /events` with a topic and a payload. Core stores it, streams it to live listeners over Server-Sent Events (with replay after a reconnect) and POSTs it to webhooks with HMAC signatures and retries. “Cooked it” in Kitchen reaches Habits, Day and n8n without Kitchen knowing any of them exist.
- **Single sign-on.** Core is an OpenID Connect provider: one account per person signs in to every Pocket app and to Immich, Nextcloud, Paperless, Jellyfin or Audiobookshelf on the same box. Pocket apps get their client just by asking in the manifest.
- **Pairing.** Tap *Pair a device*, scan the QR with Pocket Homelab, done: the phone receives every service on the box with its own token, in exactly the env-file shape the client already imports. Revoke the device later and the token dies everywhere at once.
- **A service catalog** mirroring the client's 44 integrations. Add Jellyfin with its API key once; every paired device gets `JELLYFIN_URL` and `JELLYFIN_API_KEY`. Secrets are encrypted at rest.
- **A household, not a user.** Admins and members. Each service says who receives it, so the NAS key never lands on a kid's phone, and every person can carry their own Jellyfin or Navidrome login while the rest stays shared. Members see their devices and their logins, nothing else.
- **Connectors.** Core can make each person's account in a third-party app and hand them their own key, switch on “Sign in with Pocket Core” there, and turn what happens in it into events. Audiobookshelf, Forgejo, Immich, Jellyfin, Jellyseerr, Kavita, Nextcloud and Paperless-ngx so far, plus events from Radarr, Sonarr, Lidarr, Prowlarr, Chaptarr, Komodo, Backrest, Beszel, Scrutiny, Speedtest Tracker, Changedetection.io, Uptime Kuma, Dawarich and Maloja; off until you switch them on.
- **App links.** One app can act for a person in another with no tokens pasted between them. Garage books a fill-up as a Money transaction, for every person on the box. The app asks for it in its manifest (`uses`) and the target says what it allows (`offers`). Core issues a five-minute token scoped to that one app and person, with a switch for the admin and one for each person.
- **Device tokens apps can trust.** A phone calls Pocket Habits with the token Core gave it; Habits asks Core “is this real?” (`/auth/introspect`) instead of keeping its own user list. The Go SDK does this in one middleware.

Core is not a cloud. It runs on the box, next to the apps, and the only thing it ever talks to outside the LAN is whatever you point a webhook at.

## Run it

```sh
docker run -d --name pocket-core -p 8080:8080 -v ./data:/data \
  -e BASE_URL=https://core.example.com -e SECURE_COOKIES=true \
  ghcr.io/abjelosevic88/pocket-homelab-core:latest
```

or `docker compose up -d` with the included `docker-compose.yml`, or the single binary from the releases page, or `nix run ./deploy/nix`. Open it, create the admin account, and:

1. **Settings** shows the *registration key*. Give every Pocket app `CORE_URL=https://core.example.com` and `CORE_REGISTRATION_KEY=…`; they appear under **Apps** on their next start.
2. **Services → Add service** for the rest of the box: Jellyfin, Immich, Nextcloud… with their credentials.
3. **Pair a device**, scan, open Pocket Homelab. Everything is there.

`BASE_URL` matters: it is baked into pairing codes and is the OIDC issuer. Set it to the address phones and apps really use before pairing anything.

## For app authors

```go
core := sdk.New(os.Getenv("CORE_URL"), sdk.Options{TokenFile: dataDir + "/core.token"})
reg, _ := core.Register(ctx, os.Getenv("CORE_REGISTRATION_KEY"), sdk.Manifest{
    Slug: "kitchen", Name: "Pocket Kitchen", URL: "http://pocket-kitchen:8080", PublicURL: baseURL, Version: version,
    Publishes:  []string{"kitchen.cooked", "kitchen.checkout"},
    Subscribes: []sdk.Subscription{{Topics: "inventory.*", URL: "/api/v1/core/events", Secret: hookSecret}},
    OIDC:       &sdk.OIDCRequest{RedirectURIs: []string{baseURL + "/auth/oidc/callback"}},
})
r.Use(core.Middleware)                                    // accept pcd_/pcu_/pco_ tokens from paired devices
_ = core.Publish(ctx, "kitchen.cooked", "recipe:12", map[string]any{"recipe": "Pancakes"})
habits, _ := core.Resolve(ctx, "habits")                  // habits.URL
```

Every call is a no-op error when `CORE_URL` is empty, so the same binary runs on a lab that has no Core. Manifest format, response and the introspection contract: [docs/manifest.md](docs/manifest.md).

## Security notes

Core holds every credential on the box, so a few choices are deliberate:

- Service credentials are AES-GCM encrypted with `DATA_DIR/secret.key`; every token Core issues is stored as a SHA-256 hash. Back up the key with the database and keep `DATA_DIR` at mode 0700. Point `SECRET_PATH` somewhere else if you want the key on different storage.
- Sign-in, pairing claims and app registration lock an IP (and, for sign-in, the account) for 15 minutes after 10 failures. Pairing codes are single-use and expire in 10 minutes.
- Session cookies are `HttpOnly`, `SameSite=Lax`, `Secure` behind HTTPS, and state-changing cookie requests must come from Core's own origin. Bearer tokens are exempt because a cross-site form cannot attach one.
- There is no consent screen: one box, one household. Do not point strangers' relying parties at it.
- Core is meant to sit on a LAN or tailnet. If you expose it to the internet, put it behind HTTPS and keep `BASE_URL` on that name.

Report anything you find privately before opening an issue.

## Docs

| | |
|---|---|
| [docs/configuration.md](docs/configuration.md) | every environment variable, auth modes, token prefixes, CLI, what to back up |
| [docs/manifest.md](docs/manifest.md) | how an app registers; the Go SDK |
| [docs/events.md](docs/events.md) | publishing, SSE, webhooks and signatures |
| [docs/pairing.md](docs/pairing.md) | the QR flow and the profile format |
| [docs/oidc.md](docs/oidc.md) | endpoints, claims, relying-party recipes |
| [docs/links.md](docs/links.md) | App links: one Pocket app acting for a person in another (`uses` / `offers`, `pcx_` tokens, per-person switches) |
| [docs/connectors.md](docs/connectors.md) | Core acting on third-party apps: accounts, sign-in, events (Immich first); how to write one |
| [docs/api.md](docs/api.md) | the whole REST surface |
| [docs/spec.md](docs/spec.md) | the versioned contract: tokens, manifest, events, profile, OIDC |
| [deploy/nix](deploy/nix) | Nix package and `services.pocket.core` NixOS module |
| [docs/roadmap.md](docs/roadmap.md) | where the box is going |

## Develop

```sh
make dev     # API on :8080 with demo data and no login, Vite on :5173
make test    # go vet + go test (includes an end-to-end test of register → publish → pair → OIDC) + tsc
make all     # web + binary with the UI embedded
```

Go 1.26, React 19, chi, modernc SQLite (no cgo). The only other dependency is a pure-Go QR encoder.

## License

MIT.
