import { useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { Field } from '../components/ui'
import { Icon } from '../components/icons'

export default function Login({ mode }: { mode: 'login' | 'setup' }) {
  const { status, user, refresh } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const next = new URLSearchParams(loc.search).get('next')
  const [form, setForm] = useState({ email: '', password: '', name: '', box_name: '' })
  const [err, setErr] = useState(() => new URLSearchParams(loc.search).get('error') || '')
  const [busy, setBusy] = useState(false)

  const go = () => {
    // An authorization round-trip must go through the server, not the router.
    if (next && (next.startsWith('/oidc/') || next.startsWith('/mcp/oauth/authorize?'))) { window.location.href = next; return }
    navigate((loc.state as { from?: string })?.from || next || '/')
  }
  if (status && user) { if (next && (next.startsWith('/oidc/') || next.startsWith('/mcp/oauth/authorize?'))) { window.location.href = next; return null } return <Navigate to={(loc.state as { from?: string })?.from || '/'} replace /> }
  if (status && mode === 'setup' && !status.needs_setup) return <Navigate to="/login" replace />
  if (status && mode === 'login' && status.needs_setup) return <Navigate to="/setup" replace />

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try { await api.post(`${V1}/auth/${mode}`, form); await refresh(); go() } catch (e: unknown) { setErr((e as Error).message) } finally { setBusy(false) }
  }
  const sso = mode === 'login' && status?.sso
  const passwords = mode === 'setup' || status?.password_login !== false
  const ssoHref = `${V1}/auth/sso/start?next=${encodeURIComponent((loc.state as { from?: string })?.from || next || '/')}`
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm(f => ({ ...f, [k]: e.target.value }))

  const title = status?.title || 'Pocket Core'

  return (
    <div className="login-wrap">
      <div className="login-card">
        <div className="login-brand">
          <img src="/favicon.svg" alt="" width={44} height={44} />
          <h1>{title}</h1>
          <p className="login-tagline">{mode === 'setup' ? 'Welcome! Set up your box: the first account becomes its admin.' : next ? 'Sign in to continue to the app that sent you here.' : 'One account for every Pocket app and service on this box.'}</p>
        </div>
        {sso && <>
          <a className="btn primary lg block" href={ssoHref}><Icon name="key" size={16} />Sign in with {status?.sso_name}</a>
          {passwords ? <div className="login-or"><span>or with a password</span></div> : err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        </>}
        {passwords && <form onSubmit={submit} className="stack">
          {mode === 'setup' && <Field label="Box name" help="How this box introduces itself to paired devices"><input value={form.box_name} onChange={set('box_name')} placeholder="Pocket Box" autoFocus /></Field>}
          {mode === 'setup' && <Field label="Your name"><input value={form.name} onChange={set('name')} autoComplete="name" /></Field>}
          <Field label="Email"><input type="email" value={form.email} onChange={set('email')} required autoFocus={mode === 'login' && !sso} autoComplete="username" /></Field>
          <Field label="Password" help={mode === 'setup' ? 'At least 8 characters' : undefined}><input type="password" value={form.password} onChange={set('password')} required minLength={mode === 'setup' ? 8 : 1} autoComplete={mode === 'setup' ? 'new-password' : 'current-password'} /></Field>
          {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
          <button className={`btn lg block ${sso ? '' : 'primary'}`} disabled={busy}>{busy ? 'Please wait…' : mode === 'setup' ? 'Create admin account' : 'Sign in'}</button>
        </form>}
        {mode === 'setup' && <p className="login-note">You become the admin. This one account signs in to every Pocket app on the box; add the household under People.</p>}
      </div>
      {status?.version && <p className="login-version">Pocket Core {/^v?\d/.test(status.version) ? 'v' + status.version.replace(/^v/, '') : status.version}</p>}
    </div>
  )
}
