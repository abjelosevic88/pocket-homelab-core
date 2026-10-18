# Roadmap

Pocket Core is the first piece of a **Pocket Box**: a PC that ships with NixOS, the Pocket apps, a curated set of self-hosted services and this hub, and pairs with the Pocket Homelab client in one scan.

**0.1 (this)** — registry, event bus with SSE + signed webhooks, OpenID Connect provider, pairing with env-compatible profiles, device tokens with introspection, service catalog matching the client's 44 integrations, Go SDK, Nix package + module.

**Next**
- Credential provisioning: after a bundled service is installed, Core creates the API key itself (Immich, Jellyfin, Nextcloud app passwords, Paperless tokens…) instead of an admin pasting it.
- Client: native "Pair with a box" screen (QR → `/pair/claim` → Keychain) and a Core card with box health, pending updates and backup status.
- Pocket apps adopt the SDK: register on start, resolve siblings through Core instead of `*_URL` env vars, accept device tokens, publish their events.
- Box services: update channel (binary cache + `nixos-rebuild` with health-gated rollback), Backrest preconfigured, diagnostics bundle, Tailscale/tsnet remote access, first-boot wizard with disk encryption.
- Per-user service credentials (a Jellyfin user id per person, not per box).
- Scopes on device and personal tokens.
