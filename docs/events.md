# Events

The bus is a persisted log with live fan-out. Apps publish, Core stores, streams to listeners and POSTs to webhooks with retries.

## Publish

```
POST /api/v1/events
{"topic": "kitchen.cooked", "subject": "recipe:12", "payload": {"recipe": "Pancakes", "servings": 4}}
```

- Topics are lowercase dotted words. An app may only publish under its own slug (`kitchen.*`); users and devices may publish anything (source = their email / device name).
- `subject` is a free identifier for the thing the event is about; `payload` is any JSON up to 256 KiB.
- Core itself publishes `core.app.registered`, `core.app.removed`, `core.app.ok`, `core.app.down`, `core.device.paired`, `core.device.revoked`, `core.connector.provisioned`, `core.service_key_replaced`, `core.service_key_replace_failed` (see [connectors](connectors.md#replacing-keys-when-a-link-is-switched-off)).
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

### Jellyfin (source `connector:jellyfin`)

Polled every check interval from Jellyfin's activity log and its newest library items. The first check only records a baseline.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `jellyfin.playback_started` | `activity:<id>` | someone started playing (activity `VideoPlayback` / `AudioPlayback`) | `user_email` (when the Jellyfin account is linked to a person), `jellyfin_user_id`, `jellyfin_user`, `media` (`video` \| `audio`), `summary` (Jellyfin's sentence), `detail` (client and device), `at`, `item_id`, `title`, `item_type`, `series`, `season`, `episode`, `year`, `artist`, `url` |
| `jellyfin.playback_stopped` | `activity:<id>` | playback stopped | same |
| `jellyfin.item_added` | `item:<id>` | a movie, episode, album, audiobook or book appeared in the library | `item_id`, `title`, `item_type`, `series`, `season`, `episode`, `year`, `artist`, `added_at`, `url` (no `user_email`: the library is shared) |
| `jellyfin.user_created` · `user_deleted` · `user_locked_out` | `jellyfin-user:<id>` | account changes | `jellyfin_user_id`, `summary`, `at`, plus `user_email` when linked |
| `jellyfin.login_failed` | `activity:<id>` | a failed sign-in | `summary` (names the address), `detail`, `at` |

Sessions starting and ending are not published.

### Audiobookshelf (source `connector:audiobookshelf`)

Polled every check interval from listening sessions, each account's progress, the newest items of every library and recent podcast episodes. The first check only records a baseline. `user_email` is the person linked to the Audiobookshelf account.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `audiobookshelf.listening_started` | `session:<id>` | someone started listening | `user_email`, `audiobookshelf_user_id`, `audiobookshelf_user`, `session_id`, `item_id`, `episode_id`, `title`, `author`, `media_type` (`book` \| `podcast`), `device`, `duration_seconds`, `started_at`, `url` |
| `audiobookshelf.listening_ended` | `session:<id>` | the session was not synced for 15 minutes | same, plus `ended_at`, `listened_seconds`, `position_seconds`, `progress` (0–1) |
| `audiobookshelf.item_finished` | `finished:<user>:<item>[:<episode>]` | a book or episode was marked finished | `user_email`, `audiobookshelf_user_id`, `item_id`, `episode_id`, `title`, `subtitle`, `media_type`, `duration_seconds`, `finished_at`, `url` |
| `audiobookshelf.item_added` | `item:<id>` | a book or podcast appeared in a library | `item_id`, `title`, `author`, `narrator`, `series`, `year`, `media_type`, `library`, `duration_seconds`, `added_at`, `url` (no `user_email`: libraries are shared) |
| `audiobookshelf.episode_added` | `episode:<id>` | a podcast episode was downloaded | `episode_id`, `item_id`, `title`, `podcast`, `published_at`, `added_at`, `url` |
| `audiobookshelf.user_created` · `user_deleted` | `audiobookshelf-user:<id>` | account changes | `audiobookshelf_user_id`, `audiobookshelf_user`, `type`, plus `user_email` when linked |

### Radarr, Sonarr, Lidarr (sources `connector:radarr` · `sonarr` · `lidarr`)

Pushed by a Webhook connection in each app, as it happens. Household events: **no `user_email`**. Topic prefix = the app (`radarr.imported`, `sonarr.grabbed`…).

| Topic | Subject | When | Payload |
|---|---|---|---|
| `<app>.grabbed` | `movie:<id>` · `series:<id>` · `artist:<id>` | a release was sent to the download client | the item (below), `release_title`, `indexer`, `quality`, `release_group`, `size`, `download_client`, `download_id` |
| `<app>.imported` · `<app>.upgraded` | same | a file was imported (upgraded = it replaced a worse one) | the item, `file`, `quality`, `release_group`, `size`, release fields |
| `<app>.added` · `<app>.removed` | same | a movie, series or artist was added or deleted | the item |
| `<app>.file_deleted` | same | a file was deleted (not for an upgrade) | the item, `file`, `reason` |
| `lidarr.album_removed` | `artist:<id>` | an album was deleted | the item |
| `lidarr.download_failed` · `lidarr.import_failed` | `artist:<id>`, else `download_failure` · `import_failure` | a download or import failed | `release_title`, `quality`, `download_client`, `message` |
| `<app>.manual_interaction_required` | the item | an import needs a person | the item, `release_title`, `quality`, `messages` |
| `<app>.health_issue` · `<app>.health_restored` | `health:<check>` | a health error appeared or cleared | `level`, `check`, `message`, `wiki_url`, `title` (= message) |
| `<app>.updated` | `update:<version>` | the app updated itself | `previous_version`, `new_version`, `message` |

The item: Radarr `movie_id`, `movie`, `year`, `tmdb_id`, `imdb_id`; Sonarr `series_id`, `series`, `year`, `tvdb_id`, `imdb_id`, `episodes` [`season`, `episode`, `title`, `air_date`] and, for one episode, `season`, `episode`, `episode_title`; Lidarr `artist_id`, `artist`, `musicbrainz_id`, `album` (`albums` when several), `release_date`, `tracks`. Always `event` (the app's eventType), `instance`, a readable `title` (`Dune (2021)`, `Severance S02E01`, `Massive Attack – Mezzanine`) and `url` into the app.

### Uptime Kuma (source `connector:kuma`)

Polled. Household events: no `user_email`.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `kuma.monitor_down` | `monitor:<name>` | a monitor went down | `monitor`, `monitor_type`, `target` (URL or host:port), `message` (last check, with a status page), `checked_at`, `title`, `url` |
| `kuma.monitor_up` | `monitor:<name>` | it is up again | same, plus `down_since`, `down_for_seconds` |
| `kuma.cert_expiring` | `monitor:<name>` | the certificate passes 14, 7, 3 or 1 days left (one event per step) | same, plus `days_remaining` |

### Dawarich (source `connector:dawarich`)

Polled for the API key's account; `user_email` = the person it is linked to. Never coordinates or addresses.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `dawarich.tracking_stopped` · `tracking_resumed` | `tracking:<user>` | no new point for 12 hours / points again | `last_point_at`, `silent_hours`, `title`, `url` |
| `dawarich.visit_detected` · `visit_confirmed` | `visit:<id>` | a new visit, or one confirmed since | `visit_id`, `name`, `status`, `started_at`, `ended_at`, `duration_minutes` |
| `dawarich.city_entered` · `country_entered` | `city:<name>` · `country:<name>` | the newest point is in another city or country (needs reverse geocoding in Dawarich) | `city`, `country`, `from_city` / `from_country` |

### Maloja (source `connector:maloja`)

Polled; `user_email` = the person linked as Maloja's listener.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `maloja.scrobbled` | `scrobble:<hash>` | a listen was scrobbled | `title`, `artist`, `artists`, `album`, `album_artists`, `listened_seconds`, `length_seconds`, `client`, `at`, `url` |
| `maloja.new_artist` | `artist:<name>` | the first listen ever of an artist | `artist`, `track`, `at`, `url` |

### Nextcloud (source `connector:nextcloud`)

Pushed by Nextcloud's webhooks (arrive with its cron, within about 5 minutes). `user_email` is the person Core linked to the account.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `nextcloud.file_created` | `file:<account>/<path>` | a file or folder appeared in someone's Files (upload, sync, copy) | `user_email`, `nextcloud_user`, `path` (inside Files), `name`, `folder`, `file_id`, `url` (`/f/<id>`), `by` (when someone else did it, e.g. in a shared folder) |
| `nextcloud.file_deleted` | `file:<account>/<path>` | moved to the trash or deleted | same, without `file_id` and `url` |
| `nextcloud.calendar_event_created` · `_updated` · `_deleted` | `calendar:<account>/<uid>` | an event in someone's calendar | `user_email`, `nextcloud_user`, `uid`, `calendar`, `title`, `location`, `start`, `end` (RFC 3339; a date when `all_day`), `all_day`, `recurring`, `url`; `private: true` instead of title and location for private or confidential events |
| `nextcloud.task_created` · `task_updated` · `task_completed` · `task_deleted` | `calendar:<account>/<uid>` | a task (Nextcloud Tasks, Apple Reminders over CalDAV) | same, with `due` and `status` instead of `end` |
| `nextcloud.user_created` · `user_deleted` | `user:<account>` | accounts appearing or disappearing (polled) | `nextcloud_user`, `name`, plus `user_email` when known |

The birthday calendar, previews, versions, hidden and partial files, and anything from before Core connected the person are not published.

### Paperless-ngx (source `connector:paperless`)

Pushed by two Paperless workflows; arrives as soon as Paperless has processed the document.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `paperless.document_added` | `document:<id>` | a document was consumed (upload, consume folder, mail) | `user_email` (the owner's person), `paperless_user`, `document_id`, `title`, `correspondent`, `document_type`, `created` (the document's date), `added`, `original_filename`, `url` |
| `paperless.document_updated` | `document:<id>` | a document's details changed | same |
| `paperless.user_created` · `user_deleted` | `user:<name>` | accounts appearing or disappearing (polled) | `paperless_user`, `name`, plus `user_email` when known |

Documents without an owner, and documents added before Core connected the person, are not published.

### Jellyseerr (source `connector:jellyseerr`)

Polled every check interval; the first check only records a baseline. `user_email` is the person Core linked to the requester's (or reporter's) Jellyseerr user.

| Topic | Subject | When | Payload |
|---|---|---|---|
| `jellyseerr.request_created` | `request:<id>` | someone asked for a movie or show | `user_email`, `request_id`, `title`, `year`, `media_type` (`movie` \| `tv`), `tmdb_id`, `is_4k`, `seasons`, `status`, `media_status`, `requested_by`, `auto_request`, `requested_at`, `at`, `url` |
| `jellyseerr.request_approved` · `request_declined` · `request_failed` | `request:<id>` | the request's status changed | same, plus `modified_by` when someone else did it |
| `jellyseerr.media_available` · `media_partially_available` | `request:<id>` | what was requested arrived (all of it, or some seasons) | same |
| `jellyseerr.issue_created` · `issue_resolved` · `issue_reopened` | `issue:<id>` | a problem report | `user_email` (the reporter), `issue_id`, `issue_type` (`video` \| `audio` \| `subtitles` \| `other`), `title`, `year`, `media_type`, `tmdb_id`, `reported_by`, `reported_at`, `url` |
| `jellyseerr.user_created` | `user:<id>` | a new Jellyseerr user | `jellyseerr_user_id`, `name`, plus `user_email` when linked |

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
