import { useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { Card, Field } from '../components/ui'

export default function Login({ mode }: { mode: 'login' | 'setup' }) {
  const { status, user, refresh } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const next = new URLSearchParams(loc.search).get('next')
  const [form, setForm] = useState({ email: '', password: '', name: '', box_name: '' })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const go = () => {
    // An OIDC authorize round-trip must go through the server, not the router.
    if (next && next.startsWith('/oidc/')) { window.location.href = next; return }
    navigate((loc.state as { from?: string })?.from || next || '/')
  }
  if (status && user) { if (next && next.startsWith('/oidc/')) { window.location.href = next; return null } return <Navigate to={(loc.state as { from?: string })?.from || '/'} replace /> }
  if (status && mode === 'setup' && !status.needs_setup) return <Navigate to="/login" replace />
  if (status && mode === 'login' && status.needs_setup) return <Navigate to="/setup" replace />

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try { await api.post(`${V1}/auth/${mode}`, form); await refresh(); go() } catch (e: unknown) { setErr((e as Error).message) } finally { setBusy(false) }
  }
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm(f => ({ ...f, [k]: e.target.value }))

  return (
    <div className="login-wrap">
      <div className="login-card">
        <div className="row mb" style={{ justifyContent: 'center', gap: 10 }}><span className="logo" style={{ width: 36, height: 36, borderRadius: 10, display: 'grid', placeItems: 'center' }}><img src="/favicon.svg" alt="" style={{ width: 36, height: 36 }} /></span><h1>{status?.title || 'Pocket Core'}</h1></div>
        <Card title={mode === 'setup' ? 'Welcome! Set up your box' : next ? 'Sign in to continue' : 'Sign in'}>
          <form onSubmit={submit} className="grid" style={{ gap: 14 }}>
            {mode === 'setup' && <Field label="Box name" help="How this box introduces itself to paired devices"><input value={form.box_name} onChange={set('box_name')} placeholder="Pocket Box" autoFocus /></Field>}
            {mode === 'setup' && <Field label="Your name"><input value={form.name} onChange={set('name')} /></Field>}
            <Field label="Email"><input type="email" value={form.email} onChange={set('email')} required autoFocus={mode === 'login'} autoComplete="username" /></Field>
            <Field label="Password" help={mode === 'setup' ? 'At least 8 characters' : undefined}><input type="password" value={form.password} onChange={set('password')} required minLength={mode === 'setup' ? 8 : 1} autoComplete={mode === 'setup' ? 'new-password' : 'current-password'} /></Field>
            {err && <div className="callout danger">{err}</div>}
            <button className="btn primary lg" disabled={busy}>{busy ? 'Please wait…' : mode === 'setup' ? 'Create admin account' : 'Sign in'}</button>
          </form>
        </Card>
        {mode === 'setup' && <p className="muted small mt">You become the admin. This one account signs in to every Pocket app on the box; add the household under Users.</p>}
        {status?.version && <p className="muted small right mt">v{status.version.replace(/^v/, '')}</p>}
      </div>
    </div>
  )
}
