import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import type { Overview } from '../lib/types'
import { Card, Dot, Empty, Loading, PageHeader, useAsync } from '../components/ui'
import PairModal from '../components/PairModal'

export default function Home() {
  const { data, loading, error, reload } = useAsync(() => api.get<Overview>(`${V1}/overview`), [])
  const [pair, setPair] = useState(false)
  if (loading && !data) return <Loading />
  if (error || !data) return <div className="callout danger">{error || 'Failed to load'}</div>
  const d = data
  if (d.member) return (
    <>
      <PageHeader title={d.box_name} sub="Your devices on this box." actions={<button className="btn primary" onClick={() => setPair(true)}>Pair a device</button>} />
      <div className="stats"><Link to="/devices" className="stat"><div className="n">{d.devices.length}</div><div className="l">Your devices</div></Link><Link to="/settings" className="stat"><div className="n">{d.services_enabled}</div><div className="l">Services in your profile</div></Link></div>
      <Card title="Your own logins"><p className="muted small">Under Settings you can set your own Jellyfin, Navidrome or Audiobookshelf login; otherwise your phone receives the household one.</p></Card>
      {pair && <PairModal onClose={() => { setPair(false); reload() }} />}
    </>
  )
  return (
    <>
      <PageHeader title={d.box_name} sub="Everything on this box, in one place." actions={<><button className="btn" onClick={reload}>Refresh</button><button className="btn primary" onClick={() => setPair(true)}>Pair a device</button></>} />
      <div className="stats">
        <Link to="/apps" className="stat"><div className="n">{d.apps_up}<span className="muted" style={{ fontSize: 14 }}> / {d.apps.length}</span></div><div className="l">Apps up</div></Link>
        <Link to="/services" className="stat"><div className="n">{d.services_enabled}</div><div className="l">Services in profile</div></Link>
        <Link to="/devices" className="stat"><div className="n">{d.devices.length}</div><div className="l">Paired devices</div></Link>
        <Link to="/events" className="stat"><div className="n">{d.listeners}</div><div className="l">Live listeners</div></Link>
        <Link to="/events" className="stat"><div className="n">{d.deliveries.pending || 0}</div><div className="l">Webhooks pending</div></Link>
      </div>
      <div className="two-col">
        <Card title="Apps" actions={<Link className="btn sm" to="/apps">All</Link>}>
          {d.apps.length === 0 ? <Empty title="No apps yet">Start a Pocket app with CORE_URL and the registration key from Settings and it appears here.</Empty> :
            <div className="app-grid">{d.apps.map(a => (
              <Link key={a.id} to={`/apps/${a.slug}`} className="app-card">
                <span className="icon">{a.icon ? <img src={a.icon.startsWith('http') ? a.icon : a.url + a.icon} alt="" onError={e => { (e.target as HTMLImageElement).style.display = 'none' }} /> : a.name[0]}</span>
                <span className="grow"><span className="name"><Dot status={a.status} />{a.name}</span><span className="meta block">{a.version ? `v${a.version} · ` : ''}{a.status === 'down' ? a.last_error || 'down' : `seen ${ago(a.last_seen_at || a.last_check_at)}`}</span></span>
              </Link>))}</div>}
        </Card>
        <Card title="Recent events" actions={<Link className="btn sm" to="/events">Live</Link>} flush>
          {d.recent_events.length === 0 ? <Empty title="Quiet">Nothing published yet.</Empty> :
            <table className="table compact">
              <tbody>{d.recent_events.map(e => <tr key={e.id} className="event-row"><td><span className="topic">{e.topic}</span><div className="muted">{e.source}{e.subject ? ` · ${e.subject}` : ''}</div></td><td className="right muted">{ago(e.created_at)}</td></tr>)}</tbody>
            </table>}
        </Card>
      </div>
      {pair && <PairModal onClose={() => { setPair(false); reload() }} />}
    </>
  )
}
