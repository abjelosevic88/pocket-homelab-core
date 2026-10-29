export function ago(iso?: string): string {
  if (!iso) return 'never'
  const d = new Date(iso).getTime()
  if (isNaN(d)) return iso
  const s = Math.round((Date.now() - d) / 1000)
  if (s < 5) return 'just now'
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}
export function when(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return isNaN(d.getTime()) ? iso : d.toLocaleString()
}
export function uptime(s: number): string {
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`
}
// "0.8.3" and "v0.8.3" both render as "v0.8.3" — apps report either form.
export function formatVersion(v?: string): string {
  return v ? 'v' + v.replace(/^v/i, '') : ''
}

// The family word in app names ("Pocket" unless an admin renamed it under Settings → Box).
let family = 'Pocket'
export function setFamily(f?: string) { family = f || 'Pocket' }
export function familyName(): string { return family }
// "Pocket Notes" → "Notes" (also "Pocket Notes" when the family is renamed and the app kept its own name).
export function shortName(name: string): string {
  for (const f of [family, 'Pocket']) if (name.startsWith(f + ' ')) return name.slice(f.length + 1)
  return name
}
