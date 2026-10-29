// Left column: pair/add button, sections, the registered apps (or your apps), settings — the Pocket Calendar sidebar.
import { useState } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import type { App, MyService } from '../lib/types'
import { bundledIcon, iconCandidates } from '../lib/icons'
import { formatVersion } from '../lib/format'
import { Icon } from './icons'
import type { ShellData } from './Layout'
import { type Anchor, type MenuItem, Menu, rectOf } from './ui'

export function Sidebar({ data, onPair, onNavigate }: { data: ShellData; onPair: () => void; onNavigate: () => void }) {
  const { status, user } = useApp()
  const navigate = useNavigate()
  const admin = user?.role === 'admin'
  const [menu, setMenu] = useState<Anchor | null>(null)
  const ov = data.overview
  const apps = ov?.apps || []
  const down = apps.filter(a => a.status === 'down').length
  const pending = ov?.deliveries?.pending || 0
  const title = status?.title || 'Pocket Core'
  const go = (to: string) => { onNavigate(); navigate(to) }
  const addItems: MenuItem[] = [
    { label: 'Pair a device', icon: 'qr', hint: 'P', onSelect: onPair },
    'sep',
    { label: 'Add a service', icon: 'server', onSelect: () => go('/services?add=1') },
    { label: 'Add a person', icon: 'users', onSelect: () => go('/users?add=1') },
    { label: 'Add a webhook', icon: 'send', onSelect: () => go('/events?tab=webhooks&add=1') },
    { label: 'Add a sign-on client', icon: 'shield', onSelect: () => go('/settings/sso?add=1') },
  ]
  const nav: { to: string; label: string; icon: string; end?: boolean; badge?: React.ReactNode }[] = admin ? [
    { to: '/', label: 'Overview', icon: 'hub', end: true },
    { to: '/apps', label: 'Apps', icon: 'grid', badge: down ? <span className="badge danger" title={`${down} down`}>{down}</span> : apps.length ? <span className="sb-count">{apps.length}</span> : null },
    { to: '/services', label: 'Services', icon: 'server', badge: ov ? <span className="sb-count">{ov.services_enabled}</span> : null },
    { to: '/events', label: 'Events', icon: 'activity', badge: pending ? <span className="badge warn" title={`${pending} webhook deliveries pending`}>{pending}</span> : null },
    { to: '/links', label: 'Links', icon: 'nodes' },
    { to: '/devices', label: 'Devices', icon: 'smartphone', badge: ov?.devices.length ? <span className="sb-count">{ov.devices.length}</span> : null },
    { to: '/users', label: 'People', icon: 'users' },
    { to: '/ai-connections', label: 'AI connections', icon: 'sparkles' },
  ] : [
    { to: '/', label: 'Overview', icon: 'hub', end: true },
    { to: '/devices', label: 'Devices', icon: 'smartphone', badge: ov?.devices.length ? <span className="sb-count">{ov.devices.length}</span> : null },
    { to: '/ai-connections', label: 'AI connections', icon: 'sparkles' },
  ]
  const pocket = data.services.filter(s => s.pocket)
  return (
    <aside className="sidebar" aria-label="Pocket Core">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{title !== 'Pocket Core' ? title : <>Pocket <b>Core</b></>}</span>
        {status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^v?\d+\.\d+/.test(status.version) ? 'v' + status.version.replace(/^v/, '').replace(/[-+].*$/, '') : status.version}</span>}
      </div>
      {admin ? <>
        <button type="button" className="btn primary create-btn" aria-haspopup="menu" aria-expanded={!!menu} onClick={e => setMenu(menu ? null : rectOf(e.currentTarget))}>
          <Icon name="plus" size={17} strokeWidth={2.2} />New<Icon name="chevron-down" size={15} className="create-caret" />
        </button>
        {menu && <Menu anchor={menu} items={addItems} onClose={() => setMenu(null)} width={248} />}
      </> : (
        <button type="button" className="btn primary create-btn" onClick={onPair}>
          <Icon name="qr" size={17} strokeWidth={2} />Pair a device<kbd className="kbd-on-primary">P</kbd>
        </button>
      )}

      <nav className="sb-nav" aria-label="Sections">
        {nav.map(n => (
          <NavLink key={n.to} to={n.to} end={n.end} onClick={onNavigate} className={({ isActive }) => `sb-nav-item ${isActive ? 'active' : ''}`}>
            <Icon name={n.icon} size={16} />{n.label}{n.badge}
          </NavLink>
        ))}
      </nav>

      {admin
        ? <AppGroup title="Pocket apps" apps={apps} onNavigate={onNavigate} />
        : pocket.length > 0 && <ServiceGroup title="Your apps" list={pocket} />}

      <div className="sb-foot">
        <Link to="/settings" className="sb-foot-link" onClick={onNavigate}><Icon name="settings" size={15} />Settings</Link>
        <button type="button" className="sb-foot-link" onClick={() => { onNavigate(); window.dispatchEvent(new CustomEvent('pc:shortcuts')) }}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
    </aside>
  )
}

function Group({ title, count, children, empty }: { title: string; count: number; children: React.ReactNode; empty?: React.ReactNode }) {
  const key = `pc-sb-${title}`
  const [open, setOpenRaw] = useState(() => localStorage.getItem(key) !== '0')
  const setOpen = (o: boolean) => { setOpenRaw(o); localStorage.setItem(key, o ? '1' : '0') }
  return (
    <section className="sb-sec">
      <h4 className="sb-h">
        <button type="button" className="sb-h-btn" aria-expanded={open} onClick={() => setOpen(!open)}><Icon name={open ? 'chevron-down' : 'chevron-right'} size={13} />{title}</button>
        {count > 0 && <span className="sb-count">{count}</span>}
      </h4>
      {open && (count === 0 ? empty : <ul className="cal-list">{children}</ul>)}
    </section>
  )
}

// The letter shows until an icon has loaded (an unreachable host can take a while to fail).
function SbIcon({ srcs, letter }: { srcs: string[]; letter: string }) {
  const [i, setI] = useState(0)
  const [loaded, setLoaded] = useState(false)
  return <span className="sb-ico">{!loaded && letter}{i < srcs.length && <img src={srcs[i]} alt="" className={loaded ? '' : 'loading'} onLoad={() => setLoaded(true)} onError={() => { setLoaded(false); setI(n => n + 1) }} />}</span>
}

const appIconSrc = (a: App) => {
  const base = (a.public_url || a.url).replace(/\/$/, '')
  const own = a.icon ? [a.icon.startsWith('http') ? a.icon : base + (a.icon.startsWith('/') ? '' : '/') + a.icon] : []
  const bundled = bundledIcon(a.slug)
  return bundled ? [...own, bundled] : own
}
const short = (name: string) => name.replace(/^Pocket /, '')

function AppGroup({ title, apps, onNavigate }: { title: string; apps: App[]; onNavigate: () => void }) {
  const sorted = [...apps].sort((a, b) => a.name.localeCompare(b.name))
  return (
    <Group title={title} count={apps.length} empty={<p className="sb-empty">No app has registered yet.</p>}>
      {sorted.map(a => <AppRow key={a.id} a={a} onNavigate={onNavigate} />)}
    </Group>
  )
}

function AppRow({ a, onNavigate }: { a: App; onNavigate: () => void }) {
  const navigate = useNavigate()
  const [menu, setMenu] = useState<Anchor | null>(null)
  const pub = a.open_url || a.public_url || a.url
  const items: MenuItem[] = [
    { label: 'Details', icon: 'info', onSelect: () => { onNavigate(); navigate(`/apps/${a.slug}`) } },
    ...(pub ? [{ label: 'Open app', icon: 'external', onSelect: () => window.open(pub, '_blank', 'noopener') }] : []),
  ]
  return (
    <li className={`cal-row app-row ${a.status === 'down' ? 'off' : ''}`}>
      <NavLink to={`/apps/${a.slug}`} onClick={onNavigate} className={({ isActive }) => `cal-check ${isActive ? 'is-active' : ''}`} title={a.status === 'down' ? `${a.name}: down${a.last_error ? ` — ${a.last_error}` : ''}` : `${a.name}${a.version ? ` ${formatVersion(a.version)}` : ''}`}>
        <SbIcon srcs={appIconSrc(a)} letter={short(a.name)[0]} />
        <span className="cal-name ellipsis">{short(a.name)}</span>
        <span className={`status-dot ${a.status}`} />
      </NavLink>
      <button type="button" className="btn ghost icon-only xs cal-more" aria-label={`Options for ${a.name}`} aria-haspopup="menu" aria-expanded={!!menu} onClick={e => setMenu(rectOf(e.currentTarget))}><Icon name="more" size={15} /></button>
      {menu && <Menu anchor={menu} items={items} onClose={() => setMenu(null)} width={190} />}
    </li>
  )
}

function ServiceGroup({ title, list }: { title: string; list: MyService[] }) {
  const bases = list.map(s => s.url)
  return (
    <Group title={title} count={list.length}>
      {list.map(sv => (
        <li key={sv.id} className="cal-row app-row">
          <a className="cal-check" href={sv.open_url || sv.url} target="_blank" rel="noreferrer" title={sv.open_url !== sv.url ? 'Opens signed in with your Pocket Core account' : sv.url}>
            <SbIcon srcs={[...(sv.icon ? [sv.icon] : []), ...iconCandidates({ type: sv.type, url: sv.url, pocket: sv.pocket }, bases)]} letter={short(sv.name)[0]} />
            <span className="cal-name ellipsis">{short(sv.name)}</span>
            <Icon name="external" size={13} className="sb-ext" />
          </a>
        </li>
      ))}
    </Group>
  )
}
