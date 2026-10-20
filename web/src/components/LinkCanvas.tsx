import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { App, AppLink } from '../lib/types'
import { layoutGraph } from './linkLayout'
import { bundledIcon } from '../lib/icons'

// One arrow per (acting app → target app); its scopes ride on the arrow.
export interface LinkGroup { key: string; app: string; appName: string; target: string; targetName: string; reason: string; links: AppLink[] }
export type CanvasSel = { kind: 'pair'; key: string } | { kind: 'app'; slug: string } | null
type Pos = { x: number; y: number }
type View = { x: number; y: number; k: number }
type Node = { slug: string; name: string; app?: App; linked: boolean; out: number; inn: number; offers: number }

const W = 184, H = 58, MIN_K = 0.1, MAX_K = 2.5
const LAYOUT_KEY = 'core.links.layout.v3'

/** on = every scope issues tokens; partial = some switched off; off = none; bad = a scope can't work. */
export function pairState(g: LinkGroup): 'on' | 'partial' | 'off' | 'bad' {
  if (g.links.some(l => l.state !== 'on' && l.state !== 'off_admin' && l.state !== 'off_person')) return 'bad'
  const on = g.links.filter(l => l.enabled).length
  return on === g.links.length ? 'on' : on === 0 ? 'off' : 'partial'
}

/** How many people the acting app connects to this target directly (pasted URL and key). */
export const directCount = (g: LinkGroup) => g.links[0]?.modes?.direct || 0

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
const finite = (p: unknown): p is Pos => !!p && Number.isFinite((p as Pos).x) && Number.isFinite((p as Pos).y)
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

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
  // Saved card positions; anything that isn't a pair of real numbers is dropped.
  const [saved, setSaved] = useState<Record<string, Pos>>(() => {
    try { return Object.fromEntries(Object.entries(JSON.parse(localStorage.getItem(LAYOUT_KEY) || '{}')).filter(([, v]) => finite(v))) as Record<string, Pos> } catch { return {} }
  })
  const [view, setViewRaw] = useState<View>({ x: 20, y: 20, k: 1 })
  const viewRef = useRef(view)
  viewRef.current = view
  // A view that isn't real numbers is never applied (it would freeze the canvas).
  const setView = (u: View | ((v: View) => View)) => setViewRaw(v => {
    const n = typeof u === 'function' ? u(v) : u
    return Number.isFinite(n.x) && Number.isFinite(n.y) && Number.isFinite(n.k) && n.k > 0 ? n : v
  })
  const [narrow, setNarrow] = useState(false)
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

  // A label box fits its scope names; the layout reserves exactly that much room.
  const labelLines = (g: LinkGroup) => [...g.links.map(scopeLabel), ...(directCount(g) ? [`↗ Direct for ${directCount(g)}`] : [])]
  const labelSize = (g: LinkGroup) => { const ls = labelLines(g); return { w: Math.ceil(Math.max(...ls.map(textWidth))) + 20, h: ls.length * 16 + 9 } }
  const lay = useMemo(() => layoutGraph(drawn.map(n => n.slug), groups.map(g => ({ key: g.key, from: g.app, to: g.target })), { w: W, h: H },
    key => labelSize(groups.find(g => g.key === key)!)), [drawn, groups]) // eslint-disable-line react-hooks/exhaustive-deps
  const auto = lay.pos
  const pos = useMemo(() => {
    const p = { ...auto }
    for (const n of drawn) if (finite(saved[n.slug])) p[n.slug] = saved[n.slug]
    return p
  }, [auto, saved, drawn])

  const fit = () => {
    const el = box.current
    if (!el || drawn.length === 0) return
    const xs = drawn.map(n => pos[n.slug].x), ys = drawn.map(n => pos[n.slug].y)
    const minX = Math.min(lay.bounds.minX, ...xs) - 30, maxX = Math.max(lay.bounds.maxX, ...xs.map(x => x + W)) + 30
    const minY = Math.min(lay.bounds.minY, ...ys) - 30, maxY = Math.max(lay.bounds.maxY, ...ys.map(y => y + H)) + 30
    const k = Math.max(MIN_K, Math.min(1.15, el.clientWidth / (maxX - minX), el.clientHeight / (maxY - minY)))
    setView({ k, x: (el.clientWidth - (maxX - minX) * k) / 2 - minX * k, y: (el.clientHeight - (maxY - minY) * k) / 2 - minY * k })
  }
  // Fit when what is drawn changes (not when a card is dragged).
  const shape = drawn.map(n => n.slug).join(',') + '|' + groups.map(g => g.key).join(',')
  // A counter asks for a fit after the next render, so it sees the new positions.
  const [fitTick, setFitTick] = useState(0)
  useLayoutEffect(fit, [shape, fitTick, narrow]) // eslint-disable-line react-hooks/exhaustive-deps
  // …and when the canvas gets wider or narrower (a phone turned), not on height
  // changes (a phone's address bar sliding away).
  const fitRef = useRef(fit)
  fitRef.current = fit
  useEffect(() => {
    const el = box.current
    if (!el) return
    let w = el.clientWidth
    setNarrow(w < 640)
    const ro = new ResizeObserver(() => { if (el.clientWidth !== w) { w = el.clientWidth; setNarrow(w < 640); fitRef.current() } })
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
  useEffect(() => { const id = requestAnimationFrame(() => setFitTick(t => t + 1)); return () => cancelAnimationFrame(id) }, [full])

  const zoomAt = (px: number, py: number, f: number) => setView(v => {
    if (!Number.isFinite(f) || f <= 0) return v
    const k = Math.max(MIN_K, Math.min(MAX_K, v.k * f))
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
    // iOS: keep the page itself from scrolling or zooming under a finger on the canvas.
    const stop = (e: Event) => e.preventDefault()
    el.addEventListener('wheel', onWheel, { passive: false })
    el.addEventListener('touchstart', stop, { passive: false })
    el.addEventListener('touchmove', stop, { passive: false })
    el.addEventListener('gesturestart', stop)
    return () => {
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('touchstart', stop)
      el.removeEventListener('touchmove', stop)
      el.removeEventListener('gesturestart', stop)
    }
  }, [])

  // One pointer pans the canvas or drags a card; two pinch-zoom. A press that
  // barely moves is a tap: it selects the card or arrow under it. With a finger,
  // a card moves only after a half-second hold, so dragging otherwise pans.
  // Fingers are forgotten when they lift anywhere (iOS doesn't always tell the
  // canvas), and a fresh first touch starts from a clean slate.
  type Gesture = { kind: 'pan' | 'node' | 'pinch' | 'hold'; slug?: string; start: Pos; orig: Pos; moved: boolean; hit: CanvasSel; d0?: number; k0?: number; mid0?: Pos; v0?: View; timer?: number }
  const ptrs = useRef(new Map<number, Pos>())
  const gesture = useRef<Gesture | null>(null)
  const [lifted, setLifted] = useState('')
  const local = (e: { clientX: number; clientY: number }) => { const r = svg.current!.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top } }
  const endGesture = () => { if (gesture.current?.timer) clearTimeout(gesture.current.timer); gesture.current = null; setLifted('') }
  useEffect(() => {
    const gone = (e: PointerEvent) => {
      ptrs.current.delete(e.pointerId)
      if (ptrs.current.size === 0 && gesture.current?.kind === 'pinch') endGesture()
    }
    window.addEventListener('pointerup', gone)
    window.addEventListener('pointercancel', gone)
    return () => { window.removeEventListener('pointerup', gone); window.removeEventListener('pointercancel', gone) }
  }, [])
  const startPinch = () => {
    const [a, b] = [...ptrs.current.values()]
    const d0 = Math.hypot(a.x - b.x, a.y - b.y)
    if (gesture.current?.timer) clearTimeout(gesture.current.timer)
    setLifted('')
    gesture.current = d0 < 8 ? null : { kind: 'pinch', start: a, orig: a, moved: true, hit: null, d0, k0: viewRef.current.k, mid0: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, v0: viewRef.current }
  }
  const down = (e: React.PointerEvent) => {
    if (e.pointerType === 'mouse' && e.button !== 0) return
    if (e.isPrimary) { ptrs.current.clear(); endGesture() }
    try { svg.current!.setPointerCapture(e.pointerId) } catch { /* pointer already gone */ }
    const p = local(e)
    ptrs.current.set(e.pointerId, p)
    if (ptrs.current.size >= 2) { startPinch(); return }
    const t = e.target as Element
    const nodeEl = t.closest('[data-node]'), pairEl = t.closest('[data-pair]')
    const hit: CanvasSel = nodeEl ? { kind: 'app', slug: nodeEl.getAttribute('data-node')! } : pairEl ? { kind: 'pair', key: pairEl.getAttribute('data-pair')! } : null
    const v = viewRef.current
    if (hit?.kind === 'app' && e.pointerType === 'mouse') {
      gesture.current = { kind: 'node', slug: hit.slug, start: p, orig: pos[hit.slug], moved: false, hit }
    } else if (hit?.kind === 'app') {
      const g: Gesture = { kind: 'hold', slug: hit.slug, start: p, orig: { x: v.x, y: v.y }, moved: false, hit }
      g.timer = window.setTimeout(() => {
        if (gesture.current !== g || g.moved) return
        gesture.current = { kind: 'node', slug: hit.slug, start: g.start, orig: pos[hit.slug], moved: true, hit }
        setLifted(hit.slug)
        navigator.vibrate?.(10)
      }, 450)
      gesture.current = g
    } else {
      gesture.current = { kind: 'pan', start: p, orig: { x: v.x, y: v.y }, moved: false, hit }
    }
  }
  const move = (e: React.PointerEvent) => {
    const g = gesture.current
    if (!g || !ptrs.current.has(e.pointerId)) return
    const p = local(e)
    ptrs.current.set(e.pointerId, p)
    if (g.kind === 'pinch') {
      const [a, b] = [...ptrs.current.values()]
      if (!b) return
      const k = Math.max(MIN_K, Math.min(MAX_K, g.k0! * Math.hypot(a.x - b.x, a.y - b.y) / g.d0!))
      const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, v0 = g.v0!
      // the point under the fingers' midpoint at the start stays under it
      setView({ k, x: mid.x - (g.mid0!.x - v0.x) * k / v0.k, y: mid.y - (g.mid0!.y - v0.y) * k / v0.k })
      return
    }
    const dx = p.x - g.start.x, dy = p.y - g.start.y
    if (!g.moved && Math.hypot(dx, dy) < 6) return
    g.moved = true
    if (g.kind === 'pan' || g.kind === 'hold') setView(v => ({ ...v, x: g.orig.x + dx, y: g.orig.y + dy }))
    else {
      const k = viewRef.current.k
      const np = { x: Math.round(g.orig.x + dx / k), y: Math.round(g.orig.y + dy / k) }
      if (finite(np)) setSaved(s => ({ ...s, [g.slug!]: np }))
    }
  }
  const up = (e: React.PointerEvent) => {
    ptrs.current.delete(e.pointerId)
    const g = gesture.current
    if (!g) return
    if (g.kind === 'pinch') { if (ptrs.current.size === 0) endGesture(); return }
    endGesture()
    if (e.type === 'pointercancel') return
    if (!g.moved) {
      const same = g.hit && sel && g.hit.kind === sel.kind && (g.hit.kind === 'app' ? g.hit.slug === (sel as { slug: string }).slug : g.hit.key === (sel as { key: string }).key)
      onSelect(same ? null : g.hit)
    } else if (g.kind === 'node') setSaved(s => { localStorage.setItem(LAYOUT_KEY, JSON.stringify(s)); return s })
  }

  const focus = sel?.kind === 'app' && pos[sel.slug] ? sel.slug : hover
  const touches = (g: LinkGroup) => g.app === focus || g.target === focus
  const near = new Set(focus ? groups.filter(touches).flatMap(g => [g.app, g.target]) : [])
  // Arrows follow the laid-out lanes; one touching a card you moved goes straight instead.
  const edges = groups.filter(g => pos[g.app] && pos[g.target]).map(g => {
    const r = lay.routes[g.key]
    const size = labelSize(g)
    if (r && !finite(saved[g.app]) && !finite(saved[g.target])) return { g, st: pairState(g), d: r.d, reversed: r.reversed, label: r.label }
    const back = g.app !== g.target && groups.some(h => h.app === g.target && h.target === g.app)
    const e = edgePath(pos[g.app], pos[g.target], back ? (g.app < g.target ? -9 : 9) : 0)
    return { g, st: pairState(g), d: e.d, reversed: false, label: { x: e.mid.x - size.w / 2, y: e.mid.y - size.h / 2, ...size } }
  })
  const resetLayout = () => { localStorage.removeItem(LAYOUT_KEY); setSaved({}); setFitTick(t => t + 1) }
  // Zoomed out, arrows carry their scope names only for the tapped app or arrow.
  const showLabel = (g: LinkGroup) => (sel?.kind === 'pair' && sel.key === g.key) || (!!focus && touches(g)) || (!focus && sel?.kind !== 'pair' && view.k >= 0.5)

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
          <button type="button" className="btn sm ghost" onClick={resetLayout} disabled={Object.keys(saved).length === 0} title="Put moved cards back where the diagram places them">Reset</button>
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
            {edges.map(({ g, st, d, reversed }) => {
              const on = sel?.kind === 'pair' && sel.key === g.key
              const dim = (focus && !touches(g)) || (sel?.kind === 'pair' && !on)
              return (
                <g key={g.key} data-pair={g.key} className={`lc-edge ${st}${on ? ' picked' : ''}${dim ? ' dim' : ''}`}>
                  <path d={d} className="lc-hit" />
                  <path d={d} className="lc-line" markerEnd={reversed ? undefined : `url(#lc-arrow-${st})`} markerStart={reversed ? `url(#lc-arrow-${st})` : undefined} />
                  {st === 'on' && <path d={d} className="lc-flow" />}
                </g>)
            })}
            {edges.filter(({ g }) => showLabel(g)).map(({ g, st, label }) => {
              const { w, h } = label
              const on = sel?.kind === 'pair' && sel.key === g.key
              const dim = (focus && !touches(g)) || (sel?.kind === 'pair' && !on)
              const uses = g.links.reduce((a, l) => a + l.uses, 0)
              return (
                <g key={g.key} data-pair={g.key} className={`lc-label ${st}${on ? ' picked' : ''}${dim ? ' dim' : ''}`} transform={`translate(${label.x},${label.y})`}>
                  <title>{`${g.appName} → ${g.targetName}\n${g.links.map(l => `${scopeLabel(l)} (${l.scope})${l.enabled ? '' : ', off'}`).join('\n')}\n${plural(uses, 'token')} issued`}</title>
                  <rect width={w} height={h} rx={7} />
                  {g.links.map((l, i) => <text key={l.id} x={10} y={17 + i * 16} className={l.enabled ? '' : 'off'}>{scopeLabel(l)}</text>)}
                  {directCount(g) > 0 && <text x={10} y={17 + g.links.length * 16} className="direct">↗ Direct for {directCount(g)}</text>}
                </g>)
            })}
            {drawn.map(n => {
              const p = pos[n.slug]
              const svc = n.slug.startsWith('service:') ? n.slug.slice(8) : ''
              const src = n.app ? iconSrc(n.app) : svc ? bundledIcon(svc) : ''
              const clip = 'lc-clip-' + n.slug.replace(/[^a-z0-9-]/gi, '-')
              const on = sel?.kind === 'app' && sel.slug === n.slug
              const dim = (focus && focus !== n.slug && !near.has(n.slug)) || (sel?.kind === 'pair' && !groups.some(g => g.key === sel.key && (g.app === n.slug || g.target === n.slug)))
              const sub = n.linked ? [n.out && `uses ${plural(n.out, 'app')}`, n.inn && `used by ${n.inn}`].filter(Boolean).join(' · ') : n.offers ? `offers ${plural(n.offers, 'scope')}` : 'no links'
              return (
                <g key={n.slug} data-node={n.slug} transform={`translate(${p.x},${p.y})`}
                  className={`lc-node${n.app ? '' : svc ? ' service' : ' ghost'}${lifted === n.slug ? ' lifted' : ''}${on ? ' picked' : ''}${dim ? ' dim' : ''}`}
                  onPointerEnter={e => e.pointerType === 'mouse' && setHover(n.slug)} onPointerLeave={() => setHover('')}>
                  <rect width={W} height={H} rx={12} className="lc-card" />
                  <clipPath id={clip}><rect x={12} y={13} width={32} height={32} rx={8} /></clipPath>
                  {src && !badIcons.has(n.slug)
                    ? <image href={src} x={12} y={13} width={32} height={32} clipPath={`url(#${clip})`} preserveAspectRatio="xMidYMid slice" onError={() => setBadIcons(s => new Set(s).add(n.slug))} />
                    : <><rect x={12} y={13} width={32} height={32} rx={8} className="lc-letter-bg" /><text x={28} y={34} textAnchor="middle" className="lc-letter">{short(n.name)[0]}</text></>}
                  <text x={54} y={27} className="lc-name">{short(n.name)}</text>
                  <text x={54} y={44} className="lc-sub">{n.app ? sub : svc ? `service · used by ${n.inn}` : 'not registered'}</text>
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
