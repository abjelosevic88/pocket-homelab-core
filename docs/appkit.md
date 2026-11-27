# The shared app chassis: `sdk/appkit` and the Pocket UI kit

Every Pocket app used to carry its own copy of the same ~20 files: the OpenID
Connect relying party, the order in which a request's credentials are tried,
the link-route allowlist, connection-mode reports, five React components and
two stylesheets. Each copy drifted a little, so one fix (the hover underline,
the `.block` login button, chart stretching) meant touching dozens of repos.

The chassis now lives here, once, and reaches the apps the same way the SDK
does: `scripts/sync-sdk.py` copies it into each app with checksums.

| Piece | In Core | In an app | What it replaces |
|---|---|---|---|
| Go chassis | `sdk/appkit/` | `third_party/pocket-core/sdk/appkit/` (part of the SDK snapshot) | most of `oidc.go`, `auth.go`, `links.go`, `modes.go`, `core_identity.go` |
| UI kit | `sdk/ui/` | `web/src/pocket-ui/` + `SHA256SUMS` | `components/{ui,Select,AskButton,ErrorBoundary,PocketModelSelect}.tsx`, `styles/{app,settings}.css` |

appkit holds **no store**: the app hands it functions that look people up in
its own database, so each app keeps its own `store.User` and tables. Standard
library only, like the rest of the SDK.

## Go: what an app wires

```go
// server.go — built once in New
s.rp, s.auth = s.newRP(), s.newAuth()
s.modes = &appkit.ModeReporter{Core: s.core, Modes: s.connModes, Log: s.log}

// oidc.go — the relying party; the protocol is appkit's
func (s *Server) newRP() *appkit.RP {
	return &appkit.RP{
		Settings: func() appkit.OIDCSettings {
			return appkit.EnvOrCore(s.core, s.cfg.OIDCIssuer, s.cfg.OIDCClientID, s.cfg.OIDCClientSecret, s.cfg.OIDCName, s.cfg.OIDCAutoCreate)
		},
		CallbackURL: s.oidcCallbackURL,
		Login: func(ctx context.Context, id appkit.Identity, autoCreate bool) (string, error) {
			u, err := s.identityUser(ctx, id.Issuer, id.Subject, id.Email, id.Name, autoCreate)
			if err != nil {
				return "", err // "No Pocket X account for … or identity could not be linked"
			}
			return s.store.CreateSession(ctx, u.ID, s.cfg.SessionTTL)
		},
		EndSession: func(ctx context.Context, sid string) { _ = s.store.DeleteSession(ctx, sid) },
		SetSession: func(w http.ResponseWriter, sid string) { s.setSessionCookie(w, sid, s.cfg.SessionTTL) },
		Core: s.core, Cookie: "pk_oidc", Secure: s.cfg.SecureCookies, AppName: "Pocket X", Log: s.log,
	}
}

// auth.go — who a request is for
func (s *Server) newAuth() *appkit.Auth[*store.User] {
	return &appkit.Auth[*store.User]{
		Core: s.core, Slug: "x", Mode: func() string { return s.cfg.AuthMode },
		ProxyHeader: s.cfg.AuthProxyHeader, ProxyEmailHeader: s.cfg.AuthProxyEmailHeader,
		SessionCookie: sessionCookie,
		APIToken: s.store.GetUserByAPIToken, Session: s.store.GetSessionUser,
		Person: s.firstOrSystemUser, Identity: s.coreIdentityUser,
		Allow: linkAllows,
	}
}
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return s.auth.Require(func(r *http.Request, u *store.User, via appkit.Via) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), userKey, u))
	})(next)
}

// links.go — what a linked app may do here
var linkRoutes = appkit.Routes{
	appkit.R("GET", `^/api/v1/auth/me$`, ""),
	appkit.R("GET", `^/api/v1/things$`, "things:read"),
}
var linkAllows = linkRoutes.Allow(portable, capabilities)

// http.go
r.Get("/auth/oidc/start", s.rp.Start)
r.Get("/auth/oidc/callback", s.rp.Callback)
r.Post("/client-error", appkit.ClientErrors(s.log, s.whoFor))
```

`Auth.Resolve` tries, in order: a bearer token (`pcx_` link token checked
against `Allow` → 403 `ErrLinkScope` outside it; `pcd_/pcu_/pco_` through
Core; else the app's own API token), then `Fallback` when set and there was no
bearer token (Basic auth for Nextcloud-style clients…), then `AUTH_MODE`
(`none` → the box's person, `proxy` → the trusted header, else the session
cookie, checked with Core when the session came from it). `Via` carries the
acting app's slug and name for a link token.

Also there: `ConnMode` / `ModeCore|Direct|Off`, `LinkReason(self, target,
reason)`, `SessionCookie{Name,Secure}.Set/Clear`, `OutboxHealth`,
`WriteJSON/WriteErr`, `SafeNext`, `RandomToken`, `BearerToken`.

Set `Auth.Slug` to the app's slug: a link token must then be for this app
(Core already refuses others; this checks again). Apps that only trust Core's
sign-in client once they know their `BASE_URL` use `appkit.EnvOrCoreAt(s.core,
s.cfg.BaseURL, …)`. `ModeReporter.Soon` is safe anywhere the old code did
`go s.reportModes(…)`.

Anything an app does differently stays in the app: put it in the functions it
hands appkit (`Settings` may refuse Core's client without a `BASE_URL`,
`Login` may return a `*LoginError` with its own sentence, `Fallback` adds a
credential path).

## UI kit

The kit is the skill's "copy verbatim" list, at the version the 97-app family
uses (`ui.tsx` is the superset with submenus and `ContextMenu`; the
`ErrorBoundary` also reports the crash to `POST /api/v1/client-error`). Files
import only React, `../components/icons` (`Icon`) and, for
`PocketModelSelect.tsx`, `../lib/api` (`api`, `V1`).

An app takes only the kit files it opted into — the ones in its
`web/src/pocket-ui/`, plus any named with `--ui`. The ten apps on the
Calendar-design family (`select-trigger` Select: notes, tasks, …) take
`AskButton.tsx` and `ErrorBoundary.tsx` only; their `ui.tsx`/`Select.tsx`/CSS
are a different component set.

Never edit `web/src/pocket-ui/` in an app: the Dockerfile's
`sha256sum -c SHA256SUMS` fails the build. Change `sdk/ui/` here and sync.

## Syncing and checking

```sh
python3 scripts/sync-sdk.py --app still                 # SDK snapshot + the kit files the app has
python3 scripts/sync-sdk.py --app still --ui all        # opt an app into the whole kit
python3 scripts/sync-sdk.py --check                     # every app: drift + guards, exit 1 on findings
python3 scripts/sync-sdk.py --check --json              # the same, for Fleet Health
```

`--check` reports per app: SDK snapshot current or drifted, kit files drifted,
and these guards (the papercuts that kept coming back):

| Guard | Why |
|---|---|
| `hover-underline` | a `:hover` rule with `text-decoration: underline` — colour or background only |
| `block-beats-btn` | `.block { display: block }` loaded after `base.css` beats `.btn`'s inline-flex and puts the login button's icon above its label; use `.block:not(.btn)` |
| `stretched-svg` | `preserveAspectRatio="none"` stretches text and dots; measure the width instead |
| `kit-copy` | a component is both in `components/` and `pocket-ui/` |
| `go.mod` | the snapshot's `replace` is missing |
