# Single sign-on

Core is a small OpenID Connect provider. One account per person, every app on the box logs in with it.

| Endpoint | |
|---|---|
| `/.well-known/openid-configuration` | discovery |
| `/oidc/authorize` | authorization-code flow, `response_type=code`, PKCE `S256`/`plain`; public clients must use PKCE |
| `/oidc/token` | `authorization_code` and `refresh_token`; client auth by Basic, POST body or none (public) |
| `/oidc/userinfo` | Bearer access token |
| `/oidc/jwks` | RS256 key (generated on first start, kept in the database) |
| `/oidc/logout` | `end_session_endpoint`; honours `post_logout_redirect_uri` |
| `/api/v1/auth/introspect` | app-authenticated token check (not RFC 7662 form-encoded; JSON) |

Claims: `sub` (user id), `email`, `email_verified`, `name`, `preferred_username`, `groups` (`users`, plus `admin`). A client a connector made for a service also gets `pocket_uid`: the id of the account in that app Core linked the person to, and only when there is one (Nextcloud's `user_oidc` signs people in by it). ID tokens live 1 h, access tokens 1 h, refresh tokens 30 d (rotated on use). There is no consent screen: one box, one household.

## Registering a relying party

Settings → Single sign-on → **Add client**, or let a Pocket app ask in its [manifest](manifest.md). Redirect URIs are matched exactly.

Examples with `ISSUER = https://core.example.com`:

- **Immich** – Administration → Settings → OAuth: issuer URL `ISSUER`, client id/secret, scope `openid email profile`, button text "Sign in with Pocket". Redirect: `https://photos.example.com/auth/login` and `app.immich:///oauth-callback`.
- **Nextcloud** – app *OpenID Connect user backend* (`user_oidc`): discovery `ISSUER/.well-known/openid-configuration`; redirect `https://cloud.example.com/apps/user_oidc/code`. Map groups from `groups`.
- **Paperless-ngx** – `PAPERLESS_SOCIALACCOUNT_PROVIDERS` with `openid_connect`, server URL `ISSUER`; redirect `https://docs.example.com/accounts/oidc/pocket/login/callback/`.
- **Jellyfin** – SSO-Auth plugin, OpenID mode, endpoint `ISSUER`; redirect `https://jellyfin.example.com/sso/OID/redirect/pocket`.

The browser must reach `BASE_URL` for `/oidc/authorize`; the relying party must reach `OIDC_ISSUER` for `/oidc/token` and `/oidc/jwks`.
