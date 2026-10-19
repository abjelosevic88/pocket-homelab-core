# Roadmap

Pocket Core is the first piece of a **Pocket Box**: a PC that ships with NixOS, the Pocket apps, a curated set of self-hosted services and this hub, and pairs with the Pocket Homelab client in one scan.

**0.1** — registry, event bus with SSE + signed webhooks, OpenID Connect provider, pairing with env-compatible profiles, device tokens with introspection, service catalog matching the client's 44 integrations, Go SDK, Nix package + module.

**0.2** — households: service audience, per-person credential overrides, member scoping; sign in to Core through PocketID; audience enforced for registered apps; member launcher.

**0.3** — [connectors](connectors.md): Core makes or links each person's account in a third-party app and stores their own key as their credential, turns on "Sign in with Pocket Core" in the app (config backed up first, audience enforced at `/oidc/authorize`), and polls the app into `<type>.*` events. First connector: **Immich** (accounts + per-person API keys, OAuth settings, `immich.assets_added` / `assets_removed` / `user_created`). Off by default; `pocket-core connector …` drives it from the console.

**Next**
- More connectors, same shape: Jellyfin (users + per-user API key / user id), Nextcloud (users + app passwords, `user_oidc`), Paperless (users + tokens), Audiobookshelf, Navidrome. Event sources that push (webhooks) instead of polling where the app offers them.
- Connector clean-up: disable or remove a person's remote account when they are disabled or deleted in Core (opt-in, with a confirmation).
- Client: native "Pair with a box" screen (QR → `/pair/claim` → Keychain) and a Core card with box health, pending updates and backup status.
- Pocket apps adopt the SDK: register on start, resolve siblings through Core instead of `*_URL` env vars, accept device tokens, publish their events.
- Box services: update channel (binary cache + `nixos-rebuild` with health-gated rollback), Backrest preconfigured, diagnostics bundle, Tailscale/tsnet remote access, first-boot wizard with disk encryption.
- Scopes on device and personal tokens.
