import type { App, AppLink } from '../lib/types'

// One pair per (acting app → target); its scopes ride along.
export interface LinkGroup { key: string; app: string; appName: string; target: string; targetName: string; reason: string; links: AppLink[] }

// Group links by (acting app → target app) so each pair reads as one sentence.
export function groupLinks(links: AppLink[]) {
  const byKey = new Map<string, LinkGroup>()
  for (const l of links) {
    const key = `${l.app_slug}→${l.target_slug}`
    let g = byKey.get(key)
    if (!g) { g = { key, app: l.app_slug, appName: l.app_name, target: l.target_slug, targetName: l.target_name, reason: l.reason, links: [] }; byKey.set(key, g) }
    g.links.push(l)
  }
  return [...byKey.values()]
}

/** on = every scope issues tokens; partial = some switched off; off = none; bad = a scope can't work. */
export function pairState(g: LinkGroup): 'on' | 'partial' | 'off' | 'bad' {
  if (g.links.some(l => l.state !== 'on' && l.state !== 'off_admin' && l.state !== 'off_person')) return 'bad'
  const on = g.links.filter(l => l.enabled).length
  return on === g.links.length ? 'on' : on === 0 ? 'off' : 'partial'
}

/** How many people the acting app connects to this target directly (pasted URL and key). */
export const directCount = (g: LinkGroup) => g.links[0]?.modes?.direct || 0

export const isService = (slug: string) => slug.startsWith('service:')
export const isRead = (scope: string) => /:read$/.test(scope)
/** A pair that may change something in the target, not just look. */
export const writes = (g: LinkGroup) => g.links.some(l => !isRead(l.scope))

export function appOffers(a?: App): { scope: string; name?: string; description?: string }[] {
  const o = (a?.manifest as { offers?: unknown } | undefined)?.offers
  return Array.isArray(o) ? o as { scope: string; name?: string; description?: string }[] : []
}

// The name the target app gives a scope; without one (not offered), "expenses:write" → "Write expenses".
export const scopeLabel = (l: AppLink) => {
  if (l.scope_name) return l.scope_name
  const [what, verb] = l.scope.split(':')
  return verb ? `${verb.charAt(0).toUpperCase()}${verb.slice(1)} ${what.replace(/[_-]/g, ' ')}` : l.scope
}
