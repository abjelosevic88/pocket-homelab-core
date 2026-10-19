import React, { createContext, useCallback, useContext, useEffect, useState } from 'react'

type Toast = { id: number; msg: string; kind: 'ok' | 'err' }
const ToastCtx = createContext<{ toast: (msg: string, kind?: 'ok' | 'err') => void }>({ toast: () => {} })
export function useToast() { return useContext(ToastCtx) }
export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const toast = useCallback((msg: string, kind: 'ok' | 'err' = 'ok') => {
    const id = Date.now() + Math.random()
    setItems(t => [...t, { id, msg, kind }])
    setTimeout(() => setItems(t => t.filter(x => x.id !== id)), kind === 'err' ? 6000 : 2500)
  }, [])
  return (
    <ToastCtx.Provider value={{ toast }}>
      {children}
      <div className="toasts">{items.map(t => <div key={t.id} className={`toast ${t.kind}`}>{t.msg}</div>)}</div>
    </ToastCtx.Provider>
  )
}

export function PageHeader({ title, sub, actions }: { title: React.ReactNode; sub?: React.ReactNode; actions?: React.ReactNode }) {
  return (
    <div className="page-header">
      <div><h1>{title}</h1>{sub && <div className="sub">{sub}</div>}</div>
      {actions && <div className="actions">{actions}</div>}
    </div>
  )
}

export function Card({ title, actions, children, flush, className = '' }: { title?: React.ReactNode; actions?: React.ReactNode; children: React.ReactNode; flush?: boolean; className?: string }) {
  return (
    <section className={`card ${className}`}>
      {(title || actions) && <div className="card-head"><h2>{title}</h2>{actions && <div className="row">{actions}</div>}</div>}
      <div className={`card-body ${flush ? 'flush' : ''}`}>{children}</div>
    </section>
  )
}

/** A labelled form control. Use group for button groups and star inputs: a
 *  <label> would forward clicks on its text to the first button inside. */
export function Field({ label, help, children, className = '', group = false }: { label: React.ReactNode; help?: React.ReactNode; children: React.ReactNode; className?: string; group?: boolean }) {
  const Tag = group ? 'div' : 'label'
  return <Tag className={`field ${className}`}><span className="label">{label}</span>{children}{help && <span className="help">{help}</span>}</Tag>
}

export function Loading() { return <div className="loading"><span className="spinner" /> Loading…</div> }

export function Empty({ title, children }: { title: string; children?: React.ReactNode }) {
  return <div className="empty"><h3>{title}</h3>{children && <p className="muted">{children}</p>}</div>
}

export function Modal({ title, onClose, children, size = '' }: { title: React.ReactNode; onClose: () => void; children: React.ReactNode; size?: '' | 'lg' }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div className="modal-backdrop" onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className={`modal ${size}`} role="dialog" aria-modal="true">
        <div className="modal-head"><h2>{title}</h2><button className="btn ghost icon" onClick={onClose} aria-label="Close">✕</button></div>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  )
}

export function Confirm({ title, message, onConfirm, onCancel, danger = true }: { title: string; message: React.ReactNode; onConfirm: () => void; onCancel: () => void; danger?: boolean }) {
  return (
    <Modal title={title} onClose={onCancel}>
      <p>{message}</p>
      <div className="form-actions">
        <button className="btn" onClick={onCancel}>Cancel</button>
        <button className={`btn ${danger ? 'danger' : 'primary'}`} onClick={onConfirm}>Confirm</button>
      </div>
    </Modal>
  )
}

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: string }[]; value: T; onChange: (t: T) => void }) {
  return <div className="tabs">{tabs.map(t => <button type="button" key={t.id} className={t.id === value ? 'active' : ''} onClick={() => onChange(t.id)}>{t.label}</button>)}</div>
}

export function Segmented<T extends string | number>({ options, value, onChange, size = '' }: { options: { value: T; label: React.ReactNode; title?: string }[]; value: T | null; onChange: (v: T) => void; size?: '' | 'sm' | 'lg' }) {
  return (
    <div className={`segmented ${size}`} role="radiogroup">
      {options.map(o => <button type="button" key={String(o.value)} role="radio" aria-checked={o.value === value} title={o.title} className={o.value === value ? 'active' : ''} onClick={() => onChange(o.value)}>{o.label}</button>)}
    </div>
  )
}

export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const run = useCallback(() => {
    setLoading(true)
    return fn().then(d => { setData(d); setError('') }).catch(e => setError((e as Error).message)).finally(() => setLoading(false))
  }, deps) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { run() }, [run])
  return { data, setData, error, loading, reload: run }
}

export function useDebounce<T>(value: T, ms = 300): T {
  const [v, setV] = useState(value)
  useEffect(() => { const t = setTimeout(() => setV(value), ms); return () => clearTimeout(t) }, [value, ms])
  return v
}

export function Dot({ status }: { status: string }) { return <span className={`dot ${status}`} title={status} /> }

export function Switch({ on, onChange, title, disabled }: { on: boolean; onChange: (v: boolean) => void; title?: string; disabled?: boolean }) {
  return <button type="button" role="switch" aria-checked={on} title={title} disabled={disabled} className={`switch ${on ? 'on' : ''}`} onClick={() => onChange(!on)} />
}

export function Secret({ value, label = 'Copy' }: { value: string; label?: string }) {
  const { toast } = useToast()
  return (
    <div className="row" style={{ alignItems: 'stretch' }}>
      <div className="secret grow">{value}</div>
      <button type="button" className="btn sm" onClick={() => { navigator.clipboard?.writeText(value); toast('Copied') }}>{label}</button>
    </div>
  )
}

export function Avatar({ name, email }: { name?: string; email?: string }) {
  const s = (name || email || '?').trim()
  const parts = s.split(/\s+/)
  const ini = (parts.length > 1 ? parts[0][0] + parts[1][0] : s.slice(0, 2)).toUpperCase()
  let h = 0
  for (const c of s) h = (h * 31 + c.charCodeAt(0)) % 360
  return <span className="avatar" style={{ background: `hsl(${h} 55% 45%)` }}>{ini}</span>
}

/** Collapse animates its children open and closed (height + fade, both directions); content mounts only while needed. */
export function Collapse({ open, children, className = '' }: { open: boolean; children: React.ReactNode; className?: string }) {
  const [mounted, setMounted] = useState(open)
  const [shown, setShown] = useState(open)
  useEffect(() => {
    if (open) {
      setMounted(true)
      const r = requestAnimationFrame(() => requestAnimationFrame(() => setShown(true)))
      return () => cancelAnimationFrame(r)
    }
    setShown(false)
    const t = setTimeout(() => setMounted(false), 240)
    return () => clearTimeout(t)
  }, [open])
  if (!mounted) return null
  return <div className={`collapse ${shown ? 'show' : ''} ${className}`} aria-hidden={!open}><div className="collapse-inner">{children}</div></div>
}
