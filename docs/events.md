# Events

The bus is a persisted log with live fan-out. Apps publish, Core stores, streams to listeners and POSTs to webhooks with retries.

## Publish

```
POST /api/v1/events
{"topic": "kitchen.cooked", "subject": "recipe:12", "payload": {"recipe": "Pancakes", "servings": 4}}
```

- Topics are lowercase dotted words. An app may only publish under its own slug (`kitchen.*`); users and devices may publish anything (source = their email / device name).
- `subject` is a free identifier for the thing the event is about; `payload` is any JSON up to 256 KiB.
- Core itself publishes `core.app.registered`, `core.app.removed`, `core.app.ok`, `core.app.down`, `core.device.paired`, `core.device.revoked`, `core.connector.provisioned`.
- [Connectors](connectors.md) publish what they see in third-party apps as `<type>.<event>` with source `connector:<type>` and `user_email` (the **Core** person's email) in the payload when the event belongs to a person. See below.

## Connector events

### Immich (source `connector:immich`)

Uploads arrive **by push**: Immich workflows call Core for every asset (see [connectors.md](connectors.md#events-by-push)), so there is one event per photo or video, seconds after the upload.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `immich.asset_created` | `asset:<immich asset id>` | Immich's `AssetCreate` trigger: the file was uploaded | `user_email`, `immich_user_id`, `asset_id`, `type` (`image` \| `video` \| `audio` \| `other`), `original_file_name`, `file_created_at`, `local_date_time`, `uploaded_at`, `is_favorite`, `live_photo`, `duration` (videos), `url` (the asset in Immich's web UI) |
| `immich.asset_processed` | `asset:<immich asset id>` | Immich's `AssetMetadataExtraction` trigger: EXIF read, place resolved | everything above plus, when Immich knows them, `taken_at`, `time_zone`, `city`, `state`, `country`, `camera_make`, `camera_model`, `lens_model`, `width`, `height` |
| `immich.user_created` | `user:<email>` | a new Immich account appeared (found by the connector's periodic check) | `user_email` (the Immich account's email), `immich_user_id`, `name` |

```json
{"topic": "immich.asset_processed", "source": "connector:immich", "subject": "asset:7c1e…",
 "payload": {"user_email": "sani@example.com", "immich_user_id": "0b2f…", "asset_id": "7c1e…", "type": "image",
   "original_file_name": "IMG_4512.HEIC", "file_created_at": "2026-10-19T08:00:01.000Z", "local_date_time": "2026-10-19T10:00:01.000Z",
   "uploaded_at": "2026-10-19T08:00:09.512Z", "is_favorite": false, "live_photo": true, "url": "https://photos.example/photos/7c1e…",
   "taken_at": "2026-10-19T08:00:01+00:00", "time_zone": "Europe/Sarajevo", "city": "Sarajevo", "state": "Federation of Bosnia and Herzegovina",
   "country": "Bosnia and Herzegovina", "camera_make": "Apple", "camera_model": "iPhone 16 Pro", "width": 4032, "height": 3024}}
```

What is never on the bus: exact GPS coordinates (only city, state, country), file paths on the Immich server, checksums and thumbnails. Not published at all (counted as *dropped* in the connector's status): assets whose owner is not a Core person in the Immich service's audience, the video half of a live photo (`visibility: hidden`), assets in the locked folder or the trash, a second delivery of the same asset and trigger within 24 hours, and `asset_processed` for an asset uploaded more than 7 days earlier (a library re-scan is not news).

`immich.assets_added` / `immich.assets_removed` (per-poll count differences) were removed in 0.4.0.

## Read

| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/events?topic=&source=&subject=&after=&since=&limit=` | newest first; `topic` may end in `*`; `after=<id>` returns oldest first for replay |
| GET | `/api/v1/events/{id}` | one event |
| GET | `/api/v1/events/topics` | distinct topics with counts |
| GET | `/api/v1/events/stream?topics=kitchen.*,habits.marked&after=&typed=` | Server-Sent Events |

The stream sends `id: <event id>` with every message; reconnect with `Last-Event-ID` (or `?after=`) and Core replays what was missed. With `typed=1` the SSE `event:` field is the topic (for `addEventListener('kitchen.cooked', …)`); without it every message is a plain `message`. A `: ping` comment arrives every 25 s.

## Webhooks

```
POST /api/v1/webhooks   {"name": "n8n", "url": "https://n8n/webhook/pocket", "topics": "kitchen.*,core.*", "secret": "…"}
```

Apps create theirs through the manifest; admins create any. Each matching event is POSTed as JSON (the same shape as the event objects above) with headers `X-Pocket-Event` (topic), `X-Pocket-Event-ID` and, when a secret is set, `X-Pocket-Signature: sha256=<hex HMAC-SHA256 of the body>`. Non-2xx or a network error schedules a retry: 10 s, 20 s, 40 s… up to `WEBHOOK_ATTEMPTS` (8) and capped at one hour. `POST /webhooks/{id}/test` sends a `core.test` event immediately; `GET /webhooks/{id}/deliveries` lists attempts.

Topic patterns: `*` everything, `kitchen.*` prefix, `kitchen.cooked` exact, comma-separated.
