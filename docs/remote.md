# Remote access (encrypted relay)

Remote access lets a paired phone or laptop use Pocket apps from anywhere
without Tailscale. Traffic goes through a small relay you control (your own
Cloudflare account, or a relay you host), and it is encrypted end to end
between the browser and Core: the relay only ever forwards ciphertext.

```
Browser (shell + service worker) ──wss──▶ Relay ◀──wss── Core ──http──▶ App
          Noise XK session, end to end ─────────────────▶
```

Remote access is **off** until an admin turns it on (Core → Remote access).
Tailscale and the normal `*.bjelke.org` addresses are not affected by any of it.

## Addresses

Every app gets its own address, so one app's page can never read another's
storage or keys. The pattern is configurable, default `{app}.remote.<zone>`:

| Address | What |
|---|---|
| `pocket.remote.example.org` | The launcher (portal): pairing and the list of your apps |
| `money.remote.example.org` | Pocket Money |
| `ai.remote.example.org` | Pocket AI |

With the Cloudflare relay, Core creates a Worker custom domain only for the
apps that are switched on, so an app that is off has no address at all.

## Switches

- **Remote access** (box-wide, admin): off disconnects Core from the relay.
- **Per app** (admin): *Reachable remotely*, *New devices get it* and *Ask for
  Face ID / passkey*. Pocket AI and Core start with "new devices get it" off
  and the passkey check on.
- **Per device** (owner or admin): one switch per app; *Remove* revokes every
  key of the device at once.

Switching anything off closes the affected sessions immediately and the
app-side token stops introspecting as active.

## Pairing

1. At home, Core → Remote access → **Pair a device** shows a QR code for
   `https://pocket.remote…/#pair=<code>&box=<box public key>`. The code is
   one-time and expires after 10 minutes; the fragment never reaches the relay.
2. The launcher generates a non-extractable X25519 key (WebCrypto, kept in
   IndexedDB), pins the box key from the QR code and pairs.
3. Opening an app from the launcher hands off with a fresh one-time code to
   that app's own address, which pairs its own key (scoped to that app only).
   Opening an unpaired app address directly bounces through the launcher.

A key paired for one app can never open another app.

## Protocol

### Relay

The relay is deliberately dumb. Paths on every host:

| Path | |
|---|---|
| `GET /__pocket/box` (WebSocket) | Core connects here with `Authorization: Bearer <relay secret>`. A newer box connection replaces the older one. |
| `GET /__pocket/tunnel` (WebSocket) | A browser connects here; the relay assigns a channel. |
| `GET /__pocket/config.json` | `{"portal": "https://pocket.remote…"}` (not secret) |
| `GET /__pocket/*.js` | The shell's static files |
| anything else | `shell.html` |

Frames on the box socket are binary: `type u8 | channel u32 BE | payload`.

| Type | Direction | Payload |
|---|---|---|
| `0x01` OPEN | relay → box | JSON `{"host": "...", "ip": "..."}` |
| `0x02` DATA | both | one browser WebSocket message, opaque |
| `0x03` CLOSE | both | empty |

### Session

Each channel runs `Noise_XK_25519_AESGCM_SHA256` with prologue
`pocket-remote/1 <host>`; Core is the responder with the box static key.

1. `→ e, es` (empty payload)
2. `← e, ee` payload `{"v":1}`
3. `→ s, se` payload `{"t":"open"}` or `{"t":"pair","code":"…","name":"…"}`

Core then sends one JSON control message: `{"t":"ready", …}`,
`{"t":"passkey", "challenge":…}` / `{"t":"passkey-setup", …}` (answered by the
browser with the WebAuthn result) or `{"t":"error","code":…}`.

After `ready`, every transport message is a frame `type u8 | stream u32 BE |
payload`, at most 64 KiB:

| Type | Direction | Payload |
|---|---|---|
| `0x10` REQ | → | JSON `{"m","u","h":[[k,v]…]}` |
| `0x11` REQ_BODY / `0x12` REQ_END | → | bytes / empty |
| `0x20` RES | ← | JSON `{"s","h"}` |
| `0x21` RES_BODY / `0x22` RES_END | ← | bytes / empty |
| `0x23` RES_ERR | ← | text |
| `0x30` CANCEL | → | empty |
| `0x40` WS_OPEN | → | JSON `{"u","p":[protocols]}` |
| `0x41` WS_OPENED | ← | JSON `{"p": protocol}` |
| `0x42` WS_TEXT / `0x43` WS_BINARY | both | message |
| `0x44` WS_CLOSE | both | JSON `{"c": code, "r": reason}` |
| `0x01` CTL | both | JSON control message |

### How a request reaches the app

Core forwards each request to the app's registered internal URL with a
short-lived `pcu_rly_` token that only that app can introspect, bound to the
device key. Cookies, `Authorization` and every identity-like header from the
browser are dropped; `Set-Cookie` never goes back. Apps need no changes: they
already accept Core tokens.

### The browser side

The relay serves a small shell page. It registers a service worker, keeps the
encrypted session open and shows the app in a full-screen same-origin iframe.
The service worker hands every request of the iframe to the shell, which sends
it through the session. A tiny script injected into app pages routes
same-host WebSockets through the session and stops apps from registering their
own service worker.

## Running a relay

**Cloudflare** (Workers Free plan is enough for one household): Core uploads
the Worker and creates the custom domains with an API token that has
*Account · Workers Scripts · Edit*, *Zone · Workers Routes · Edit*,
*Zone · DNS · Edit* and *Zone · Zone · Read*.

**Self-hosted**: run `pocket-core relay` (same image) behind a TLS proxy that
answers for the whole pattern (a wildcard certificate), for example:

```
docker run -d --name pocket-relay -p 8080:8080 \
  -e RELAY_SECRET=<secret from Core> -e RELAY_PORTAL=https://pocket.remote.example.org \
  ghcr.io/abjelosevic88/pocket-homelab-core relay
```

```
*.remote.example.org {
  tls { dns cloudflare {env.CF_API_TOKEN} }
  reverse_proxy 127.0.0.1:8080
}
```

## Security notes

- The relay sees when and how much, never what. It cannot pair: pairing needs
  the one-time code, and sessions need a key Core knows.
- The relay does serve the shell code. Whoever controls the relay (your
  Cloudflare account, your server) is trusted for that code: protect it with
  2FA. The shell's files are served with a strict CSP and no third-party code.
- Keys are non-extractable WebCrypto keys: a script can use a key while the
  page is open but cannot copy it off the device.
