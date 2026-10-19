# Pairing

Pairing is how the **Pocket Homelab** iPhone/Mac client learns about every service on the box in one go, with its own credentials, without anyone typing a URL or copying an API key.

1. An admin (or any user, for their own devices) taps **Pair a device**. Core mints an 8-character code valid for `PAIR_TTL` (10 min) and shows a QR.
2. The QR carries `pockethomelab://pair?core=<BASE_URL>&code=<CODE>`. The client claims it:

   ```
   POST /api/v1/pair/claim        (no auth — the code is the secret, one use)
   {"code": "RG95-CQF9", "name": "Aleks's iPhone", "platform": "ios"}
   ```

3. The answer is the **profile**:

   ```json
   {
     "box": {"name": "Living-room box", "core_url": "https://core.example.com", "version": "0.1.0", "issuer": "…"},
     "user": {"id": 1, "email": "…", "name": "…", "role": "admin"},
     "device": {"id": 3, "name": "Aleks's iPhone", "platform": "ios", "token_prefix": "pcd_…"},
     "token": "pcd_…",
     "services": [
       {"type": "core",     "name": "Pocket Core",   "group": "Pocket", "url": "…", "pocket": true,  "env": {"CORE_URL": "…", "CORE_API_KEY": "pcd_…"}},
       {"type": "habits",   "name": "Pocket Habits", "group": "Pocket", "url": "…", "pocket": true,  "env": {"HABITS_URL": "…", "HABITS_API_KEY": "pcd_…"}},
       {"type": "jellyfin", "name": "Jellyfin",      "group": "Media",  "url": "…", "pocket": false, "env": {"JELLYFIN_URL": "…", "JELLYFIN_API_KEY": "…"}}
     ],
     "values": {"CORE_URL": "…", "HABITS_URL": "…", "...": "every key flat"},
     "env": "# Pocket Homelab configuration\nCORE_URL=…\n…"
   }
   ```

   `env` is byte-for-byte the client's existing env-file format, so the current import path works unchanged; `services`/`values` are for a native pairing screen.

4. Later the device refreshes with its token: `GET /api/v1/pair/profile` (`?format=env` for the file). Revoking the device under **Devices** kills the token everywhere at once.

## What goes into a profile

- **Registered Pocket apps**: `<PREFIX>_URL` = the app's public URL, `<PREFIX>_API_KEY` = the device token. Apps validate it through [introspection](manifest.md#accepting-device-tokens).
- **Services added by an admin** (Jellyfin, Immich, Nextcloud… the full list is `GET /api/v1/catalog`, mirroring the client's template): the URL plus the credential fields entered, stored encrypted with `secret.key`.
- Anything switched off under Services is left out, and so is anything whose **audience** excludes the person: `all` (default), `admins`, or `users` with a list. Admin keys (TrueNAS, AdGuard, the arr apps) belong on `admins`.
- **Per-person overrides** (Users → Credentials, or Settings → My logins for a member) replace the service's URL and/or fields for that person: their own Jellyfin user id, their Navidrome login. Everything else falls through to the box-wide value.

Env prefixes and field names match the client's template exactly, e.g. `NEXTCLOUD_URL` / `NEXTCLOUD_USERNAME` / `NEXTCLOUD_APP_PASSWORD`, `SERVICES_SSH_HOST` / `SERVICES_SSH_PORT`.

## From the console

`pocket-core pair` prints a code and the QR URL without a browser: useful on a box's first boot or over SSH.
