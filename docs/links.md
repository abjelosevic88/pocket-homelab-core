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

Scopes match `^[a-z0-9][a-z0-9_.:-]{0,60}$`. Core makes one **link** per (app, target, scope). A link is created switched **on**: the apps on a box are installed by its admin, and both sides had to ask for it. Registering again without a use deletes its links.

## The Links page

Admins see every link two ways: a **diagram** (each Pocket app a card, one arrow per acting app → target app labelled with its scopes' names, IDs on hover, coloured on / partly off / off / can't work; apps without links sit as chips in a **Not linked** strip under it, and move onto the diagram once they get a link) and a **list**. Tap an arrow for its switches, or a card for everything that app uses, lets others use, and offers that nobody uses yet. Cards can be dragged; the layout is kept in the browser. **Full screen** gives the diagram the whole window (real full screen where the browser allows, a page-covering view on iPhone), with the tapped arrow or app in a floating panel; Esc leaves.

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

Core checks the links again at every introspection, and `ActAs` asks Core for a fresh token every time, so switching a link off stops the next action at once. A token already handed out keeps working only for as long as the target caches its introspection verdict (60 s in the SDK). A `pcx_` token is useless against Core's own API.

## API

| Method | Path | Who | |
|---|---|---|---|
| POST | `/links/token` | app | `{user, app, scope \| scopes}` → `{access_token, token_type, expires_in, scope, app, url, public_url}`; `403 {error, reason, scope}` |
| GET | `/links/status?user=&app=` | app | the caller's links for that person: `[{app, scope, state, on, detail}]` |
| GET | `/links` · PUT `/links/{id}` | admin | every link with names, state, `uses`, `last_used_at`, `opt_outs` / `{enabled}` |
| GET | `/me/links` · PUT `/me/links/{id}` | person | links that can act for the caller, with `on_for_me` / `{enabled}` (their own switch) |

Changing the box-wide switch publishes `core.link.changed`.
