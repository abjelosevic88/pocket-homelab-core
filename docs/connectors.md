# Connectors

A **service** in Core is a third-party app with box-wide credentials (Immich with an admin's API key). A **connector** lets Core act on that app for the household, so nobody copies keys between admin pages:

1. **Accounts.** When an admin creates a person, Core makes (or finds) that person's account in the app and stores **the person's own credential** as their per-person override. Their paired phone then receives their key, not the admin's.
2. **Sign in with Pocket Core.** Core creates an OpenID Connect client for the service and writes it into the app's own settings.
3. **Events.** What happens in the app becomes `<type>.<event>` on the bus: pushed by the app itself where it can (Immich workflows call Core for every upload), polled otherwise.

Connectors are opt-in per service and **off by default**. Everything runs through Core's own API and is visible under **Services → Connector**.

## What Core does, exactly

| | When | Rules |
|---|---|---|
| Provision one person | an admin creates a person (in the background, if *Create accounts for new people* is on), **Provision** in the people table, or `POST …/connector/provision {"user_id": N}` | only people in the service's **audience**; disabled accounts skipped; an account Core already provisioned (`ok`) is not touched again |
| Provision everyone missing | **Provision everyone missing**, or `{"all": true}` | everyone in the audience without an account (or whose last attempt failed) |
| Link | the app already has an account with the person's email | status `linked`, the account is not modified and no credential is minted (its password is unknown). An app admin account is never modified |
| Credential | the app returned one | saved as the person's override with `source: connector`. If the person (or an admin, by hand) already set their own override for this service, it is **kept** and nothing is minted. A connector only ever replaces its own overrides |
| Errors | anything failed | recorded per person (`status: error` + message) and shown in the people table and on the person's credentials page; the next attempt retries |
| Sign-in on | the *Sign in with Pocket Core* switch | makes or reuses an OIDC client tied to the service (`service_id`), with the connector's redirect URIs and a fresh secret, then asks the app to use it. The switch only flips when the app accepted the change |
| Sign-in off | the same switch | asks the app to turn its OIDC login off; the client stays in Core (and is reused next time) |
| Audience | always | `/oidc/authorize` answers `access_denied` (and `/oidc/token` refuses codes and refresh tokens) for a person outside the service's audience, exactly as for registered Pocket apps |
| Events | the *Events* switch, then every `poll_interval_s` (default 300, minimum 60) + up to 10 % jitter | push connectors: switching on creates the subscriptions in the app at once, switching off deletes them (see [Events by push](#events-by-push)). Every interval: one poll per connector at a time (60 s timeout; the **first poll records a baseline only**; turning events on again starts a new baseline), then a subscription check that re-creates anything deleted in the app. Published with source `connector:<type>`; when an event belongs to a person the payload carries `user_email` (and the subject is `user:<email>` unless the event names its own, like `asset:<id>`) |
| Master switch | *Connector on* | off: Core makes no accounts, stops checking and deletes its push subscriptions. Sign-in stays however the app has it |

Core also publishes `core.connector.provisioned` `{type, service_id, user_email, status, created}` after each successful provisioning.

## Replacing keys when a link is switched off

Apps that reach a service through Core ([service connections](links.md#service-connections)) may keep a person's key in memory. When one of that person's service links is **switched off**, Core **replaces the key in the service**: the old key is deleted there (so every copy of it, in any app, dies) and a new one is saved as the person's credential, which the apps still allowed get at their next `ServiceFor` (the SDK caches a credential for up to a minute).

| | |
|---|---|
| When | a person switches a service link off for themselves (`PUT /me/links/{id}` → that person), or the admin switches it off for the box (`PUT /links/{id}` → everyone with a connector-made key in that service). Only on → off; switching back on does nothing extra. In the background (90 s per person), after the switch is saved |
| Whose keys | only a key **the connector minted for an account it created**: account status `ok` (not `linked`, not linked by hand), the person's credential stored by the connector (`source: connector`) and holding a key. Never a linked account (the owner's), a credential set by hand, a hand-set or claimed copy of the box key (an entry holding the **service's box-wide key** is refused and says so), or the box-wide key itself |
| Admin accounts | every connector checks the account in the app first and refuses an administrator's |
| Outcome | on the connector account: `key_replaced_at`, `key_error` (the old key may still work), `key_note` (a side effect, e.g. a password Core had to reset). Shown in the connector's people table and on the Links page under the service link. A failure is recorded and logged, never fatal: the switch-off itself always stands |
| Events | `core.service_key_replaced` `{type, service_id, user_email, reason: off_person \| off_admin, app, note?, error?}` (`error` = the new key is saved but an old one survived), `core.service_key_replace_failed` `{…, error}`. Never the key |
| Connector off | nothing is replaced (`key_error` says so) |

What it does per app (endpoints in each section below): **Immich**, **Jellyfin**, **Audiobookshelf**, **Paperless-ngx**, **Nextcloud** can replace keys. Jellyseerr has no per-person keys. A connector that can't replace keys records *Core can't replace keys in X automatically* on the person's account, and its panel and links say so.

## Events by push

Polling only sees totals ("sani has 5 more photos"). An app that can call a URL when something happens gives one real event per thing instead, seconds after it happened. Core does that with **hooks**:

1. For each subscription it wants, Core picks a random hook id and a random secret, and asks the app (with the right credential) to call `POST https://<BASE_URL>/api/v1/connectors/hooks/<id>` with the header `X-Pocket-Hook: <secret>`. It stores the hook (`connector_hooks`: service, person, trigger, the app's id for the subscription, a SHA-256 of the secret, counters). The secret itself exists only inside the app.
2. A delivery with an unknown id or the wrong secret gets `404`. A good one is decoded by the connector, attributed to a Core person, de-duplicated (same thing and trigger within 24 h) and published; `202` either way. Everything that was not published is counted as *dropped* with the reason, shown under **Services → Connector → Events**.
3. The subscriptions follow the switches and the people: created when Events goes on (or a person is provisioned while it is on, or comes back into the audience), deleted when Events or the connector goes off, a person is disabled, leaves the audience, is deleted (before Core forgets their key), or their key in Core is replaced (then re-created with the new key). The periodic check re-creates subscriptions deleted in the app (with a new secret) and removes subscriptions carrying Core's names that Core has no record of (they could never authenticate). **Re-create workflows** does all of them again with new secrets.

`BASE_URL` must be an address the **app's server** reaches Core at (for Immich, from inside the Immich container).

## Immich

Needs: the Immich service in Core with its internal URL, its public URL (where people sign in) and the API key of an **Immich admin** with `adminUser.read`, `adminUser.create`, `systemConfig.read`, `systemConfig.update` (or `all`). Events also need, in each connected person's key, `user.read`, `workflow.read`, `workflow.create`, `workflow.delete` (keys Core mints have `all`). **Test** tells you what the box-wide key can do.

| Immich call (below `/api`) | Used by | Writes? |
|---|---|---|
| `GET /server/version` | Test | – |
| `GET /api-keys/me` | Test (the key's permissions) | – |
| `GET /admin/users` | Test, Provision, the periodic check (new accounts) | – |
| `POST /admin/users` | Provision: a missing person — email, name, a 40-character random password used once and never stored, logged or returned, `shouldChangePassword: false`, `notify: false`, `isAdmin: false` | **yes** |
| `POST /auth/login` | Provision: sign in once as the new person (Immich only lets a user mint their own keys) | **yes** (a session) |
| `POST /api-keys` | Provision: the person's key, named `Pocket Core`, permission `all` | **yes** |
| `POST /auth/logout` | Provision: close that session | **yes** |
| `GET /system-config` | Sign-in on/off: read the whole configuration | – |
| `PUT /system-config` | Sign-in on/off: write it back with only `oauth` changed | **yes** |
| `GET /users/me` | Events: whose account a key is (as the person) | – |
| `GET /workflows` | Events: which of the person's workflows still exist (as the person) | – |
| `POST /workflows` | Events on / heal: create a workflow (as the person) | **yes** |
| `DELETE /workflows/{id}` | Events off, person removed, re-create (as the person) | **yes** |
| `GET /users/me`, `GET /api-keys/me` | Replacing a key: check the stored key is the account's, and its id (as the person) | – |
| `POST /api-keys` · `GET /api-keys` · `DELETE /api-keys/{id}` | Replacing a key: mint the new one (`Pocket Core`, `all`) with the stored key, then, with the new one, delete the account's keys named `Pocket Core` and the stored key | **yes** |
| `PUT /admin/users/{id}` `{password}` · `POST /auth/login` · `POST /auth/logout` | Replacing a key, **only when the stored key no longer works**: a new 40-character random password (needs `adminUser.update` on the box key, or `all`), one sign-in to mint the new key | **yes** |

`/system-config` is deprecated since Immich 3.2 in favour of `/admin/config` (still alpha); the connector falls back to `/admin/config` if the old path ever answers 404.

**Sign-in on** writes a backup of the full configuration to `DATA_DIR/connectors/immich-system-config-<timestamp>.json` (mode 0600; it contains Immich's secrets) and then changes only `oauth`: `enabled: true`, `issuerUrl` = Core's issuer, `clientId`, `clientSecret`, `scope: "openid email profile"`, `buttonText: "Sign in with Pocket Core"`, `autoRegister: true`, `autoLaunch: false`, `mobileOverrideEnabled: true`, `mobileRedirectUri: https://<public>/api/oauth/mobile-redirect`. Password login is not touched. **Sign-in off** backs up again and sets `oauth.enabled: false`. To undo by hand, PUT a backup file back to `/api/system-config` with an admin key.

Redirect URIs registered on the Core client: `https://<public>/auth/login`, `https://<public>/user-settings`, `app.immich:///oauth-callback`, `https://<public>/api/oauth/mobile-redirect`. Immich fetches `https://<core>/.well-known/openid-configuration` and the token/userinfo endpoints **from the Immich server**, so the Immich container must be able to reach Core's public URL.

Immich links an OIDC login to an existing account with the same email, so a person Core provisioned (or linked) signs in to the same account either way.

**Events** ([topics and payloads](events.md#immich-source-connectorimmich)): `immich.asset_created` and `immich.asset_processed` by push, one per asset; `immich.user_created` from the periodic check.

How Immich (3.3) workflows behave, read from its source (`workflow-execution.service.ts`, `workflow.repository.ts`, `immich-plugin-core`): a workflow **belongs to the user who created it** and runs only for **that user's** assets (`AssetCreate` / `AssetMetadataExtracted` look up workflows by the asset owner). So Core creates, per connected person and **with that person's own key**, two workflows — `Pocket Core · uploads` (trigger `AssetCreate`) and `Pocket Core · details` (trigger `AssetMetadataExtraction`) — each with one step, the built-in `immich-plugin-core#webhook` (`{url, method: "POST", headerName: "X-Pocket-Hook", headerValue: <secret>}`), enabled, logging off. The step POSTs `{"type": "AssetV1", "trigger": "…", "data": {"asset": {…}}}` where `asset` carries `id`, `ownerId`, `type`, `originalFileName`, `fileCreatedAt`, `localDateTime`, `createdAt`, `visibility`, `exifInfo` (null until metadata extraction; GPS included) and more; Immich ignores the answer and does not retry.

Whose key: a person's own override (the key Core minted at provisioning, or one set by hand); for a **linked** person without one, the service's box-wide key **only if** `GET /users/me` with it returns that person's Immich account (typically the owner whose admin key it is). Anyone else shows **needs a key** and gets no workflows. A key that belongs to a different Immich account is an error. A delivery is published only when the asset's `ownerId` is the hook's person's Immich account and that person is enabled and in the service's audience; the payload's `user_email` is their **Core** email.

**Replacing a key** (a link switched off, see [above](#replacing-keys-when-a-link-is-switched-off)): Immich lets only a user mint their own keys, so Core uses the stored key itself to mint the new one, then deletes the account's old `Pocket Core` keys with the new key. Its password is not touched. Only if the stored key was already deleted does Core set a new random password on the account (Core made it, nobody knows the old one) and sign in once; the box key needs `adminUser.update` for that, else the replacement fails and says so. Workflows belong to the account, not the key, and keep running.

The workflows are visible to the person under Immich → Utilities → Workflows; deleting one there is healed at the next check, so to stop events turn **Events** off in Core. To remove them by hand anyway (Core down for good): delete the two `Pocket Core · …` workflows in each person's Immich account.

## Jellyfin

Needs: the Jellyfin service in Core with its internal URL, its public URL and a Jellyfin **API key** (Dashboard → API Keys; API keys act as an administrator). **Test** shows the version, the number of accounts and whether the SSO Authentication plugin is installed.

| Jellyfin call | Used by | Writes? |
|---|---|---|
| `GET /System/Info/Public`, `GET /System/Info` | Test | – |
| `GET /Users` | Test, Provision, linking by hand, events (names) | – |
| `POST /Users/New` | Provision: a missing person; name = the part of their email before `@`, a 40-character random password used once and never stored, logged or returned. Jellyfin's default policy for new users applies | **yes** |
| `POST /Users/AuthenticateByName` | Provision: sign in once as the new account; the session token becomes the person's `API_KEY` (shown under Dashboard → Devices as client *Pocket Core*, device = their email) | **yes** (a session) |
| `GET /Plugins` | Test, sign-in | – |
| `GET /sso/OID/Get` | Sign-in on/off: backup | – |
| `POST /sso/OID/Add/pocket-core` · `GET /sso/OID/Del/pocket-core` | Sign-in on / off | **yes** |
| `GET /Branding/Configuration` · `POST /System/Configuration/branding` | Sign-in on/off: the login button | **yes** |
| `GET /System/ActivityLog/Entries` | Events | – |
| `GET /Items` | Events (new items, titles) | – |
| `POST /QuickConnect/Initiate` · `POST /QuickConnect/Authorize?code=` · `POST /Users/AuthenticateWithQuickConnect` | Replacing a key: a new session from a new device `pocket-core-<user id>-<time>`, approved with the stored token (or by the API key for the account, `&userId=`, Jellyfin 10.9+) | **yes** (a session) |
| `POST /Users/{id}/Password` (or `/Users/Password?userId=`) · `POST /Users/AuthenticateByName` | Replacing a key, **only when Quick Connect is off**: a new random password, then sign in with it | **yes** |
| `GET /Devices?userId=` · `DELETE /Devices?id=` | Replacing a key: delete the account's older *Pocket Core* devices (`pocket-core-…`), which revokes their tokens | **yes** |

**Accounts.** Jellyfin accounts have no email, so a person matches an account named like their email or the part before `@` (the same name Core sends as `preferred_username`). A match is **linked**: Core saves the account's `USER_ID` as the person's credential and mints nothing, because Jellyfin cannot issue a token for someone else and the password is unknown. A new account gets `USER_ID` plus its own token as `API_KEY`. Nobody knows its password, so the person signs in with Pocket Core (needs the SSO plugin) or Quick Connect, or an admin sets a password in Jellyfin.

**Linking by hand.** When the names don't line up (the owner watches as `root`), pick the account in the people table, or run `pocket-core connector jellyfin link <email> <account>`. A link made by hand is marked *by hand*, and provisioning never changes it. Unlinking removes the link and the credential Core saved for it, never one the person set themselves. One Jellyfin account can be linked to one person only.

**Replacing a key** (a link switched off, see [above](#replacing-keys-when-a-link-is-switched-off)): the person's credential is a session token. Core opens a new session through Quick Connect and deletes Core's older devices of that account, so the old token stops working; the person's own devices (phone, TV) are left alone. With Quick Connect switched off in Jellyfin, Core sets a new random password to sign in (nobody knew the old one), and Jellyfin then **signs the account out on every device**; the person's account shows a note saying so.

**Sign in with Pocket Core** needs the [SSO Authentication plugin](https://github.com/9p4/jellyfin-plugin-sso). Add the repository `https://raw.githubusercontent.com/9p4/jellyfin-plugin-sso/manifest-release/manifest.json` under Dashboard → Plugins → Repositories, install it and restart Jellyfin. Without the plugin, or while it waits for a Jellyfin restart (status `Restart`), the switch refuses and says so.

Turning sign-in on does three things, in this order:
1. Backs up the plugin's providers and Jellyfin's branding to `DATA_DIR/connectors/jellyfin-sso-<timestamp>.json`.
2. Creates provider `pocket-core`: issuer = Core, its client id and secret, scopes `openid profile email groups`, username from `preferred_username`, and `schemeOverride: https` when the public URL is https (behind a TLS proxy Jellyfin sees `http` and the callback would not match Core's client). **Authorization stays off**, so the plugin never changes anyone's libraries or admin rights. Core's audience already decides who may sign in.
   - It also writes the plugin's **canonical links** (`username → Jellyfin account`) for everyone Core has linked, so a person linked by hand (the owner → `root`) lands in that account rather than one with the same name.
   - Links the plugin made itself for other names are kept. A person Core no longer links falls back to matching by name.
   - Linking or unlinking someone while sign-in is on re-applies the provider (with a fresh client secret) at once.
3. Puts a *Sign in with Pocket Core* button, linking to `https://<public>/sso/OID/start/pocket-core`, at the top of the login disclaimer, between `<!-- pocket-core-sso -->` markers. Anything else in the disclaimer is kept.

Turning sign-in off deletes the provider and the marked block. Redirect URIs: `https://<public>/sso/OID/redirect/pocket-core` (plugin 3.5+) and `…/sso/OID/r/pocket-core` (older). Jellyfin's TV and phone apps cannot use OIDC: sign in once in the browser, then approve them with Quick Connect.

**Events** are polled from the activity log and the newest library items ([topics](events.md#jellyfin-source-connectorjellyfin)): `playback_started`, `playback_stopped`, `item_added`, `user_created`, `user_deleted`, `user_locked_out`, `login_failed`. Sessions coming and going are not published. A playback carries `user_email` only when its Jellyfin account is linked to a person who is enabled and in the service's audience; otherwise it carries just the Jellyfin user's id and name.

## Audiobookshelf

Needs: the Audiobookshelf service in Core with its internal URL, its public URL and an **API key** (Settings → API Keys, 2.26+) for an administrator. Only root's own key may change the root user, so give Core root's key if the owner's account is root. **Test** shows the version, the number of accounts and which OpenID provider people sign in with today.

| Audiobookshelf call | Used by | Writes? |
|---|---|---|
| `GET /status` | Test | – |
| `GET /api/users`, `GET /api/users/:id` | Test, Provision, linking by hand, sign-in, events (finished items) | – |
| `POST /api/users` | Provision: a missing person; type `user`, active, their email, a 40-character random password never stored, logged or returned | **yes** |
| `POST /api/api-keys` | Provision: the new account's own key (`Pocket Core · <email>`, no expiry) becomes the person's `TOKEN` | **yes** |
| `PATCH /api/users/:id` | Sign-in on/off: give a linked account the person's email | **yes** |
| `PATCH /api/users/:id/openid-unlink` | Sign-in on/off: detach a linked account from the previous provider | **yes** |
| `GET /api/auth-settings` · `PATCH /api/auth-settings` | Test, sign-in on/off | **yes** |
| `GET /api/sessions`, `GET /api/libraries`, `GET /api/libraries/:id/items`, `GET /api/libraries/:id/recent-episodes` | Events | – |
| `POST /api/api-keys` · `GET /api/api-keys` · `DELETE /api/api-keys/:id` | Replacing a key: a new `Pocket Core · <email>` key for the account, then delete that account's other keys whose name starts with `Pocket Core` (keys the person made themselves stay) | **yes** |

**Accounts.** A person matches the account with **their email** and nothing else. A match is **linked** with its `USER_ID` and Core mints nothing. A missing person gets a new account named like the part of their email before `@` (the full email if that name is taken: never taken over by name) plus `USER_ID` and their own API key as `TOKEN`. Nobody knows its password: they sign in with Pocket Core, or an admin sets one. **Linking by hand** works as for Jellyfin (service accounts without an email, the owner under another address).

**Replacing a key** (a link switched off, see [above](#replacing-keys-when-a-link-is-switched-off)): all with the admin key; root and admin accounts are refused.

**Sign in with Pocket Core** uses Audiobookshelf's built-in OpenID, which has **one provider**. Turning it on:
1. Backs up the sign-in settings to `DATA_DIR/connectors/audiobookshelf-sso-<timestamp>.json`. If they point at another provider (PocketID…), they are also kept as `audiobookshelf-sso-original.json`.
2. For each linked person: sets the account's email to theirs, and detaches it from the previous provider's subject. Audiobookshelf refuses a second provider for an account ("already linked to a different OpenID subject"), and matches the next sign-in by email.
3. Sets issuer, endpoints, client id and secret (RS256), button *Sign in with Pocket Core*, **match existing users by email**, and **no group or permissions claim**, so sign-in never changes anyone's type or libraries; Core's audience decides who may sign in. Mobile redirect URIs are kept, plus `audiobookshelf://oauth`. Auto-register, auto-launch and the redirect subfolder stay as they were.
4. Switches OpenID off and on again in two requests: Audiobookshelf rebuilds its OpenID client only then, so **no restart is needed**. The active methods stay as they were (OpenID-only stays OpenID-only).

Turning it off puts the saved original back (provider, secret, methods; linked accounts are detached again so the old provider matches them by email) and marks the copy `-restored-`. With no original, OpenID is switched off and username/password on. Redirect URIs: `https://<public>/audiobookshelf/auth/openid/callback` and `…/mobile-redirect`, plus the same without `/audiobookshelf` for root installs.

**Events** are polled ([topics](events.md#audiobookshelf-source-connectoraudiobookshelf)): `listening_started`, `listening_ended`, `item_finished`, `item_added`, `episode_added`, `user_created`, `user_deleted`. Listening and finished items carry `user_email` when the Audiobookshelf account is linked to a person.

## Radarr, Sonarr, Lidarr, Prowlarr, Chaptarr

Needs: the service in Core with its URL, public URL and **API key** (Settings → General). Chaptarr is a Readarr fork; Test accepts either name. These are household tools: no per-person accounts and no OpenID sign-in, so the connector does **events only**. **Test** shows the version and whether Core's connection exists.

| Call (`/api/v3`; Lidarr, Prowlarr, Chaptarr `/api/v1`) | Used by | Writes? |
|---|---|---|
| `GET /system/status` | Test | – |
| `GET /notification` | Test, keeping the connection in step | – |
| `GET /notification/schema` | Events on: the Webhook template of this version | – |
| `POST /notification?forceSave=true` | Events on: add the connection | **yes** |
| `DELETE /notification/:id` | Events off | **yes** |

Switching **Events** on adds one **Webhook** connection named *Pocket Core* (Settings → Connect): `POST` to `https://<core>/api/v1/connectors/hooks/<id>` with Core's secret in the `X-Pocket-Hook` header. Prowlarr's and Chaptarr's Webhook has no headers field, only a username and password: Core sets the username `pocket-core` and its secret as the password, and accepts the secret as a Basic-auth password. Saving an enabled connection makes the app call it once with a *Test* event first (`forceSave` only silences warnings), so Core records every hook **before** it subscribes; the test call authenticates and is dropped. Core re-creates the connection if someone deletes it, removes it when Events go off, and never touches other connections (Apprise, ntfy, Jellyfin…).

Triggers switched on (only those the version has **and supports**: a trigger whose `supportsOn…` is false stays off): grab, import (Lidarr, Chaptarr: release import), upgrade, added, deleted, file deleted (not "deleted for upgrade"), album deleted (Lidarr), book added and deleted (Chaptarr), download and import failures (Lidarr; Chaptarr lists them but does not support them), manual interaction required, health issue and restored (**errors only**: `includeHealthWarnings` off, warnings flap with every WAN blip), application update. Renames and retags stay off: they repeat what an import said. Prowlarr: grabs (manual ones too, `includeManualGrabs`; a grab for Radarr also arrives as `radarr.grabbed`, Prowlarr's says which app asked), health issue and restored, application update.

Every delivery is a **household event**: no `user_email` (connectors mark such deliveries `Shared`, and Core publishes them without looking up a person). Test calls are dropped; the same body twice inside 24 h is published once (health and update events are never deduplicated, they legitimately repeat).

## Uptime Kuma

Events only, **polled** (no accounts or sign-in; adding a notification in Kuma needs its socket.io login, so Core reads instead). Read-only:

| Call | Needs | Gives |
|---|---|---|
| `GET /metrics` | `API_KEY` (Settings → API Keys; sent as the Basic-auth password) | every monitor's status and certificate days |
| `GET /api/status-page/:slug` · `/api/status-page/heartbeat/:slug` | `STATUS_SLUG` (public) | the page's monitors and the message of their last check |

With an API key Core watches every monitor; with only a slug, the monitors on that page. With both, events carry the last check's message. Down, up and certificate events are **household events** (no `user_email`). The first check records what is already down or expiring without announcing it; *pending* (Kuma retrying) is not down. Choose the check interval in the Events section: down is noticed within one interval.

## Changedetection.io

Events only, **polled**, read-only, with the `API_KEY` (Settings → API). Changedetection has one password and no accounts.

| Call (header `x-api-key`) | Used by |
|---|---|
| `GET /api/v1/systeminfo` | Test: version, watches, overdue checks |
| `GET /api/v1/watch` | every watch's last change, last check and last error (**never** with `recheck_all`, which queues a recheck of everything) |
| `GET /api/v1/tags` | group names, and which groups are muted |

A newer `last_changed` is `changedetection.change_detected` (linking to the diff page); an error appearing or clearing is `check_failed` / `check_recovered`. A watch whose groups are **all muted** stays quiet, as in Changedetection's own notifications. At most 50 events per check; the rest come next time.

## Scrutiny

Events only, **polled**, read-only, no credential (Scrutiny has no API authentication): `GET /api/health` (Test) and `GET /api/summary` (every disk's status, temperature and last collection; archived disks ignored). Optional fields: `TEMP_WARN` (°C, default 55) and `STALE_HOURS` (default 48).

`device_status` is a bit set: 1 = the drive's own SMART self-assessment failed, 2 = Scrutiny's failure model says it is failing. Changes become `scrutiny.disk_failing` / `disk_passing`; a disk at or above `TEMP_WARN` is `disk_hot` once, and again only after it cooled 5 °C below; `disk_added` / `disk_removed`; a host whose collector sent nothing for `STALE_HOURS` is `host_silent`, then `host_reporting`.

## Speedtest Tracker

Events only, **polled**, read-only. With a `TOKEN` (Settings → API Tokens, *Read results*) Core reads `GET /api/v1/results?sort=-created_at` (every test, failed ones, Speedtest Tracker's thresholds); without one, the public `GET /api/speedtest/latest` (the newest finished test at each check, no thresholds).

Every finished test is `speedtest.finished` (Mbit/s, ms); a failed one `speedtest.failed`; with a token, the line dropping below the thresholds is `speedtest.degraded` and coming back `speedtest.recovered`. A test still running waits for the next check (one stuck while later tests finished is skipped). The WAN address in a result is never copied.

## Kavita

Needs: an **admin's auth key** (`API_KEY`: User Settings → Auth Keys, signed in as a Kavita admin), sent in the `x-api-key` header (never in a URL; Kavita's plugin sign-in logs a failed key, so Core does not use it). Accounts, linking by hand, sign-in, key replacement and events.

| Feature | Calls | Writes? |
|---|---|---|
| Test | `GET /api/health`, `/api/Account` (version, must be admin), `/api/Users?includePending=true`, `/api/Settings` | – |
| Accounts | `GET /api/Users`, `/api/Library/libraries`, `/api/Account/invite-url` (an invitation nobody accepted) | – |
| Make an account | `POST /api/Account/invite`, `POST /api/Account/confirm-email` (with the invite token), `POST /api/Account/create-auth-key` (as the person) | **yes** |
| Sign-in | `GET /api/Settings`, `POST /api/Settings` (everything sent back, only `oidcConfig` changed) | **yes** |
| Replace a key | `GET /api/Account/auth-keys`, `POST /api/Account/rotate-auth-key` (as the person); only when the stored key is dead: `POST /api/Account/reset-password` (admin) and one `login` | **yes** |
| Events | `GET /api/Users`, `/api/Activity/current`, `POST /api/Series/all-v2` (a search), `GET /api/Series/volume`, `/api/Series/:id` | – |

People are matched by email (an accepted account: linked, no key; an admin is left alone). Otherwise Core invites them and completes the invitation with a random password nobody knows, then mints their own auth key *Pocket Core* (not the built-in *opds* key, so replacing it kills only Core's). Username: the email's local part, else the full email. Role `Login` (Kavita's invite default) and **every library** the admin key sees, no age restriction; libraries added later are granted in Kavita. If Kavita has email set up, its own invitation mail goes out too (its link stops working once Core completed it).

**Sign-in**: Core backs up the settings to `DATA_DIR/connectors/kavita-sso-<stamp>.json` (SMTP password left out), keeps the previous provider in `kavita-sso-original.json`, and sets authority = Core's issuer, client id and secret, provider name = the button text, *provision accounts* off, *sync user settings* off (claims never change roles or libraries), no custom scopes. Password login, auto-login and the verified-email rule stay as they were. Kavita matches a sign-in by subject, then email; Core refuses to switch on, changing nothing, when a hand-linked person's Kavita account has another email. Off puts the saved provider back exactly, except a previous provider's client secret (Kavita never shows it): Core then says to re-enter it. **Kavita reads its provider at start: restart the Kavita container after switching sign-in on or off.** Redirect: `<public>/signin-oidc`.

**Events**: an admin cannot read other people's reading history in Kavita 0.9, so reading comes from the admin's view of **active sessions** (kept 10 minutes after the last page turn; the default 5-minute check sees each session end). `reading_started` and `reading_ended` are a person's own (`Personal`: dropped unless the Kavita account is linked to someone), as is `book_finished` (the last page of a single-chapter volume; manga and comic chapters are counted in `reading_ended`). New series (`series_added`, or one `series_added_bulk` for more than 10 at once) and accounts are household events.

## Forgejo

Needs: a **site administrator's** token (`TOKEN`: read:admin, write:admin, read:user). Accounts, linking by hand, key replacement and events; no sign-in switch.

| Feature | Calls (`/api/v1`) | Writes? |
|---|---|---|
| Test | `GET /version`, `/user` (must be admin), `/users/:admin/tokens` (the stored token's scopes), `/admin/users?limit=1`, `/admin/hooks` | – |
| Accounts | `GET /admin/users` (paged), `/admin/emails/search` (secondary emails), `/users/:name` (is the name free) | – |
| Make an account | `POST /admin/users`, `POST /admin/users/:u/tokens` | **yes** |
| Replace a key | `GET /user` with the old token, `POST`/`GET`/`DELETE /admin/users/:u/tokens…`, then the old token must get 401 | **yes** |
| Events | `GET /admin/hooks`, `POST /admin/hooks`, `DELETE /admin/hooks/:id` | **yes** |
| New and removed accounts | `GET /admin/users` (polled) | – |

People are matched by primary, then secondary email (linked, no key; an admin account is left alone). Otherwise Core makes an account with a random password nobody knows, a username from the email or name (never one a user or organisation has, never a reserved one), and mints the person's own token *Pocket Core* through Forgejo 16's admin token API (older Forgejo and Gitea: by Basic auth with that password). The person's token may read their user, organisations, repositories and packages and write issues, pull request comments and notifications: it cannot push, change settings, mint tokens or administer. Replacing a key deletes only that account's *Pocket Core* tokens.

**Sign-in** cannot be set over Forgejo's API (auth sources live in the admin UI and the CLI). Switching it on returns the command to run instead: `forgejo admin auth add-oauth --name PocketCore --provider openidConnect --key <client id> --secret <secret> --auto-discover-url <issuer>/.well-known/openid-configuration`, then a restart; redirect `<public>/user/oauth2/PocketCore/callback`. People land in Core-made accounts only with `[oauth2_client] ENABLE_AUTO_REGISTRATION=true` and `ACCOUNT_LINKING=auto` (Forgejo matches the username first, then the email).

**Events** come by push: one **system webhook** per kind (push, create, delete, release, issues, pull request, repository, fork), JSON, with Core's secret as `Authorization: Bearer <secret>` (Forgejo cannot set other headers; Core accepts either). Created with `config.is_system_webhook: "true"`: without it Forgejo makes a *default* hook, copied only into repositories created later. Every delivery belongs to the **sender**: people Core has not linked are dropped, as are ghost, Actions and `[bot]` senders, mirror syncs, tag and empty pushes (create/delete cover them), drafts and other actions. Comments, labels, reviews, wiki, packages and Actions runs are left out.

**Forgejo blocks webhooks to private and tailnet addresses** unless allowed: set `FORGEJO__webhook__ALLOWED_HOST_LIST: core.bjelke.org` (Core's host) on the container and restart Forgejo before switching Events on.

## Backrest

Events only, **polled**, strictly read-only: Core never starts, cancels or changes anything. `USERNAME` and `PASSWORD` go as HTTP Basic auth on every call (Backrest checks it per request; empty when auth is off in Backrest, which Test warns about). Connect RPC, JSON:

| Call | Used by |
|---|---|
| `POST /v1.Backrest/GetConfig` | instance name, repo ids, plans with their repo and schedule, whether auth is off |
| `POST /v1.Backrest/GetSummaryDashboard` | Test, and on the first check each plan's last good backup |
| `POST /v1.Backrest/GetOperations` (`selector.instanceId`, `modnoGte`, `lastN`) | exactly the operations that changed since the last check |
| `GET /` and its `assets/*.js` | the version (the API has none; empty if the UI bundle changes shape) |

`GetConfig` returns the whole configuration, repository passwords included: Core decodes only the fields above and keeps nothing else (it already holds Backrest's login).

Only finished operations count. Backups: `backup_finished`, `backup_warning` (e.g. unreadable files), `backup_failed`. Check, prune and forget: every failure, and one `…_recovered` at the next success. Restores, and failing hooks (`hook_failed`, with the condition). **Overdue**: a plan whose last good backup (a warning counts, as on Backrest's dashboard) ended longer ago than its schedule's longest gap plus a grace is `backup_overdue`, once per stretch, and `backup_resumed` when a good one arrives. The schedule is `maxFrequencyHours`/`Days` or a cron (sampled over 62 days; unreadable = daily, `schedule_guessed`); grace is `GRACE_HOURS`, else a quarter of the interval, at least 2 hours (30 h for a daily plan). Messages are cut to 500 characters with the password and URL credentials removed. At most 50 events per check.

## Beszel

Events only, **polled**, read-only. Core signs in with the service's `EMAIL` and `PASSWORD` (a Beszel user; if that fails, the same as a PocketBase superuser, the `/_/` login) on every check and never stores the token. Reads `GET /api/beszel/info` (version), `/api/collections/systems/records`, `/api/collections/alerts/records` (Test) and `/api/collections/alerts_history/records`. Signing in changes nothing Beszel shows; PocketBase notes the login origin (a fixed User-Agent, `Pocket Core (Beszel connector)`).

A user sees only the systems shared with them and **only their own alerts**: alert events come from the alerts that account set. A superuser sees everyone's; the same alert set by two users is then merged into one event. **Paused and pending are never down**; down → pending → down is one outage. Alert history stores the alert's threshold, not the reading that fired it: events carry `threshold` and, for metrics on a system that is up, `current_value` (the reading at the check, not the average that fired). Up to 50 new alert records per check; the rest come next time.

Not implemented: sign-in and accounts. Both would need a PocketBase **superuser**: OIDC is `PATCH /api/collections/users` (`oauth2.providers`, redirect `<public>/api/oauth2-redirect`; new people only with `USER_CREATION=true`), accounts `POST /api/collections/users/records` plus adding them to each system's users.

## Komodo

Events, **polled**, and people **linked by hand**. Save an API key and secret (`API_KEY`, `API_SECRET`: Profile → API Keys, or a service user's under Settings → Users); an admin's key lets Core list Komodo's users for linking and see every resource, any other key works with what that user may read. Read-only: `GET /user` and `POST /read` (`{"type", "params"}`) with `GetVersion`, `ListUsers`, the servers/stacks/deployments summaries, `ListUpdates`, `GetUpdate`, `ListAlerts`, `GetAlert` and the resource lists for names.

No provisioning (Komodo has no per-person keys to mint; admins make users in Komodo, or Komodo's OpenID login makes them on first sign-in) and no sign-in switch: Komodo reads OpenID from its environment. To sign in with Core, set on the Komodo Core container `KOMODO_OIDC_ENABLED=true`, `KOMODO_OIDC_PROVIDER=<Core's issuer>`, `KOMODO_OIDC_CLIENT_ID`, `KOMODO_OIDC_CLIENT_SECRET` and `KOMODO_HOST=<Komodo's public URL>`; Komodo's redirect URI is `<KOMODO_HOST>/auth/oidc/callback`.

Finished operations (deploys, stack up/down/restart/pull, builds, repo pulls, procedures, actions, syncs, resource changes) and alerts are **household events**. An operation run by a Komodo user linked to a person also carries their `user_email`; schedules, webhooks, procedures and Komodo's other built-in operators never do. Operations still running are announced when they finish; failures are one `operation_failed`. Logs and resource TOML never go on the bus. At most 50 operations per check (the rest come next check) and 50 alert events.

## Dawarich

Events, **polled**, for the account whose `API_KEY` is saved on the service; link that account to its person under People (Dawarich has no API to list or create users, so there is no provisioning, and its OpenID sign-in is environment-only: `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_ISSUER`, `OIDC_REDIRECT_URI` on the container). Reads `GET /api/v1/health` (version header), `/api/v1/users/me`, the newest of `/api/v1/points` and `/api/v1/visits` of the last 14 days.

**Exact positions never go on the bus**: no coordinates, no addresses. Events: `tracking_stopped` (no new point for 12 hours) and `tracking_resumed`; `visit_detected` and `visit_confirmed` (name, times, duration); `city_entered` / `country_entered` when Dawarich reverse-geocodes points (a geocoder configured in Dawarich).

## Maloja

Events, **polled** from the public read API (`/apis/mlj_1/scrobbles`, newest first; `artistinfo` to spot a first listen). Maloja is one person's history with no accounts: link **Maloja's listener** to its person under People and every scrobble carries their email. An `API_KEY` on the service is optional: Core only reads, and Test checks the key that scrobbling devices use.

## Nextcloud

Needs: the Nextcloud service in Core with its URL, its public URL, and a Nextcloud **admin**:
- `USERNAME` and `APP_PASSWORD`: the admin's app password (Personal settings → Security → Devices & sessions). Core uses it for everything that only reads.
- `ADMIN_PASSWORD`, **Login password (connector only)**: the same admin's real login password. Nextcloud guards adding accounts and changing sign-in providers with *password confirmation* and refuses app passwords there. This field is **connector-only**: it is encrypted at rest and never goes into a device profile, an env file or an app answer, and nobody can set it per person. It does not work for an account with two-factor sign-in, so a dedicated admin account for Core is the cleanest choice.

**Test** shows the version and the number of accounts, and says what is missing: the login password, the `user_oidc` app (sign-in) or the `webhook_listeners` app (events, shipped with Nextcloud 30+).

| Nextcloud call | Used by | Writes? |
|---|---|---|
| `GET /status.php`, `GET /ocs/v2.php/cloud/user`, `GET /ocs/v2.php/cloud/apps?filter=enabled` | Test | – |
| `GET /ocs/v2.php/cloud/users/details` | Test, Provision, linking by hand, account events | – |
| `GET /ocs/v2.php/cloud/users/{id}` | Linking by hand | – |
| `POST /ocs/v2.php/cloud/users` | Provision: a missing person; user name = their email's local part, display name and email from Core, a 40-character random password used once and never stored, logged or returned (login password) | **yes** |
| `PROPFIND /remote.php/dav/files/{id}/` | Provision: open the new account's files once, as the new account | **yes** (welcome files) |
| `GET /ocs/v2.php/core/getapppassword` | Provision: as the new account, its own app password (*Pocket Core* under its Devices & sessions) | **yes** |
| `GET/POST/PUT/DELETE /ocs/v2.php/apps/user_oidc/api/v1/provider` | Sign-in on/off (writes with the login password; backed up first) | **yes** |
| `GET/POST/DELETE /ocs/v2.php/apps/webhook_listeners/api/v1/webhooks` | Events on/off | **yes** |
| `GET /ocs/v2.php/cloud/user` · `POST /ocs/v2.php/core/apppassword/rotate` | Replacing a key: as the person, with the stored app password: check it is theirs and not an admin, then Nextcloud's own rotation (the old app password stops working at once) | **yes** |

**Replacing a key** (a link switched off, see [above](#replacing-keys-when-a-link-is-switched-off)) never touches the account's password: if the stored app password was already revoked, the replacement fails and says so; set a new one by hand.

**Accounts.** A person is **linked** to the Nextcloud account with their email (nothing minted: Nextcloud cannot issue an app password for someone else). With none, Core creates one named like the email's local part and keeps the person's own `USERNAME` + `APP_PASSWORD`. An account with that name but **another email is never taken over**: provisioning stops with an error and an admin links the person by hand (people table, or `pocket-core connector nextcloud link <email> <account>`). Nextcloud sends its usual "account created" email to new accounts when mail is set up.

**Sign in with Pocket Core** needs the *OpenID Connect user backend* app (`user_oidc`, Apps → Security, or `occ app:install user_oidc`). Turning it on:
1. backs up `user_oidc`'s providers to `DATA_DIR/connectors/nextcloud-sso-<timestamp>.json`;
2. creates (or updates) provider **Pocket Core**: Core's discovery URL, its client id and secret, scopes `openid email profile`, and
   - **user id = the `pocket_uid` claim**, not hashed (`uniqueUid` off). Core puts `pocket_uid` in the tokens of a connector's client only for people it linked to an account in that service, holding that account's id. So everyone lands in the account Core linked them to (linked by email or by hand), nobody lands in an account just because the name matches, and a person Core has not linked is refused (*Failed to provision the user*);
   - **no group provisioning**: nobody's groups or admin rights change;
   - **name and email stay Nextcloud's**: they are mapped to claims Core never sends, so a linked account keeps its own.

The login page gets a *Log in with Pocket Core* button; password login stays. Turning sign-in off deletes the provider. Redirect URIs: `https://<public>/apps/user_oidc/code` and `…/index.php/apps/user_oidc/code`. `user_oidc` only signs people in over https, and it counts refused attempts toward Nextcloud's brute-force throttle. Desktop and phone apps sign in through the browser login flow, which shows the same button.

**Events by push** ([topics](events.md#nextcloud-source-connectornextcloud)): five box-wide webhooks (`webhook_listeners` runs them for every account), made with the admin app password and carrying Core's per-hook secret header: file created, file deleted, calendar object created, updated and moved to the trash. Nextcloud sends them from its background jobs, so they arrive with its cron (every 5 minutes on a default setup). Core attributes each one to the person linked to the account it belongs to (the file's owner or the calendar's owner) and drops:
- files outside someone's own Files (previews, versions, trash, upload chunks: filtered in Nextcloud already), hidden files, Office lock files and `.part` files;
- anything from **before Core connected the person**: a new account's welcome files are copied when Core opens its files during provisioning, before the link is saved, so they never reach the bus;
- the birthday calendar; and the title and place of anything marked private or confidential.

New and deleted accounts are polled.

## Paperless-ngx

Needs: the Paperless service in Core with its URL, its public URL and a **superuser's** API token (`TOKEN`: My Profile → API Auth Token). **Test** shows the version and the number of accounts (Paperless's own `consumer` and `AnonymousUser` are not counted).

| Paperless call | Used by | Writes? |
|---|---|---|
| `GET /api/ui_settings/` | Test (whose token, superuser?) | – |
| `GET /api/users/` | Test, Provision, linking by hand, account events | – |
| `GET /api/groups/?name__iexact=Pocket Core` · `POST /api/groups/` | Provision: the group new accounts go in (created once) | **yes** |
| `POST /api/users/` | Provision: a missing person; user name = the email's local part, first/last name and email from Core, a 40-character random password used once and never stored, logged or returned | **yes** |
| `POST /api/token/` | Provision: as the new account, its own API token | **yes** |
| `GET/POST/DELETE /api/workflows/` | Events on/off | **yes** |
| `GET /api/ui_settings/` · `POST /api/profile/generate_auth_token/` | Replacing a key: as the person, with the stored token: check it is theirs, then regenerate it (Paperless keeps one token per account, so the old one is gone) | **yes** |
| `PATCH /api/users/{id}/` `{password}` · `POST /api/token/` | Replacing a key, **only when the stored token no longer works**: the superuser sets a new random password, Core signs in once for a token and regenerates it | **yes** |

**Accounts.** A person is **linked** to the account with their email. With none, Core creates one in the **Pocket Core** group and keeps the person's own `TOKEN`. That group may view, add, change and delete documents, notes, tags, correspondents, document types, storage paths, custom fields, saved views, share links, mail accounts and rules, and see their tasks. It does not cover users, groups, workflows or settings. Paperless's object permissions still apply, so a member sees their own documents and what is shared with them; adjust the group in Paperless if the household wants more. An account with that name but **another email is never taken over**: link the person by hand. Paperless's first admin usually has `root@localhost` as email, so the owner is linked by hand (`pocket-core connector paperless link <email> admin`) before provisioning anyone.

**Sign in with Pocket Core** cannot be switched on by Core: Paperless reads its OpenID providers from its environment at start. To set it up by hand:
1. In Core, Settings → OIDC clients → add a client with redirect URI `https://<paperless>/accounts/oidc/pocket-core/login/callback/`.
2. In Paperless's environment:
   ```
   PAPERLESS_APPS=allauth.socialaccount.providers.openid_connect
   PAPERLESS_SOCIALACCOUNT_PROVIDERS={"openid_connect":{"OAUTH_PKCE_ENABLED":true,"APPS":[{"provider_id":"pocket-core","name":"Pocket Core","client_id":"<id>","secret":"<secret>","settings":{"server_url":"https://<core>/.well-known/openid-configuration"}}]}}
   ```
   and restart Paperless.
3. People with an existing account connect it once from their Paperless profile (social accounts). Set `PAPERLESS_SOCIAL_AUTO_SIGNUP` / `PAPERLESS_SOCIALACCOUNT_ALLOW_SIGNUPS` to taste.

**Events by push** ([topics](events.md#paperless-ngx-source-connectorpaperless)): two workflows, *Pocket Core · document added* (Document Added, every source: consume folder, API, mail, web) and *Pocket Core · document updated*. Each has one webhook action that sends JSON (`use_params`, `as_json`) rendered from Paperless's placeholders (id, title, owner's user name, correspondent, type, created and added dates, original file name; never the document itself) with Core's per-hook secret header. Workflows run for every account, so Core attributes each delivery to the person linked to the owner. It drops documents without an owner and documents added before Core connected the person. Paperless sends from its task queue and retries a failed call three times. New and deleted accounts are polled.

## Jellyseerr

Needs: the Jellyseerr service in Core with its URL, its public URL and its **API key** (Settings → General; it acts as the admin). Tested against Seerr 3.5.

| Jellyseerr call | Used by | Writes? |
|---|---|---|
| `GET /api/v1/status`, `GET /api/v1/auth/me` | Test | – |
| `GET /api/v1/user` | Test, Provision, linking by hand, events | – |
| `GET /api/v1/user/{id}` | Linking by hand | – |
| `POST /api/v1/user/import-from-jellyfin` | Provision: a person with no Jellyseerr user yet | **yes** |
| `GET /api/v1/request`, `GET /api/v1/issue` | Events | – |
| `GET /api/v1/movie/{tmdb}`, `GET /api/v1/tv/{tmdb}` | Events: titles of what changed | – |

**Accounts.** Jellyseerr users come from Jellyfin, and an imported user's "email" is their Jellyfin name, so matching by email alone rarely works. A person is linked:
1. to the user with their email; else
2. to the user whose Jellyfin account is the one **Core's Jellyfin connector** linked them to (Core hands every connector the person's links in other services); else
3. their Jellyfin account is **imported** into Jellyseerr, which gives it Jellyseerr's default permissions.

Someone Core has not linked to a Jellyfin account gets an error asking for that first, or an admin links them by hand. Jellyseerr has no per-person keys, so nothing is minted.

**Sign-in:** none. People sign in to Jellyseerr with their Jellyfin account.

**Events are polled** ([topics](events.md#jellyseerr-source-connectorjellyseerr)), not pushed. Jellyseerr has a single webhook slot, often already used (for example by a Jellyfin plugin), and Core does not take it over. Each check reads the 100 most recently changed requests and issues and compares them with the previous check. Each event belongs to whoever made the request or reported the issue. A request Core sees for the first time with an id at or below the highest one it already knew (an old request edited) produces no event, because there is nothing to compare it with.

## Navidrome

Needs: the Navidrome service in Core with its internal URL and `USER` / `PASSWORD` of a **Navidrome administrator**. Navidrome has no API keys: Core signs in for a session token on every call.

| Navidrome call | Used by | Writes? |
|---|---|---|
| `POST /auth/login` | Test, Provision: an admin session | – (opens a session) |
| `GET /api/user` | Test, Provision | – |
| `POST /api/user` | Provision: a missing person — `userName` from the email's local part (a number added when taken), name, email, a 32-character random password, `isAdmin: false` | **yes** |

Accounts carry emails: a person whose email already has an account is **linked** (its password is unknown to Core, so nothing is minted; an administrator's account is never touched). A new account gets the libraries Navidrome gives new users by default, and the person's credential is their `USER` and `PASSWORD`, what Pocket Music and Subsonic clients sign in with. A person who keeps their own credential gets no account made for them. No sign-in, no events, no key replacement.

## From the console

Everything above is also available without a browser, through the running server (so locking and the bus behave the same):

```sh
# an admin's personal token: Users → My API tokens
docker exec -e POCKET_CORE_TOKEN=pcu_… pocket-core pocket-core connector immich test
docker exec -e POCKET_CORE_TOKEN=pcu_… pocket-core pocket-core connector immich status
#   enable | disable | enable-events | disable-events | poll | recreate-hooks | enable-sso | disable-sso
#   enable-provisioning | disable-provisioning | provision <email> | provision-all
#   remote-accounts | link <email> <account> | unlink <email>     (Jellyfin)
```

## Writing a connector

A connector lives in `internal/connectors`, implements one interface and registers itself in `init()`. It talks HTTP to its app and nothing else: no store, no server imports. Core gives it a `Target`:

```go
type Target struct {
	BaseURL   string            // the service's internal URL
	PublicURL string            // where people's browsers and phones reach it
	Fields    map[string]string // decrypted service fields (the admin credential)
	HTTP      *http.Client      // with timeouts
	DataDir   string            // DATA_DIR/connectors, for backups
}

type Connector interface {
	Type() string               // the catalog type: "immich"
	Capabilities() Capabilities // {Provision, SSO, Events}
	Test(ctx, Target) (Info, error)
	Provision(ctx, Target, Person) (Account, error)
	ConfigureSSO(ctx, Target, SSOConfig) error
	SSORedirects(publicURL string) []string
	Poll(ctx, Target, state json.RawMessage) ([]Event, json.RawMessage, error)
}

func init() { Register(myApp{}) }
```

An app without emails on its accounts also implements `Linker` (and sets `Capabilities.Link`): `RemoteAccounts` lists the accounts for the admin's picker, and `Link(ctx, Target, remoteID)` checks the account exists and returns `Account{Status: StatusLinked, Fields: {USER_ID…}}`. Poll events then carry `RemoteUserID`, and Core turns it into `user_email` through the accounts it linked.

Polled events about one person carry `RemoteUserID` (the app account); Core fills in `user_email` from the linked person. Set `Personal: true` on events that are someone's own activity (what they read, listen to, push): Core then **drops** them when that account is linked to nobody, instead of publishing them for the household. Pushed deliveries are attributed through `Delivery.OwnerID` and dropped the same way unless `Shared`. An app that cannot send `X-Pocket-Hook` may send the secret as `Authorization: Bearer <secret>` or as a Basic-auth password.

A connector that mints people's keys should also implement `KeyRotator`: `RotateKey(ctx, Target, AccountRef{RemoteID, Email, Fields}) (Rotated{Fields, Note}, error)`. Core calls it only for accounts the connector created and whose stored key it minted, when one of that person's service links is switched off. Mint a new key, delete the old one(s) Core made for that account, touch nothing else (check the account isn't an admin; never the box key). Return `Fields` *and* an error when the new key exists but an old one survived. Without it, Core records that keys there can't be replaced automatically.

A connector whose app can call a URL also implements `Pusher`; Core then manages hooks for it (create, check, heal, delete, receive, dedupe, attribute, count) and the connector only translates:

```go
type Pusher interface {
	PushTriggers() []PushTrigger // [{Trigger: "AssetCreate", Name: "Pocket Core · uploads"}, …]
	PushPerUser() bool           // the app runs a subscription only for its owner's things
	WhoAmI(ctx, Target) (RemoteUser, error)
	Subscribe(ctx, Target, Subscription) (remoteID string, err error) // {Trigger, Name, Description, URL, HeaderName, HeaderValue}
	Subscriptions(ctx, Target) (map[string]string, error)              // remote id → name, for the credential
	Unsubscribe(ctx, Target, remoteID string) error                    // gone already = nil
	Decode(Target, trigger string, body []byte) (Delivery, error)      // {OwnerID, DedupeKey, Event, Drop}
}
```

Every `Pusher` call acts with the credential in `Target.Fields`: for a per-person subscription Core lays the person's override over the box-wide fields. `Decode` returns the app account the delivery is about (`OwnerID`; Core maps it to a person through `connector_accounts.remote_id` and checks it against the hook), a `DedupeKey`, and an `Event` without `user_email` (Core adds the Core person's). Set `Drop` with a reason for things that are not news. Keep exact locations, file paths and blobs out of payloads.

Rules of the road:

- **Test** is read-only. When the credential is not good enough, say which permissions are missing.
- **Provision** finds by email first. Existing account → `Account{Status: StatusLinked}` and leave it alone. New account → `StatusOK` and, when you can mint one, `Fields` keyed like the catalog entry's fields (`API_KEY`, `USER_ID`…). When `Person.KeepOwnCredential` is set, make or find the account but mint nothing. Throwaway passwords come from `RandomPassword`; never return, store or log them, and scrub them from error text.
- **ConfigureSSO** changes only the app's OIDC settings and backs up whatever it overwrites to `Target.DataDir` first. `SSORedirects` must list every redirect URI the app (web and mobile) will send.
- **Poll** with an empty `state` records a baseline and returns no events. Return events with a short `Name` (`user_created`; Core prefixes the type) and set `UserEmail` when the event belongs to a person. The returned state is stored as-is and handed back next time. A `Pusher` polls only for what its pushes do not cover.
- Test against a fake of the app built with `httptest` (see `internal/server/connectors_test.go`), never against a live instance.
