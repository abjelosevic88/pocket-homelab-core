# Notifications to Pocket Hub and the Pocket Core app

Pocket Hub (Mac, iPhone, iPad, Android) gets real system notifications — Pocket Alerts uses this for its
"Pocket Hub" channel. The Pocket Core app (the admins' iPhone app) gets Core's own admin alerts the same
way: see [The Pocket Core app](#the-pocket-core-app). Three parts:

```
app (Pocket Alerts) ──pca_──► Pocket Core ──pps_──► Pocket Push relay ──.p8──► APNs ──► Pocket Hub
                     POST /api/v1/push/notify     POST /v1/send      └─Firebase─► FCM ──► Pocket Hub for Android
```

- **Android** registers the same way with `environment: "fcm"`, its Firebase registration token
  (case-sensitive, kept as sent) and topic `org.bjelke.pockethub`. The relay delivers a data-only
  Firebase message with the sealed blob; the phone opens it with its own key.

- **Pocket Hub** makes an X25519 key pair per install (the private key stays in the Keychain),
  asks Apple for a device token and registers both with Core: `PUT /api/v1/devices/me/push`
  with its paired device token (`pcd_`). Only device tokens may use the `/devices/me/push`
  endpoints.
- **Core** keeps one registration per device (`push_registrations`; revoking the device removes it).
  `POST /api/v1/push/notify` finds the person (`email` or `user_id`), checks the calling app's
  audience (who may use it — the same rule as `/auth/introspect`'s `not_in_audience`), seals the
  message to each enabled device and hands the sealed blobs to the relay in batches of 100.
- **Pocket Push** is the only place with Apple's push key. It never sees the text and keeps no
  device list; it answers per message `sent`, `gone` (Apple says the token is dead — Core deletes
  that registration), `failed` or `limited`.

Endpoints: see [api.md](api.md#notifications-pocket-hub).

## Sealing

Per message and device:

1. a fresh ephemeral X25519 key `e`; `shared = X25519(e, device_pub)`
2. `key = HKDF-SHA256(ikm = shared, salt = e_pub ‖ device_pub, info = "pocket-push v1", 32 bytes)`
3. ChaCha20-Poly1305 with a random 12-byte nonce, no associated data
4. wire = standard base64 of `0x01 ‖ e_pub(32) ‖ nonce(12) ‖ ciphertext ‖ tag(16)`

The plaintext is JSON: `{"v":1,"title","body","url","source","id","at","sound","thread"}` (all but `v`
optional). Core clips the title to 120 and the body to 900 characters. The relay's APNs payload is a
placeholder ("Pocket Hub — New notification", `mutable-content: 1`) plus the sealed blob in `p`; Pocket
Hub's Notification Service Extension opens it and shows the real text. `internal/push` holds the Go
side and a test vector that the Swift side decrypts too.

Sound: a message plays a sound only if the caller asked for it (default yes) and the device keeps
sound on. APNs keeps a notification for an offline device for 24 hours.

## Relay

| Env | Default | |
|---|---|---|
| `PUSH_RELAY_URL` | `https://push.pockethomelab.com` | |
| `PUSH_RELAY_TOKEN` | — | the relay's `pps_` sender token for this box |
| `PUSH_NAMES_ACCOUNT` | — | a Pocket Box's Pocket Names account file (`{"name","token"}`); with no token set Core enrolls at `/v1/enroll` and keeps the sender token in `DATA_DIR/push-relay.token` (mode 0600) |
| `PUSH_TOPICS` | `org.bjelke.pockethub,org.bjelke.pockethub.mac,org.bjelke.pocketcore` | APNs topics a device may register for |
| `PUSH_OFFLINE_AFTER` | `10m` | how long the box may be silent before the relay tells admins it is offline (at least `4m`; `off` = don't ask the relay to watch) |

The `relay` value a device sees (`GET /devices/me/push`) comes from the relay's `GET /v1/status`,
cached for a minute:

- `ready` — the relay answers and has Apple's key (for an Android registration: Google's)
- `no_apple_key` — the relay answers but has no Apple push key yet (messages fail with that reason)
- `no_google_key` — Android only: the relay has no Firebase key yet, or is older than Pocket Push 0.2.0
- `no_relay` — no sender token, or the relay can't be reached or refuses the token

`POST /push/notify` answers `200` with the counts whenever it got an answer; with no relay token every
device is `failed` with reason `no push relay set up on this box`. When nothing went out because the
relay was unreachable it answers `502`, over the relay's limit `503` (same body), so a caller can retry.
Each registration records `last_sent_at` and `last_error`; Core's Devices page shows "Notifications on"
per device (amber when the last message had a problem).

Logs never carry message text, sealed blobs, tokens or more than the last 6 characters of a device token.

## The Pocket Core app

The Pocket Core app registers like Pocket Hub (same `PUT /devices/me/push`, same sealing) with the topic
`org.bjelke.pocketcore`. Those registrations:

- **never get the apps' messages** — `/push/notify` skips them (Pocket Hub shows those), so an admin with
  both apps doesn't get everything twice;
- get **Core's admin alerts** when the device belongs to an admin (not disabled). Each registration
  chooses its kinds (`kinds` in the `PUT`; left out = keep what was chosen; `[]` = none):

| Kind | Default | When | Example |
|---|---|---|---|
| `app_down` | on | an app failed two scheduled health checks in a row (`HEALTH_INTERVAL`, 60 s). A restart in between doesn't alert, apps Core is updating or removing aren't checked, "Check now" doesn't count, and nothing in the first 3 minutes after Core starts | "Money is down on Home" — "dial tcp 172.18.0.4:8080: connect: connection refused" |
| `app_up` | on | the first good check of an app reported down (also after Core restarted in between) | "Money is back up on Home" — "It was down for 12 min." |
| `job_failed` | on | an install, update (also the nightly automatic one) or removal failed | "Money didn't update on Home" — the installer's error |
| `job_done` | off | one finished | "Sky is installed on Home" — "Version 0.6.2 is ready." |
| `updates` | on | the nightly pass found newer versions it didn't apply: a digest once something new is waiting, at most once a day, between 9:00 and 21:00 box time | "3 updates waiting on Home" — "Notes 0.14.1 → 0.14.2, …" |
| `box_offline` | on | the box stopped checking in with the relay (below), and back | "Home is offline" / "Home is back online" — "It was offline for about 25 min, from 14:02." |

Every alert carries `url` = `pocketcore://box?core=<Core's base URL>&device=<device id>[&app=<slug>]` (the
app opens that box, and the app's page), `thread` = `box-<16 hex>` (one per box, so the phone groups them)
and `source` "Pocket Core". The test notification (`POST /devices/me/push/test`) of such a registration
reads "Notifications from Home work." and opens the box. `GET /devices/me/push` also answers `kinds`,
`admin` (only admins get alerts) and `offline_watch`.

### Box offline

A box that is down can't say so. While at least one admin device wants `box_offline`, Core checks in with
the relay every 2 minutes (`PUSH_OFFLINE_AFTER` / 5, between 30 s and 2 min):
`PUT /v1/heartbeat {"deadline": 600, "messages": […]}` — one "Home is offline" notice per such device,
sealed like any message (the relay still never sees text), with the APNs collapse id `status-<16 hex>`.
If the relay doesn't hear again by the deadline it sends those notices once. Core's next heartbeat is
answered `"fired": true`; Core then sends "Home is back online" itself (same collapse id, so it replaces
the offline notice on the phone). The time of the last heartbeat the relay took is kept in the database
(`push_heartbeat_ok_at`), so "offline for about 25 min" survives Core's own restart.

When no device wants it any more Core sends `DELETE /v1/heartbeat` (also once after each start, in case
the relay still watches from before). A Core update or restart shorter than the deadline never alerts.
The relay keeps heartbeats in SQLite and gives every box a full deadline again after its own restart, so
neither side's restart false-fires (see Pocket Push's README).

`offline_watch` in `GET /devices/me/push`: `on` (the relay watches), `starting`, `off` (nobody wants it,
or `PUSH_OFFLINE_AFTER=off`), `no_relay` (no sender token, or the relay can't be reached; tried again every
beat) or `relay_too_old` (no `/v1/heartbeat`: Pocket Push before 0.3.0; asked again hourly).

State kept for the alerts (settings table): `push_down_alerted` (apps whose "down" went out),
`push_updates_pending`, `push_updates_seen`, `push_updates_sent_at`, `push_heartbeat_ok_at`.
