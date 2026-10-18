import { useEffect, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { Delivery, Event, Topic, Webhook } from '../lib/types'
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

function Log() {
  const [topic, setTopic] = useState('')
  const [live, setLive] = useState(true)
  const [items, setItems] = useState<Event[]>([])
  const [open, setOpen] = useState<number | null>(null)
  const { data: topics } = useAsync(() => api.get<Topic[]>(`${V1}/events/topics`), [items.length])
  const es = useRef<EventSource | null>(null)
  useEffect(() => { api.get<Event[]>(`${V1}/events?limit=100${topic ? `&topic=${encodeURIComponent(topic)}` : ''}`).then(setItems) }, [topic])
  useEffect(() => {
    es.current?.close()
    if (!live) return
    const s = new EventSource(`${V1}/events/stream?topics=${encodeURIComponent(topic || '*')}`)
    s.onmessage = (e: MessageEvent) => { const ev = JSON.parse(e.data) as Event; setItems(list => [ev, ...list.filter(x => x.id !== ev.id)].slice(0, 300)) }
    es.current = s
    return () => s.close()
  }, [live, topic])
  return (
    <>
      <div className="toolbar">
        <select value={topic} onChange={e => setTopic(e.target.value)}><option value="">All topics</option>{(topics || []).map(t => <option key={t.topic} value={t.topic}>{t.topic} ({t.count})</option>)}</select>
        <input style={{ maxWidth: 220 }} placeholder="or a pattern: kitchen.*" value={topic} onChange={e => setTopic(e.target.value.toLowerCase())} />
        <span className="spacer" />
        <span className="live"><span className={`dot ${live ? 'on' : ''}`} />{live ? 'live' : 'paused'}</span><Switch on={live} onChange={setLive} />
      </div>
      {items.length === 0 ? <Card><Empty title="No events">Publish one with <span className="inline-code">POST /api/v1/events</span> or wait for an app.</Empty></Card> :
        <Card flush><table className="table compact">
          <thead><tr><th>Topic</th><th>Source</th><th>Subject</th><th>When</th></tr></thead>
          <tbody>{items.map(e => <tr key={e.id} className="event-row" style={{ cursor: 'pointer' }} onClick={() => setOpen(open === e.id ? null : e.id)}>
            <td><span className="topic">{e.topic}</span>{open === e.id && <pre>{JSON.stringify(e.payload, null, 2)}</pre>}</td>
            <td>{e.source}</td><td>{e.subject}</td><td title={when(e.created_at)} className="muted">{ago(e.created_at)}</td>
          </tr>)}</tbody>
        </table></Card>}
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
