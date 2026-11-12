# Notifications to Pocket Hub

Pocket Hub (Mac, iPhone, iPad) gets real system notifications — Pocket Alerts uses this for its
"Pocket Hub" channel. Three parts:

```
app (Pocket Alerts) ──pca_──► Pocket Core ──pps_──► Pocket Push relay ──.p8──► APNs ──► Pocket Hub
                     POST /api/v1/push/notify     POST /v1/send
```

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
| `PUSH_TOPICS` | `org.bjelke.pockethub,org.bjelke.pockethub.mac` | APNs topics a device may register for |

The `relay` value a device sees (`GET /devices/me/push`) comes from the relay's `GET /v1/status`,
cached for a minute:

- `ready` — the relay answers and has Apple's key
- `no_apple_key` — the relay answers but has no Apple push key yet (messages fail with that reason)
- `no_relay` — no sender token, or the relay can't be reached or refuses the token

`POST /push/notify` answers `200` with the counts whenever it got an answer; with no relay token every
device is `failed` with reason `no push relay set up on this box`. When nothing went out because the
relay was unreachable it answers `502`, over the relay's limit `503` (same body), so a caller can retry.
Each registration records `last_sent_at` and `last_error`; Core's Devices page shows "Notifications on"
per device (amber when the last message had a problem).

Logs never carry message text, sealed blobs, tokens or more than the last 6 characters of a device token.
