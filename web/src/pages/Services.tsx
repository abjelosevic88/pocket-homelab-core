import { useEffect, useMemo, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { CatalogEntry, ConnectorCaps, ConnectorListItem, Service, User } from '../lib/types'
import { useApp } from '../lib/app-context'
import { Card, Confirm, Empty, Field, Loading, Modal, PageHeader, Segmented, Switch, useAsync } from '../components/ui'
import { ConnectorModal } from '../components/ConnectorPanel'
import { Select } from '../components/Select'
import { bundledIcon, iconCandidates } from '../lib/icons'

type TestResult = { ok: boolean; status?: number; ms?: number; error?: string; busy?: boolean }

// Missing: a Pocket app that hasn't registered, or a service Core has a
// connector for that nobody added yet. Shown greyed out so a fresh box isn't
// an empty page; nothing is stored and devices never see it.
type Missing = { entry: CatalogEntry; kind: 'pocket' | 'connector'; caps?: ConnectorCaps }

export default function Services() {
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const { data, loading, reload } = useAsync(() => api.get<Service[]>(`${V1}/services`), [])
  const { data: catalog } = useAsync(() => api.get<CatalogEntry[]>(`${V1}/catalog`), [])
  const { data: conns, reload: reloadConns } = useAsync(() => admin ? api.get<ConnectorListItem[]>(`${V1}/connectors`).catch(() => [] as ConnectorListItem[]) : Promise.resolve([] as ConnectorListItem[]), [admin])
  const [conn, setConn] = useState<Service | null>(null)
  const connFor = (s: Service) => (conns || []).find(c => c.type === s.type && c.service_id === s.id)
  const [adding, setAdding] = useState<boolean | CatalogEntry>(false)
  const [howTo, setHowTo] = useState<CatalogEntry | null>(null)
  const [edit, setEdit] = useState<Service | null>(null)
  const [del, setDel] = useState<Service | null>(null)
  const [q, setQ] = useState('')
  const [show, setShow] = useState<'all' | 'on' | 'off' | 'conn'>('all')
  const [tests, setTests] = useState<Record<number, TestResult>>({})
  const list = data || []
  const pocketBases = list.filter(s => s.catalog.pocket || s.app_id).map(s => s.public_url || s.url)
  const missing = useMemo<Missing[]>(() => {
    if (!admin || !catalog) return []
    const have = new Set(list.map(s => s.type))
    const out: Missing[] = []
    for (const c of catalog) {
      if (have.has(c.type) || c.type === 'core') continue
      const k = (conns || []).find(x => x.type === c.type)
      if (c.pocket) out.push({ entry: c, kind: 'pocket' })
      else if (k) out.push({ entry: c, kind: 'connector', caps: k.capabilities })
    }
    return out
  }, [admin, catalog, list, conns])
  const groups = useMemo(() => {
    const g = new Map<string, { svcs: Service[]; missing: Missing[] }>()
    const at = (k: string) => { if (!g.has(k)) g.set(k, { svcs: [], missing: [] }); return g.get(k)! }
    const match = (text: string) => !q || text.toLowerCase().includes(q.toLowerCase())
    for (const s of list) {
      if (!match(s.name + s.type + s.url + s.public_url)) continue
      if ((show === 'on' && !s.enabled) || (show === 'off' && s.enabled) || (show === 'conn' && !connFor(s))) continue
      at(s.catalog.group).svcs.push(s)
    }
    for (const m of missing) {
      if (show === 'on' || show === 'off' || (show === 'conn' && m.kind !== 'connector') || !match(m.entry.name + m.entry.type)) continue
      at(m.entry.group).missing.push(m)
    }
    return [...g.entries()]
  }, [list, missing, q, show, conns]) // eslint-disable-line react-hooks/exhaustive-deps
  const toggle = async (s: Service, on: boolean) => { await api.put(`${V1}/services/${s.id}`, { enabled: on }); await reload() }
  const test = async (s: Service) => {
    setTests(t => ({ ...t, [s.id]: { ok: false, busy: true } }))
    try { const r = await api.post<TestResult>(`${V1}/services/${s.id}/test`); setTests(t => ({ ...t, [s.id]: r })) }
    catch (e) { setTests(t => ({ ...t, [s.id]: { ok: false, error: (e as Error).message } })) }
  }
  const testAll = async () => { for (const s of list.filter(x => x.enabled)) void test(s) }
  const remove = async () => { if (del) { await api.del(`${V1}/services/${del.id}`); setDel(null); await reload() } }
  if (loading && !data) return <Loading />
  return (
    <>
      <PageHeader title="Services" sub="Everything a paired device receives: registered Pocket apps plus the services you add with their credentials."
        actions={<><a className="btn" href={`${V1}/pair/profile?format=env`} download>Download env</a>{admin && <button className="btn primary" onClick={() => setAdding(true)}>Add service</button>}</>} />
      <div className="toolbar">
        <div className="search"><span className="ico">⌕</span><input placeholder="Search services…" value={q} onChange={e => setQ(e.target.value)} /></div>
        <Segmented size="sm" value={show} onChange={setShow} options={[{ value: 'all', label: `All ${list.length + missing.length}` }, { value: 'on', label: `In profile ${list.filter(s => s.enabled).length}` }, { value: 'off', label: `Hidden ${list.filter(s => !s.enabled).length}` }, ...(admin && (conns || []).length ? [{ value: 'conn' as const, label: `Connectors ${list.filter(s => connFor(s)).length + missing.filter(m => m.kind === 'connector').length}` }] : [])]} />
        <button className="btn sm" onClick={testAll} title="Check every service in the profile">Test all</button>
      </div>
      {admin && (conns || []).length > 0 && <ConnectorsOverview conns={conns || []} services={list} pocketBases={pocketBases} onOpen={setConn} onAdd={type => { const e = catalog?.find(c => c.type === type); if (e) setAdding(e) }} />}
      {list.length === 0 && missing.length === 0 ? <Card><Empty title="Nothing yet">Registered Pocket apps appear here automatically. Add Jellyfin, Immich, Nextcloud… with their API keys and every paired phone gets them.</Empty></Card> :
        groups.length === 0 ? <Card><Empty title="No match">Nothing matches this search.</Empty></Card> :
        groups.map(([g, { svcs: items, missing: absent }]) => <section key={g} className="svc-group">
          <div className="svc-group-head"><span>{g}</span><span className="count">{items.length}</span>{absent.length > 0 && <span className="count muted">+{absent.length} not set up</span>}</div>
          <div className="svc-list">{items.map(s => {
            const c = connFor(s)?.connector
            const href = s.public_url || s.url
            const keys = [`${s.catalog.prefix}_${s.catalog.url_key}`, ...s.catalog.fields.map(f => `${s.catalog.prefix}_${f.key}`)]
            const secrets = s.catalog.fields.filter(f => f.secret)
            const hasSecret = secrets.some(f => s.fields[f.key])
            const t = tests[s.id]
            return (
              <div key={s.id} className={`svc-row ${s.enabled ? '' : 'off'}`}>
                <ServiceIcon s={s} pocketBases={pocketBases} />
                <div className="svc-main">
                  <div className="svc-name">
                    <b>{s.name || s.catalog.name}</b>
                    {s.app_id ? <span className="badge cat">Pocket app</span> : null}
                    {s.audience === 'admins' && <span className="badge warn">Admins</span>}
                    {s.audience === 'users' && <span className="badge">{s.audience_users.length} people</span>}
                  </div>
                  <div className="svc-sub">
                    {href ? <a href={href} target="_blank" rel="noreferrer" title={s.public_url && s.url && s.public_url !== s.url ? `internal ${s.url}` : href}>{href.replace(/^https?:\/\//, '').replace(/\/$/, '')}</a> : <span>no address</span>}
                    <span className="env" title={keys.join('\n')}>{s.catalog.prefix}_*</span>
                  </div>
                  {admin && connFor(s) && <ConnectorLine c={c} onOpen={() => setConn(s)} />}
                </div>
                <div className="svc-foot">
                  <div className="svc-status">
                    {s.catalog.pocket ? <span className="pill">Device token</span>
                      : secrets.length > 0 ? (hasSecret ? <span className="pill ok">Key saved</span> : <span className="pill warn">No key</span>)
                      : null}
                    {t && <span className={`pill ${t.busy ? '' : t.ok ? 'ok' : 'err'}`} title={t.error || ''}>{t.busy ? 'Testing…' : t.ok ? `${t.status} · ${t.ms} ms` : 'Unreachable'}</span>}
                  </div>
                  <div className="svc-actions">
                    {admin && <button className="btn sm ghost" onClick={() => setEdit(s)}>Edit</button>}
                    <RowMenu items={[
                      { label: t?.busy ? 'Testing…' : 'Test connection', onClick: () => { if (!t?.busy) test(s) } },
                      ...(admin && connFor(s) ? [{ label: 'Connector…', onClick: () => setConn(s) }] : []),
                      ...(admin && !s.app_id ? [{ label: 'Remove', danger: true, onClick: () => setDel(s) }] : []),
                    ]} />
                  </div>
                </div>
                <Switch on={s.enabled} onChange={v => toggle(s, v)} title={s.enabled ? 'In the profile' : 'Hidden from devices'} />
              </div>
            )
          })}{absent.map(m => <MissingRow key={m.entry.type} m={m} onAdd={() => setAdding(m.entry)} onHowTo={() => setHowTo(m.entry)} />)}</div>
        </section>)}
      {adding && catalog && <AddService catalog={catalog.filter(c => !list.some(s => s.type === c.type))} initial={adding === true ? undefined : adding} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); reload() }} />}
      {howTo && <PocketHowTo entry={howTo} onClose={() => setHowTo(null)} />}
      {conn && <ConnectorModal service={conn} onClose={() => { setConn(null); reloadConns() }} />}
      {edit && <EditService s={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
      {del && <Confirm title="Remove service" message={<>Remove <b>{del.name}</b>? Paired devices lose it on their next profile refresh.</>} onConfirm={remove} onCancel={() => setDel(null)} />}
    </>
  )
}

// ConnectorLine sits under a service's name: what its connector is doing.
function ConnectorLine({ c, onOpen }: { c?: ConnectorListItem['connector']; onOpen: () => void }) {
  if (!c || !c.enabled) return <button type="button" className="conn-line off" onClick={onOpen}><span className="conn-bolt">ϟ</span><span className="conn-label">Connector available · off</span></button>
  const parts: [string, boolean, boolean][] = [['Accounts', c.provision_on_create, c.capabilities.provision], ['Sign-in', c.sso_enabled, c.capabilities.sso], ['Events', c.events_enabled, c.capabilities.events]]
  return (
    <button type="button" className={`conn-line ${c.last_error ? 'err' : 'on'}`} onClick={onOpen} title={c.last_error || 'Open connector'}>
      <span className="conn-bolt">ϟ</span><span className="conn-label">{c.last_error ? 'Connector error' : 'Connector on'}</span>
      {parts.filter(p => p[2]).map(([label, on]) => <span key={label} className={`conn-part ${on ? 'on' : ''}`}>{on ? '✓' : '–'} {label}</span>)}
    </button>
  )
}

// MissingRow: a greyed-out row for something not set up yet. The tooltip
// says what's missing; the visible line says the same for phones.
function MissingRow({ m, onAdd, onHowTo }: { m: Missing; onAdd: () => void; onHowTo: () => void }) {
  const pocket = m.kind === 'pocket'
  const name = m.entry.name
  const caps = m.caps ? [m.caps.provision && 'accounts', m.caps.sso && 'sign-in with Core', m.caps.events && 'events'].filter(Boolean).join(', ') : ''
  const tip = pocket
    ? `${name} isn't running on this box. Start it with CORE_URL and the registration key from Settings; it registers itself and this row comes alive.`
    : `${name} isn't added yet. Add its address and an admin API key to use its connector${caps ? ` (${caps})` : ''}.`
  const icon = bundledIcon(m.entry.type)
  return (
    <div className="svc-row off missing" title={tip}>
      {icon ? <span className="svc-icon"><img src={icon} alt="" /></span> : <span className="svc-icon letter" style={{ background: `hsl(${hue(name)} 70% 92%)`, color: `hsl(${hue(name)} 55% 35%)` }}>{name[0]}</span>}
      <div className="svc-main">
        <div className="svc-name"><b>{name}</b><span className="badge cat">{pocket ? 'Pocket app' : 'Connector'}</span></div>
        <div className="svc-sub"><span>{pocket ? 'Registers itself once it runs with CORE_URL' : `Add it to use ${caps || 'its connector'}`}</span></div>
      </div>
      <div className="svc-foot">
        <div className="svc-status"><span className="pill">{pocket ? 'Not installed' : 'Not added'}</span></div>
        <div className="svc-actions">{pocket ? <button className="btn sm ghost" onClick={onHowTo}>How to</button> : <button className="btn sm" onClick={onAdd}>Add</button>}</div>
      </div>
      <Switch on={false} onChange={() => {}} disabled title={pocket ? 'Install it first' : 'Add it first'} />
    </div>
  )
}

// PocketHowTo: the two lines a Pocket app needs to find this box.
function PocketHowTo({ entry, onClose }: { entry: CatalogEntry; onClose: () => void }) {
  const { data: st } = useAsync(() => api.get<{ registration_key: string }>(`${V1}/settings`), [])
  return (
    <Modal title={`Connect ${entry.name}`} onClose={onClose}>
      <p className="muted small">Add these to {entry.name}'s environment and restart it. It registers with Core, shows up here by itself, and paired phones get it with their device token.</p>
      {st ? <pre>{`CORE_URL=${window.location.origin}\nCORE_REGISTRATION_KEY=${st.registration_key}`}</pre> : <Loading />}
      <div className="form-actions"><button className="btn" onClick={onClose}>Close</button></div>
    </Modal>
  )
}

// ConnectorsOverview: every connector Core ships, at the top of the page.
function ConnectorsOverview({ conns, services, pocketBases, onOpen, onAdd }: { conns: ConnectorListItem[]; services: Service[]; pocketBases: string[]; onOpen: (s: Service) => void; onAdd: (type: string) => void }) {
  return (
    <section className="svc-group">
      <div className="svc-group-head"><span>Connectors</span><span className="count">{conns.length}</span><span className="muted" style={{ textTransform: 'none', letterSpacing: 0, fontWeight: 500 }}>create accounts, sign people in with Core, listen to events</span></div>
      <div className="conn-cards">{conns.map(k => {
        const svc = services.find(s => s.id === k.service_id && s.type === k.type)
        const c = k.connector
        const state = !svc ? 'missing' : !c || !c.enabled ? 'off' : c.last_error ? 'err' : 'on'
        return (
          <div key={k.type} className={`conn-card ${state}`}>
            <div className="conn-card-head">
              {svc ? <ServiceIcon s={svc} pocketBases={pocketBases} /> : bundledIcon(k.type) ? <span className="svc-icon"><img src={bundledIcon(k.type)} alt="" /></span> : <span className="svc-icon letter" style={{ background: `hsl(${hue(k.name)} 70% 92%)`, color: `hsl(${hue(k.name)} 55% 35%)` }}>{k.name[0]}</span>}
              <div className="grow"><b>{k.name}</b><div className="muted small">{state === 'missing' ? 'Add the service first' : state === 'off' ? 'Available, switched off' : state === 'err' ? (c?.last_error || 'Error') : c?.last_poll_at ? `Last check ${new Date(c.last_poll_at).toLocaleTimeString()}` : 'On'}</div></div>
              <span className={`pill ${state === 'on' ? 'ok' : state === 'err' ? 'err' : ''}`}>{state === 'on' ? 'On' : state === 'err' ? 'Error' : state === 'off' ? 'Off' : 'Not added'}</span>
            </div>
            <div className="conn-caps">
              {k.capabilities.provision && <span className={`conn-part ${c?.enabled && c.provision_on_create ? 'on' : ''}`}>Accounts</span>}
              {k.capabilities.sso && <span className={`conn-part ${c?.enabled && c.sso_enabled ? 'on' : ''}`}>Sign-in</span>}
              {k.capabilities.events && <span className={`conn-part ${c?.enabled && c.events_enabled ? 'on' : ''}`}>Events</span>}
              <span className="grow" />
              {svc ? <button className="btn sm" onClick={() => onOpen(svc)}>{state === 'off' ? 'Set up' : 'Manage'}</button> : <button className="btn sm" onClick={() => onAdd(k.type)} title={`Add ${k.name}'s address and API key first`}>Add</button>}
            </div>
          </div>
        )
      })}</div>
    </section>
  )
}

function groupBy(items: CatalogEntry[]): [string, CatalogEntry[]][] {
  const m = new Map<string, CatalogEntry[]>()
  for (const c of items) m.set(c.group, [...(m.get(c.group) || []), c])
  return [...m.entries()]
}

function hue(text: string) { let h = 0; for (const ch of text) h = (h * 31 + ch.charCodeAt(0)) % 360; return h }

// ServiceIcon tries the service's own icon, then a coloured initial.
function ServiceIcon({ s, pocketBases = [] }: { s: Service; pocketBases?: string[] }) {
  const candidates = iconCandidates({ type: s.type, url: s.url, public_url: s.public_url, pocket: s.catalog.pocket || !!s.app_id }, pocketBases)
  const [i, setI] = useState(0)
  const name = s.name || s.catalog.name
  if (i >= candidates.length) return <span className="svc-icon letter" style={{ background: `hsl(${hue(name)} 70% 92%)`, color: `hsl(${hue(name)} 55% 35%)` }}>{name[0]}</span>
  return <span className="svc-icon"><img src={candidates[i]} alt="" onError={() => setI(n => n + 1)} /></span>
}

function RowMenu({ items }: { items: { label: string; onClick: () => void; danger?: boolean }[] }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', close); document.addEventListener('keydown', esc)
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc) }
  }, [open])
  return (
    <div className="row-menu" ref={ref}>
      <button className="btn sm ghost icon" aria-label="More" aria-expanded={open} onClick={() => setOpen(o => !o)}>⋯</button>
      {open && <div className="row-menu-pop" role="menu">{items.map(it => <button key={it.label} role="menuitem" className={it.danger ? 'danger-text' : ''} onClick={() => { setOpen(false); it.onClick() }}>{it.label}</button>)}</div>}
    </div>
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
        <Select ariaLabel="Who receives it" value={audience} onChange={v => setAudience(v as Service['audience'])} options={[
          { value: 'all', label: 'Everyone on the box' },
          { value: 'admins', label: 'Admins only' },
          { value: 'users', label: 'Specific people', hint: 'pick below' },
        ]} />
      </Field>
      {audience === 'users' && <Field label="People" group><div className="chips">{(users || []).map(u => <button type="button" key={u.id} className={`chip ${audUsers.includes(u.id) ? 'active' : ''}`} onClick={() => setAudUsers(a => a.includes(u.id) ? a.filter(x => x !== u.id) : [...a, u.id])}>{u.name || u.email}</button>)}</div></Field>}
      <Field label="Notes"><input value={notes} onChange={e => setNotes(e.target.value)} /></Field>
      {err && <div className="callout danger">{err}</div>}
      <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>Save</button></div>
    </form>
  )
}

function AddService({ catalog, initial, onClose, onSaved }: { catalog: CatalogEntry[]; initial?: CatalogEntry; onClose: () => void; onSaved: () => void }) {
  const [entry, setEntry] = useState<CatalogEntry | null>(initial || null)
  const [q, setQ] = useState('')
  const [custom, setCustom] = useState('')
  const items = catalog.filter(c => !c.pocket || c.type === 'core' ? !c.pocket : false).filter(c => !q || (c.name + c.type + c.group).toLowerCase().includes(q.toLowerCase()))
  if (!entry) return (
    <Modal title="Add a service" onClose={onClose} size="lg">
      <div className="toolbar"><div className="search"><span className="ico">⌕</span><input autoFocus placeholder="Jellyfin, Immich, Paperless…" value={q} onChange={e => setQ(e.target.value)} /></div></div>
      {items.length === 0 ? <p className="muted small">Nothing matches. Use “Something else” below for a custom entry.</p> :
        (q ? [['Results', items] as [string, CatalogEntry[]]] : groupBy(items)).map(([g, list]) => <div key={g} className="pick-group">
          {!q && <div className="pick-group-head">{g}</div>}
          <div className="pick-grid">{list.map(c => (
            <button key={c.type} type="button" className="pick-tile" onClick={() => setEntry(c)} title={c.name}>
              <span className="pick-ico" style={{ background: `hsl(${hue(c.name)} 70% 92%)`, color: `hsl(${hue(c.name)} 55% 35%)` }}>{c.name[0]}</span>
              <span className="pick-text"><span className="pick-name">{c.name}</span>{q && <span className="pick-group-label">{c.group}</span>}</span>
            </button>))}</div>
        </div>)}
      <details className="mt"><summary className="muted small">Something else</summary>
        <div className="row mt-sm"><input placeholder="prefix, e.g. GRAFANA" value={custom} onChange={e => setCustom(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ''))} /><button type="button" className="btn" disabled={!custom} onClick={() => setEntry({ type: custom.toLowerCase(), name: custom, prefix: custom, group: 'Custom', url_key: 'URL', pocket: false, fields: [{ key: 'API_KEY', label: 'API key', secret: true }] })}>Use</button></div>
        <p className="muted small mt-sm">Custom entries land in the profile as <span className="inline-code">PREFIX_URL</span> and <span className="inline-code">PREFIX_API_KEY</span>; the client ignores keys it does not know.</p></details>
    </Modal>)
  return <Modal title={`Add ${entry.name}`} onClose={onClose}><ServiceForm entry={entry} onClose={onClose} onSubmit={async b => { await api.post(`${V1}/services`, b); onSaved() }} /></Modal>
}

function EditService({ s, onClose, onSaved }: { s: Service; onClose: () => void; onSaved: () => void }) {
  return <Modal title={`Edit ${s.name}`} onClose={onClose}><ServiceForm entry={s.catalog} initial={s} onClose={onClose} onSubmit={async b => { await api.put(`${V1}/services/${s.id}`, b); onSaved() }} /></Modal>
}
