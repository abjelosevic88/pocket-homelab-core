# Installing and removing apps

An admin can install any released Pocket app with one button (**Apps → Install app**) and take any
app off the server again (**Apps → an app → Remove app**). Core stays unprivileged: the Docker work
is done by the **installer agent**, `pocket-core installer`, which runs on the Docker host as the user
who owns the app folders and answers Core on a unix socket.

## What Install does

1. Lists the owner's GitHub repos named `INSTALLER_REPO_PREFIX*` that have a `vX.Y.Z` tag and a
   `docker-compose.yml` running the repo's own `ghcr.io/<owner>/<repo>` image. The newest plain
   semver tag wins (pre-releases are skipped). Tags come from git's ref advertisement and files from
   raw.githubusercontent.com, so only the repo list counts against GitHub's API limit (cached 15 min).
2. Writes `INSTALLER_APPS_DIR/<container_name>/` from that compose file:
   - the image pinned to the release (`:0.4.5`), `build:` dropped;
   - every published port moved to a free host port from `INSTALLER_PORTS`;
   - `BASE_URL` (from `INSTALLER_PUBLIC_URL`), `TZ`, `SECURE_COOKIES`, `ADMIN_*`, and Core's
     `CORE_URL` / `CORE_REGISTRATION_KEY` / `CORE_APP_URL`; `host.docker.internal:host-gateway`;
     `/usr/share/zoneinfo` mounted read-only;
   - `.env` (mode 600) with the admin's email, a random `ADMIN_PASSWORD`, the registration key and a
     fresh random value for every secret-looking variable the stack reads (the samples' defaults are
     public);
   - relative bind-mount folders created first, so Docker doesn't make them root-owned.
3. `docker compose pull` + `up -d` with a clean environment (the agent's own variables never reach
   compose), then waits for `/healthz`. If anything fails, it stops the stack and moves the folder to
   the archive as `…-failed`, so the server is left as it was.
4. With `INSTALLER_CADDYFILE`: appends a site for the public host (`import` of `INSTALLER_CADDY_IMPORTS`,
   `reverse_proxy` with `flush_interval -1`), writes the file **in place** (Caddy's container mounts the
   single file, so a new inode would go unseen), validates, reloads, and restores the old file if Caddy
   refuses. If the container still serves a stale copy it is restarted once. A backup
   `Caddyfile.bak-installer-<slug>-<time>` is kept.
5. Core waits for the app's first start: registering creates its service entry (paired phones get it),
   its *Sign in with Pocket Core* client and its app links (approved on first registration).

## What Remove does

1. Finds the app's container by the port Core reaches it on (or `pocket-<slug>`), and its compose
   project from Docker's labels. It refuses when the project also runs another app, when another
   project was started from the same folder, or when the folder is outside `INSTALLER_APPS_DIR` —
   those are removed by hand.
2. `docker compose down --remove-orphans`, then moves the whole folder, data included, to
   `INSTALLER_ARCHIVE_DIR/<folder>-<time>`. Nothing is deleted. Images stay.
3. Removes the public host from the Caddyfile (a site that also serves other hosts only loses that
   address), with a backup.
4. Reports files in `INSTALLER_LEFTOVER_FILES` that still mention the app (dashboards, tunnels…).
5. Core unregisters the app: service entry, sign-in client, webhooks, app links. Health checks are
   paused for it meanwhile, so no "app down" event fires.

Pocket Core itself (and anything in `INSTALLER_PROTECTED`) is never removed. An app the agent can't
find (one that runs outside Docker) can still be removed from Core only.

## Switching it off

- **Settings → Box → Install and remove apps** (admins): off hides *Install app*, and *Remove app* goes back
  to only unregistering the app from Core. Stored in Core's settings (`installs_off`).
- **`INSTALLER_ENABLED=false`** in Core's environment: off for good on that Core; the switch shows greyed out.
- No `INSTALLER_SOCKET` (or no agent running): the feature doesn't exist there at all.

## Setup

```sh
# on the Docker host, as the user owning the app folders
install -m 755 pocket-core ~/pocket-core/bin/pocket-core
openssl rand -hex 32   # → INSTALLER_TOKEN, the same value in Core's environment
```

A systemd user unit:

```ini
[Service]
ExecStart=%h/pocket-core/bin/pocket-core installer
Environment=PATH=/usr/local/bin:/usr/bin:/bin
Environment=INSTALLER_SOCKET=%h/pocket-core/installer/installer.sock
Environment=INSTALLER_PUBLIC_URL=https://{slug}.example.com
Environment=INSTALLER_CADDYFILE=%h/caddy/Caddyfile
Environment=INSTALLER_CADDY_IMPORTS=tls_cf
EnvironmentFile=%h/pocket-core/installer.env   # INSTALLER_TOKEN=…
Restart=on-failure
```

Core's compose file mounts the socket's folder and gets the same token:

```yaml
    environment:
      INSTALLER_SOCKET: /run/pocket-installer/installer.sock
      INSTALLER_TOKEN: ${INSTALLER_TOKEN}
    volumes:
      - ./installer:/run/pocket-installer
```

The socket is mode 600, so Core's container user must have the agent's uid (1000 in the image).
`pocket-core installer -h` lists every setting.

## API

| Core (admins) | Agent | |
|---|---|---|
| `GET /api/v1/installer[?refresh=1]` | `GET /v1/status`, `GET /v1/catalog` | releases + which are registered here |
| `POST /api/v1/installer/install {slug}` | `POST /v1/install` | starts a job |
| `POST /api/v1/apps/{slug}/uninstall` | `POST /v1/uninstall` | starts a job |
| `GET /api/v1/installer/jobs/{id}` | `GET /v1/jobs/{id}` | `state` running/done/failed, `log`, `result` (dir, archive, port, warnings), `open_url` |

One job runs at a time (409 otherwise). `GET /api/v1/auth/status` carries `installer: true` for
admins when an agent is configured. Jobs live in memory for a day.

Testing: `go test ./internal/installer/` covers the compose rewrite and Caddyfile edits;
`POCKET_INSTALLER_DOCKER_TEST=1 env -i PATH=$PATH HOME=$HOME go test -run TestDockerEndToEnd ./internal/installer/`
installs and removes a real release image behind a throwaway Caddy, against a fake GitHub and Core.
