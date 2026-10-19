import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { APIToken, Credential, User, UserConnector } from '../lib/types'
import { useApp } from '../lib/app-context'
import { Avatar, Card, Confirm, Field, Loading, Modal, PageHeader, Secret, useAsync, useToast } from '../components/ui'
import { StatusBadge } from '../components/ConnectorPanel'

export default function Users() {
  const { user: me } = useApp()
  const { toast } = useToast()
  const { data, loading, reload } = useAsync(() => api.get<User[]>(`${V1}/users`), [])
  const [edit, setEdit] = useState<Partial<User> & { password?: string } | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const [creds, setCreds] = useState<User | null>(null)
  if (loading && !data) return <Loading />
  const list = data || []
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!edit) return
    const body = { ...edit }
    if (!body.password) delete body.password
    if (edit.id) await api.put(`${V1}/users/${edit.id}`, body); else await api.post(`${V1}/users`, body)
    setEdit(null); await reload(); toast('Saved')
  }
  return (
    <>
      <PageHeader title="Users" sub="One account per person. It signs in to Core, to every Pocket app over single sign-on, and owns its paired devices." actions={<button className="btn primary" onClick={() => setEdit({ role: 'member' })}>Add user</button>} />
      <Card flush><table className="table">
        <thead><tr><th>User</th><th>Role</th><th>Created</th><th></th></tr></thead>
        <tbody>{list.map(u => <tr key={u.id} className={u.disabled ? 'muted' : ''}>
          <td><span className="row"><Avatar name={u.name} email={u.email} /><span><b>{u.name || u.email}</b>{u.name && <div className="muted small">{u.email}</div>}</span>{u.disabled && <span className="badge warn">disabled</span>}</span></td>
          <td>{u.role}</td><td className="muted small" title={when(u.created_at)}>{ago(u.created_at)}</td>
          <td className="right" style={{ whiteSpace: 'nowrap' }}><button className="btn sm" onClick={() => setCreds(u)}>Credentials</button> <button className="btn sm" onClick={() => setEdit({ id: u.id, email: u.email, name: u.name, role: u.role, disabled: u.disabled })}>Edit</button> {u.id !== me?.id && <button className="btn sm ghost danger-text" onClick={() => setDel(u)}>✕</button>}</td>
        </tr>)}</tbody></table></Card>
      <Tokens />
      {edit && <Modal title={edit.id ? 'Edit user' : 'Add user'} onClose={() => setEdit(null)}>
        <form onSubmit={save} className="grid" style={{ gap: 12 }}>
          <Field label="Name"><input value={edit.name || ''} onChange={e => setEdit(v => ({ ...v, name: e.target.value }))} /></Field>
          <Field label="Email"><input type="email" required value={edit.email || ''} onChange={e => setEdit(v => ({ ...v, email: e.target.value }))} /></Field>
          <Field label="Password" help={edit.id ? 'Leave empty to keep' : 'At least 8 characters'}><input type="password" value={edit.password || ''} onChange={e => setEdit(v => ({ ...v, password: e.target.value }))} autoComplete="new-password" minLength={edit.id ? 0 : 8} required={!edit.id} /></Field>
          <div className="two-col tight">
            <Field label="Role"><select value={edit.role || 'member'} onChange={e => setEdit(v => ({ ...v, role: e.target.value as User['role'] }))} disabled={edit.id === me?.id}><option value="member">member</option><option value="admin">admin</option></select></Field>
            {edit.id && edit.id !== me?.id && <Field label="Disabled"><select value={edit.disabled ? '1' : '0'} onChange={e => setEdit(v => ({ ...v, disabled: e.target.value === '1' }))}><option value="0">no</option><option value="1">yes</option></select></Field>}
          </div>
          <div className="form-actions"><button type="button" className="btn" onClick={() => setEdit(null)}>Cancel</button><button className="btn primary">Save</button></div>
        </form></Modal>}
      {creds && <CredentialsModal user={creds} onClose={() => setCreds(null)} />}
      {del && <Confirm title="Delete user" message={<>Delete <b>{del.name || del.email}</b>, their devices and tokens?</>} onConfirm={async () => { await api.del(`${V1}/users/${del.id}`); setDel(null); reload() }} onCancel={() => setDel(null)} />}
    </>
  )
}

export function Tokens() {
  const { toast } = useToast()
  const { data, reload } = useAsync(() => api.get<APIToken[]>(`${V1}/tokens`), [])
  const [name, setName] = useState('')
  const [secret, setSecret] = useState('')
  const create = async (e: React.FormEvent) => { e.preventDefault(); const r = await api.post<{ secret: string }>(`${V1}/tokens`, { name: name || 'token' }); setSecret(r.secret); setName(''); reload() }
  return (
    <Card title="My API tokens" className="mt">
      <p className="muted small">For scripts, Raycast and Homepage widgets. They act as you against Core and, through introspection, against every Pocket app.</p>
      <form onSubmit={create} className="row mt-sm"><input placeholder="Name (e.g. Raycast)" value={name} onChange={e => setName(e.target.value)} style={{ maxWidth: 260 }} /><button className="btn">Create</button></form>
      {secret && <div className="mt-sm"><div className="callout success mb-sm">Copy it now — it is shown once.</div><Secret value={secret} /></div>}
      {(data || []).length > 0 && <table className="table compact mt"><tbody>{(data || []).map(t => <tr key={t.id}><td><b>{t.name}</b></td><td className="mono small">{t.prefix}…</td><td className="muted small">used {ago(t.last_used_at)}</td><td className="right"><button className="btn sm ghost danger-text" onClick={async () => { await api.del(`${V1}/tokens/${t.id}`); reload(); toast('Revoked') }}>✕</button></td></tr>)}</tbody></table>}
    </Card>
  )
}

/** Per-person overrides: this user's own Jellyfin login, Navidrome account, etc. */
export function CredentialsModal({ user, onClose }: { user: User; onClose: () => void }) {
  const { toast } = useToast()
  const { data, loading, reload } = useAsync(() => api.get<Credential[]>(`${V1}/users/${user.id}/credentials`), [user.id])
  const { user: me } = useApp()
  const { data: conns } = useAsync(() => me?.role === 'admin' ? api.get<UserConnector[]>(`${V1}/users/${user.id}/connectors`).catch(() => [] as UserConnector[]) : Promise.resolve([] as UserConnector[]), [user.id, me?.role])
  const [open, setOpen] = useState<number | null>(null)
  const [form, setForm] = useState<{ url: string; fields: Record<string, string> }>({ url: '', fields: {} })
  const start = (c: Credential) => { setOpen(c.service.id); setForm({ url: c.url, fields: { ...c.fields } }) }
  const save = async (c: Credential) => { await api.put(`${V1}/users/${user.id}/credentials/${c.service.id}`, form); setOpen(null); await reload(); toast('Saved') }
  const clear = async (c: Credential) => { await api.del(`${V1}/users/${user.id}/credentials/${c.service.id}`); setOpen(null); await reload(); toast('Cleared') }
  const list = (data || []).filter(c => c.visible || c.set)
  return (
    <Modal title={`${user.name || user.email} · credentials`} onClose={onClose} size="lg">
      <p className="muted small">Services this person receives, with their own login where it differs from the box-wide one. Leave a service alone to hand out the shared credential.</p>
      {loading ? <Loading /> : list.length === 0 ? <p className="muted">Nothing is visible to this user yet. Set a service's audience under Services.</p> :
        <table className="table compact mt"><tbody>{list.map(c => <tr key={c.service.id}>
          <td><b>{c.service.name || c.service.catalog.name}</b>{c.set && <span className="badge cat" style={{ marginLeft: 6 }} title={c.source === 'connector' ? 'Made by Core through the connector' : 'Set by hand'}>{c.source === 'connector' ? 'own · by Core' : 'own'}</span>}{!c.visible && <span className="badge warn" style={{ marginLeft: 6 }}>hidden</span>}
            {open === c.service.id && <div className="grid mt-sm" style={{ gap: 8 }}>
              <Field label={`${c.service.catalog.prefix}_${c.service.catalog.url_key}`} help="Empty = the box-wide URL"><input value={form.url} onChange={e => setForm(f => ({ ...f, url: e.target.value }))} /></Field>
              {c.service.catalog.fields.map(f => <Field key={f.key} label={`${c.service.catalog.prefix}_${f.key}`}><input type={f.secret ? 'password' : 'text'} autoComplete="off" value={form.fields[f.key] || ''} onChange={e => setForm(v => ({ ...v, fields: { ...v.fields, [f.key]: e.target.value } }))} placeholder="empty = box-wide value" /></Field>)}
              <div className="form-actions"><button type="button" className="btn" onClick={() => setOpen(null)}>Cancel</button>{c.set && <button type="button" className="btn danger" onClick={() => clear(c)}>Clear</button>}<button type="button" className="btn primary" onClick={() => save(c)}>Save</button></div>
            </div>}
          </td>
          <td className="right">{open !== c.service.id && <button className="btn sm" onClick={() => start(c)}>{c.set ? 'Edit' : 'Set own'}</button>}</td>
        </tr>)}</tbody></table>}
      {(conns || []).length > 0 && <>
        <div className="mt"><b>Connected services</b></div>
        <p className="muted small">Accounts Core made or linked for this person through a service's connector (Services → Connector).</p>
        <table className="table compact mt-sm"><tbody>{(conns || []).map(c => <tr key={c.service_id} className={c.in_audience ? '' : 'muted'}>
          <td><b>{c.service_name}</b>{!c.enabled && <span className="badge" style={{ marginLeft: 6 }}>connector off</span>}{!c.in_audience && <span className="badge warn" style={{ marginLeft: 6 }}>outside audience</span>}</td>
          <td><StatusBadge status={c.status} />{c.error && <div className="small" style={{ color: 'var(--danger)', maxWidth: 380 }}>{c.error}</div>}</td>
          <td className="small muted" title={when(c.updated_at)}>{c.remote_id && <span className="mono">{c.remote_id}</span>}{c.updated_at && <div>{ago(c.updated_at)}</div>}</td>
        </tr>)}</tbody></table>
      </>}
    </Modal>
  )
}
