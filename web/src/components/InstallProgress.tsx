import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { bundledIcon } from '../lib/icons'
import { shortName } from '../lib/format'
import { Icon } from './icons'
import { Popover, Spinner, rectOf, type Anchor } from './ui'

// How far the apps on their way are, on every page: the apps picked in the setup (Core installs them
// one after the other, internal/server/setup.go) and, on a Pocket Box, the apps the box still fetches.
// A chip in the top bar shows "12/60"; it opens the whole list. GET /api/v1/installer/progress.

export interface QueueItem { slug: string; name: string; state: 'waiting' | 'installing' | 'done' | 'failed'; error?: string; job?: string; began_at?: string; ended_at?: string }
interface Queue { running: boolean; started_at?: string; pause?: string; items: QueueItem[] }
interface Arriving { state: 'downloading' | 'switching' | 'starting'; message: string; apps: string[]; running: number; total: number }
interface Progress { queue: Queue | null; arriving: Arriving | null }

export interface QueueSummary {
  total: number; done: number; failed: number; finished: boolean
  current?: QueueItem; position: number // 1-based place of the app installing now
  left?: number // seconds, a guess from the apps done so far
  pause?: string // why the next app waits for the machine to catch up
  key: string // tells one queue from the next (the "seen it" mark)
}

export function summarize(q: Queue): QueueSummary {
  const items = q.items
  const done = items.filter(i => i.state === 'done').length
  const failed = items.filter(i => i.state === 'failed').length
  const idx = items.findIndex(i => i.state === 'installing')
  const current = idx >= 0 ? items[idx] : undefined
  const waiting = items.filter(i => i.state === 'waiting').length
  const finished = !q.running && waiting === 0 && !current
  // Time left: the pace so far (from the start to the last app that ended), breathers included.
  const ends = items.map(i => i.ended_at ? Date.parse(i.ended_at) : 0).filter(Boolean)
  let left: number | undefined
  if (ends.length && q.started_at && !finished) {
    const per = (Math.max(...ends) - Date.parse(q.started_at)) / 1000 / ends.length
    const now = current?.began_at ? Math.max(0, per - (Date.now() - Date.parse(current.began_at)) / 1000) : 0
    if (per > 0) left = waiting * per + now
  }
  return { total: items.length, done, failed, finished, current, position: idx >= 0 ? idx + 1 : done + failed, left, pause: finished ? undefined : q.pause, key: q.started_at || String(items.length) }
}

export function aboutLeft(s?: number): string {
  if (s === undefined) return ''
  if (s < 90) return 'about a minute left'
  const m = Math.round(s / 60)
  if (m < 60) return `about ${m} min left`
  const h = Math.floor(m / 60), r = Math.round((m % 60) / 5) * 5
  return `about ${h} h${r ? ` ${r} min` : ''} left`
}

const SEEN = 'pc-install-seen'
const Ctx = createContext<{ progress: Progress | null; open: () => void }>({ progress: null, open: () => {} })
export const useInstallProgress = () => useContext(Ctx)

/** Polls while something installs (every 4 s), looks again on every page change and when an install starts. */
export function InstallProgressProvider({ enabled, children }: { enabled: boolean; children: React.ReactNode }) {
  const [progress, setProgress] = useState<Progress | null>(null)
  const loc = useLocation()
  const doneSeen = useRef(-1)
  const [kick, setKick] = useState(0)
  useEffect(() => {
    const k = () => setKick(n => n + 1)
    window.addEventListener('pc:install-started', k)
    return () => window.removeEventListener('pc:install-started', k)
  }, [])
  useEffect(() => {
    if (!enabled) return
    let stop = false, timer: number | undefined
    const tick = async () => {
      try {
        const p = await api.get<Progress>(`${V1}/installer/progress`)
        if (stop) return
        setProgress(p)
        // Each app that comes up: the sidebar and the Apps page load their lists again.
        const n = (p.queue ? p.queue.items.filter(i => i.state === 'done').length : 0) + (p.arriving ? p.arriving.running : 0)
        if (doneSeen.current !== -1 && n !== doneSeen.current) window.dispatchEvent(new CustomEvent('pc:apps-changed'))
        doneSeen.current = n
        if (p.queue?.running || p.arriving) timer = window.setTimeout(tick, 4000)
      } catch { /* no installer, or not now: the chip stays away */ }
    }
    tick()
    return () => { stop = true; window.clearTimeout(timer) }
  }, [enabled, loc.pathname, kick])
  const open = () => window.dispatchEvent(new CustomEvent('pc:install-progress'))
  return <Ctx.Provider value={{ progress: enabled ? progress : null, open }}>{children}</Ctx.Provider>
}

function shouldShow(p: Progress | null, seen: string): { q?: QueueSummary; a?: Arriving } | null {
  if (!p) return null
  const a = p.arriving || undefined
  let q: QueueSummary | undefined
  if (p.queue) {
    const s = summarize(p.queue)
    // A finished queue shows until it is dismissed, and only for a day (older ones were seen at the time).
    const last = Math.max(0, ...p.queue.items.map(i => i.ended_at ? Date.parse(i.ended_at) : 0))
    if (!s.finished || (s.key !== seen && last > 0 && Date.now() - last < 24 * 3600e3)) q = s
  }
  return q || a ? { q, a } : null
}

/** The chip in the top bar. */
export function InstallChip({ compact }: { compact: boolean }) {
  const { progress } = useInstallProgress()
  const [seen, setSeen] = useState(() => localStorage.getItem(SEEN) || '')
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    const h = () => { if (btn.current) setAnchor(rectOf(btn.current)) }
    window.addEventListener('pc:install-progress', h)
    return () => window.removeEventListener('pc:install-progress', h)
  }, [])
  const show = shouldShow(progress, seen)
  if (!show) return null
  const { q, a } = show
  const total = (q?.total || 0) + (a?.total || 0)
  const ready = (q?.done || 0) + (a?.running || 0)
  const finished = !!q?.finished && !a
  const pct = total ? (ready + (q?.failed || 0)) / total : 0
  const dismiss = () => { if (q) { localStorage.setItem(SEEN, q.key); setSeen(q.key) } setAnchor(null) }
  const label = finished ? `${q!.done} of ${q!.total} apps installed` : `Installing apps: ${ready} of ${total} ready`
  return (
    <>
      <button ref={btn} type="button" className={`btn ghost install-chip${finished ? (q!.failed ? ' warn' : ' ok') : ''}`} onClick={e => setAnchor(anchor ? null : rectOf(e.currentTarget))}
        aria-label={label} title={label} aria-haspopup="dialog" aria-expanded={!!anchor}>
        {finished ? <Icon name={q!.failed ? 'alert' : 'check'} size={15} /> : <Ring pct={pct} />}
        {!compact && <span>{finished ? 'Apps installed' : 'Installing'}</span>}
        <b>{ready}/{total}</b>
      </button>
      {anchor && <Popover anchor={anchor} onClose={() => setAnchor(null)} prefer="below" width={380} className="install-pop" label="Apps being installed">
        <ProgressPanel q={q} a={a} queue={progress?.queue || null} onClose={() => setAnchor(null)} onDismiss={finished ? dismiss : undefined} />
      </Popover>}
    </>
  )
}

function Ring({ pct }: { pct: number }) {
  const r = 7, c = 2 * Math.PI * r
  return (
    <svg className="install-ring" width="18" height="18" viewBox="0 0 18 18" aria-hidden="true">
      <circle cx="9" cy="9" r={r} fill="none" strokeWidth="2.5" className="track" />
      <circle cx="9" cy="9" r={r} fill="none" strokeWidth="2.5" strokeLinecap="round" className="fill"
        strokeDasharray={c} strokeDashoffset={c * (1 - Math.max(0.04, pct))} transform="rotate(-90 9 9)" />
    </svg>
  )
}

/** The bar and the line under it, for the Apps page and the chip's panel. */
export function QueueBar({ q }: { q: QueueSummary }) {
  return (
    <div className="install-bar" role="progressbar" aria-valuemin={0} aria-valuemax={q.total} aria-valuenow={q.done + q.failed}>
      <span style={{ width: `${Math.round(100 * q.done / Math.max(1, q.total))}%` }} />
      {q.failed > 0 && <span className="failed" style={{ width: `${Math.round(100 * q.failed / Math.max(1, q.total))}%` }} />}
    </div>
  )
}

function ProgressPanel({ q, a, queue, onClose, onDismiss }: { q?: QueueSummary; a?: Arriving; queue: Queue | null; onClose: () => void; onDismiss?: () => void }) {
  const navigate = useNavigate()
  const list = useRef<HTMLUListElement>(null)
  // Once it's over, the ones that didn't install come first.
  const items = q?.finished ? [...(queue?.items || [])].sort((x, y) => Number(y.state === 'failed') - Number(x.state === 'failed')) : queue?.items || []
  // Open on the app installing now, not on the top of a long list.
  useLayoutEffect(() => {
    const el = list.current?.querySelector('.installing') as HTMLElement | null
    if (el && list.current) list.current.scrollTop = el.offsetTop - list.current.offsetTop - 48
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="install-panel">
      {q && <>
        <div className="install-head">
          <b>{q.finished ? (q.failed ? `${q.done} of ${q.total} apps installed` : `All ${q.total} apps installed`) : 'Installing your apps'}</b>
          <span className="muted small">
            {q.finished ? (q.failed ? `${q.failed} didn't install; try them again under Apps → Install app.` : 'Each one signs you in with your Pocket Core account.')
              : <>{q.done} of {q.total} ready{q.failed ? ` · ${q.failed} didn't install` : ''}{q.left !== undefined ? ` · ${aboutLeft(q.left)}` : ''}</>}
          </span>
        </div>
        <QueueBar q={q} />
        {q.pause && !q.current && <div className="install-now pause">
          <Icon name="clock" size={14} /><span className="grow">Next app waits: {q.pause}. Slower, but your machine keeps up.</span>
        </div>}
        {q.current && <div className="install-now">
          <Spinner size={13} /><span className="grow">Now: <b>{q.current.name}</b> <span className="muted">· app {q.position} of {q.total}</span></span>
        </div>}
        <ul ref={list} className="install-list">
          {items.map(it => (
            <li key={it.slug} className={it.state}>
              <AppIcon slug={it.slug} name={it.name} />
              <span className="grow ellipsis">{it.name}</span>
              <span className="install-state" title={it.error}>
                {it.state === 'installing' ? <><Spinner size={12} />Installing</> : it.state === 'done' ? <><Icon name="check" size={13} />Ready</> : it.state === 'failed' ? <><Icon name="alert" size={13} />Didn't install</> : 'Waiting'}
              </span>
            </li>
          ))}
        </ul>
      </>}
      {a && <div className={q ? 'install-box mt-s' : 'install-box'}>
        <div className="install-head"><b>{a.total === 1 ? 'An app is' : `${a.total} apps are`} on the way to this box</b><span className="muted small">{a.message}</span></div>
        {a.state === 'starting' && a.total > 0 && <div className="install-bar"><span style={{ width: `${Math.round(100 * a.running / a.total)}%` }} /></div>}
      </div>}
      {!q?.finished && <p className="muted small install-note">Keep working; installs carry on on the server, even with this page closed. Each app shows up in the sidebar when it's ready.</p>}
      <div className="install-foot">
        {onDismiss && <button type="button" className="btn sm" onClick={onDismiss}>Dismiss</button>}
        <button type="button" className="btn sm primary" onClick={() => { onClose(); navigate('/apps') }}>Open Apps<Icon name="chevron-right" size={14} /></button>
      </div>
    </div>
  )
}

function AppIcon({ slug, name }: { slug: string; name: string }) {
  const [ok, setOk] = useState(true)
  const src = bundledIcon(slug)
  return <span className="install-icon">{src && ok ? <img src={src} alt="" onError={() => setOk(false)} /> : shortName(name)[0]}</span>
}
