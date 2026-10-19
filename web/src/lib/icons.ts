// Service icons bundled with Core (web/public/icons), so nothing is fetched
// from outside the box. Artwork: homarr-labs/dashboard-icons (Apache-2.0),
// keyed by catalog type.
const FILES: Record<string, string> = {
  "adguard": "adguard.svg",
  "audiobookshelf": "audiobookshelf.svg",
  "backrest": "backrest.png",
  "bazarr": "bazarr.svg",
  "beszel": "beszel.svg",
  "calibre": "calibre.svg",
  "chaptarr": "chaptarr.svg",
  "dawarich": "dawarich.png",
  "firefly": "firefly.svg",
  "forgejo": "forgejo.svg",
  "glances": "glances.svg",
  "homepage": "homepage.png",
  "immich": "immich.svg",
  "jellyfin": "jellyfin.svg",
  "jellyseerr": "jellyseerr.svg",
  "jellystat": "jellystat.svg",
  "komodo": "komodo.png",
  "kuma": "kuma.svg",
  "maloja": "maloja.png",
  "lidarr": "lidarr.png",
  "lubelogger": "lubelogger.png",
  "metube": "metube.svg",
  "navidrome": "navidrome.svg",
  "nextcloud": "nextcloud.svg",
  "ntfy": "ntfy.svg",
  "nzbget": "nzbget.svg",
  "paperless": "paperless.svg",
  "pico": "pico.svg",
  "pinepods": "pinepods.png",
  "prowlarr": "prowlarr.svg",
  "qbittorrent": "qbittorrent.svg",
  "radarr": "radarr.svg",
  "sabnzbd": "sabnzbd.svg",
  "scrutiny": "scrutiny.svg",
  "searxng": "searxng.svg",
  "slskd": "slskd.svg",
  "sonarr": "sonarr.svg",
  "speedtest": "speedtest.png",
  "ssh": "ssh.svg",
  "truenas": "truenas.svg",
  // Pocket apps: their own favicons, for when the app isn't running yet
  "bookshelf": "pocket-bookshelf.svg",
  "clips": "pocket-clips.svg",
  "collaborator": "pocket-collaborator.svg",
  "day": "pocket-day.svg",
  "garage": "pocket-garage.svg",
  "habits": "pocket-habits.svg",
  "inventory": "pocket-inventory.svg",
  "invoicing": "pocket-invoicing.svg",
  "kitchen": "pocket-kitchen.svg",
  "money": "pocket-money.svg",
  "split": "pocket-split.svg",
  "subtitles": "pocket-subtitles.svg",
  "travel": "pocket-travel.svg",
}

/** The bundled icon for a catalog type, or "". */
export function bundledIcon(type: string): string {
  return FILES[type] ? `/icons/${FILES[type]}` : ''
}

const trim = (u?: string) => (u || '').replace(/\/$/, '')
function host(u: string) {
  try { return new URL(u).host } catch { return '' }
}

export interface IconSource { type: string; url?: string; public_url?: string; pocket?: boolean }

/**
 * Where to look for a service's icon, best first: a Pocket app's own
 * favicon; the Pocket app living on the same host (an alias like "Pocket
 * Money (Firefly API)"); the bundled icon; the service's own favicon.
 */
export function iconCandidates(s: IconSource, pocketBases: string[] = []): string[] {
  const base = trim(s.public_url || s.url)
  const out: string[] = []
  if (s.pocket && base) out.push(`${base}/favicon.svg`, `${base}/favicon.ico`)
  const h = base ? host(base) : ''
  const sibling = h ? pocketBases.map(trim).find(b => host(b) === h && b !== base) : ''
  if (sibling) out.push(`${sibling}/favicon.svg`)
  const b = bundledIcon(s.type)
  if (b) out.push(b)
  if (base && !s.pocket) out.push(`${base}/favicon.ico`)
  return out
}
