# Installing, updating and removing apps

An admin can install any released Pocket app with one button (**Apps → Install app**), move an
installed one to its newest release (**Update** in the same list) and take any app off the server
again (**Apps → an app → Remove app**).

The quickest way to get all of this on a fresh Linux machine is the one-command install, which sets
up Core, the installer agent and (optionally) Caddy in containers:

```sh
curl -fsSL https://pockethomelab.com/install.sh | sh                 # apps on their own ports
curl -fsSL https://pockethomelab.com/install.sh | sh -s -- --domain example.com --email you@example.com
```

The script lives in [`deploy/install.sh`](../deploy/install.sh) (`--help` lists its options); the
rest of this page explains the parts. Core stays unprivileged: the Docker work
is done by the **installer agent**, `pocket-core installer`, which runs on the Docker host as the user
who owns the app folders and answers Core on a unix socket.

## The app catalog

The list of installable apps is **one signed file**, not a crawl of GitHub:
`https://github.com/abjelosevic88/pocket-homelab-core/releases/download/catalog/apps.json`
(also at `https://pockethomelab.com/apps.json`). Per app it carries the newest version, the release
image **pinned by digest**, the platforms the image is built for (`linux/amd64`, `linux/arm64`, …),
what the stack asks of the host (Docker socket, host network, devices, host folders, extra
containers — shown as chips before Install), the name, category, icon and web page, and the
`docker-compose.yml` + `.env.example` of that release.

- **Signed.** The file is an envelope `{"format":"pocket-apps/1","key_id","signature","catalog"}`;
  the ed25519 signature covers the exact bytes of `catalog`. The agent trusts the official key built
  into Core (`internal/appstore`) plus any in `INSTALLER_CATALOG_KEYS`; anything else is refused,
  and a catalog that fails the check is never used. If the download fails, the last good copy holds.
- **Built by CI.** `.github/workflows/catalog.yml` runs every 30 minutes: `pocket-core catalog build`
  reads each `pocket-homelab-*` repo's newest `vX.Y.Z` tag and compose file, asks ghcr.io for the
  tag's digest and platforms, merges the website's names/categories/icons, signs with the
  `CATALOG_SIGNING_KEY` secret and uploads `apps.json` to the `catalog` release (only when a release
  changed). An app whose newest tag has no image yet keeps its previous entry until CI has built it.
- **By hand:** `pocket-core catalog verify [url|file]` checks a catalog and lists it;
  `pocket-core catalog keygen <id>` makes a key pair for your own catalog (serve it anywhere, point
  `INSTALLER_CATALOG_URL` at it, add the public half to `INSTALLER_CATALOG_KEYS`).
- `INSTALLER_CATALOG_URL=github` switches back to reading the repos straight from GitHub (the
  pre-0.31 behaviour: newest tag via git's ref advertisement, files from raw.githubusercontent.com,
  repo list cached 15 min; no digests, platforms or requirements).

## What Install does

1. Finds the app in the catalog and checks its image exists for this machine's platform.
2. Writes `INSTALLER_APPS_DIR/<container_name>/` from that compose file:
   - the image pinned to the release and its digest (`:0.4.5@sha256:…`), `build:` dropped;
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
4. With `INSTALLER_CADDYFILE` (and a `{slug}` address): appends a site for the public host (`import` of `INSTALLER_CADDY_IMPORTS`,
   `reverse_proxy` with `flush_interval -1`), writes the file **in place** (Caddy's container mounts the
   single file, so a new inode would go unseen), validates, reloads, and restores the old file if Caddy
   refuses. If the container still serves a stale copy it is restarted once. A backup
   `Caddyfile.bak-installer-<slug>-<time>` is kept.
5. Core waits for the app's first start: registering creates its service entry (paired phones get it),
   its *Sign in with Pocket Core* client and its app links (approved on first registration).

## Bringing back a removed app's data

Remove never deletes data: the app's folder goes to the archive whole. When you install that app
again, the list offers **Bring back your data from <date>** (on by default) for the newest archived
folder (`<folder>-<YYYYMMDD-HHMMSS>`, failed installs excluded). With it, Install:

- copies the archived data folders the new release uses (its relative bind mounts, e.g. `./data`)
  into the new folder; **the archive stays as it is**;
- keeps the archived secrets (values from the old `.env` and literal values in the old compose
  file) for every variable the new `.env` sets, because apps encrypt stored passwords and tokens
  with them; Core's registration key is always the current one;
- leaves out the app's old Core token (`core.token`, `core.token.oidc.json`): Core deleted it on
  removal and apps never re-enroll on their own when it is refused, so the new install registers
  afresh (new sign-in client, app links approved again).

Untick it to start empty. Agent API: `POST /v1/install` with `"restore_from": "<archived folder name>"`;
the catalog lists the newest archive per app as `archive: {name, saved_at}`.

## Addresses: a domain, or just ports

`INSTALLER_PUBLIC_URL` is a template:

- `https://{slug}.example.com` — each app gets its own name; with `INSTALLER_CADDYFILE` the agent
  adds the HTTPS site for it.
- `http://192.168.1.20:{port}` — **no domain needed**: each app is opened on its own host port on
  this machine (`{port}` is the free port it got). Nothing is added to a proxy, and cookies are not
  marked Secure (plain http). Use the machine's Tailscale name instead of the LAN address to reach
  the apps from anywhere on your tailnet.

## What Update does

The list marks installed apps whose registered version is older than the catalog's. **Update**:

1. finds the app's container and compose project like Remove does, and its `image:` line for the
   release image;
2. refuses apps that follow a moving tag (`latest`, `edge` — something else keeps those current),
   are built from source on this server (`build:`), or run the image more than once;
3. downloads the new image while the old version still runs;
4. **backs up the data**: stops the app and copies its data folders (the stack's relative bind
   mounts, e.g. `./data`) to `INSTALLER_ARCHIVE_DIR/updates/<container>/<time>-<old version>/`,
   with the compose file. Host folders mounted by absolute path (media, documents) are not copied;
   named volumes are reported, not copied. It refuses to start without room for the copy. The
   newest 3 backups per app are kept;
5. rewrites **only the image line** to `:<new version>@<digest>` (comments and everything else
   stay) and starts the app; the new version migrates its database on start;
6. waits for `/healthz`. If the new version does not come up, it stops it, moves the data it left
   to `…-<new version>-failed` (kept for reading), copies the backup back, restores the old line
   and starts the old version, so the old version never meets a database it doesn't know;
7. Core waits for the app to register with its new version (health checks are paused meanwhile).

## Automatic updates and notifications

Settings → Box → **Install and remove apps** (admins):

- **Update apps automatically** (off by default): once a night at the **Update time** (this server's
  clock, 04:00 by default) Core reads the catalog and runs Update for every app the installer can update,
  one after the other: same backup, rollback and checks as the button. Apps that follow a moving tag or
  are built from source are never touched. It runs inside Core in the background; no page needs to be open.
- **Notify me about app updates** (on by default): one notification for each update, each failed
  update (the app is back on its old version with its data) and each new version that was not applied
  (automatic updates off, or an app that has to be updated by hand), once per version, collected into
  one message per night. The same notices go on the event bus as `core.app.updated`,
  `core.app.update_failed` and `core.apps.updates_available` (source `core`, admins and Pocket Flows).
- **Send notifications to**: an ntfy topic URL (`https://ntfy.sh/my-topic`; Title/Priority/Tags
  headers) or an Apprise API URL (`…/notify/<key>`); `user:password@` becomes Basic auth. Empty = the
  server's `NOTIFY_URL`. On a box with Pocket Alerts: `http://host.docker.internal:8096/alerts`.
  **Send test** sends one now.
- **Check now** runs the nightly pass immediately (`POST /api/v1/installer/check-updates`).

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

### In containers (what install.sh writes)

The agent ships as its own image, `ghcr.io/abjelosevic88/pocket-homelab-core-installer:<version>`
(Core's binary plus the docker CLI and compose plugin). It runs next to Core with the Docker socket,
host networking (to see free ports and check new apps on 127.0.0.1) and the apps folder mounted at
the **same path** inside and out. It runs as root (the socket is root-equivalent anyway) so it can
hand each new app's data folders to the uid the Pocket images run as (`INSTALLER_APP_OWNER`,
1000:1000) and the folder, compose file, `.env` and its socket to you (`INSTALLER_FILES_OWNER`,
the uid Core's container runs as). Run as a normal user, it leaves everything owned by that user,
which only works when that user is uid 1000.

```yaml
  installer:
    image: ghcr.io/abjelosevic88/pocket-homelab-core-installer:${POCKET_VERSION}
    network_mode: host
    environment:
      INSTALLER_SOCKET: /run/pocket-installer/installer.sock
      INSTALLER_TOKEN: ${INSTALLER_TOKEN}
      INSTALLER_PUBLIC_URL: http://192.168.1.20:{port}
      INSTALLER_APPS_DIR: /home/you/pocket/apps
      INSTALLER_FILES_OWNER: ${PUID}:${PGID}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /home/you/pocket:/home/you/pocket
      - ./installer:/run/pocket-installer
```

### On the host (systemd)

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
| `POST /api/v1/apps/{slug}/upgrade` | `POST /v1/update` | starts a job: move to the catalog's newest release |
| `POST /api/v1/apps/{slug}/uninstall` | `POST /v1/uninstall` | starts a job |
| `GET /api/v1/installer/jobs/{id}` | `GET /v1/jobs/{id}` | `state` running/done/failed, `log`, `result` (dir, archive, port, warnings), `open_url` |

One job runs at a time (409 otherwise). `GET /api/v1/auth/status` carries `installer: true` for
admins when an agent is configured. Jobs live in memory for a day.

Testing: `go test ./internal/installer/ ./internal/appstore/` covers the compose rewrite, Caddyfile
edits, catalog building/signing/verification and the update rewrite;
`POCKET_INSTALLER_DOCKER_TEST=1 env -i PATH=$PATH HOME=$HOME go test -run TestDockerEndToEnd ./internal/installer/`
installs and removes a real release image behind a throwaway Caddy, against a fake GitHub and Core.
