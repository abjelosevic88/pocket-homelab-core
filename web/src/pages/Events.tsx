import { Fragment, useEffect, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { Delivery, Event, Topic, Webhook } from '../lib/types'
import { SourceIcon, describeEvent, sourceInfo, useEventCtx, type SourceKind } from '../components/EventView'
import { Select } from '../components/Select'
import { useApp } from '../lib/app-context'
import { Card, Confirm, Empty, Field, Loading, Modal, PageHeader, Switch, Tabs, useAsync, useToast } from '../components/ui'

export default function Events() {
  const [tab, setTab] = useState<'log' | 'webhooks'>('log')
  return (
    <>
      <PageHeader title="Events" sub="Every message apps publish on the bus. Live, searchable, replayable." />
      <Tabs tabs={[{ id: 'log', label: 'Log' }, { id: 'webhooks', label: 'Webhooks' }]} value={tab} onChange={setTab} />
      <div className="mt">{tab === 'log' ? <Log /> : <Webhooks />}</div>
    </>
  )
}

// autoFacts turns a payload's top-level plain values into readable rows; nested values stay in the raw JSON.
function autoFacts(payload: unknown): [string, string][] {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return []
  return Object.entries(payload as Record<string, unknown>)
    .filter(([, v]) => v !== null && v !== '' && typeof v !== 'object')
    .map(([k, v]) => [k.replace(/_/g, ' ').replace(/^./, c => c.toUpperCase()), typeof v === 'boolean' ? (v ? 'yes' : 'no') : String(v)])
}

function dayLabel(iso: string) {
  const d = new Date(iso), today = new Date()
  const y = new Date(); y.setDate(today.getDate() - 1)
  if (d.toDateString() === today.toDateString()) return 'Today'
  if (d.toDateString() === y.toDateString()) return 'Yesterday'
  return d.toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' })
}

function Log() {
  const { toast } = useToast()
  const [topic, setTopic] = useState('')
  const [source, setSource] = useState('')
  const [live, setLive] = useState(true)
  const [items, setItems] = useState<Event[]>([])
  const [fresh, setFresh] = useState<Set<number>>(new Set())
  const [open, setOpen] = useState<number | null>(null)
  const { data: topics } = useAsync(() => api.get<Topic[]>(`${V1}/events/topics`), [items.length])
  const ctx = useEventCtx()
  const es = useRef<EventSource | null>(null)
  useEffect(() => { api.get<Event[]>(`${V1}/events?limit=200${topic ? `&topic=${encodeURIComponent(topic)}` : ''}`).then(setItems) }, [topic])
  useEffect(() => {
    es.current?.close()
    if (!live) return
    const s = new EventSource(`${V1}/events/stream?topics=${encodeURIComponent(topic || '*')}`)
    s.onmessage = (e: MessageEvent) => {
      const ev = JSON.parse(e.data) as Event
      setItems(list => [ev, ...list.filter(x => x.id !== ev.id)].slice(0, 300))
      setFresh(f => new Set(f).add(ev.id))
      setTimeout(() => setFresh(f => { const n = new Set(f); n.delete(ev.id); return n }), 2500)
    }
    es.current = s
    return () => s.close()
  }, [live, topic])
  const sources = [...new Set(items.map(e => e.source))].map(src => ({ src, info: sourceInfo(src, ctx), n: items.filter(e => e.source === src).length }))
  const groups: [SourceKind, string][] = [['app', 'Pocket apps'], ['connector', 'Connectors'], ['core', 'Core'], ['other', 'Other']]
  const shown = items.filter(e => !source || (source.startsWith('kind:') ? sourceInfo(e.source, ctx).kind === source.slice(5) : e.source === source))
  const days: [string, Event[]][] = []
  for (const e of shown) { const k = dayLabel(e.created_at); const last = days[days.length - 1]; if (last && last[0] === k) last[1].push(e); else days.push([k, [e]]) }
  const copy = async (e: Event) => { await navigator.clipboard.writeText(JSON.stringify(e.payload, null, 2)); toast('Payload copied') }
  return (
    <>
      <div className="toolbar">
        <div className="search"><span className="ico">⌕</span><input placeholder="Topic or pattern, e.g. kitchen.*" value={topic} onChange={e => setTopic(e.target.value.toLowerCase())} list="event-topics" /></div>
        <datalist id="event-topics">{(topics || []).map(t => <option key={t.topic} value={t.topic}>{t.count}</option>)}</datalist>
        <div className="ev-filter wide"><Select ariaLabel="Source" value={source} onChange={setSource} placeholder={`All sources · ${items.length}`} clearLabel="All sources"
          options={groups.flatMap(([k, title]) => {
            const list = sources.filter(x => x.info.kind === k).sort((x, y) => x.info.label.localeCompare(y.info.label))
            if (list.length === 0) return []
            const all = list.length > 1 ? [{ value: `kind:${k}`, label: `All ${title.toLowerCase()}`, hint: String(list.reduce((n, x) => n + x.n, 0)), group: title, icon: <span className="sel-all-ico">▦</span> }] : []
            return [...all, ...list.map(x => ({ value: x.src, label: x.info.label, hint: String(x.n), group: title, icon: <SourceIcon info={x.info} /> }))]
          })} /></div>
        {source && <button className="btn sm ghost" onClick={() => setSource('')}>Clear</button>}
        <span className="spacer" />
        <span className={`live-pill ${live ? 'on' : ''}`}><span className="pulse" />{live ? 'Live' : 'Paused'}</span>
        <Switch on={live} onChange={setLive} title={live ? 'Pause the live stream' : 'Resume the live stream'} />
      </div>
      {shown.length === 0 ? <Card><Empty title="No events">Publish one with <span className="inline-code">POST /api/v1/events</span> or wait for an app.</Empty></Card> :
        days.map(([day, list]) => <section key={day} className="svc-group">
          <div className="svc-group-head"><span>{day}</span><span className="count">{list.length}</span></div>
          <div className="svc-list feed">{list.map(e => { const info = sourceInfo(e.source, ctx); const d = describeEvent(e, ctx); const isOpen = open === e.id; return (
            <div key={e.id} className={`feed-item ${fresh.has(e.id) ? 'fresh' : ''} ${isOpen ? 'open' : ''} src-${info.kind}`}>
              <button type="button" className="feed-row" onClick={() => setOpen(isOpen ? null : e.id)} aria-expanded={isOpen}>
                <SourceIcon info={info} />
                <span className="grow">
                  <span className="feed-title">{d.title}<span className="muted"> · {info.label}</span>{info.kind === 'connector' && <span className="badge cat src-badge">connector</span>}</span>
                  <span className="feed-sub">{d.sub}</span>
                </span>
                {d.facts ? null : e.subject && <span className="subject-chip mono">{e.subject}</span>}
                <span className="feed-time" title={when(e.created_at)}>{ago(e.created_at)}</span>
                <span className="chev">›</span>
              </button>
              {isOpen && <div className="feed-detail-wrap"><div className="feed-detail">
                <div className="feed-detail-meta">
                  <span className="mono">{e.topic}</span><span>#{e.id}</span><span title={e.created_at}>{when(e.created_at)}</span>{e.subject && <span className="mono">{e.subject}</span>}
                  <span className="spacer" />
                  {d.link && <a className="btn sm primary" href={d.link.href} target="_blank" rel="noreferrer">{d.link.label} ↗</a>}
                  <button className="btn sm ghost" onClick={() => copy(e)}>Copy JSON</button>
                </div>
                {(() => { const facts = d.facts || autoFacts(e.payload); return facts.length > 0 && <dl className="event-facts">{facts.filter(([, v]) => v).map(([k, v]) => <Fragment key={k}><dt>{k}</dt><dd>{v}</dd></Fragment>)}</dl> })()}
                <details className="cx-more"><summary>Raw payload</summary><pre>{JSON.stringify(e.payload, null, 2)}</pre></details>
              </div></div>}
            </div>) })}</div>
        </section>)}
    </>
  )
}

function Webhooks() {
  const { toast } = useToast()
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const { data, loading, reload, error } = useAsync(() => api.get<Webhook[]>(`${V1}/webhooks`), [])
  const [edit, setEdit] = useState<Partial<Webhook> & { secret?: string } | null>(null)
  const [del, setDel] = useState<Webhook | null>(null)
  const [deliv, setDeliv] = useState<Webhook | null>(null)
  if (!admin) return <div className="callout">Webhooks are managed by admins and by the apps that own them.</div>
  if (loading && !data) return <Loading />
  if (error) return <div className="callout danger">{error}</div>
  const list = data || []
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!edit) return
    if (edit.id) await api.put(`${V1}/webhooks/${edit.id}`, edit); else await api.post(`${V1}/webhooks`, edit)
    setEdit(null); await reload(); toast('Saved')
  }
  const test = async (w: Webhook) => { const r = await api.post<{ ok: boolean; status: number; error?: string }>(`${V1}/webhooks/${w.id}/test`); toast(r.ok ? `Delivered · HTTP ${r.status}` : `Failed: ${r.error || r.status}`, r.ok ? 'ok' : 'err') }
  return (
    <>
      <div className="toolbar"><span className="muted small">Core POSTs matching events as JSON with an <span className="inline-code">X-Pocket-Signature</span> HMAC when a secret is set, retrying with backoff.</span><span className="spacer" /><button className="btn primary" onClick={() => setEdit({ topics: '*', enabled: true })}>Add webhook</button></div>
      {list.length === 0 ? <Card><Empty title="No webhooks">Apps declare theirs in the manifest; add your own here for n8n, Home Assistant, ntfy…</Empty></Card> :
        <Card flush><table className="table">
          <thead><tr><th></th><th>Name</th><th>Topics</th><th>URL</th><th></th></tr></thead>
          <tbody>{list.map(w => <tr key={w.id} className={w.enabled ? '' : 'muted'}>
            <td style={{ width: 44 }}><Switch on={w.enabled} onChange={async v => { await api.put(`${V1}/webhooks/${w.id}`, { enabled: v }); reload() }} /></td>
            <td><b>{w.name || '—'}</b>{w.app_id ? <span className="badge cat" style={{ marginLeft: 6 }}>app</span> : null}{w.has_secret && <span className="badge" style={{ marginLeft: 6 }}>signed</span>}</td>
            <td className="mono small">{w.topics}</td><td className="mono small">{w.url}</td>
            <td className="right" style={{ whiteSpace: 'nowrap' }}><button className="btn sm" onClick={() => test(w)}>Test</button> <button className="btn sm" onClick={() => setDeliv(w)}>Log</button> <button className="btn sm" onClick={() => setEdit({ ...w })}>Edit</button> <button className="btn sm ghost danger-text" onClick={() => setDel(w)}>✕</button></td>
          </tr>)}</tbody>
        </table></Card>}
      {edit && <Modal title={edit.id ? 'Edit webhook' : 'Add webhook'} onClose={() => setEdit(null)}>
        <form onSubmit={save} className="grid" style={{ gap: 12 }}>
          <Field label="Name"><input value={edit.name || ''} onChange={e => setEdit(v => ({ ...v, name: e.target.value }))} /></Field>
          <Field label="URL"><input required value={edit.url || ''} onChange={e => setEdit(v => ({ ...v, url: e.target.value }))} placeholder="https://n8n.example.com/webhook/pocket" /></Field>
          <Field label="Topics" help="Comma list; * for all, kitchen.* for a prefix"><input value={edit.topics || ''} onChange={e => setEdit(v => ({ ...v, topics: e.target.value }))} /></Field>
          <Field label="Secret" help={edit.id ? 'Leave empty to keep the current one' : 'Optional HMAC-SHA256 key'}><input value={edit.secret || ''} onChange={e => setEdit(v => ({ ...v, secret: e.target.value }))} autoComplete="off" /></Field>
          <div className="form-actions"><button type="button" className="btn" onClick={() => setEdit(null)}>Cancel</button><button className="btn primary">Save</button></div>
        </form></Modal>}
      {deliv && <Deliveries w={deliv} onClose={() => setDeliv(null)} />}
      {del && <Confirm title="Delete webhook" message={<>Delete <b>{del.name || del.url}</b>?</>} onConfirm={async () => { await api.del(`${V1}/webhooks/${del.id}`); setDel(null); reload() }} onCancel={() => setDel(null)} />}
    </>
  )
}

function Deliveries({ w, onClose }: { w: Webhook; onClose: () => void }) {
  const { data, loading } = useAsync(() => api.get<Delivery[]>(`${V1}/webhooks/${w.id}/deliveries?limit=50`), [w.id])
  return (
    <Modal title={`Deliveries · ${w.name || w.url}`} onClose={onClose} size="lg">
      {loading ? <Loading /> : (data || []).length === 0 ? <Empty title="Nothing delivered yet" /> :
        <table className="table compact"><thead><tr><th>Event</th><th>Status</th><th>Attempts</th><th>Last</th><th>When</th></tr></thead>
          <tbody>{(data || []).map(d => <tr key={d.id}><td>#{d.event_id}</td><td className={d.status === 'ok' ? 'ok' : d.status === 'failed' ? 'bad' : ''}>{d.status}</td><td>{d.attempts}</td><td className="small">{d.last_code || ''} {d.last_error}</td><td className="muted small">{ago(d.delivered_at || d.created_at)}</td></tr>)}</tbody></table>}
    </Modal>
  )
}
