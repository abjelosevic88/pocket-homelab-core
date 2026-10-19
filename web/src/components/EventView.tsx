import { useState } from 'react'
import { api, V1 } from '../lib/api'
import type { App, Event, Service, User } from '../lib/types'
import { useAsync } from './ui'
import { AppIcon, humanTopic } from './AppIcon'

/** What the event views need to name sources and people. Admin-only lists fail soft. */
export interface EventCtx { apps: App[]; services: Service[]; users: User[] }

export function useEventCtx(): EventCtx {
  const { data: apps } = useAsync(() => api.get<App[]>(`${V1}/apps`).catch(() => [] as App[]), [])
  const { data: services } = useAsync(() => api.get<Service[]>(`${V1}/services`).catch(() => [] as Service[]), [])
  const { data: users } = useAsync(() => api.get<User[]>(`${V1}/users`).catch(() => [] as User[]), [])
  return { apps: apps || [], services: services || [], users: users || [] }
}

export type SourceKind = 'app' | 'connector' | 'core' | 'other'
export interface SourceInfo { kind: SourceKind; label: string; app?: App; service?: Service }

export function sourceInfo(src: string, ctx: EventCtx): SourceInfo {
  if (src === 'core') return { kind: 'core', label: 'Core' }
  if (src.startsWith('connector:')) {
    const type = src.slice('connector:'.length)
    const service = ctx.services.find(s => s.type === type)
    return { kind: 'connector', label: service?.name || type.charAt(0).toUpperCase() + type.slice(1), service }
  }
  const app = ctx.apps.find(a => a.slug === src)
  return app ? { kind: 'app', label: app.name.replace(/^Pocket /, ''), app } : { kind: 'other', label: src }
}

export function SourceIcon({ info, small = true }: { info: SourceInfo; small?: boolean }) {
  const [i, setI] = useState(0)
  if (info.app) return <AppIcon a={info.app} small={small} />
  const cls = small ? 'feed-ico' : 'mini-ico'
  if (info.kind === 'core') return <span className={cls}><img src="/favicon.svg" alt="" /></span>
  const base = (info.service?.public_url || info.service?.url || '').replace(/\/$/, '')
  const candidates = base ? [`${base}/favicon.ico`, `${base}/favicon.svg`, `${base}/favicon.png`] : []
  if (i < candidates.length) return <span className={`${cls} plain`}><img src={candidates[i]} alt="" onError={() => setI(n => n + 1)} /></span>
  return <span className={cls}>{info.label[0]}</span>
}

type P = Record<string, unknown>
const str = (v: unknown) => (typeof v === 'string' ? v : v == null ? '' : String(v))

function person(email: string, ctx: EventCtx) {
  const u = ctx.users.find(x => x.email.toLowerCase() === email.toLowerCase())
  return u?.name || (email ? email.split('@')[0] : 'Someone')
}

function clock(iso: string) {
  const d = new Date(iso)
  return isNaN(d.getTime()) ? '' : d.toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}

export interface EventDescription { title: string; sub: string; link?: { href: string; label: string }; facts?: [string, string][]; thumb?: string }

/** A human description for an event; known topics get a sentence, the rest a tidy fallback. */
export function describeEvent(e: Event, ctx: EventCtx): EventDescription {
  const p = (e.payload && typeof e.payload === 'object' ? e.payload : {}) as P
  const who = person(str(p.user_email), ctx)
  const kind = str(p.type) === 'video' ? 'video' : 'photo'
  const place = [str(p.city), str(p.country)].filter(Boolean).join(', ')
  const camera = [str(p.camera_make), str(p.camera_model)].filter(Boolean).join(' ').replace(/^Apple (iPhone)/, '$1')
  const size = p.width && p.height ? `${p.width}×${p.height}` : ''
  switch (e.topic) {
    case 'immich.asset_created':
      return {
        title: `${who} uploaded a ${p.live_photo ? 'live photo' : kind}`,
        sub: [str(p.original_file_name), p.is_favorite ? '★ favourite' : ''].filter(Boolean).join(' · '),
        link: p.url ? { href: str(p.url), label: 'Open in Immich' } : undefined,
        facts: [['Person', who], ['File', str(p.original_file_name)], ['Type', p.live_photo ? 'Live photo' : kind === 'video' ? 'Video' : 'Photo'], ['Taken', clock(str(p.local_date_time || p.file_created_at))], ['Uploaded', clock(str(p.uploaded_at))]],
      }
    case 'immich.asset_processed':
      return {
        title: place ? `${who}'s ${kind} from ${place}` : `${who}'s ${kind} details are ready`,
        sub: [str(p.original_file_name), camera, size].filter(Boolean).join(' · '),
        link: p.url ? { href: str(p.url), label: 'Open in Immich' } : undefined,
        facts: [['Person', who], ['File', str(p.original_file_name)], ['Place', [str(p.city), str(p.state), str(p.country)].filter(Boolean).join(', ')], ['Taken', [clock(str(p.taken_at || p.local_date_time)), str(p.time_zone)].filter(Boolean).join(' ')], ['Camera', camera], ['Lens', str(p.lens_model)], ['Size', size]],
      }
    case 'immich.user_created':
      return { title: `New Immich account${p.name ? `: ${str(p.name)}` : ''}`, sub: str(p.email || p.user_email) }
  }
  return { title: humanTopic(e.topic), sub: e.topic + (e.subject ? ` · ${e.subject}` : '') }
}
