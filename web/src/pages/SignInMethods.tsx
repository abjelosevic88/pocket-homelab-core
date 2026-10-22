// Settings → Single sign-on, top: how people sign in to Core itself — an external OpenID provider
// (PocketID, Authentik, Keycloak…), passwords, or both — with the provider-side setup spelled out.
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from '../components/icons'
import { Collapse, Field, Loading, Secret, Switch, errMsg, useAsync, useToast } from '../components/ui'

type Signin = {
  enabled: boolean; issuer: string; client_id: string; has_secret: boolean; name: string
  auto_create: boolean; password_login: boolean; callback_url: string; source: 'settings' | 'env' | 'none'; auth_mode: string
}

export function SignInMethods() {
  const { toast } = useToast()
  const { data, reload } = useAsync(() => api.get<Signin>(`${V1}/signin`), [])
  const [f, setF] = useState<Signin | null>(null)
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [tested, setTested] = useState('')
  const [guide, setGuide] = useState(false)
  // the form starts from what is stored, with the generic button name shown as PocketID
  const base = data ? { ...data, name: data.name === 'single sign-on' ? 'PocketID' : data.name } : null
  useEffect(() => { if (base) { setF(base); setSecret(''); setGuide(!base.enabled) } }, [data]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!f) return <Loading />

  const dirty = !!base && (secret !== '' || (['enabled', 'issuer', 'client_id', 'name', 'auto_create', 'password_login'] as const).some(k => f[k] !== base[k]))
  const set = (v: Partial<Signin>) => { setF(x => x && ({ ...x, ...v })); setError(''); if ('issuer' in v) setTested('') }

  const test = async () => {
    setBusy('test'); setError(''); setTested('')
    try { const r = await api.post<{ issuer: string }>(`${V1}/signin/test`, { issuer: f.issuer }); setTested(r.issuer) } catch (e) { setError(errMsg(e)) } finally { setBusy('') }
  }
  const save = async () => {
    if (!f.password_login && data?.password_login && !confirm(`Switch password sign-in off? Only ${f.name || 'the provider'} will work. Make sure you can sign in with it first — try it in a private window.`)) return
    setBusy('save'); setError('')
    try {
      await api.put(`${V1}/signin`, { enabled: f.enabled, issuer: f.issuer, client_id: f.client_id, client_secret: secret, name: f.name, auto_create: f.auto_create, password_login: f.password_login })
      toast('Sign-in methods saved'); reload()
    } catch (e) { setError(errMsg(e)) } finally { setBusy('') }
  }
  const reset = async () => {
    if (!confirm('Forget what is set here and use the server’s environment (.env) again?')) return
    try { await api.del(`${V1}/signin`); toast('Back to the server’s settings'); reload() } catch (e) { setError(errMsg(e)) }
  }

  const label = f.name || 'PocketID'
  return (
    <section className="set-section">
      <div className="set-section-head">
        <span className="set-app-ico"><Icon name="key" size={17} /></span>
        <div className="grow"><h3>How people sign in to Core</h3>
          <p>Passwords, an identity provider you already run (PocketID, Authentik, Keycloak…), or both — the sign-in page offers each one that is on. Pocket apps follow: they sign in through Core.</p></div>
      </div>
      <div className="set-section-body stack">
        {f.auth_mode !== 'local' && <div className="callout"><Icon name="info" size={15} /><span>Core runs with <code>AUTH_MODE={f.auth_mode}</code>: the reverse proxy signs people in, so these methods are not used.</span></div>}
        {data?.source === 'env' && <div className="callout"><Icon name="info" size={15} /><span>Right now this comes from the server’s environment (<code>SSO_*</code>, <code>PASSWORD_LOGIN</code>). Saving here takes over; Reset hands it back.</span></div>}

        <div className="signin-row">
          <div className="grow"><b>Passwords</b><div className="muted small">Email and password, as set in Users and Profile.</div></div>
          <Switch on={f.password_login} onChange={v => set({ password_login: v })} disabled={!f.enabled && f.password_login} title={!f.enabled ? 'Switch on an identity provider first' : undefined} />
        </div>

        <div className="signin-row">
          <div className="grow"><b>Identity provider{f.enabled && data?.enabled ? ` · ${label}` : ''}</b><div className="muted small">A “Continue with {label}” button. People are matched by their verified email.</div></div>
          <Switch on={f.enabled} onChange={v => set({ enabled: v, ...(v ? {} : { password_login: true }) })} />
        </div>

        <Collapse open={f.enabled}>
          <div className="stack signin-provider">
            <div className="two-col">
              <Field label="Button name" help="What the sign-in button says."><input value={f.name} onChange={e => set({ name: e.target.value })} placeholder="PocketID" /></Field>
              <Field label="Issuer" help="The provider’s address.">
                <div className="row"><input className="grow" value={f.issuer} onChange={e => set({ issuer: e.target.value })} placeholder="https://auth.example.com" spellCheck={false} />
                  <button type="button" className="btn" disabled={!f.issuer || busy === 'test'} onClick={test}>{busy === 'test' ? 'Testing…' : 'Test'}</button></div>
              </Field>
            </div>
            {tested && <div className="callout success"><Icon name="check" size={15} /><span>Core reached <code>{tested}</code> and read its configuration.</span></div>}
            <div className="two-col">
              <Field label="Client ID"><input value={f.client_id} onChange={e => set({ client_id: e.target.value })} spellCheck={false} autoComplete="off" /></Field>
              <Field label="Client secret" help={data?.has_secret ? 'Stored, encrypted. Leave empty to keep it.' : 'Shown once by the provider when you create the client.'}>
                <input type="password" value={secret} onChange={e => { setSecret(e.target.value); setError('') }} placeholder={data?.has_secret ? '•••••••• stored' : ''} autoComplete="new-password" /></Field>
            </div>
            <Field label="Callback URL" help="Paste this into the provider’s client."><Secret value={f.callback_url} /></Field>
            <div className="signin-row">
              <div className="grow"><b>New people from {label}</b><div className="muted small">Off: only people who already have a Core account can sign in. On: anyone {label} vouches for gets a member account on first sign-in.</div></div>
              <Switch on={f.auto_create} onChange={v => set({ auto_create: v })} />
            </div>

            <button type="button" className="btn ghost sm signin-guide-toggle" onClick={() => setGuide(g => !g)}><Icon name={guide ? 'chevron-down' : 'chevron-right'} size={14} />How to set this up in PocketID</button>
            <Collapse open={guide}>
              <ol className="signin-guide">
                <li>In PocketID open <b>Settings → OIDC Clients → Add OIDC client</b>.</li>
                <li><b>Name:</b> Pocket Core (anything you like).</li>
                <li><b>Callback URLs:</b> <code>{f.callback_url}</code></li>
                <li><b>Public client: off.</b> Core keeps a client secret, so it is a confidential client.</li>
                <li><b>PKCE: on.</b> Core always sends a PKCE challenge.</li>
                <li><b>Group restriction: off</b> — or pick the groups whose members may sign in. New clients in PocketID v2 start restricted with no groups, which refuses everyone with “You are not allowed to access this service”.</li>
                <li>Save, then copy the <b>Client ID</b> and the <b>Client secret</b> (shown only once) into the fields above.</li>
                <li><b>Issuer:</b> PocketID’s own address, e.g. <code>https://auth.example.com</code>. Press Test.</li>
                <li>For each person: their PocketID email must be the same as their Core email, and <b>Email verified</b> must be on in their PocketID profile.</li>
                <li>Save here, then try it in a private window before switching passwords off.</li>
              </ol>
              <p className="muted small">Authentik, Keycloak, Authelia and other OpenID Connect providers work the same way: a confidential client with this callback URL and the scopes <code>openid email profile</code>.</p>
            </Collapse>
          </div>
        </Collapse>

        {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
        <div className="row">
          <button type="button" className="btn primary" disabled={!dirty || !!busy} onClick={save}>{busy === 'save' ? 'Saving…' : 'Save'}</button>
          {dirty && <button type="button" className="btn ghost" onClick={() => { setF(base || f); setSecret(''); setError('') }}>Cancel</button>}
          <span className="grow" />
          {data?.source === 'settings' && <button type="button" className="btn ghost sm" onClick={reset}>Use the server’s settings</button>}
        </div>
      </div>
    </section>
  )
}
