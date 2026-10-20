// Layered layout for the Links diagram (a small Sugiyama): apps in columns by
// depth, every arrow's label in the gap right after the app that acts, arrows
// that skip a column get their own lane through it, the order inside each
// column is swept to cut crossings, and heights are solved so each card or
// label sits as close as it can to what it connects to without overlapping.

export type Pos = { x: number; y: number }
export type Box = { x: number; y: number; w: number; h: number }
export type Route = { d: string; label: Box; reversed: boolean }
type Edge = { key: string; from: string; to: string }
type Item = { id: string; col: number; kind: 'node' | 'label' | 'dummy'; h: number; w: number; y: number; nb: string[] }

const PAD = 34 // space on each side of the labels in a gap
const NODE_SEP = 26, SEP = 12

export function layoutGraph(slugs: string[], edges: Edge[], size: { w: number; h: number }, labelSize: (key: string) => { w: number; h: number }) {
  const W = size.w, H = size.h
  const out = (s: string) => edges.filter(e => e.from === s && e.to !== s).map(e => e.to)
  const degree = (s: string) => edges.filter(e => e.from === s).length - edges.filter(e => e.to === s).length

  // Break loops: a depth-first walk from the busiest actors drops the edge that closes one.
  const back = new Set<string>()
  const state: Record<string, 1 | 2> = {}
  const walk = (s: string) => {
    state[s] = 1
    for (const t of out(s)) { if (state[t] === 1) back.add(`${s}→${t}`); else if (!state[t]) walk(t) }
    state[s] = 2
  }
  for (const s of [...slugs].sort((a, b) => degree(b) - degree(a) || a.localeCompare(b))) if (!state[s]) walk(s)

  // Levels: longest path over the loop-free edges; apps that only receive go last.
  const dir = edges.filter(e => e.from !== e.to).map(e => back.has(`${e.from}→${e.to}`) ? { ...e, a: e.to, b: e.from, rev: true } : { ...e, a: e.from, b: e.to, rev: false })
  const level: Record<string, number> = Object.fromEntries(slugs.map(s => [s, 0]))
  for (let i = 0; i < slugs.length; i++) {
    let changed = false
    for (const e of dir) if (level[e.b] < level[e.a] + 1) { level[e.b] = level[e.a] + 1; changed = true }
    if (!changed) break
  }
  const last = Math.max(0, ...Object.values(level))
  for (const s of slugs) if (out(s).length === 0 && edges.some(e => e.to === s)) level[s] = last

  // Items: cards in even columns, labels in the odd column after the actor, lanes for skipped columns.
  const items = new Map<string, Item>()
  const add = (it: Omit<Item, 'y' | 'nb'>) => { const x = { ...it, y: 0, nb: [] as string[] }; items.set(it.id, x); return x }
  for (const s of slugs) add({ id: 'n:' + s, col: 2 * level[s], kind: 'node', h: H, w: W })
  const chains: Record<string, string[]> = {}
  for (const e of dir) {
    const la = level[e.a], lb = Math.max(level[e.b], la + 1)
    const ls = labelSize(e.key)
    const chain = ['n:' + e.a, add({ id: 'l:' + e.key, col: 2 * la + 1, kind: 'label', h: ls.h, w: ls.w }).id]
    // a lane through every column it skips, the gaps included, so it never cuts a card or a label
    for (let c = 2 * la + 2; c < 2 * lb; c++) chain.push(add({ id: `d:${e.key}:${c}`, col: c, kind: 'dummy', h: 2, w: 0 }).id)
    chain.push('n:' + e.b)
    for (let i = 0; i + 1 < chain.length; i++) { items.get(chain[i])!.nb.push(chain[i + 1]); items.get(chain[i + 1])!.nb.push(chain[i]) }
    chains[e.key] = chain
  }

  // Order inside each column: barycentre sweeps, down and up.
  const ncol = 2 * last + 1
  const cols: Item[][] = Array.from({ length: ncol }, () => [])
  for (const it of items.values()) cols[it.col].push(it)
  cols[0].sort((a, b) => degree(b.id.slice(2)) - degree(a.id.slice(2)) || a.id.localeCompare(b.id))
  const rank = new Map<string, number>()
  const setRanks = () => cols.forEach(c => c.forEach((it, i) => rank.set(it.id, c.length > 1 ? i / (c.length - 1) : 0.5)))
  setRanks()
  const sweep = (c: Item[], side: (n: Item) => boolean) => {
    const bc = new Map(c.map(it => {
      const ns = it.nb.map(id => items.get(id)!).filter(side)
      return [it.id, ns.length ? ns.reduce((a, n) => a + rank.get(n.id)!, 0) / ns.length : rank.get(it.id)!] as const
    }))
    c.sort((a, b) => bc.get(a.id)! - bc.get(b.id)!)
    c.forEach((it, i) => rank.set(it.id, c.length > 1 ? i / (c.length - 1) : 0.5))
  }
  for (let it = 0; it < 12; it++) {
    for (let ci = 1; ci < ncol; ci++) sweep(cols[ci], n => n.col < ci)
    for (let ci = ncol - 2; ci >= 0; ci--) sweep(cols[ci], n => n.col > ci)
  }

  // Heights: each item wants the mean centre of its neighbours; within a column
  // the order is kept and overlaps are solved by least squares (pool adjacent violators).
  const sep = (a: Item, b: Item) => (a.kind === 'node' && b.kind === 'node' ? NODE_SEP : SEP)
  for (const c of cols) { let y = 0; for (let i = 0; i < c.length; i++) { if (i) y += c[i - 1].h + sep(c[i - 1], c[i]); c[i].y = y } }
  const place = (c: Item[]) => {
    if (!c.length) return
    const off: number[] = []
    let acc = 0
    c.forEach((it, i) => { if (i) acc += c[i - 1].h + sep(c[i - 1], it); off.push(acc) })
    const want = c.map((it, i) => {
      const ns = it.nb.map(id => items.get(id)!)
      const centre = ns.length ? ns.reduce((a, n) => a + n.y + n.h / 2, 0) / ns.length : it.y + it.h / 2
      return centre - it.h / 2 - off[i]
    })
    const blocks: { v: number; n: number }[] = []
    for (const w of want) {
      blocks.push({ v: w, n: 1 })
      while (blocks.length > 1 && blocks[blocks.length - 2].v > blocks[blocks.length - 1].v) {
        const b = blocks.pop()!, a = blocks.pop()!
        blocks.push({ v: (a.v * a.n + b.v * b.n) / (a.n + b.n), n: a.n + b.n })
      }
    }
    let i = 0
    for (const b of blocks) for (let k = 0; k < b.n; k++, i++) c[i].y = b.v + off[i]
  }
  for (let it = 0; it < 40; it++) {
    for (let ci = 0; ci < ncol; ci++) place(cols[ci])
    for (let ci = ncol - 1; ci >= 0; ci--) place(cols[ci])
  }
  // Straighten: a run of lanes of one arrow takes a single height where the
  // neighbours in each of its columns leave room, so it reads as one line.
  const fits = (it: Item, y: number) => {
    const c = cols[it.col], i = c.indexOf(it)
    const p = c[i - 1], n = c[i + 1]
    return (!p || p.y + p.h + sep(p, it) <= y + 0.5) && (!n || y + it.h + sep(it, n) <= n.y + 0.5)
  }
  for (let pass = 0; pass < 3; pass++) for (const ch of Object.values(chains)) {
    const run = ch.map(id => items.get(id)!).filter(it => it.kind === 'dummy')
    if (run.length < 2) continue
    const mean = run.reduce((a, it) => a + it.y, 0) / run.length
    const cands = run.map(it => it.y).sort((a, b) => Math.abs(a - mean) - Math.abs(b - mean))
    const y = cands.find(c => run.every(it => fits(it, c)))
    if (y !== undefined) run.forEach(it => { it.y = y })
  }
  const minY = Math.min(...[...items.values()].map(i => i.y))
  for (const i of items.values()) i.y -= minY

  // Columns left to right: cards are W wide, a gap fits its widest label.
  const colX: number[] = [], colW: number[] = []
  let x = 0
  for (let ci = 0; ci < ncol; ci++) {
    const w = ci % 2 === 0 ? W : Math.max(90, ...cols[ci].map(i => i.w + 2 * PAD))
    colX.push(x); colW.push(w); x += w
  }
  const box = (it: Item): Box => it.kind === 'label'
    ? { x: colX[it.col] + (colW[it.col] - it.w) / 2, y: it.y, w: it.w, h: it.h }
    : { x: colX[it.col], y: it.y, w: it.kind === 'node' ? W : colW[it.col], h: it.h }

  // Ports: arrows leave and enter a card spread over its height, ordered by where they go.
  const port = new Map<string, number>()
  for (const s of slugs) {
    const n = items.get('n:' + s)!
    for (const side of ['out', 'in'] as const) {
      const ends = dir.flatMap(e => {
        const ch = chains[e.key]
        if (side === 'out' && ch[0] === n.id) return [{ key: e.key, y: items.get(ch[1])!.y + items.get(ch[1])!.h / 2 }]
        if (side === 'in' && ch[ch.length - 1] === n.id) { const p = items.get(ch[ch.length - 2])!; return [{ key: e.key, y: p.y + p.h / 2 }] }
        return []
      }).sort((a, b) => a.y - b.y)
      const step = ends.length > 1 ? Math.min(9, (H - 18) / (ends.length - 1)) : 0
      ends.forEach((e, i) => port.set(`${side}:${e.key}`, n.y + H / 2 + (i - (ends.length - 1) / 2) * step))
    }
  }

  // Paths: straight through each label and each run of lanes at one height,
  // smooth bends (horizontal tangents) in between.
  const routes: Record<string, Route> = {}
  for (const e of dir) {
    const ch = chains[e.key].map(id => items.get(id)!)
    const segs: { x1: number; x2: number; y: number; lane?: boolean }[] = []
    ch.forEach((it, i) => {
      const b = box(it)
      if (i === 0) { segs.push({ x1: b.x + b.w, x2: b.x + b.w + (e.rev ? 4 : 0), y: port.get('out:' + e.key)! }); return }
      if (i === ch.length - 1) { segs.push({ x1: b.x - (e.rev ? 0 : 4), x2: b.x, y: port.get('in:' + e.key)! }); return }
      const y = b.y + b.h / 2, prev = segs[segs.length - 1]
      if (it.kind === 'dummy' && i > 1 && Math.abs(prev.y - y) < 0.5) prev.x2 = b.x + b.w
      else segs.push({ x1: b.x, x2: b.x + b.w, y, lane: it.kind === 'dummy' })
    })
    // a lane that changes height leaves room to bend instead of hooking at the end
    segs.forEach((g, i) => {
      if (!g.lane) return
      const room = Math.min(48, (g.x2 - g.x1) / 3)
      if (Math.abs(segs[i - 1].y - g.y) > 0.5) g.x1 += room
      if (Math.abs(segs[i + 1].y - g.y) > 0.5) g.x2 -= room
    })
    const first = segs[0]
    let d = `M${e.rev ? first.x2 : first.x1},${first.y}`
    for (let i = 1; i < segs.length; i++) {
      const a = segs[i - 1], b = segs[i]
      const from = i === 1 ? (e.rev ? a.x2 : a.x1) : a.x2
      if (i > 1) d += ` L${a.x2},${a.y}`
      const dx = Math.max(12, (b.x1 - from) / 2)
      d += ` C${from + dx},${a.y} ${b.x1 - dx},${b.y} ${b.x1},${b.y}`
    }
    routes[e.key] = { d, label: box(items.get('l:' + e.key)!), reversed: e.rev }
  }

  const pos: Record<string, Pos> = {}
  for (const s of slugs) { const b = box(items.get('n:' + s)!); pos[s] = { x: b.x, y: b.y } }
  const all = [...items.values()].map(box)
  const bounds = { minX: 0, minY: Math.min(...all.map(b => b.y)), maxX: x, maxY: Math.max(...all.map(b => b.y + b.h)) }
  return { pos, routes, bounds }
}
