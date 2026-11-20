import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import type { BoxAddress } from '../lib/types'
import { ADDRESS } from './BoxAddress'
import { Icon } from './icons'
import { Spinner } from './ui'

// The secure address carries on in the background after the setup's first step. These show how far
// it got (the setup's side rail), say when it's ready (a banner in the setup), and offer the new
// address to anyone still on this machine's plain address afterwards (a strip over every page).

/** Follows the address choice: every 6 s while it is on its way, then it stops. */
export function useBoxAddress(enabled: boolean, again?: unknown) {
  const [a, setA] = useState<BoxAddress | null>(null)
  useEffect(() => {
    if (!enabled) return
    let stop = false, timer: number | undefined
    const tick = async () => {
      try {
        const v = await api.get<BoxAddress>(ADDRESS)
        if (stop) return
        setA(v)
        if (onTheWay(v)) timer = window.setTimeout(tick, 6000)
      } catch { /* no address service: nothing to show */ }
    }
    tick()
    return () => { stop = true; window.clearTimeout(timer) }
  }, [enabled, again])
  return a
}

const wants = (a: BoxAddress) => (a.mode === 'pocket' || a.mode === 'cloudflare')
const want = (a: BoxAddress) => a.mode === 'cloudflare' && a.cloudflare ? a.cloudflare.domain : ''

/** Chosen and not there yet. */
export function onTheWay(a: BoxAddress | null): boolean {
  if (!a || !wants(a)) return false
  return !a.domain || (!!want(a) && a.domain !== want(a))
}

/** Pocket Core's secure address once it is ready, or "". */
export function secureURL(a: BoxAddress | null): string {
  return a && wants(a) && a.domain && !onTheWay(a) ? `https://core.${a.domain}` : ''
}

/** Whether this page is already on the secure address. */
export function onSecure(url: string) {
  try { return new URL(url).host === window.location.host } catch { return false }
}

function stepText(a: BoxAddress): string {
  switch (a.status?.state) {
    case 'asking': return 'Asking for the name…'
    case 'certificate': return 'Getting the padlock certificate…'
    case 'waiting': return 'Waiting for Pocket Names; asks again every few minutes.'
    case 'limited': return a.status?.message || "Can't get one yet today; it keeps trying."
    case 'offline': return "This machine can't reach the internet right now."
    case 'signin': return 'Cloudflare needs you to sign in again (Settings → System).'
    case 'failed': return a.status?.message || "Didn't work this time; tried again in a few minutes."
    default: return 'Getting it ready; a minute or two.'
  }
}

/** The setup's side rail: how far the address got, then where it is. */
export function AddressRailStatus({ a }: { a: BoxAddress | null }) {
  if (!a || !wants(a)) return null
  const url = secureURL(a)
  const stuck = ['limited', 'offline', 'signin', 'failed'].includes(a.status?.state || '')
  if (url) return (
    <div className="addr-rail ok">
      <span className="addr-rail-head"><Icon name="lock" size={13} /><b>Secure address ready</b></span>
      <a href={onSecure(url) ? undefined : `${url}${window.location.pathname}`} className="addr-rail-url">{url.replace('https://', '')}</a>
    </div>
  )
  return (
    <div className={`addr-rail ${stuck ? 'warn' : ''}`} title={stepText(a)}>
      <span className="addr-rail-head">{stuck ? <Icon name="alert" size={13} /> : <Spinner size={12} />}<b>Secure address</b></span>
      <span className="addr-rail-text">{stepText(a)}</span>
    </div>
  )
}

/** In the setup, once the address is ready and this page isn't on it yet. */
export function AddressReadyBanner({ a }: { a: BoxAddress | null }) {
  const url = secureURL(a)
  if (!url || onSecure(url)) return null
  return (
    <div className="callout success addr-ready">
      <Icon name="lock" size={15} />
      <span className="grow">Your secure address is ready: <b>{url.replace('https://', '')}</b>. Carry on here; the last step opens it, or open it now and sign in once more.</span>
      <a className="btn sm" href={`${url}${window.location.pathname}`}>Open<Icon name="external" size={13} /></a>
    </div>
  )
}

const SNOOZE = 'pc-addr-strip'

/** Over every page for an admin still on this machine's plain address once the secure one is ready. */
export function AddressStrip({ enabled }: { enabled: boolean }) {
  const a = useBoxAddress(enabled)
  const [hidden, setHidden] = useState(() => sessionStorage.getItem(SNOOZE) === '1')
  const url = secureURL(a)
  if (!enabled || hidden || !url || onSecure(url)) return null
  return (
    <div className="addr-strip" role="status">
      <Icon name="lock" size={14} />
      <span className="grow">You're on <b>{window.location.host}</b> without a padlock. Pocket's secure address is <b>{url.replace('https://', '')}</b>.</span>
      <a className="btn sm primary" href={`${url}${window.location.pathname}${window.location.search}`}>Open it there<Icon name="external" size={13} /></a>
      <button type="button" className="btn ghost sm icon-only" aria-label="Not now" title="Not now" onClick={() => { sessionStorage.setItem(SNOOZE, '1'); setHidden(true) }}><Icon name="x" size={14} /></button>
    </div>
  )
}
