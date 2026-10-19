import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import type { App, MyService, Overview } from '../lib/types'
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
      <PageHeader title={d.box_name} sub="Your services and devices on this box." actions={<button className="btn primary" onClick={() => setPair(true)}>Pair a device</button>} />
      <div className="stats"><Link to="/devices" className="stat"><div className="n">{d.devices.length}</div><div className="l">Your devices</div></Link><a href="#services" className="stat"><div className="n">{d.services_enabled}</div><div className="l">Services in your profile</div></a></div>
      <MyServices />
      <Card title="Your own logins"><p className="muted small">Under <Link to="/settings">Settings</Link> you can set your own Jellyfin, Navidrome or Audiobookshelf login; otherwise your phone receives the household one.</p></Card>
      {pair && <PairModal onClose={() => { setPair(false); reload() }} />}
    </>
  )
  const down = d.apps.filter(a => a.status === 'down').length
  const appByName = new Map(d.apps.map(a => [a.slug, a]))
  return (
    <>
      <PageHeader title={d.box_name} sub="Everything on this box, in one place." actions={<><button className="btn" onClick={reload}>Refresh</button><button className="btn primary" onClick={() => setPair(true)}>Pair a device</button></>} />
      <div className="kpis">
        <Kpi to="/apps" icon="▦" tone={down ? 'err' : 'ok'} value={<>{d.apps_up}<span className="of"> / {d.apps.length}</span></>} label={down ? `${down} app${down > 1 ? 's' : ''} down` : 'Apps up'} />
        <Kpi to="/services" icon="⚙" value={d.services_enabled} label="Services in profile" />
        <Kpi to="/devices" icon="📱" value={d.devices.length} label="Paired devices" />
        <Kpi to="/events" icon="⚡" value={d.listeners} label="Live listeners" />
        <Kpi to="/events" icon="↻" tone={(d.deliveries.pending || 0) > 0 ? 'warn' : ''} value={d.deliveries.pending || 0} label="Webhooks pending" />
      </div>
      <div className="two-col">
        <Card title="Apps" actions={<Link className="btn sm ghost" to="/apps">All apps →</Link>}>
          {d.apps.length === 0 ? <Empty title="No apps yet">Start a Pocket app with CORE_URL and the registration key from Settings and it appears here.</Empty> :
            <div className="mini-apps">{d.apps.map(a => (
              <Link key={a.id} to={`/apps/${a.slug}`} className="mini-app" title={a.status === 'down' ? a.last_error || 'down' : `seen ${ago(a.last_seen_at || a.last_check_at)}`}>
                <AppIcon a={a} />
                <span className="grow"><span className="name">{a.name.replace(/^Pocket /, '')}</span><span className="meta">{a.version ? `v${a.version}` : ''}{a.status === 'down' ? ' · down' : ` · ${ago(a.last_seen_at || a.last_check_at)}`}</span></span>
                <span className={`status-dot ${a.status}`} />
              </Link>))}</div>}
        </Card>
        <Card title="Recent events" actions={<Link className="btn sm ghost" to="/events">Live →</Link>} flush>
          {d.recent_events.length === 0 ? <Empty title="Quiet">Nothing published yet.</Empty> :
            <div className="feed">{d.recent_events.map(e => {
              const src = appByName.get(e.source)
              return (
                <Link key={e.id} to="/events" className="feed-row">
                  {src ? <AppIcon a={src} small /> : <span className="feed-ico"><img src="/favicon.svg" alt="" /></span>}
                  <span className="grow">
                    <span className="feed-title">{humanTopic(e.topic)}<span className="muted"> · {src ? src.name.replace(/^Pocket /, '') : e.source}</span></span>
                    <span className="feed-sub mono">{e.topic}{e.subject ? ` · ${e.subject}` : ''}</span>
                  </span>
                  <span className="feed-time">{ago(e.created_at)}</span>
                </Link>
              )
            })}</div>}
        </Card>
      </div>
      {pair && <PairModal onClose={() => { setPair(false); reload() }} />}
    </>
  )
}

function Kpi({ to, icon, value, label, tone = '' }: { to: string; icon: string; value: React.ReactNode; label: string; tone?: string }) {
  return <Link to={to} className={`kpi ${tone}`}><span className="kpi-ico">{icon}</span><span><span className="kpi-n">{value}</span><span className="kpi-l">{label}</span></span></Link>
}

// "clips.clip_created" → "Clip created", "core.app.registered" → "App registered"
function humanTopic(t: string) {
  const rest = t.split('.').slice(1).join(' ').replace(/_/g, ' ').trim() || t
  return rest.charAt(0).toUpperCase() + rest.slice(1)
}

function AppIcon({ a, small = false }: { a: App; small?: boolean }) {
  const [ok, setOk] = useState(true)
  const base = (a.public_url || a.url).replace(/\/$/, '')
  const src = a.icon ? (a.icon.startsWith('http') ? a.icon : base + (a.icon.startsWith('/') ? '' : '/') + a.icon) : ''
  return <span className={small ? 'feed-ico' : 'mini-ico'}>{src && ok ? <img src={src} alt="" onError={() => setOk(false)} /> : a.name.replace(/^Pocket /, '')[0]}</span>
}

// MyServices is the member's launcher: every service their profile holds, as a link.
function MyServices() {
  const { data, loading, error } = useAsync(() => api.get<MyService[]>(`${V1}/me/services`), [])
  if (loading && !data) return <Card title="Your services"><Loading /></Card>
  if (error || !data) return <div className="callout danger">{error || 'Failed to load services'}</div>
  if (data.length === 0) return <Card title="Your services"><Empty title="Nothing shared with you yet">Ask the person who runs this box to add you to a service.</Empty></Card>
  const groups: [string, MyService[]][] = []
  for (const sv of data) {
    const g = sv.group || 'Other'
    const hit = groups.find(x => x[0] === g)
    if (hit) hit[1].push(sv); else groups.push([g, [sv]])
  }
  return (
    <div id="services" className="my-services">
      {groups.map(([g, list]) => (
        <Card key={g} title={g}>
          <div className="app-grid">{list.map(sv => (
            <a key={sv.id} href={sv.open_url || sv.url} target="_blank" rel="noreferrer" className="app-card" title={sv.open_url !== sv.url ? 'Opens signed in with your Pocket Core account' : undefined}>
              <ServiceIcon sv={sv} />
              <span className="grow"><span className="name">{sv.name}</span><span className="meta block">{sv.url.replace(/^https?:\/\//, '')}</span></span>
            </a>))}</div>
        </Card>))}
    </div>
  )
}

function ServiceIcon({ sv }: { sv: MyService }) {
  const [src, setSrc] = useState(sv.icon || `${sv.url}/favicon.ico`)
  const [failed, setFailed] = useState(false)
  const fallback = () => { if (src !== `${sv.url}/favicon.ico` && !failed) setSrc(`${sv.url}/favicon.ico`); else setFailed(true) }
  return <span className="icon">{failed ? sv.name[0] : <img src={src} alt="" onError={fallback} />}</span>
}
