import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { App, AppLink } from '../lib/types'

// One arrow per (acting app → target app); its scopes ride on the arrow.
export interface LinkGroup { key: string; app: string; appName: string; target: string; targetName: string; reason: string; links: AppLink[] }
export type CanvasSel = { kind: 'pair'; key: string } | { kind: 'app'; slug: string } | null
type Pos = { x: number; y: number }
type View = { x: number; y: number; k: number }
type Node = { slug: string; name: string; app?: App; linked: boolean; out: number; inn: number; offers: number }

const W = 184, H = 58, COL = 380, ROW = 88
const LAYOUT_KEY = 'core.links.layout'

/** on = every scope issues tokens; partial = some switched off; off = none; bad = a scope can't work. */
export function pairState(g: LinkGroup): 'on' | 'partial' | 'off' | 'bad' {
  if (g.links.some(l => l.state !== 'on' && l.state !== 'off_admin' && l.state !== 'off_person')) return 'bad'
  const on = g.links.filter(l => l.enabled).length
  return on === g.links.length ? 'on' : on === 0 ? 'off' : 'partial'
}

export function appOffers(a?: App): { scope: string; name?: string; description?: string }[] {
  const o = (a?.manifest as { offers?: unknown } | undefined)?.offers
  return Array.isArray(o) ? o as { scope: string; name?: string; description?: string }[] : []
}

const short = (name: string) => name.replace(/^Pocket /, '')
const iconSrc = (a: App) => {
  const base = (a.public_url || a.url).replace(/\/$/, '')
  return a.icon ? (a.icon.startsWith('http') ? a.icon : base + (a.icon.startsWith('/') ? '' : '/') + a.icon) : ''
}
// Label widths from the real font, so names of any length get a snug pill.
let ctx2d: CanvasRenderingContext2D | null = null
const textWidth = (t: string) => {
  if (!ctx2d) {
    ctx2d = document.createElement('canvas').getContext('2d')
    if (ctx2d) ctx2d.font = `550 11.5px ${getComputedStyle(document.documentElement).getPropertyValue('--font') || 'sans-serif'}`
  }
  return ctx2d ? ctx2d.measureText(t).width : t.length * 6.4
}
// The name the target app gives a scope; without one (not offered), "expenses:write" → "Write expenses".
export const scopeLabel = (l: AppLink) => {
  if (l.scope_name) return l.scope_name
  const [what, verb] = l.scope.split(':')
  return verb ? `${verb.charAt(0).toUpperCase()}${verb.slice(1)} ${what.replace(/[_-]/g, ' ')}` : l.scope
}
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

// Columns by depth (apps nobody calls on the left, what they call to the right),
// each column ordered by where its callers sit.
function autoLayout(nodes: Node[], groups: LinkGroup[]): Record<string, Pos> {
  const linked = nodes.filter(n => n.linked)
  const level: Record<string, number> = Object.fromEntries(linked.map(n => [n.slug, 0]))
  for (let i = 0; i < linked.length; i++) {
    let changed = false
    for (const g of groups) {
      if (g.app === g.target) continue
      const v = level[g.app] + 1
      if (level[g.target] < v && v < linked.length) { level[g.target] = v; changed = true }
    }
    if (!changed) break
  }
  const cols: Node[][] = []
  for (const n of linked) (cols[level[n.slug]] ||= []).push(n)
  const tallest = Math.max(1, ...cols.map(c => c?.length || 0))
  const pos: Record<string, Pos> = {}
  cols.forEach((c, ci) => {
    if (!c) return
    const rank = (n: Node) => {
      const ys = groups.filter(g => g.target === n.slug && pos[g.app]).map(g => pos[g.app].y)
      return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : 0
    }
    c.sort((a, b) => (ci ? rank(a) - rank(b) : 0) || a.name.localeCompare(b.name))
    c.forEach((n, ri) => { pos[n.slug] = { x: ci * COL, y: (ri - (c.length - 1) / 2) * ROW + (tallest - 1) * ROW / 2 } })
  })
  return pos
}

// A curve from the side of one card to the facing side of the other.
function edgePath(a: Pos, b: Pos, bend: number) {
  let p0: Pos, p3: Pos, c1: Pos, c2: Pos
  if (b.x >= a.x + W + 20 || b.x + W + 20 <= a.x) {
    const right = b.x > a.x
    p0 = { x: right ? a.x + W : a.x, y: a.y + H / 2 + bend }
    p3 = { x: right ? b.x - 4 : b.x + W + 4, y: b.y + H / 2 + bend }
    const d = Math.max(50, Math.abs(p3.x - p0.x) / 2) * (right ? 1 : -1)
    c1 = { x: p0.x + d, y: p0.y }; c2 = { x: p3.x - d, y: p3.y }
  } else {
    const down = b.y > a.y
    p0 = { x: a.x + W / 2 + bend, y: down ? a.y + H : a.y }
    p3 = { x: b.x + W / 2 + bend, y: down ? b.y - 4 : b.y + H + 4 }
    const d = Math.max(30, Math.abs(p3.y - p0.y) / 2) * (down ? 1 : -1)
    c1 = { x: p0.x, y: p0.y + d }; c2 = { x: p3.x, y: p3.y - d }
  }
  const mid = { x: (p0.x + 3 * c1.x + 3 * c2.x + p3.x) / 8, y: (p0.y + 3 * c1.y + 3 * c2.y + p3.y) / 8 }
  return { d: `M${p0.x},${p0.y} C${c1.x},${c1.y} ${c2.x},${c2.y} ${p3.x},${p3.y}`, mid }
}

export function LinkCanvas({ groups, apps, sel, onSelect, detail }: { groups: LinkGroup[]; apps: App[]; sel: CanvasSel; onSelect: (s: CanvasSel) => void; detail?: React.ReactNode }) {
  const [saved, setSaved] = useState<Record<string, Pos>>(() => { try { return JSON.parse(localStorage.getItem(LAYOUT_KEY) || '{}') } catch { return {} } })
  const [view, setView] = useState<View>({ x: 20, y: 20, k: 1 })
  const [hover, setHover] = useState<string>('')
  const [badIcons, setBadIcons] = useState<Set<string>>(new Set())
  const [full, setFull] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const box = useRef<HTMLDivElement>(null)
  const svg = useRef<SVGSVGElement>(null)

  const nodes = useMemo(() => {
    const ns: Node[] = []
    const seen = new Set<string>()
    const add = (slug: string, name: string, app?: App) => {
      if (seen.has(slug)) return
      seen.add(slug)
      ns.push({ slug, name, app, linked: false, out: 0, inn: 0, offers: appOffers(app).length })
    }
    for (const a of apps) add(a.slug, a.name, a)
    for (const g of groups) { add(g.app, g.appName); add(g.target, g.targetName || g.target) }
    for (const n of ns) {
      n.out = groups.filter(g => g.app === n.slug).length
      n.inn = groups.filter(g => g.target === n.slug).length
      n.linked = n.out + n.inn > 0
    }
    return ns
  }, [apps, groups])
  // Only apps with a link are drawn; the rest wait in the tray under the canvas.
  const drawn = useMemo(() => nodes.filter(n => n.linked), [nodes])
  const idle = useMemo(() => nodes.filter(n => !n.linked && n.app).sort((a, b) => a.name.localeCompare(b.name)), [nodes])

  const auto = useMemo(() => autoLayout(drawn, groups), [drawn, groups])
  const pos = useMemo(() => {
    const p = { ...auto }
    for (const n of drawn) if (saved[n.slug]) p[n.slug] = saved[n.slug]
    return p
  }, [auto, saved, drawn])

  const fit = () => {
    const el = box.current
    if (!el || drawn.length === 0) return
    const xs = drawn.map(n => pos[n.slug].x), ys = drawn.map(n => pos[n.slug].y)
    const minX = Math.min(...xs) - 30, maxX = Math.max(...xs) + W + 30
    const minY = Math.min(...ys) - 40, maxY = Math.max(...ys) + H + 30
    const k = Math.max(0.3, Math.min(1.15, el.clientWidth / (maxX - minX), el.clientHeight / (maxY - minY)))
    setView({ k, x: (el.clientWidth - (maxX - minX) * k) / 2 - minX * k, y: (el.clientHeight - (maxY - minY) * k) / 2 - minY * k })
  }
  // Fit when what is drawn changes (not when a card is dragged).
  const shape = drawn.map(n => n.slug).join(',') + '|' + groups.map(g => g.key).join(',')
  useLayoutEffect(fit, [shape]) // eslint-disable-line react-hooks/exhaustive-deps
  // …and when the canvas gets wider or narrower (a phone turned), not on height
  // changes (a phone's address bar sliding away).
  const fitRef = useRef(fit)
  fitRef.current = fit
  useEffect(() => {
    const el = box.current
    if (!el) return
    let w = el.clientWidth
    const ro = new ResizeObserver(() => { if (el.clientWidth !== w) { w = el.clientWidth; fitRef.current() } })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // Full screen: the canvas covers the page (works everywhere, iPhone included)
  // and asks the browser for real full screen where it can; Esc leaves both.
  const toggleFull = () => {
    if (full) { if (document.fullscreenElement) document.exitFullscreen().catch(() => {}); setFull(false); return }
    setFull(true)
    root.current?.requestFullscreen?.().catch(() => {})
  }
  useEffect(() => {
    if (!full) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.fullscreenElement) setFull(false) }
    const onFs = () => { if (!document.fullscreenElement) setFull(false) }
    document.addEventListener('keydown', onKey)
    document.addEventListener('fullscreenchange', onFs)
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { document.removeEventListener('keydown', onKey); document.removeEventListener('fullscreenchange', onFs); document.body.style.overflow = overflow }
  }, [full])
  useEffect(() => { const id = requestAnimationFrame(() => fitRef.current()); return () => cancelAnimationFrame(id) }, [full])

  const zoomAt = (px: number, py: number, f: number) => setView(v => {
    const k = Math.max(0.25, Math.min(2.5, v.k * f))
    return { k, x: px - (px - v.x) * k / v.k, y: py - (py - v.y) * k / v.k }
  })
  // Wheel zoom needs a non-passive listener to keep the page from scrolling.
  useEffect(() => {
    const el = svg.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const r = el.getBoundingClientRect()
      zoomAt(e.clientX - r.left, e.clientY - r.top, Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015)))
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  // One pointer pans the canvas or drags a card; two pinch-zoom. A press that
  // barely moves is a tap: it selects the card or arrow under it.
  const ptrs = useRef(new Map<number, Pos>())
  const gesture = useRef<{ kind: 'pan' | 'node' | 'pinch'; slug?: string; start: Pos; orig: Pos; moved: boolean; hit: CanvasSel; d0?: number; k0?: number } | null>(null)
  const local = (e: React.PointerEvent) => { const r = svg.current!.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top } }
  const down = (e: React.PointerEvent) => {
    if (e.button !== 0) return
    try { svg.current!.setPointerCapture(e.pointerId) } catch { /* pointer already gone */ }
    const p = local(e)
    ptrs.current.set(e.pointerId, p)
    if (ptrs.current.size === 2) {
      const [a, b] = [...ptrs.current.values()]
      gesture.current = { kind: 'pinch', start: p, orig: p, moved: true, hit: null, d0: Math.hypot(a.x - b.x, a.y - b.y), k0: view.k }
      return
    }
    const t = e.target as Element
    const nodeEl = t.closest('[data-node]'), pairEl = t.closest('[data-pair]')
    const hit: CanvasSel = nodeEl ? { kind: 'app', slug: nodeEl.getAttribute('data-node')! } : pairEl ? { kind: 'pair', key: pairEl.getAttribute('data-pair')! } : null
    gesture.current = hit?.kind === 'app'
      ? { kind: 'node', slug: hit.slug, start: p, orig: pos[hit.slug], moved: false, hit }
      : { kind: 'pan', start: p, orig: { x: view.x, y: view.y }, moved: false, hit }
  }
  const move = (e: React.PointerEvent) => {
    const g = gesture.current
    if (!g || !ptrs.current.has(e.pointerId)) return
    const p = local(e)
    ptrs.current.set(e.pointerId, p)
    if (g.kind === 'pinch') {
      const [a, b] = [...ptrs.current.values()]
      if (!b) return
      const k = Math.max(0.25, Math.min(2.5, g.k0! * Math.hypot(a.x - b.x, a.y - b.y) / g.d0!))
      zoomAt((a.x + b.x) / 2, (a.y + b.y) / 2, k / view.k)
      return
    }
    const dx = p.x - g.start.x, dy = p.y - g.start.y
    if (!g.moved && Math.hypot(dx, dy) < 5) return
    g.moved = true
    if (g.kind === 'pan') setView(v => ({ ...v, x: g.orig.x + dx, y: g.orig.y + dy }))
    else setSaved(s => ({ ...s, [g.slug!]: { x: Math.round(g.orig.x + dx / view.k), y: Math.round(g.orig.y + dy / view.k) } }))
  }
  const up = (e: React.PointerEvent) => {
    ptrs.current.delete(e.pointerId)
    const g = gesture.current
    if (!g) return
    if (g.kind === 'pinch') { if (ptrs.current.size === 0) gesture.current = null; return }
    gesture.current = null
    if (!g.moved) {
      const same = g.hit && sel && g.hit.kind === sel.kind && (g.hit.kind === 'app' ? g.hit.slug === (sel as { slug: string }).slug : g.hit.key === (sel as { key: string }).key)
      onSelect(same ? null : g.hit)
    } else if (g.kind === 'node') setSaved(s => { localStorage.setItem(LAYOUT_KEY, JSON.stringify(s)); return s })
  }

  const focus = sel?.kind === 'app' && pos[sel.slug] ? sel.slug : hover
  const touches = (g: LinkGroup) => g.app === focus || g.target === focus
  const near = new Set(focus ? groups.filter(touches).flatMap(g => [g.app, g.target]) : [])
  const edges = groups.filter(g => pos[g.app] && pos[g.target]).map(g => {
    const back = g.app !== g.target && groups.some(h => h.app === g.target && h.target === g.app)
    return { g, st: pairState(g), ...edgePath(pos[g.app], pos[g.target], back ? (g.app < g.target ? -9 : 9) : 0) }
  })
  const resetLayout = () => { localStorage.removeItem(LAYOUT_KEY); setSaved({}); requestAnimationFrame(fit) }

  return (
    <div className={`lc${full ? ' full' : ''}`} ref={root}>
      <div className="lc-bar">
        <div className="lc-legend">
          <span><i className="lc-key on" />On</span>
          <span><i className="lc-key partial" />Partly off</span>
          <span><i className="lc-key off" />Off</span>
          <span><i className="lc-key bad" />Can't work</span>
        </div>
        <div className="lc-tools">
          <button type="button" className="btn sm ghost" onClick={() => { const el = box.current!; zoomAt(el.clientWidth / 2, el.clientHeight / 2, 1 / 1.25) }} aria-label="Zoom out">−</button>
          <button type="button" className="btn sm ghost" onClick={() => { const el = box.current!; zoomAt(el.clientWidth / 2, el.clientHeight / 2, 1.25) }} aria-label="Zoom in">+</button>
          <button type="button" className="btn sm ghost" onClick={fit}>Fit</button>
          {Object.keys(saved).length > 0 && <button type="button" className="btn sm ghost" onClick={resetLayout} title="Forget where you dragged the cards">Reset layout</button>}
          <button type="button" className="btn sm ghost lc-full-btn" onClick={toggleFull} title={full ? 'Leave full screen (Esc)' : 'Full screen'} aria-label={full ? 'Leave full screen' : 'Full screen'}>
            <svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true">{full
              ? <path d="M6 1v5H1M10 1v5h5M6 15v-5H1M10 15v-5h5" />
              : <path d="M1 6V1h5M15 6V1h-5M1 10v5h5M15 10v5h-5" />}</svg>
            <span>{full ? 'Exit' : 'Full screen'}</span>
          </button>
        </div>
      </div>
      <div className="lc-box" ref={box}>
        <svg ref={svg} className="lc-svg" onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up}>
          <defs>
            {(['on', 'partial', 'off', 'bad'] as const).map(s => (
              <marker key={s} id={`lc-arrow-${s}`} viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                <path d="M0,0 L10,5 L0,10 z" className={`lc-mk ${s}`} />
              </marker>))}
            <pattern id="lc-grid" width="24" height="24" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="1" className="lc-grid-dot" /></pattern>
          </defs>
          <rect width="100%" height="100%" fill="url(#lc-grid)" />
          <g transform={`translate(${view.x},${view.y}) scale(${view.k})`}>
            {edges.map(({ g, st, d }) => {
              const on = sel?.kind === 'pair' && sel.key === g.key
              const dim = (focus && !touches(g)) || (sel?.kind === 'pair' && !on)
              return (
                <g key={g.key} data-pair={g.key} className={`lc-edge ${st}${on ? ' picked' : ''}${dim ? ' dim' : ''}`}>
                  <path d={d} className="lc-hit" />
                  <path d={d} className="lc-line" markerEnd={`url(#lc-arrow-${st})`} />
                  {st === 'on' && <path d={d} className="lc-flow" />}
                </g>)
            })}
            {edges.map(({ g, st, mid }) => {
              const lines = g.links.map(scopeLabel)
              const w = Math.ceil(Math.max(...lines.map(textWidth))) + 20, h = lines.length * 16 + 9
              const on = sel?.kind === 'pair' && sel.key === g.key
              const dim = (focus && !touches(g)) || (sel?.kind === 'pair' && !on)
              const uses = g.links.reduce((a, l) => a + l.uses, 0)
              return (
                <g key={g.key} data-pair={g.key} className={`lc-label ${st}${on ? ' picked' : ''}${dim ? ' dim' : ''}`} transform={`translate(${mid.x - w / 2},${mid.y - h / 2})`}>
                  <title>{`${g.appName} → ${g.targetName}\n${g.links.map(l => `${scopeLabel(l)} (${l.scope})${l.enabled ? '' : ', off'}`).join('\n')}\n${plural(uses, 'token')} issued`}</title>
                  <rect width={w} height={h} rx={7} />
                  {g.links.map((l, i) => <text key={l.id} x={10} y={17 + i * 16} className={l.enabled ? '' : 'off'}>{scopeLabel(l)}</text>)}
                </g>)
            })}
            {drawn.map(n => {
              const p = pos[n.slug]
              const src = n.app ? iconSrc(n.app) : ''
              const on = sel?.kind === 'app' && sel.slug === n.slug
              const dim = (focus && focus !== n.slug && !near.has(n.slug)) || (sel?.kind === 'pair' && !groups.some(g => g.key === sel.key && (g.app === n.slug || g.target === n.slug)))
              const sub = n.linked ? [n.out && `uses ${plural(n.out, 'app')}`, n.inn && `used by ${n.inn}`].filter(Boolean).join(' · ') : n.offers ? `offers ${plural(n.offers, 'scope')}` : 'no links'
              return (
                <g key={n.slug} data-node={n.slug} transform={`translate(${p.x},${p.y})`}
                  className={`lc-node${n.app ? '' : ' ghost'}${on ? ' picked' : ''}${dim ? ' dim' : ''}`}
                  onPointerEnter={e => e.pointerType === 'mouse' && setHover(n.slug)} onPointerLeave={() => setHover('')}>
                  <rect width={W} height={H} rx={12} className="lc-card" />
                  <clipPath id={`lc-clip-${n.slug}`}><rect x={12} y={13} width={32} height={32} rx={8} /></clipPath>
                  {src && !badIcons.has(n.slug)
                    ? <image href={src} x={12} y={13} width={32} height={32} clipPath={`url(#lc-clip-${n.slug})`} preserveAspectRatio="xMidYMid slice" onError={() => setBadIcons(s => new Set(s).add(n.slug))} />
                    : <><rect x={12} y={13} width={32} height={32} rx={8} className="lc-letter-bg" /><text x={28} y={34} textAnchor="middle" className="lc-letter">{short(n.name)[0]}</text></>}
                  <text x={54} y={27} className="lc-name">{short(n.name)}</text>
                  <text x={54} y={44} className="lc-sub">{n.app ? sub : 'not registered'}</text>
                  {n.app && <circle cx={W - 13} cy={13} r={4} className={`lc-status ${n.app.status}`}><title>{n.app.status}</title></circle>}
                </g>)
            })}
          </g>
        </svg>
        {drawn.length === 0 && <div className="lc-empty muted">{nodes.length ? 'No app uses another yet.' : 'No apps registered yet.'}</div>}
        {full && sel && detail && (
          <div className="lc-float">
            <button type="button" className="btn sm ghost lc-float-close" onClick={() => onSelect(null)} aria-label="Close">✕</button>
            {detail}
          </div>)}
      </div>
      {idle.length > 0 && (
        <div className="lc-tray">
          <span className="lc-tray-label">Not linked</span>
          {idle.map(n => {
            const src = iconSrc(n.app!)
            const on = sel?.kind === 'app' && sel.slug === n.slug
            return (
              <button type="button" key={n.slug} className={`lc-chip${on ? ' picked' : ''}`} onClick={() => onSelect(on ? null : { kind: 'app', slug: n.slug })}
                title={n.offers ? `${n.name} offers ${plural(n.offers, 'scope')} no app uses yet` : `${n.name} neither uses nor offers anything`}>
                {src && !badIcons.has(n.slug) ? <img src={src} alt="" onError={() => setBadIcons(s => new Set(s).add(n.slug))} /> : <i>{short(n.name)[0]}</i>}
                {short(n.name)}{n.offers > 0 && <span className="lc-chip-n">{n.offers}</span>}
              </button>)
          })}
        </div>)}
    </div>
  )
}
