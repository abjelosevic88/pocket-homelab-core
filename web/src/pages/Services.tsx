import { useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { CatalogEntry, ConnectorListItem, Service, User } from '../lib/types'
import { useApp } from '../lib/app-context'
import { Card, Confirm, Empty, Field, Loading, Modal, PageHeader, Switch, useAsync, useToast } from '../components/ui'
import { ConnectorModal } from '../components/ConnectorPanel'

export default function Services() {
  const { toast } = useToast()
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const { data, loading, reload } = useAsync(() => api.get<Service[]>(`${V1}/services`), [])
  const { data: catalog } = useAsync(() => api.get<CatalogEntry[]>(`${V1}/catalog`), [])
  const { data: conns, reload: reloadConns } = useAsync(() => admin ? api.get<ConnectorListItem[]>(`${V1}/connectors`).catch(() => [] as ConnectorListItem[]) : Promise.resolve([] as ConnectorListItem[]), [admin])
  const [conn, setConn] = useState<Service | null>(null)
  const connFor = (s: Service) => (conns || []).find(c => c.type === s.type && c.service_id === s.id)
  const [adding, setAdding] = useState(false)
  const [edit, setEdit] = useState<Service | null>(null)
  const [del, setDel] = useState<Service | null>(null)
  const [q, setQ] = useState('')
  const list = data || []
  const groups = useMemo(() => {
    const g = new Map<string, Service[]>()
    for (const s of list) { if (q && !(s.name + s.type + s.url).toLowerCase().includes(q.toLowerCase())) continue; const k = s.catalog.group; g.set(k, [...(g.get(k) || []), s]) }
    return [...g.entries()]
  }, [list, q])
  const toggle = async (s: Service, on: boolean) => { await api.put(`${V1}/services/${s.id}`, { enabled: on }); await reload() }
  const test = async (s: Service) => { const r = await api.post<{ ok: boolean; status?: number; ms?: number; error?: string }>(`${V1}/services/${s.id}/test`); toast(r.ok ? `Reachable · HTTP ${r.status} in ${r.ms} ms` : `Unreachable: ${r.error || r.status}`, r.ok ? 'ok' : 'err') }
  const remove = async () => { if (del) { await api.del(`${V1}/services/${del.id}`); setDel(null); await reload() } }
  if (loading && !data) return <Loading />
  return (
    <>
      <PageHeader title="Services" sub="Everything a paired device receives: registered Pocket apps plus the services you add with their credentials."
        actions={<><a className="btn" href={`${V1}/pair/profile?format=env`} download>Download env</a>{admin && <button className="btn primary" onClick={() => setAdding(true)}>Add service</button>}</>} />
      <div className="toolbar"><div className="search"><span className="ico">⌕</span><input placeholder="Filter…" value={q} onChange={e => setQ(e.target.value)} /></div><span className="muted small">{list.filter(s => s.enabled).length} of {list.length} in the profile</span></div>
      {list.length === 0 ? <Card><Empty title="Nothing yet">Registered Pocket apps appear here automatically. Add Jellyfin, Immich, Nextcloud… with their API keys and every paired phone gets them.</Empty></Card> :
        groups.map(([g, items]) => <div key={g}>
          <div className="group-title">{g}</div>
          <Card flush>
            <table className="table">
              <tbody>{items.map(s => <tr key={s.id} className={s.enabled ? '' : 'muted'}>
                <td style={{ width: 44 }}><Switch on={s.enabled} onChange={v => toggle(s, v)} title={s.enabled ? 'In the profile' : 'Hidden from devices'} /></td>
                <td><b>{s.name || s.catalog.name}</b>{s.app_id ? <span className="badge cat" style={{ marginLeft: 6 }}>app</span> : null}{s.audience === 'admins' && <span className="badge warn" style={{ marginLeft: 6 }}>admins</span>}{s.audience === 'users' && <span className="badge" style={{ marginLeft: 6 }}>{s.audience_users.length} people</span>}{connFor(s)?.connector?.enabled && <span className={`badge ${connFor(s)?.connector?.last_error ? 'err' : 'ok'}`} style={{ marginLeft: 6 }} title={connFor(s)?.connector?.last_error || 'Connector on'}>connector</span>}<div className="muted small mono">{s.catalog.prefix}_{s.catalog.url_key}{s.catalog.fields.length ? ' · ' + s.catalog.fields.map(f => s.catalog.prefix + '_' + f.key).join(' · ') : ''}</div></td>
                <td className="mono small">{s.public_url || s.url || <span className="muted">no url</span>}{s.public_url && s.url && s.public_url !== s.url && <div className="muted">internal {s.url}</div>}</td>
                <td className="small">{Object.entries(s.fields).filter(([, v]) => v).map(([k, v]) => <div key={k}><span className="muted">{k}</span> {v}</div>)}</td>
                <td className="right" style={{ whiteSpace: 'nowrap' }}><button className="btn sm" onClick={() => test(s)}>Test</button> {admin && connFor(s) && <button className="btn sm" onClick={() => setConn(s)}>Connector</button>} {admin && <button className="btn sm" onClick={() => setEdit(s)}>Edit</button>} {admin && !s.app_id && <button className="btn sm ghost danger-text" onClick={() => setDel(s)}>✕</button>}</td>
              </tr>)}</tbody>
            </table>
          </Card>
        </div>)}
      {adding && catalog && <AddService catalog={catalog.filter(c => !list.some(s => s.type === c.type))} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); reload() }} />}
      {conn && <ConnectorModal service={conn} onClose={() => { setConn(null); reloadConns() }} />}
      {edit && <EditService s={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
      {del && <Confirm title="Remove service" message={<>Remove <b>{del.name}</b>? Paired devices lose it on their next profile refresh.</>} onConfirm={remove} onCancel={() => setDel(null)} />}
    </>
  )
}

function ServiceForm({ entry, initial, onSubmit, onClose }: { entry: CatalogEntry; initial?: Service; onSubmit: (body: Record<string, unknown>) => Promise<void>; onClose: () => void }) {
  const [name, setName] = useState(initial?.name || entry.name)
  const [url, setUrl] = useState(initial?.url || '')
  const [pub, setPub] = useState(initial?.public_url || '')
  const [fields, setFields] = useState<Record<string, string>>(initial?.fields || {})
  const [notes, setNotes] = useState(initial?.notes || '')
  const [audience, setAudience] = useState<Service['audience']>(initial?.audience || 'all')
  const [audUsers, setAudUsers] = useState<number[]>(initial?.audience_users || [])
  const { data: users } = useAsync(() => api.get<User[]>(`${V1}/users`).catch(() => [] as User[]), [])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const isApp = !!initial?.app_id
  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setErr('')
    try { await onSubmit({ type: entry.type, name, url, public_url: pub, fields, notes, audience, audience_users: audUsers }) } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }
  return (
    <form onSubmit={submit} className="grid" style={{ gap: 12 }}>
      <div className="two-col tight"><Field label="Name"><input value={name} onChange={e => setName(e.target.value)} /></Field><Field label="Type"><input value={entry.name} disabled /></Field></div>
      <Field label={`${entry.prefix}_${entry.url_key}`} help={isApp ? 'Managed by the app’s registration' : 'Base URL as the phone should reach it; or set an internal URL here and the public one below'}><input value={url} onChange={e => setUrl(e.target.value)} placeholder="https://service.example.com" disabled={isApp} /></Field>
      <Field label="Public URL" help="Optional. Used in the profile instead of the URL above"><input value={pub} onChange={e => setPub(e.target.value)} disabled={isApp} /></Field>
      {entry.fields.map(f => <Field key={f.key} label={`${entry.prefix}_${f.key}`} help={f.help || (f.secret ? 'Stored encrypted, masked here' : undefined)}>
        <input type={f.secret ? 'password' : 'text'} value={fields[f.key] || ''} onChange={e => setFields(v => ({ ...v, [f.key]: e.target.value }))} autoComplete="off" placeholder={entry.pocket && f.key === 'API_KEY' ? 'leave empty = device token' : ''} />
      </Field>)}
      <Field label="Who receives it" help="What a paired phone gets in its profile. Admin keys (NAS, DNS, the arr apps) should not go to every household member.">
        <select value={audience} onChange={e => setAudience(e.target.value as Service['audience'])}>
          <option value="all">Everyone on the box</option>
          <option value="admins">Admins only</option>
          <option value="users">Specific people</option>
        </select>
      </Field>
      {audience === 'users' && <Field label="People" group><div className="chips">{(users || []).map(u => <button type="button" key={u.id} className={`chip ${audUsers.includes(u.id) ? 'active' : ''}`} onClick={() => setAudUsers(a => a.includes(u.id) ? a.filter(x => x !== u.id) : [...a, u.id])}>{u.name || u.email}</button>)}</div></Field>}
      <Field label="Notes"><input value={notes} onChange={e => setNotes(e.target.value)} /></Field>
      {err && <div className="callout danger">{err}</div>}
      <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>Save</button></div>
    </form>
  )
}

function AddService({ catalog, onClose, onSaved }: { catalog: CatalogEntry[]; onClose: () => void; onSaved: () => void }) {
  const [entry, setEntry] = useState<CatalogEntry | null>(null)
  const [q, setQ] = useState('')
  const [custom, setCustom] = useState('')
  const items = catalog.filter(c => !c.pocket || c.type === 'core' ? !c.pocket : false).filter(c => !q || (c.name + c.type + c.group).toLowerCase().includes(q.toLowerCase()))
  if (!entry) return (
    <Modal title="Add a service" onClose={onClose} size="lg">
      <div className="toolbar"><div className="search"><span className="ico">⌕</span><input autoFocus placeholder="Jellyfin, Immich, Paperless…" value={q} onChange={e => setQ(e.target.value)} /></div></div>
      <div className="sel-list">{items.map(c => <button key={c.type} type="button" className="btn" onClick={() => setEntry(c)}><span className="grow">{c.name}</span><span className="muted small">{c.group}</span></button>)}</div>
      <details className="mt"><summary className="muted small">Something else</summary>
        <div className="row mt-sm"><input placeholder="prefix, e.g. GRAFANA" value={custom} onChange={e => setCustom(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ''))} /><button type="button" className="btn" disabled={!custom} onClick={() => setEntry({ type: custom.toLowerCase(), name: custom, prefix: custom, group: 'Custom', url_key: 'URL', pocket: false, fields: [{ key: 'API_KEY', label: 'API key', secret: true }] })}>Use</button></div>
        <p className="muted small mt-sm">Custom entries land in the profile as <span className="inline-code">PREFIX_URL</span> and <span className="inline-code">PREFIX_API_KEY</span>; the client ignores keys it does not know.</p></details>
    </Modal>)
  return <Modal title={`Add ${entry.name}`} onClose={onClose}><ServiceForm entry={entry} onClose={onClose} onSubmit={async b => { await api.post(`${V1}/services`, b); onSaved() }} /></Modal>
}

function EditService({ s, onClose, onSaved }: { s: Service; onClose: () => void; onSaved: () => void }) {
  return <Modal title={`Edit ${s.name}`} onClose={onClose}><ServiceForm entry={s.catalog} initial={s} onClose={onClose} onSubmit={async b => { await api.put(`${V1}/services/${s.id}`, b); onSaved() }} /></Modal>
}
