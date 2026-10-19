# Roadmap

Pocket Core is the first piece of a **Pocket Box**: a PC that ships with NixOS, the Pocket apps, a curated set of self-hosted services and this hub, and pairs with the Pocket Homelab client in one scan.

**0.1** — registry, event bus with SSE + signed webhooks, OpenID Connect provider, pairing with env-compatible profiles, device tokens with introspection, service catalog matching the client's 44 integrations, Go SDK, Nix package + module.

**0.2** — households: service audience, per-person credential overrides, member scoping; sign in to Core through PocketID; audience enforced for registered apps; member launcher.

**0.3** — [connectors](connectors.md): Core makes or links each person's account in a third-party app and stores their own key as their credential, turns on "Sign in with Pocket Core" in the app (config backed up first, audience enforced at `/oidc/authorize`), and polls the app into `<type>.*` events. First connector: **Immich** (accounts + per-person API keys, OAuth settings, `immich.assets_added` / `assets_removed` / `user_created`). Off by default; `pocket-core connector …` drives it from the console.

**0.4** — events by push: connectors can register hooks in their app (`POST /api/v1/connectors/hooks/{id}` with a per-hook secret) that Core creates, heals and deletes with the Events switch, the people and the audience. Immich gets two workflows per connected person (webhook step) and publishes `immich.asset_created` / `immich.asset_processed` per photo or video, replacing the count polling.

**0.5** — [app links](links.md): apps declare `uses` / `offers` in their manifest, Core swaps an app's token for a five-minute `pcx_` token that acts for one person in one target app with the declared scopes, re-checked at every introspection. There's a switch per link for the admin (Links page) and one per person (Settings → Apps acting for me). The SDK gets `ActAs`, `LinkOn` and `IsLinkToken`. First link: Pocket Garage → Pocket Money (book car costs as transactions).

**0.6** — **Jellyfin connector**: provisions accounts (name = the email's local part; new accounts get their own session token as `API_KEY` plus `USER_ID`; matches are linked with `USER_ID`), lets an admin **link people to existing accounts by hand** (connectors without emails implement `Linker`), switches on "Sign in with Pocket Core" through the SSO Authentication plugin (provider + login button, backed up first), and polls playback, new items and account events into `jellyfin.*`.

**Next**
- Move the hand-pasted cross-app tokens to links: Kitchen → Inventory / Split / Habits, Garage → Travel / Inventory, Day's Pocket sources.
- More connectors, same shape: Nextcloud (users + app passwords, `user_oidc`), Paperless (users + tokens), Audiobookshelf, Navidrome. Push events (hooks) for every app that can call a URL.
- Connector clean-up: disable or remove a person's remote account when they are disabled or deleted in Core (opt-in, with a confirmation).
- Client: native "Pair with a box" screen (QR → `/pair/claim` → Keychain) and a Core card with box health, pending updates and backup status.
- Pocket apps adopt the SDK: register on start, resolve siblings through Core instead of `*_URL` env vars, accept device tokens, publish their events.
- Box services: update channel (binary cache + `nixos-rebuild` with health-gated rollback), Backrest preconfigured, diagnostics bundle, Tailscale/tsnet remote access, first-boot wizard with disk encryption.
- Scopes on device and personal tokens.
