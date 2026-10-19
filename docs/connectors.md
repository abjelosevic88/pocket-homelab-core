# Connectors

A **service** in Core is a third-party app with box-wide credentials (Immich with an admin's API key). A **connector** lets Core act on that app for the household, so nobody copies keys between admin pages:

1. **Accounts.** When an admin creates a person, Core makes (or finds) that person's account in the app and stores **the person's own credential** as their per-person override. Their paired phone then receives their key, not the admin's.
2. **Sign in with Pocket Core.** Core creates an OpenID Connect client for the service and writes it into the app's own settings.
3. **Events.** Core polls the app and publishes what changed on the bus as `<type>.<event>`.

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
| Events | the *Events* switch, then every `poll_interval_s` (default 300, minimum 60) + up to 10 % jitter | one poll per connector at a time, 60 s timeout; the **first poll records a baseline only**; turning events on again starts a new baseline. Published with source `connector:<type>`; when an event belongs to a person the payload carries `user_email` (and the subject is `user:<email>`) |
| Master switch | *Connector on* | off: Core makes no accounts and stops polling. Sign-in stays however the app has it |

Core also publishes `core.connector.provisioned` `{type, service_id, user_email, status, created}` after each successful provisioning.

## Immich

Needs: the Immich service in Core with its internal URL, its public URL (where people sign in) and the API key of an **Immich admin** with `adminUser.read`, `adminUser.create`, `systemConfig.read`, `systemConfig.update`, `server.statistics` (or `all`). **Test** tells you what the key can do.

| Immich call (below `/api`) | Used by | Writes? |
|---|---|---|
| `GET /server/version` | Test | – |
| `GET /api-keys/me` | Test (the key's permissions) | – |
| `GET /admin/users` | Test, Provision, Poll | – |
| `POST /admin/users` | Provision: a missing person — email, name, a 40-character random password used once and never stored, logged or returned, `shouldChangePassword: false`, `notify: false`, `isAdmin: false` | **yes** |
| `POST /auth/login` | Provision: sign in once as the new person (Immich only lets a user mint their own keys) | **yes** (a session) |
| `POST /api-keys` | Provision: the person's key, named `Pocket Core`, permission `all` | **yes** |
| `POST /auth/logout` | Provision: close that session | **yes** |
| `GET /system-config` | Sign-in on/off: read the whole configuration | – |
| `PUT /system-config` | Sign-in on/off: write it back with only `oauth` changed | **yes** |
| `GET /server/statistics` | Poll: per-person photo and video counts | – |

`/system-config` is deprecated since Immich 3.2 in favour of `/admin/config` (still alpha); the connector falls back to `/admin/config` if the old path ever answers 404.

**Sign-in on** writes a backup of the full configuration to `DATA_DIR/connectors/immich-system-config-<timestamp>.json` (mode 0600; it contains Immich's secrets) and then changes only `oauth`: `enabled: true`, `issuerUrl` = Core's issuer, `clientId`, `clientSecret`, `scope: "openid email profile"`, `buttonText: "Sign in with Pocket Core"`, `autoRegister: true`, `autoLaunch: false`, `mobileOverrideEnabled: true`, `mobileRedirectUri: https://<public>/api/oauth/mobile-redirect`. Password login is not touched. **Sign-in off** backs up again and sets `oauth.enabled: false`. To undo by hand, PUT a backup file back to `/api/system-config` with an admin key.

Redirect URIs registered on the Core client: `https://<public>/auth/login`, `https://<public>/user-settings`, `app.immich:///oauth-callback`, `https://<public>/api/oauth/mobile-redirect`. Immich fetches `https://<core>/.well-known/openid-configuration` and the token/userinfo endpoints **from the Immich server**, so the Immich container must be able to reach Core's public URL.

Immich links an OIDC login to an existing account with the same email, so a person Core provisioned (or linked) signs in to the same account either way.

**Events:** `immich.user_created` `{user_email, immich_user_id, name}`, `immich.assets_added` / `immich.assets_removed` `{user_email, immich_user_id, photos, videos}` (counts are the change since the last poll).

## From the console

Everything above is also available without a browser, through the running server (so locking and the bus behave the same):

```sh
# an admin's personal token: Users → My API tokens
docker exec -e POCKET_CORE_TOKEN=pcu_… pocket-core pocket-core connector immich test
docker exec -e POCKET_CORE_TOKEN=pcu_… pocket-core pocket-core connector immich status
#   enable | disable | enable-events | disable-events | poll | enable-sso | disable-sso
#   enable-provisioning | disable-provisioning | provision <email> | provision-all
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

Rules of the road:

- **Test** is read-only. When the credential is not good enough, say which permissions are missing.
- **Provision** finds by email first. Existing account → `Account{Status: StatusLinked}` and leave it alone. New account → `StatusOK` and, when you can mint one, `Fields` keyed like the catalog entry's fields (`API_KEY`, `USER_ID`…). When `Person.KeepOwnCredential` is set, make or find the account but mint nothing. Throwaway passwords come from `RandomPassword`; never return, store or log them, and scrub them from error text.
- **ConfigureSSO** changes only the app's OIDC settings and backs up whatever it overwrites to `Target.DataDir` first. `SSORedirects` must list every redirect URI the app (web and mobile) will send.
- **Poll** with an empty `state` records a baseline and returns no events. Return events with a short `Name` (`assets_added`; Core prefixes the type) and set `UserEmail` when the event belongs to a person. The returned state is stored as-is and handed back next time.
- Test against a fake of the app built with `httptest` (see `internal/server/connectors_test.go`), never against a live instance.
