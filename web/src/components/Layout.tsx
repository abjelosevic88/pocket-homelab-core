import { useState } from 'react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { Avatar } from './ui'

const nav = [
  { to: '/', label: 'Overview', ico: '◉', end: true },
  { to: '/apps', label: 'Apps', ico: '▦' },
  { to: '/services', label: 'Services', ico: '⚙' },
  { to: '/events', label: 'Events', ico: '⚡' },
  { to: '/links', label: 'Links', ico: '⇄' },
  { to: '/devices', label: 'Devices', ico: '📱' },
  { to: '/users', label: 'Users', ico: '👤' },
  { to: '/ai-connections', label: 'AI connections', shortLabel: 'AI', ico: '✧' },
  { to: '/settings', label: 'Settings', ico: '⋯' },
]

export default function Layout() {
  const { user, status, theme, toggleTheme, setUser } = useApp()
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()
  const logout = async () => { await api.post(`${V1}/auth/logout`); setUser(null); navigate('/login') }
  const title = status?.title || 'Pocket Core'
  const admin = user?.role === 'admin'
  const items = nav.filter(n => admin || ['/', '/devices', '/ai-connections', '/settings'].includes(n.to))
  return (
    <div className="app">
      <aside className={`sidebar ${open ? 'open' : ''}`} onClick={() => setOpen(false)}>
        <div className="brand"><span className="logo"><img src="/favicon.svg" alt="" /></span><span className="brand-name">{title}</span></div>
        <nav>
          {items.map(n => <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => isActive ? 'active' : ''}><span className="ico">{n.ico}</span>{n.label}</NavLink>)}
        </nav>
        <div className="foot">
          <span className="row" title={user?.email}>{user && <Avatar name={user.name} email={user.email} />}<span className="foot-id"><span>{user?.name || user?.email}</span>{status?.version && <a className="ver" href="https://github.com/abjelosevic88/pocket-homelab-core/releases" target="_blank" rel="noreferrer">v{status.version.replace(/^v/, '')}</a>}</span></span>
          <span className="row">
            <button className="btn ghost sm" onClick={toggleTheme} title="Toggle theme">{theme === 'dark' ? '☀' : '☾'}</button>
            {status?.auth_mode === 'local' && <button className="btn ghost sm" onClick={logout} title="Sign out">⎋</button>}
          </span>
        </div>
      </aside>
      <div className="main">
        <header className="topbar">
          <button className="btn ghost icon menu-btn" onClick={() => setOpen(o => !o)} aria-label="Menu">☰</button>
          <span className="topbar-title">{title}</span>
          <span className="spacer" />
          <button className="btn ghost sm" onClick={toggleTheme} title="Toggle theme">{theme === 'dark' ? '☀' : '☾'}</button>
        </header>
        <main className="content"><Outlet /></main>
        <nav className="tabbar">
          {items.filter(n => !['/users', '/settings', '/links'].includes(n.to)).map(n => <NavLink key={n.to} to={n.to} end={n.end} aria-label={n.label} className={({ isActive }) => isActive ? 'active' : ''}><span className="ico" aria-hidden="true">{n.ico}</span><span>{n.shortLabel || n.label}</span></NavLink>)}
        </nav>
      </div>
    </div>
  )
}
