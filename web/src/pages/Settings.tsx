import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { uptime, when } from '../lib/format'
import type { OIDCClient, Settings as S, SystemInfo } from '../lib/types'
import { useApp } from '../lib/app-context'
import { Card, Confirm, Field, Loading, Modal, PageHeader, Secret, useAsync, useToast } from '../components/ui'
import { CredentialsModal, Tokens } from './Users'
import { Select } from '../components/Select'
import { MyLinks } from './Links'

export default function Settings() {
  const { user, refresh } = useApp()
  const { toast } = useToast()
  const admin = user?.role === 'admin'
  const { data: st, reload } = useAsync(() => admin ? api.get<S>(`${V1}/settings`) : Promise.resolve(null), [admin])
  const { data: sys } = useAsync(() => api.get<SystemInfo>(`${V1}/system`), [])
  const [form, setForm] = useState<{ title?: string; box_name?: string }>({})
  const [rot, setRot] = useState(false)
  const [me, setMe] = useState({ name: user?.name || '', password: '', current_password: '' })
  const saveMe = async (e: React.FormEvent) => { e.preventDefault(); await api.put(`${V1}/auth/me`, me); setMe(m => ({ ...m, password: '', current_password: '' })); await refresh(); toast('Profile saved') }
  if (admin && !st) return <Loading />
  return (
    <>
      <PageHeader title="Settings" />
      <div className="two-col">
        <div className="grid" style={{ gap: 16 }}>
          {admin && st && <Card title="Box">
            <form onSubmit={async e => { e.preventDefault(); await api.put(`${V1}/settings`, form); await reload(); await refresh(); toast('Saved') }} className="grid" style={{ gap: 12 }}>
              <Field label="Box name" help="Shown to paired devices and on the login page"><input defaultValue={st.box_name} onChange={e => setForm(f => ({ ...f, box_name: e.target.value }))} /></Field>
              <Field label="Title"><input defaultValue={st.title} onChange={e => setForm(f => ({ ...f, title: e.target.value }))} /></Field>
              <div className="form-actions"><button className="btn primary">Save</button></div>
            </form>
          </Card>}
          {admin && st && <Card title="App registration key">
            <p className="muted small">Apps present this once to join the box; afterwards they use their own token. Rotating it does not affect apps already registered.</p>
            <Secret value={st.registration_key} />
            <div className="form-actions mt-sm"><button className="btn" onClick={() => setRot(true)}>Rotate</button></div>
          </Card>}
          <Card title="Profile">
            <form onSubmit={saveMe} className="grid" style={{ gap: 12 }}>
              <Field label="Name"><input value={me.name} onChange={e => setMe(m => ({ ...m, name: e.target.value }))} /></Field>
              <div className="two-col tight"><Field label="Current password"><input type="password" value={me.current_password} onChange={e => setMe(m => ({ ...m, current_password: e.target.value }))} autoComplete="current-password" /></Field><Field label="New password"><input type="password" value={me.password} onChange={e => setMe(m => ({ ...m, password: e.target.value }))} autoComplete="new-password" /></Field></div>
              <div className="form-actions"><button className="btn primary">Save</button></div>
            </form>
          </Card>
          <MyLinks />
          {!admin && user && <MyCredentials user={user} />}
          {!admin && <Tokens />}
        </div>
        <div className="grid" style={{ gap: 16 }}>
          {admin && <OIDCClients />}
          {sys && <Card title="System">
            <dl className="kv">
              <dt>Version</dt><dd>{sys.version} · {sys.go} · {sys.os}</dd>
              <dt>Uptime</dt><dd>{uptime(sys.uptime_s)} (since {when(sys.started_at)})</dd>
              <dt>Base URL</dt><dd className="mono">{sys.base_url}</dd>
              <dt>OIDC issuer</dt><dd className="mono">{sys.issuer}<div className="muted small">{sys.issuer}/.well-known/openid-configuration</div></dd>
              <dt>Auth mode</dt><dd>{sys.auth_mode}</dd>
              <dt>Data</dt><dd className="mono">{sys.data_dir}</dd>
              <dt>Health poll</dt><dd>every {sys.health_interval_s}s · events kept {Math.round(sys.event_retention_h / 24)}d</dd>
              <dt>Rows</dt><dd className="small">{Object.entries(sys.counts).map(([k, v]) => `${k} ${v}`).join(' · ')}</dd>
            </dl>
            {admin && <div className="form-actions mt"><a className="btn" href={`${V1}/backup`}>Download database backup</a></div>}
          </Card>}
        </div>
      </div>
      {rot && <Confirm title="Rotate registration key" message="Apps that have not registered yet will need the new key. Registered apps keep working." onConfirm={async () => { await api.post(`${V1}/settings/registration-key`); setRot(false); reload(); toast('Rotated') }} onCancel={() => setRot(false)} danger={false} />}
    </>
  )
}

function OIDCClients() {
  const { toast } = useToast()
  const { data, reload } = useAsync(() => api.get<OIDCClient[]>(`${V1}/oidc/clients`), [])
  const [edit, setEdit] = useState<{ id?: number; client_id?: string; name: string; redirect_uris: string; public: boolean } | null>(null)
  const [secret, setSecret] = useState<{ id: string; secret: string } | null>(null)
  const [del, setDel] = useState<OIDCClient | null>(null)
  const issuer = window.location.origin
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!edit) return
    const body = { client_id: edit.client_id, name: edit.name, redirect_uris: edit.redirect_uris.split('\n').map(s => s.trim()).filter(Boolean), public: edit.public }
    const r = edit.id ? await api.put<{ client: OIDCClient; client_secret: string }>(`${V1}/oidc/clients/${edit.id}`, body) : await api.post<{ client: OIDCClient; client_secret: string }>(`${V1}/oidc/clients`, body)
    setEdit(null); reload()
    if (r.client_secret) setSecret({ id: r.client.client_id, secret: r.client_secret }); else toast('Saved')
  }
  const rotate = async (c: OIDCClient) => { const r = await api.put<{ client: OIDCClient; client_secret: string }>(`${V1}/oidc/clients/${c.id}`, { rotate_secret: true }); setSecret({ id: c.client_id, secret: r.client_secret }) }
  return (
    <Card title="Single sign-on (OpenID Connect)" actions={<button className="btn sm primary" onClick={() => setEdit({ name: '', redirect_uris: '', public: false })}>Add client</button>}>
      <p className="muted small">Core is an OpenID provider. Point Immich, Nextcloud, Paperless, Audiobookshelf, Kavita, Jellyfin (SSO plugin) or any Pocket app at <span className="inline-code">{issuer}</span> and everyone signs in with their Core account. Pocket apps that ask in their manifest get a client automatically.</p>
      {(data || []).length > 0 && <div className="client-list mt">{(data || []).map(c => (
        <div key={c.id} className="client-row">
          <div className="client-head">
            <div className="grow">
              <div className="client-name"><b>{c.name}</b>{c.app_id ? <span className="badge cat">app</span> : null}{!c.has_secret && <span className="badge">PKCE</span>}</div>
              <div className="mono small muted">{c.client_id}</div>
            </div>
            <div className="client-actions">{c.has_secret && <button className="btn sm" onClick={() => rotate(c)}>Secret</button>}<button className="btn sm" onClick={() => setEdit({ id: c.id, client_id: c.client_id, name: c.name, redirect_uris: c.redirect_uris.join('\n'), public: !c.has_secret })}>Edit</button><button className="btn sm ghost danger-text" onClick={() => setDel(c)} aria-label={`Delete ${c.name}`}>✕</button></div>
          </div>
          <div className="client-redirects mono small">{c.redirect_uris.map(u => <div key={u}>{u}</div>)}</div>
        </div>))}</div>}
      {edit && <Modal title={edit.id ? 'Edit client' : 'Add OIDC client'} onClose={() => setEdit(null)}>
        <form onSubmit={save} className="grid" style={{ gap: 12 }}>
          <Field label="Name"><input required value={edit.name} onChange={e => setEdit(v => v && ({ ...v, name: e.target.value }))} placeholder="Immich" /></Field>
          <Field label="Client id" help="Optional; generated when empty"><input value={edit.client_id || ''} disabled={!!edit.id} onChange={e => setEdit(v => v && ({ ...v, client_id: e.target.value }))} /></Field>
          <Field label="Redirect URIs" help="One per line, exact match"><textarea required value={edit.redirect_uris} onChange={e => setEdit(v => v && ({ ...v, redirect_uris: e.target.value }))} placeholder={'https://photos.example.com/auth/login\napp.immich:///oauth-callback'} /></Field>
          {!edit.id && <Field label="Client type"><Select ariaLabel="Client type" value={edit.public ? 'public' : 'confidential'} onChange={v => setEdit(x => x && ({ ...x, public: v === 'public' }))} options={[{ value: 'confidential', label: 'Confidential', hint: 'client secret' }, { value: 'public', label: 'Public', hint: 'PKCE, no secret' }]} /></Field>}
          <div className="form-actions"><button type="button" className="btn" onClick={() => setEdit(null)}>Cancel</button><button className="btn primary">Save</button></div>
        </form></Modal>}
      {secret && <Modal title="Client credentials" onClose={() => setSecret(null)}>
        <div className="callout success mb">The secret is shown once. Configure the relying party with:</div>
        <dl className="kv"><dt>Issuer</dt><dd className="mono">{issuer}</dd><dt>Discovery</dt><dd className="mono">{issuer}/.well-known/openid-configuration</dd><dt>Client id</dt><dd className="mono">{secret.id}</dd><dt>Scopes</dt><dd className="mono">openid profile email groups</dd></dl>
        <div className="mt-sm"><Secret value={secret.secret} /></div>
        <div className="form-actions mt"><button className="btn primary" onClick={() => setSecret(null)}>Done</button></div></Modal>}
      {del && <Confirm title="Delete client" message={<>Delete <b>{del.name}</b>? Logins through it stop immediately.</>} onConfirm={async () => { await api.del(`${V1}/oidc/clients/${del.id}`); setDel(null); reload() }} onCancel={() => setDel(null)} />}
    </Card>
  )
}

function MyCredentials({ user }: { user: NonNullable<ReturnType<typeof useApp>['user']> }) {
  const [open, setOpen] = useState(false)
  return (
    <Card title="My logins">
      <p className="muted small">Your phone receives the household's credentials unless you set your own here, for example your Jellyfin user.</p>
      <div className="form-actions"><button className="btn" onClick={() => setOpen(true)}>Edit my logins</button></div>
      {open && <CredentialsModal user={user} onClose={() => setOpen(false)} />}
    </Card>
  )
}
