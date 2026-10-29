import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { ago, formatVersion, shortName } from '../lib/format'
import type { MyService, Overview } from '../lib/types'
import { AppIcon } from '../components/AppIcon'
import { SourceIcon, describeEvent, sourceInfo, useEventCtx } from '../components/EventView'
import { Card, Dot, Empty, Loading, PageHeader, useAsync } from '../components/ui'
import PairModal from '../components/PairModal'
import { Icon } from '../components/icons'
import { iconCandidates } from '../lib/icons'

export default function Home() {
  const { data, loading, error, reload } = useAsync(() => api.get<Overview>(`${V1}/overview`), [])
  const [pair, setPair] = useState(false)
  const ectx = useEventCtx()
  if (loading && !data) return <Loading />
  if (error || !data) return <div className="callout danger">{error || 'Failed to load'}</div>
  const d = data
  if (d.member) return (
    <>
      <PageHeader title={d.box_name} sub="Your services and devices on this box." actions={<button type="button" className="btn primary" onClick={() => setPair(true)}><Icon name="qr" size={15} />Pair a device</button>} />
      <div className="stats"><Link to="/devices" className="stat"><div className="n">{d.devices.length}</div><div className="l">Your devices</div></Link><a href="#services" className="stat"><div className="n">{d.services_enabled}</div><div className="l">Services in your profile</div></a></div>
      <MyServices />
      <Card title={<><Icon name="key" size={16} />Your own logins</>}><p className="muted small">Under <Link to="/settings/logins">Settings → My logins</Link> you can set your own Jellyfin, Navidrome or Audiobookshelf login; otherwise your phone receives the household one.</p></Card>
      {pair && <PairModal onClose={() => { setPair(false); reload() }} />}
    </>
  )
  const down = d.apps.filter(a => a.status === 'down').length
  return (
    <>
      <PageHeader title={d.box_name} sub="Everything on this box, in one place." actions={<><button type="button" className="btn" onClick={reload}><Icon name="refresh" size={15} />Refresh</button><button type="button" className="btn primary" onClick={() => setPair(true)}><Icon name="qr" size={15} />Pair a device</button></>} />
      <div className="kpis">
        <Kpi to="/apps" icon="grid" tone={down ? 'err' : 'ok'} value={<>{d.apps_up}<span className="of"> / {d.apps.length}</span></>} label={down ? `${down} app${down > 1 ? 's' : ''} down` : 'Apps up'} />
        <Kpi to="/services" icon="server" value={d.services_enabled} label="Services in profile" />
        <Kpi to="/devices" icon="smartphone" value={d.devices.length} label="Paired devices" />
        <Kpi to="/events" icon="activity" value={d.listeners} label="Live listeners" />
        <Kpi to="/events?tab=webhooks" icon="send" tone={(d.deliveries.pending || 0) > 0 ? 'warn' : ''} value={d.deliveries.pending || 0} label="Webhooks pending" />
      </div>
      <div className="two-col">
        <Card title={<><Icon name="grid" size={16} />Apps</>} actions={<Link className="btn sm ghost" to="/apps">All apps<Icon name="chevron-right" size={14} /></Link>}>
          {d.apps.length === 0 ? <Empty title="No apps yet">Start a Pocket app with CORE_URL and the registration key from Settings and it appears here.</Empty> :
            <div className="mini-apps">{d.apps.map(a => (
              <Link key={a.id} to={`/apps/${a.slug}`} className="mini-app" title={a.status === 'down' ? a.last_error || 'down' : `seen ${ago(a.last_seen_at || a.last_check_at)}`}>
                <AppIcon a={a} />
                <span className="grow"><span className="name">{shortName(a.name)}</span><span className="meta">{formatVersion(a.version)}{a.status === 'down' ? ' · down' : ` · ${ago(a.last_seen_at || a.last_check_at)}`}</span></span>
                <span className={`status-dot ${a.status}`} />
              </Link>))}</div>}
        </Card>
        <Card title={<><Icon name="activity" size={16} />Recent events</>} actions={<Link className="btn sm ghost" to="/events">Live<Icon name="chevron-right" size={14} /></Link>} flush>
          {d.recent_events.length === 0 ? <Empty title="Quiet">Nothing published yet.</Empty> :
            <div className="feed">{d.recent_events.map(e => {
              const info = sourceInfo(e.source, ectx)
              const desc = describeEvent(e, ectx)
              return (
                <Link key={e.id} to="/events" className="feed-row">
                  <SourceIcon info={info} />
                  <span className="grow">
                    <span className="feed-title">{desc.title}<span className="muted"> · {info.label}</span></span>
                    <span className="feed-sub">{desc.sub}</span>
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
  return <Link to={to} className={`kpi ${tone}`}><span className="kpi-ico"><Icon name={icon} size={19} /></span><span><span className="kpi-n">{value}</span><span className="kpi-l">{label}</span></span></Link>
}

// MyServices is the member's launcher: every service their profile holds, as a link.
function MyServices() {
  const { data, loading, error } = useAsync(() => api.get<MyService[]>(`${V1}/me/services`), [])
  if (loading && !data) return <Card title="Your services"><Loading /></Card>
  if (error || !data) return <div className="callout danger">{error || 'Failed to load services'}</div>
  if (data.length === 0) return <Card title="Your services"><Empty title="Nothing shared with you yet">Ask the person who runs this box to add you to a service.</Empty></Card>
  const groups: [string, MyService[]][] = []
  const pocketBases = data.filter(x => x.pocket).map(x => x.url)
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
              <ServiceIcon sv={sv} pocketBases={pocketBases} />
              <span className="grow"><span className="name">{sv.name}</span><span className="meta block">{sv.url.replace(/^https?:\/\//, '')}</span></span>
            </a>))}</div>
        </Card>))}
    </div>
  )
}

function ServiceIcon({ sv, pocketBases }: { sv: MyService; pocketBases: string[] }) {
  const list = [...(sv.icon ? [sv.icon] : []), ...iconCandidates({ type: sv.type, url: sv.url, pocket: sv.pocket }, pocketBases)]
  const [i, setI] = useState(0)
  // the initial shows until an icon has loaded (an unreachable host can take a while to fail)
  const [loaded, setLoaded] = useState(false)
  return <span className="icon">{!loaded && sv.name[0]}{i < list.length && <img src={list[i]} alt="" className={loaded ? '' : 'loading'} onLoad={() => setLoaded(true)} onError={() => { setLoaded(false); setI(n => n + 1) }} />}</span>
}
