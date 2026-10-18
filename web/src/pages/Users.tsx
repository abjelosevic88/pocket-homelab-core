import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { APIToken, User } from '../lib/types'
import { useApp } from '../lib/app-context'
import { Avatar, Card, Confirm, Field, Loading, Modal, PageHeader, Secret, useAsync, useToast } from '../components/ui'

export default function Users() {
  const { user: me } = useApp()
  const { toast } = useToast()
  const { data, loading, reload } = useAsync(() => api.get<User[]>(`${V1}/users`), [])
  const [edit, setEdit] = useState<Partial<User> & { password?: string } | null>(null)
  const [del, setDel] = useState<User | null>(null)
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
          <td className="right"><button className="btn sm" onClick={() => setEdit({ id: u.id, email: u.email, name: u.name, role: u.role, disabled: u.disabled })}>Edit</button> {u.id !== me?.id && <button className="btn sm ghost danger-text" onClick={() => setDel(u)}>✕</button>}</td>
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
