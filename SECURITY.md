# Security policy

Pocket Core holds the sign-in, device tokens and service credentials for a whole Pocket Homelab box, so security reports are welcome and handled first.

## Reporting a problem

Please report privately, not in a public issue:

- GitHub: **Security → Report a vulnerability** on this repository (a private advisory), or
- email the maintainer listed on the GitHub profile.

Say what you found, which version (`/healthz` or Settings shows it), and how to reproduce it. You get an answer within a week; a fix for anything that lets someone in, raise their rights or read credentials ships as a patch release as soon as it is ready, with an advisory once boxes have had time to update.

## Supported versions

Only the latest release gets fixes. Core updates itself from the app catalog; a box that stays current is covered.

## Scope

In scope: Core's API, OIDC provider, pairing, device and app tokens, the relay and remote access, the installer agent, the SDK in `sdk/`.

Out of scope: apps installed through Core (report to that app's repository), and setups that ignore the deployment notes in the README (for example Core on the open internet over plain http).

## What CI checks

Every push runs `go vet`, the tests (including replay and privilege tests for tokens and pairing) and `govulncheck` against the Go standard library and modules.
