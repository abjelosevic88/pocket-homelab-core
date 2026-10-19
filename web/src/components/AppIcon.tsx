import { useState } from 'react'
import type { App } from '../lib/types'

// "clips.clip_created" → "Clip created", "core.app.registered" → "App registered"
export function humanTopic(t: string) {
  const rest = t.split('.').slice(1).join(' ').replace(/_/g, ' ').trim() || t
  return rest.charAt(0).toUpperCase() + rest.slice(1)
}

export function AppIcon({ a, small = false }: { a: App; small?: boolean }) {
  const [ok, setOk] = useState(true)
  const base = (a.public_url || a.url).replace(/\/$/, '')
  const src = a.icon ? (a.icon.startsWith('http') ? a.icon : base + (a.icon.startsWith('/') ? '' : '/') + a.icon) : ''
  return <span className={small ? 'feed-ico' : 'mini-ico'}>{src && ok ? <img src={src} alt="" onError={() => setOk(false)} /> : a.name.replace(/^Pocket /, '')[0]}</span>
}

