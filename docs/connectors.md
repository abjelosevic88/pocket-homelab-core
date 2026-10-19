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

`/system-config` is deprecated since Immich 3.2 in favour of `/admin/config` (still alpha); the connector falls back to `/admin/config` if the old path ever answers 404.

**Sign-in on** writes a backup of the full configuration to `DATA_DIR/connectors/immich-system-config-<timestamp>.json` (mode 0600; it contains Immich's secrets) and then changes only `oauth`: `enabled: true`, `issuerUrl` = Core's issuer, `clientId`, `clientSecret`, `scope: "openid email profile"`, `buttonText: "Sign in with Pocket Core"`, `autoRegister: true`, `autoLaunch: false`, `mobileOverrideEnabled: true`, `mobileRedirectUri: https://<public>/api/oauth/mobile-redirect`. Password login is not touched. **Sign-in off** backs up again and sets `oauth.enabled: false`. To undo by hand, PUT a backup file back to `/api/system-config` with an admin key.

Redirect URIs registered on the Core client: `https://<public>/auth/login`, `https://<public>/user-settings`, `app.immich:///oauth-callback`, `https://<public>/api/oauth/mobile-redirect`. Immich fetches `https://<core>/.well-known/openid-configuration` and the token/userinfo endpoints **from the Immich server**, so the Immich container must be able to reach Core's public URL.

Immich links an OIDC login to an existing account with the same email, so a person Core provisioned (or linked) signs in to the same account either way.

**Events** ([topics and payloads](events.md#immich-source-connectorimmich)): `immich.asset_created` and `immich.asset_processed` by push, one per asset; `immich.user_created` from the periodic check.

How Immich (3.3) workflows behave, read from its source (`workflow-execution.service.ts`, `workflow.repository.ts`, `immich-plugin-core`): a workflow **belongs to the user who created it** and runs only for **that user's** assets (`AssetCreate` / `AssetMetadataExtracted` look up workflows by the asset owner). So Core creates, per connected person and **with that person's own key**, two workflows — `Pocket Core · uploads` (trigger `AssetCreate`) and `Pocket Core · details` (trigger `AssetMetadataExtraction`) — each with one step, the built-in `immich-plugin-core#webhook` (`{url, method: "POST", headerName: "X-Pocket-Hook", headerValue: <secret>}`), enabled, logging off. The step POSTs `{"type": "AssetV1", "trigger": "…", "data": {"asset": {…}}}` where `asset` carries `id`, `ownerId`, `type`, `originalFileName`, `fileCreatedAt`, `localDateTime`, `createdAt`, `visibility`, `exifInfo` (null until metadata extraction; GPS included) and more; Immich ignores the answer and does not retry.

Whose key: a person's own override (the key Core minted at provisioning, or one set by hand); for a **linked** person without one, the service's box-wide key **only if** `GET /users/me` with it returns that person's Immich account (typically the owner whose admin key it is). Anyone else shows **needs a key** and gets no workflows. A key that belongs to a different Immich account is an error. A delivery is published only when the asset's `ownerId` is the hook's person's Immich account and that person is enabled and in the service's audience; the payload's `user_email` is their **Core** email.

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

**Accounts.** Jellyfin accounts have no email, so a person matches an account named like their email or the part before `@` (the same name Core sends as `preferred_username`). A match is **linked**: Core saves the account's `USER_ID` as the person's credential and mints nothing, because Jellyfin cannot issue a token for someone else and the password is unknown. A new account gets `USER_ID` plus its own token as `API_KEY`. Nobody knows its password, so the person signs in with Pocket Core (needs the SSO plugin) or Quick Connect, or an admin sets a password in Jellyfin.

**Linking by hand.** When the names don't line up (the owner watches as `root`), pick the account in the people table, or run `pocket-core connector jellyfin link <email> <account>`. A link made by hand is marked *by hand*, and provisioning never changes it. Unlinking removes the link and the credential Core saved for it, never one the person set themselves. One Jellyfin account can be linked to one person only.

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
