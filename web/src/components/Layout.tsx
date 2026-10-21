// The app shell: sidebar, top bar, search palette, shortcuts — the Pocket Calendar layout.
import { useCallback, useEffect, useRef, useState } from 'react'
import { Outlet, useLocation, useMatch, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { MyService, Overview } from '../lib/types'
import { Icon } from './icons'
import { Sidebar } from './Sidebar'
import { CommandPalette, type PaletteCommand } from './CommandPalette'
import { ShortcutsSheet } from './ShortcutsSheet'
import PairModal from './PairModal'
import { Avatar, type MenuItem, MenuButton, modalOpen, useIsMobile, useMediaQuery } from './ui'

const SIDEBAR_KEY = 'pc-sidebar'

/** What the sidebar and the palette show: the overview (apps, devices, counts) and, for members, their services. */
export interface ShellData { overview: Overview | null; services: MyService[] }

function useShellData(admin: boolean) {
  const loc = useLocation()
  const [data, setData] = useState<ShellData>({ overview: null, services: [] })
  const load = useCallback(async () => {
    const [overview, services] = await Promise.all([
      api.get<Overview>(`${V1}/overview`).catch(() => null),
      admin ? Promise.resolve([] as MyService[]) : api.get<MyService[]>(`${V1}/me/services`).catch(() => [] as MyService[]),
    ])
    setData(d => ({ overview: overview || d.overview, services }))
  }, [admin])
  // fresh on every page change, after a pairing, and once a minute for the status dots
  useEffect(() => { load() }, [load, loc.pathname])
  useEffect(() => {
    const t = setInterval(load, 60000)
    window.addEventListener('pc:paired', load)
    return () => { clearInterval(t); window.removeEventListener('pc:paired', load) }
  }, [load])
  return data
}

export default function Layout() {
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const navigate = useNavigate()
  const loc = useLocation()
  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 1099px)')
  const drawer = mobile || narrow
  const [sidebarOpen, setSidebarOpen] = useState(() => !drawer && localStorage.getItem(SIDEBAR_KEY) !== '0')
  useEffect(() => { setSidebarOpen(!drawer && localStorage.getItem(SIDEBAR_KEY) !== '0') }, [drawer])
  const drawerOpenedAt = useRef(0)
  const toggleSidebar = () => setSidebarOpen(o => { drawerOpenedAt.current = performance.now(); const n = !o; if (!drawer) localStorage.setItem(SIDEBAR_KEY, n ? '1' : '0'); return n })
  const closeDrawer = () => { if (drawer) setSidebarOpen(false) }
  useEffect(() => { document.querySelector('.content')?.scrollTo({ top: 0 }) }, [loc.pathname])

  const shell = useShellData(admin)
  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const [pair, setPair] = useState(false)
  const startPair = () => { closeDrawer(); setPair(true) }

  useEffect(() => {
    const open = () => setShortcuts(true)
    window.addEventListener('pc:shortcuts', open)
    return () => window.removeEventListener('pc:shortcuts', open)
  }, [])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette(p => !p); return }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (t.closest('input, textarea, select, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop, .lc.full')) return
      const go = (to: string) => { e.preventDefault(); navigate(to) }
      const adminGo = (to: string) => { if (admin) go(to) }
      switch (e.key) {
        case '/': e.preventDefault(); setPalette(true); break
        case '?': e.preventDefault(); setShortcuts(true); break
        case 'p': e.preventDefault(); setPair(true); break
        case 'o': go('/'); break
        case 'd': go('/devices'); break
        case 'i': go('/ai-connections'); break
        case 'a': adminGo('/apps'); break
        case 's': adminGo('/services'); break
        case 'e': adminGo('/events'); break
        case 'l': adminGo('/links'); break
        case 'u': adminGo('/users'); break
        case ',': go('/settings'); break
        case '[': e.preventDefault(); toggleSidebar(); break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }) // eslint-disable-line react-hooks/exhaustive-deps

  const commands: PaletteCommand[] = [
    { id: 'pair', label: 'Pair a device', icon: 'qr', hint: 'P', keywords: 'phone qr code scan add device', run: () => setPair(true) },
    ...(admin ? [
      { id: 'add-service', label: 'Add a service', icon: 'plus', keywords: 'new jellyfin immich credentials', run: () => navigate('/services?add=1') },
      { id: 'add-user', label: 'Add a person', icon: 'users', keywords: 'new user account member household', run: () => navigate('/users?add=1') },
    ] : []),
    { id: 'home', label: 'Overview', icon: 'hub', hint: 'O', keywords: 'home', run: () => navigate('/') },
    ...(admin ? [
      { id: 'apps', label: 'Apps', icon: 'grid', hint: 'A', keywords: 'registry registered', run: () => navigate('/apps') },
      { id: 'services', label: 'Services', icon: 'server', hint: 'S', keywords: 'connectors credentials profile', run: () => navigate('/services') },
      { id: 'events', label: 'Events', icon: 'activity', hint: 'E', keywords: 'bus log live', run: () => navigate('/events') },
      { id: 'webhooks', label: 'Webhooks', icon: 'send', keywords: 'events deliveries', run: () => navigate('/events?tab=webhooks') },
      { id: 'links', label: 'Links', icon: 'nodes', hint: 'L', keywords: 'app links permissions diagram grid', run: () => navigate('/links') },
    ] : []),
    { id: 'devices', label: 'Devices', icon: 'smartphone', hint: 'D', keywords: 'paired phones revoke', run: () => navigate('/devices') },
    ...(admin ? [{ id: 'users', label: 'People', icon: 'users', hint: 'U', keywords: 'users accounts credentials', run: () => navigate('/users') }] : []),
    { id: 'ai', label: 'AI connections', icon: 'sparkles', hint: 'I', keywords: 'mcp assistant codex claude token', run: () => navigate('/ai-connections') },
    { id: 'settings', label: 'Settings', icon: 'settings', hint: ',', run: () => navigate('/settings') },
    ...(admin ? [{ id: 'sso', label: 'Single sign-on clients', icon: 'shield', keywords: 'oidc openid settings', run: () => navigate('/settings/sso') }] : []),
    { id: 'tokens', label: 'API tokens', icon: 'key', keywords: 'raycast homepage settings', run: () => navigate('/settings/tokens') },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
  ]

  return (
    <div className={`app ${sidebarOpen ? 'with-sidebar' : 'no-sidebar'} ${mobile ? 'is-mobile' : ''} ${drawer ? 'drawer-mode' : ''}`}>
      {drawer && sidebarOpen && <div className="drawer-backdrop" onClick={e => { if (e.timeStamp - drawerOpenedAt.current > 400) setSidebarOpen(false) }} />}
      <div className={`sidebar-wrap ${sidebarOpen ? 'open' : ''}`}>
        <Sidebar data={shell} onPair={startPair} onNavigate={closeDrawer} />
      </div>
      <div className="main">
        <TopBar onMenu={toggleSidebar} onPalette={() => setPalette(true)} mobile={mobile} compact={narrow} sidebarOpen={sidebarOpen} data={shell} />
        <main className="content"><div className="content-inner"><Outlet /></div></main>
      </div>
      {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} data={shell} />}
      {shortcuts && <ShortcutsSheet admin={admin} onClose={() => setShortcuts(false)} />}
      {pair && <PairModal onClose={() => { setPair(false); window.dispatchEvent(new CustomEvent('pc:paired')) }} />}
    </div>
  )
}

const SECTION: Record<string, string> = { '': 'Overview', apps: 'Apps', services: 'Services', events: 'Events', links: 'Links', devices: 'Devices', users: 'People', 'ai-connections': 'AI connections' }

function TopBar({ onMenu, onPalette, mobile, compact, sidebarOpen, data }: { onMenu: () => void; onPalette: () => void; mobile: boolean; compact: boolean; sidebarOpen: boolean; data: ShellData }) {
  const { user, themeMode, setThemeMode, status, setUser } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const appMatch = useMatch('/apps/:slug')
  const app = appMatch ? data.overview?.apps.find(a => a.slug === appMatch.params.slug) : undefined
  const section = SECTION[loc.pathname.split('/')[1] || ''] || ''
  const signOut = async () => { await api.post(`${V1}/auth/logout`).catch(() => {}); setUser(null); navigate('/login') }
  const avatarMenu = (): MenuItem[] => [
    { heading: user?.name || user?.email || '' },
    { label: 'Profile', icon: 'user', onSelect: () => navigate('/settings/profile') },
    { label: 'Settings', icon: 'settings', onSelect: () => navigate('/settings'), hint: ',' },
    { label: 'Keyboard shortcuts', icon: 'keyboard', onSelect: () => window.dispatchEvent(new CustomEvent('pc:shortcuts')), hint: '?' },
    'sep',
    { heading: 'Appearance' },
    { label: 'System', checked: themeMode === 'system', onSelect: () => setThemeMode('system') },
    { label: 'Light', checked: themeMode === 'light', onSelect: () => setThemeMode('light') },
    { label: 'Dark', checked: themeMode === 'dark', onSelect: () => setThemeMode('dark') },
    ...(status?.auth_mode === 'local' ? ['sep' as const, { label: 'Sign out', icon: 'log-out', onSelect: signOut }] : []),
  ]
  return (
    <header className="topbar">
      <button type="button" className="btn ghost icon-only" onClick={onMenu} aria-label={sidebarOpen ? 'Hide the sidebar' : 'Show the sidebar'} title="Menu ([)"><Icon name={mobile || compact ? 'menu' : 'sidebar'} size={18} /></button>
      <h1 className="range-title">
        {appMatch ? <><button type="button" className="crumb" onClick={() => navigate('/apps')}>Apps</button><Icon name="chevron-right" size={16} className="crumb-sep" /><span className="ellipsis">{app?.name || appMatch.params.slug}</span></> : section}
      </h1>
      <span className="spacer" />
      <button type="button" className="btn ghost icon-only" onClick={onPalette} aria-label="Search" title="Search (⌘K or /)"><Icon name="search" size={18} /></button>
      <MenuButton label="Account" className="avatar-btn" align="right" width={220} items={avatarMenu}>
        <Avatar name={user?.name} email={user?.email} size={30} />
      </MenuButton>
    </header>
  )
}
