import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'

export interface SelectOption { value: string; label: string; hint?: string; group?: string }

interface Props {
  value: string
  onChange: (value: string) => void
  options: SelectOption[]
  placeholder?: string
  /** Shown while filtering; "Create “x”" appears when nothing matches exactly. */
  onCreate?: (label: string) => Promise<string> | string
  createLabel?: string
  ariaLabel?: string
  className?: string
  disabled?: boolean
  /** Also offer an "× clear" row when a value is set (value "" means cleared). */
  clearLabel?: string
}

const Chevron = () => <svg className="sel-chevron" width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" /></svg>

/** A searchable combobox: looks like an input, opens a filterable list, keyboard friendly. */
export function Select({ value, onChange, options, placeholder = 'Choose…', onCreate, createLabel = 'Create', ariaLabel, className = '', disabled, clearLabel }: Props) {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState('')
  const [hl, setHl] = useState(0)
  const [busy, setBusy] = useState(false)
  const [up, setUp] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const search = useRef<HTMLInputElement>(null)
  const id = useId()

  const current = options.find(o => o.value === value)
  const filtered = useMemo(() => {
    const s = q.trim().toLowerCase()
    return s ? options.filter(o => o.label.toLowerCase().includes(s) || (o.hint || '').toLowerCase().includes(s)) : options
  }, [q, options])
  const exact = filtered.some(o => o.label.toLowerCase() === q.trim().toLowerCase())
  const canCreate = Boolean(onCreate) && q.trim() !== '' && !exact
  const rows: { kind: 'opt' | 'create' | 'clear'; opt?: SelectOption }[] = [
    ...(clearLabel && value && !q ? [{ kind: 'clear' as const }] : []),
    ...filtered.map(o => ({ kind: 'opt' as const, opt: o })),
    ...(canCreate ? [{ kind: 'create' as const }] : []),
  ]

  useEffect(() => {
    if (!open) return
    setQ(''); setHl(Math.max(0, rows.findIndex(r => r.opt?.value === value)))
    const t = setTimeout(() => search.current?.focus(), 0)
    const onDoc = (e: MouseEvent) => { if (root.current && !root.current.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', onDoc)
    return () => { clearTimeout(t); document.removeEventListener('mousedown', onDoc) }
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { setHl(0) }, [q])
  useLayoutEffect(() => {
    if (!open || !root.current) return
    const r = root.current.getBoundingClientRect()
    setUp(window.innerHeight - r.bottom < 300 && r.top > 300)
  }, [open])
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>(`[data-i="${hl}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [hl, open])

  const pick = async (row: (typeof rows)[number]) => {
    if (row.kind === 'clear') { onChange(''); setOpen(false); return }
    if (row.kind === 'create' && onCreate) {
      setBusy(true)
      try { const v = await onCreate(q.trim()); onChange(v); setOpen(false) } finally { setBusy(false) }
      return
    }
    if (row.opt) { onChange(row.opt.value); setOpen(false) }
  }
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setHl(h => Math.min(rows.length - 1, h + 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setHl(h => Math.max(0, h - 1)) }
    else if (e.key === 'Enter') { e.preventDefault(); if (rows[hl]) pick(rows[hl]) }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setOpen(false) }
    else if (e.key === 'Tab') setOpen(false)
  }

  let group = ''
  return (
    <div ref={root} className={`sel ${className} ${open ? 'open' : ''}`} onKeyDown={open ? onKey : undefined}>
      <button type="button" className={`sel-btn ${current ? '' : 'empty'}`} onClick={() => !disabled && setOpen(o => !o)} aria-haspopup="listbox" aria-expanded={open} aria-label={ariaLabel} aria-controls={id} disabled={disabled}
        onKeyDown={e => { if (!open && (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); setOpen(true) } }}>
        <span className="sel-val">{current ? current.label : placeholder}</span>
        <Chevron />
      </button>
      {open && (
        <div className={`sel-pop ${up ? 'up' : ''}`}>
          <div className="sel-search"><span className="ico">⌕</span><input ref={search} value={q} onChange={e => setQ(e.target.value)} placeholder="Type to filter…" aria-label="Filter options" autoComplete="off" /></div>
          <div className="sel-list" role="listbox" id={id} ref={list}>
            {rows.length === 0 && <div className="sel-empty muted">No matches</div>}
            {rows.map((r, i) => {
              const head = r.opt?.group && r.opt.group !== group ? (group = r.opt.group) : null
              return (
                <div key={r.kind === 'opt' ? r.opt!.value : r.kind}>
                  {head && <div className="sel-group">{head}</div>}
                  <div role="option" aria-selected={r.opt?.value === value} data-i={i} className={`sel-opt ${i === hl ? 'hl' : ''} ${r.opt && r.opt.value === value ? 'cur' : ''} ${r.kind !== 'opt' ? 'action' : ''}`}
                    onMouseEnter={() => setHl(i)} onMouseDown={e => e.preventDefault()} onClick={() => pick(r)}>
                    {r.kind === 'clear' ? <span>× {clearLabel}</span> : r.kind === 'create' ? <span>{busy ? 'Creating…' : `+ ${createLabel} “${q.trim()}”`}</span> : <><span className="sel-lbl">{r.opt!.label}</span>{r.opt!.hint && <span className="sel-hint muted">{r.opt!.hint}</span>}</>}
                    {r.opt && r.opt.value === value && <span className="sel-check">✓</span>}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
