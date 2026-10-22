# Getting started

From an empty box to "everyone signs in to every Pocket app with one account". Steps 1–4 are needed; 5–7 are optional.

## 1. Decide the address

Pick the one URL that phones, browsers **and** the app containers will use for Core, e.g. `https://core.home.example` or `http://192.168.1.10:8179`. It becomes `BASE_URL`, is baked into pairing QR codes and is the issuer every app checks at sign-in. Changing it later means re-pairing devices and restarting apps.

Then match the cookie setting to it:

| Core is opened over | `SECURE_COOKIES` |
|---|---|
| `https://…` (reverse proxy with a certificate) | `true` |
| `http://<lan-ip>:<port>` | `false` |

With `true` on plain http the browser drops the session cookie: the password is accepted and you land on the login page again. Core logs a warning at startup and the login page says so, but nothing else stops it.

## 2. Start Core and create the admin

```sh
mkdir pocket-core && cd pocket-core
cat > .env <<'ENV'
BASE_URL=http://192.168.1.10:8179
SECURE_COOKIES=false
ADMIN_EMAIL=you@example.com
ADMIN_PASSWORD=at-least-8-characters
ENV
docker run -d --name pocket-core --restart unless-stopped -p 8179:8080 \
  --env-file .env -v ./data:/data ghcr.io/abjelosevic88/pocket-homelab-core:latest
docker logs pocket-core   # look for "level=WARN" lines
```

- `ADMIN_EMAIL` / `ADMIN_PASSWORD` create the first admin **only on an empty database**. Editing them afterwards changes nothing (Core logs a warning when they no longer match). To change a password: `docker exec pocket-core pocket-core reset-password -email you@example.com -password new-password`.
- Leave them out and the first visit shows a setup form instead; whoever fills it in becomes the admin.
- Use the **same email you already use in your Pocket apps**. Apps match people by email (step 4).

Sign in at `BASE_URL`. Ten wrong passwords lock that IP or account for 15 minutes; `docker restart pocket-core` clears it.

## 3. Connect each Pocket app

Copy the **registration key** from Core → Settings. In every Pocket app's compose file add:

```yaml
    environment:
      BASE_URL: https://habits.home.example          # the app's own address, as the browser sees it
      CORE_URL: http://host.docker.internal:8179     # how the app container reaches Core
      CORE_REGISTRATION_KEY: ${CORE_REGISTRATION_KEY}
      # CORE_APP_URL: http://host.docker.internal:8169  # how Core reaches the app; defaults to BASE_URL
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

Restart the app. It registers on start (and keeps retrying until Core answers) and shows up under **Apps** in Core.

- The app's **`BASE_URL` must be real**. The sign-in callback is `BASE_URL/api/v1/auth/oidc/callback`; the compose default `http://localhost:8080` sends the browser to the wrong place.
- The app container must also reach **Core's** `BASE_URL` to finish sign-in. A public domain that only resolves from outside, or a router without hairpin NAT, breaks this; test with `docker exec <app> wget -qO- <core BASE_URL>/.well-known/openid-configuration`.

## 4. Sign in to the apps

Each connected app's login page now has **Sign in with Pocket Core**. The first time, the app matches the Core account to its own account by email:

- **Same email** → you land in your existing account with all your data.
- **Different email** → the app creates a new, empty account. Your data is not gone; fix the email on one side, or sign in with the old password and change it there.

Apps without Core keep working exactly as before; the password login stays.

## 5. Add people

Core → **People → Add person**. Each person signs in to Core once and then to every connected app with "Sign in with Pocket Core"; they only ever see their own data.

## 6. Pair phones (optional)

**Pair a device** shows a QR code (valid 10 minutes). Scanning it in Pocket Homelab gives the phone every service the person may use, with its own revocable token. See [pairing.md](pairing.md).

## 7. Add the rest of the box (optional)

**Services → Add service** for Jellyfin, Immich, Nextcloud…, with credentials. **Audience** decides who gets each one in their profile (keep admin keys on `admins`). Some services also have connectors that create accounts and sign-in for people: [connectors.md](connectors.md).

## When sign-in doesn't work

| What you see | Cause | Fix |
|---|---|---|
| Password accepted, back on the login page | `SECURE_COOKIES=true` on plain http | `SECURE_COOKIES=false`, or serve Core over https |
| "invalid email or password" with the password from `.env` | Admin was created earlier; `.env` was edited after | `pocket-core reset-password` |
| "too many failed sign-ins" | Lockout after 10 failures | wait 15 min or restart Core |
| No "Sign in with Pocket Core" button in an app | App not registered | check `CORE_URL`, the key and `extra_hosts`, then the app's logs |
| App sign-in ends on `localhost` or an error page | App's `BASE_URL` is wrong | set it to the app's real address, restart |
| App sign-in fails after Core's login | App container can't reach Core's `BASE_URL` | step 3, last bullet |
| Signed in, but the app is empty | Different email in Core and in the app | step 4 |
| QR code or app links point to `example.com` / `localhost` | `BASE_URL` never set | set it, restart Core, pair again |
