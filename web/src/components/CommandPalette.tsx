// ⌘K / "/" — jump to an app or a page, open one of your services, or run a command from the keyboard.
import React, { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from 'react-router-dom'
import { Icon } from './icons'
import type { ShellData } from './Layout'

export interface PaletteCommand { id: string; label: string; icon: string; hint?: string; keywords?: string; run: () => void }

export function CommandPalette({ onClose, commands, data }: { onClose: () => void; commands: PaletteCommand[]; data: ShellData }) {
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [hi, setHi] = useState(0)
  const list = useRef<HTMLDivElement>(null)

  type Row = { key: string; group: string; icon: React.ReactNode; label: React.ReactNode; sub?: React.ReactNode; hint?: string; run: () => void }
  const rows = useMemo<Row[]>(() => {
    const out: Row[] = []
    const n = q.trim().toLowerCase()
    const words = n.split(/\s+/).filter(Boolean)
    const hit = (text: string) => words.every(w => text.toLowerCase().includes(w))
    for (const a of data.overview?.apps || []) {
      if (n && !hit(`${a.name} ${a.slug} ${a.description}`)) continue
      out.push({ key: `a${a.id}`, group: 'Apps', icon: <span className={`status-dot ${a.status}`} />, label: a.name, sub: [a.version && `v${a.version}`, a.status === 'down' ? 'down' : a.status === 'ok' ? 'up' : 'unknown'].filter(Boolean).join(' · '), run: () => navigate(`/apps/${a.slug}`) })
    }
    for (const s of data.services) {
      if (n && !hit(`${s.name} ${s.type} ${s.group}`)) continue
      out.push({ key: `s${s.id}`, group: 'Your services', icon: <Icon name="external" size={16} />, label: s.name, sub: s.url.replace(/^https?:\/\//, ''), run: () => window.open(s.open_url || s.url, '_blank', 'noopener') })
    }
    for (const c of commands) {
      if (n && !hit(`${c.label} ${c.keywords || ''}`)) continue
      out.push({ key: c.id, group: 'Commands', icon: <Icon name={c.icon} size={16} />, label: c.label, hint: c.hint, run: c.run })
    }
    // with nothing typed, commands first: the apps list is long
    return n ? out : [...out.filter(r => r.group === 'Commands'), ...out.filter(r => r.group !== 'Commands')]
  }, [q, data, commands, navigate])
  useEffect(() => setHi(0), [q])
  useEffect(() => { list.current?.querySelector(`[data-i="${hi}"]`)?.scrollIntoView({ block: 'nearest' }) }, [hi])

  const run = (r: Row | undefined) => { if (!r) return; onClose(); r.run() }
  let last = ''
  return createPortal(
    <div className="palette-backdrop" onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="palette-box" role="dialog" aria-label="Search and commands">
        <div className="pal-input">
          <Icon name="search" size={18} />
          <input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder="Jump to an app or a page, or run a command…" aria-label="Search"
            onKeyDown={e => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setHi(h => Math.min(rows.length - 1, h + 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setHi(h => Math.max(0, h - 1)) }
              else if (e.key === 'Enter') { e.preventDefault(); run(rows[hi]) }
              else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose() }
            }} />
          <kbd>esc</kbd>
        </div>
        <div className="pal-list" ref={list} role="listbox">
          {rows.length === 0 && <div className="pal-empty">Nothing matches.</div>}
          {rows.map((r, i) => {
            const head = r.group !== last ? <div className="pal-group" key={'g' + r.group}>{r.group}</div> : null
            last = r.group
            return (
              <React.Fragment key={r.key}>
                {head}
                <div data-i={i} role="option" aria-selected={i === hi} className={`pal-row ${i === hi ? 'hi' : ''}`} onMouseEnter={() => setHi(i)} onClick={() => run(r)}>
                  <span className="pal-ico">{r.icon}</span>
                  <span className="grow"><span className="pal-label">{r.label}</span>{r.sub && <span className="pal-sub">{r.sub}</span>}</span>
                  {r.hint && <kbd>{r.hint}</kbd>}
                  {i === hi && <Icon name="chevron-right" size={14} className="muted" />}
                </div>
              </React.Fragment>
            )
          })}
        </div>
      </div>
    </div>, document.body)
}
