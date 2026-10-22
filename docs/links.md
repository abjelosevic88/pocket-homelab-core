# App links

Events say *something happened*; links let one Pocket app **do something in another app for a person** and get an answer back. Pocket Garage booking a fill-up as a transaction in Pocket Money is the first one.

```
 Garage ── POST /api/v1/links/token {user, app: "money", scopes} ──► Core   (Garage's own pca_ token)
        ◄── {access_token: "pcx_…", expires_in: 300, url} ─────────────┘
 Garage ── POST /api/v1/transactions  Bearer pcx_… ──► Money
                                      Money ── POST /api/v1/auth/introspect ──► Core
                                            ◄── {active, kind: "delegated", user, app: garage, scope}
```

Nobody pastes tokens between apps, and every person on the box gets the link, not just the owner.

## Declaring a link

The acting app lists what it needs under `uses`; the target lists what it allows under `offers`. Both go in the [manifest](manifest.md) the app already sends on every start.

```json
// Pocket Garage
"uses":   [{ "app": "money", "scopes": ["accounts:read", "transactions:write"], "reason": "Book fuel, service and other car costs as transactions" }]

// Pocket Money
"offers": [{ "scope": "accounts:read", "name": "See accounts and categories", "description": "…" },
           { "scope": "transactions:write", "name": "Book transactions", "description": "…" }]
```

Scopes match `^[a-z0-9][a-z0-9_.:-]{0,60}$`. Core makes one **link** per (app, target, scope). Initial enrollment approves its declared links. Newly requested scopes on later manifest updates start **off** until an admin approves them. Removing a declaration hides the link but preserves approvals and opt-outs for a later re-addition.

## The Links page

Admins see every link two ways: a **diagram** (each Pocket app a card, one arrow per acting app → target app labelled with its scopes' names, IDs on hover, coloured on / partly off / off / can't work; apps without links sit as chips in a **Not linked** strip under it, and move onto the diagram once they get a link) and a **list**. Tap an arrow for its switches, or a card for everything that app uses, lets others use, and offers that nobody uses yet. Cards can be dragged; the layout is kept in the browser. **Full screen** gives the diagram the whole window (real full screen where the browser allows, a page-covering view on iPhone), with the tapped arrow or app in a floating panel; Esc leaves.

Four more views read the same links differently, all with the same switches:

- **Grid**: apps that act down the side, apps they act in across the top; each cell says in one word what the pair adds up to ("changes" when it may change anything there, which includes seeing; else "sees"), with a green ✓ when allowed or a red ✕ when switched off. Best for "who does what" at a glance.
- **Focus**: one app in the middle, the apps that act in it on one side and the apps it acts in on the other, in plain sentences with a ✓ or ✕ and a switch per scope; tap an app to move the focus to it.
- **Access**: by data instead of by app: "Your Money: *See your transactions* — Day, Travel; *Book transactions* — Garage", including scopes an app offers that nobody uses yet. Best for "who can see or change my stuff".
- **List**: every pair with its switches.

A line above every view counts the links, how many are on, off or can't work, and the tokens issued.

## When a link works

A link issues tokens for a person only when all of these hold (the first that fails is the `reason`):

| Reason | Meaning |
|---|---|
| `not_declared` | the acting app did not ask for this scope |
| `target_missing` | the target app is not registered |
| `not_offered` | the target does not offer the scope |
| `not_in_audience` | the person may not use one of the two apps (service audience) |
| `off_admin` | the admin switched the link off for the box (**Links** page) |
| `off_person` | the person switched it off for themselves (**Settings → Apps acting for me**) |
| `unknown_user` | no active Core person has that email |

## Acting app

```go
tok, err := core.ActAs(ctx, user.Email, "money", "transactions:write")
// tok.AccessToken (pcx_…), tok.URL (where the target lives); every call asks Core, so use it for the work at hand
var le *sdk.LinkError
if errors.As(err, &le) { /* le.Reason is one of the table above */ }

on, reason := core.LinkOn(ctx, user.Email, "money", "transactions:write") // no token issued: show or hide an option
```

## Target app

A link token starts with `pcx_`. `sdk.IsCoreToken` deliberately does **not** match it, so an app that never offered anything keeps rejecting such tokens. An app that offers scopes:

1. Routes `sdk.IsLinkToken(tok)` to `core.Introspect` like other Core tokens. Core answers `active: true` **only to the target app**. Any other app gets `{active: false, reason: "wrong_audience"}`.
2. Gets `kind: "delegated"`, `user`, `app` (the acting app), `scope` (space-separated), `audience`.
3. **Enforces the scope.** It allows only the routes the scope covers and keeps everything else closed, ideally limiting changes to rows the acting app created (Money: `source = <acting slug>`).
4. Records the acting app in its audit trail.

Core checks the links again at every introspection, and `ActAs` asks Core for a fresh token every time, so switching a link off stops the next action at once. A token already handed out keeps working only for as long as the target caches its introspection verdict (disabled by default in the SDK; explicit caches are bounded by token expiry). A `pcx_` token is useless against Core's own API.

## API

| Method | Path | Who | |
|---|---|---|---|
| POST | `/links/token` | app | `{user, app, scope \| scopes}` → `{access_token, token_type, expires_in, scope, app, url, public_url}`; `403 {error, reason, scope}` |
| GET | `/links/status?user=&app=` | app | the caller's links for that person: `[{app, scope, state, on, detail}]` |
| GET | `/links` · PUT `/links/{id}` | admin | every link with names, state, `uses`, `last_used_at`, `opt_outs` / `{enabled}` |
| GET | `/me/links` · PUT `/me/links/{id}` | person | links that can act for the caller, with `on_for_me` / `{enabled}` (their own switch) |

Changing the box-wide switch publishes `core.link.changed`.

## Service connections

The same switches cover a Pocket app reaching a **third-party service** (Immich, Maloja, Dawarich, Jellyfin…) for a person, instead of a URL and key pasted into the app.

- The app lists the services in its manifest: `"services": [{"type": "immich", "reason": "Photos of the day"}]` (catalog types).
- Core makes a link per service, target `service:<type>`, scope `account`, approved at initial enrollment and off pending approval when added by an update; it shows on the Links page ("Use your Immich") with the admin's switch and each person's.
- `POST /api/v1/services/credential {"user": "a@b.c", "type": "immich"}` with the app's `pca_` token returns `{url, public_url, key, fields, source}`. SDK: `core.ServiceFor(ctx, email, "immich")`. Ask every time; don't keep the key.
- **Whose key:** always the person's own credential, set in Core by hand or written by a connector (`source: "personal"`) — admins included. The service's box-wide key never leaves Core (it's usually the owner's account or an admin key that sees everyone; Core's connectors use it). Without their own credential a person gets `no_account`. `pocket-core credentials claim-box [-email USER] [-only TYPES] [-dry-run]` (Core stopped) copies the box-wide keys of the services apps reach through Core (or just `-only`'s) into one person's own credentials once — for the person those accounts really belong to — and never overwrites one they already have. Connector-only fields never leave Core.
- Keyless services (a box entry with no key filled in, like Maloja's read API): the person's own entry with just the address counts; `claim-box` writes that entry.
- Refusals: `off_admin`, `off_person`, `no_account`, `not_in_audience` (the service isn't in the person's audience), `target_missing` (no such service in Core), `not_declared`, `unknown_user`. `GET /api/v1/links/status?user=…&app=service:immich` reports the state without handing anything out.
- Service targets never get a `pcx_` token: `POST /links/token` refuses them.
- **Switching off replaces connector-made keys.** When a service link goes from on to off — a person's own switch (that person) or the admin's box-wide switch (everyone) — Core replaces, in the background, each affected person's key that **a connector minted for an account it created** (account `ok`, credential `source: connector`): the old key is deleted in the service, so every copy any app kept dies, and a new one is saved as the person's credential, so apps still allowed get it at their next `ServiceFor`. Linked accounts, credentials set by hand, entries holding the box-wide key and the box-wide key itself are never touched. Switching back on does nothing extra. Outcome per person: `key_replacements` on the link (`{email, name, replaced_at, error, note}`; a person's own list carries only theirs), `key_rotation` says whether the service's connector `replaces` keys or `cannot`; events `core.service_key_replaced` / `core.service_key_replace_failed`. Per app: [connectors](connectors.md#replacing-keys-when-a-link-is-switched-off).

## Direct connections on the map

Every app also lets a person connect **Direct** (a URL and key pasted into the app) or switch a connection **Off**. Core can't switch those, but apps report them so the Links page shows the whole map:

- `PUT /api/v1/links/modes {"modes": [{"user": "a@b.c", "target": "money" | "service:immich", "mode": "core" | "direct" | "off"}]}` with the app's token; SDK `core.ReportModes(ctx, []sdk.ConnMode{…})`. Apps report on start, when a person changes a mode, and every 30 minutes. Report only: nothing in Core decides on it.
- The admin's links list carries `modes` (counts) and `mode_people` per link; a person's own list carries `my_mode`. The views mark Direct connections (↗ Direct) and the summary line counts them.

## Credential cache

`ServiceFor` reuses a person's credential for a minute (`Options.ServiceCache`), so a page of 300 photos asks Core once; a refusal from Core drops it at once, and while Core can't be reached the last answer is used for up to 15 minutes (`Options.ServiceStale`) so a Core restart doesn't stop the apps. A link switch stops future retrieval when the client next contacts Core. Previously returned upstream credentials remain usable until rotated or removed in that service; the cache lifetime does not revoke copied credentials. App links via `ActAs` apply on the next request. `ServiceForFresh` skips the cache (connection tests).

### Pocket AI text generation

The SDK's `InferenceUse()` requests `inference:write` from app `ai`. This grants
text generation only, not access to agent threads or tools. `SelectAI` implements
personal `auto`, `pocket`, and `openai` selection; empty preferences mean `auto`.
Automatic uses an enabled AI link, otherwise the person's direct provider.
A Core outage fails closed rather than silently changing providers. Each
`InferenceClient.Complete` call requests a new person-scoped delegation token,
so background jobs also respect link revocation. Existing app registrations
need an admin to approve this new permission after upgrading.
