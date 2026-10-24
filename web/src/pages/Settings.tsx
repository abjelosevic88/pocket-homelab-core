import { AskButton } from '../components/AskButton'
import React, { useEffect, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { uptime, when } from '../lib/format'
import type { OIDCClient, Settings as S, SystemInfo } from '../lib/types'
import { useApp, type ThemeMode } from '../lib/app-context'
import { Confirm, Field, Loading, Modal, Secret, Segmented, SettingRow, Tabs, useAsync, useIsMobile, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { CredentialsModal, Tokens } from './Users'
import { Select } from '../components/Select'
import { MyLinks } from './Links'
import { SignInMethods } from './SignInMethods'

type TabId = 'box' | 'sso' | 'profile' | 'links' | 'logins' | 'tokens' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string; admin?: boolean; member?: boolean }[] = [
  { id: 'box', label: 'Box', icon: 'server', sub: 'The box’s name and title, and the key Pocket apps use to join it.', admin: true },
  { id: 'sso', label: 'Single sign-on', icon: 'shield', sub: 'How people sign in to Core (passwords, PocketID…), and the services that sign people in with their Core account.', admin: true },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, password and how Core looks for you.' },
  { id: 'links', label: 'Apps acting for me', icon: 'nodes', sub: 'Pocket apps that can do things in another app as you. Switching one off stops it at once, for you only.' },
  { id: 'logins', label: 'My logins', icon: 'key', sub: 'Your own login for each service. Pocket apps only ever use these — the household’s shared key stays in Core.' },
  { id: 'tokens', label: 'API tokens', icon: 'code', sub: 'Personal tokens for scripts, Raycast and Homepage widgets.' },
  { id: 'about', label: 'About', icon: 'info', sub: 'Version, the server and its numbers.' },
]

export default function Settings() {
  const { tab: raw } = useParams()
  const { user } = useApp()
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const admin = user?.role === 'admin'
  const tabs = TABS.filter(t => (!t.admin || admin) && (!t.member || !admin))
  const tab: TabId = tabs.find(t => t.id === raw)?.id || tabs[0].id
  const current = tabs.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })

  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.querySelector('.modal-backdrop, .menu, .sel-pop')) navigate('/') }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate])

  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back to Pocket Core" title="Back (esc)" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
        <span style={{ flex: 1 }} />
        <AskButton />
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={tabs.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {tabs.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner" key={tab}>
            <div className="set-title">
              <h2>{current.label}</h2>
              <p>{current.sub}</p>
            </div>
            {tab === 'box' && <Box />}
            {tab === 'sso' && <OIDCClients />}
            {tab === 'profile' && <Profile />}
            {tab === 'links' && <MyLinks />}
            {tab === 'logins' && user && <MyCredentials />}
            {tab === 'tokens' && <Tokens />}
            {tab === 'about' && <About />}
          </div>
        </main>
      </div>
    </div>
  )
}

/** A settings card: title, explanation, actions on the right — Pocket Calendar's section. */
function Section({ title, sub, icon, actions, children }: { title: React.ReactNode; sub?: React.ReactNode; icon?: string; actions?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        {icon && <span className="set-app-ico"><Icon name={icon} size={17} /></span>}
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children && <div className="set-section-body">{children}</div>}
    </section>
  )
}

function Box() {
  const { refresh } = useApp()
  const { toast } = useToast()
  const { data: st, reload } = useAsync(() => api.get<S>(`${V1}/settings`), [])
  const [form, setForm] = useState<{ title?: string; box_name?: string }>({})
  const [rot, setRot] = useState(false)
  if (!st) return <Loading />
  const save = async (e: React.FormEvent) => { e.preventDefault(); await api.put(`${V1}/settings`, form); await reload(); await refresh(); toast('Saved') }
  return (
    <div className="stack loose">
      <Section title="This box" icon="server">
        <form onSubmit={save}>
          <SettingRow title="Box name" help="Shown to paired devices and on the login page."><input defaultValue={st.box_name} onChange={e => setForm(f => ({ ...f, box_name: e.target.value }))} style={{ width: 240 }} /></SettingRow>
          <SettingRow title="Title" help="The name in the sidebar and the browser tab."><input defaultValue={st.title} onChange={e => setForm(f => ({ ...f, title: e.target.value }))} style={{ width: 240 }} /></SettingRow>
          <div className="form-actions"><button className="btn primary">Save changes</button></div>
        </form>
      </Section>
      <Section title="App registration key" icon="key" sub="Apps present this once to join the box; afterwards they use their own token. Rotating it does not affect apps already registered."
        actions={<button type="button" className="btn sm" onClick={() => setRot(true)}><Icon name="refresh" size={14} />Rotate</button>}>
        <Secret value={st.registration_key} />
        <p className="muted small mt-s">A Pocket app needs <code>CORE_URL={window.location.origin}</code> and <code>CORE_REGISTRATION_KEY</code> with this value.</p>
      </Section>
      {rot && <Confirm title="Rotate registration key" message="Apps that have not registered yet will need the new key. Registered apps keep working." onConfirm={async () => { await api.post(`${V1}/settings/registration-key`); setRot(false); reload(); toast('Rotated') }} onCancel={() => setRot(false)} danger={false} confirmLabel="Rotate" />}
    </div>
  )
}

function Profile() {
  const { user, refresh, themeMode, setThemeMode } = useApp()
  const { toast } = useToast()
  const [me, setMe] = useState({ name: user?.name || '', password: '', current_password: '' })
  const saveMe = async (e: React.FormEvent) => { e.preventDefault(); await api.put(`${V1}/auth/me`, me); setMe(m => ({ ...m, password: '', current_password: '' })); await refresh(); toast('Profile saved') }
  return (
    <div className="stack loose">
      <form onSubmit={saveMe} className="stack loose">
        <Section title="You" icon="user">
          <SettingRow title="Name"><input value={me.name} onChange={e => setMe(m => ({ ...m, name: e.target.value }))} autoComplete="name" style={{ width: 240 }} /></SettingRow>
          <SettingRow title="Email" help="Admins change it under People."><span className="muted">{user?.email}</span></SettingRow>
          <SettingRow title="Role"><span className="badge">{user?.role}</span></SettingRow>
        </Section>
        <Section title="Password" icon="lock" sub="Leave both empty to keep the current one.">
          <div className="two-col tight">
            <Field label="Current password"><input type="password" value={me.current_password} onChange={e => setMe(m => ({ ...m, current_password: e.target.value }))} autoComplete="current-password" /></Field>
            <Field label="New password"><input type="password" value={me.password} onChange={e => setMe(m => ({ ...m, password: e.target.value }))} autoComplete="new-password" /></Field>
          </div>
        </Section>
        <div className="row"><button className="btn primary">Save profile</button></div>
      </form>
      <Section title="Appearance" icon="palette">
        <SettingRow title="Theme" help="System follows your device's light or dark setting.">
          <Segmented size="sm" value={themeMode} onChange={(m: ThemeMode) => setThemeMode(m)} options={[{ value: 'system', label: 'System' }, { value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }]} />
        </SettingRow>
      </Section>
    </div>
  )
}

function MyCredentials() {
  const { user } = useApp()
  const [open, setOpen] = useState(false)
  if (!user) return null
  return (
    <Section title="Service logins" icon="key" sub="Pocket apps reach a service (Navidrome, Immich, Jellyfin…) only with your own login from here — admins included. Your phone receives the household's credentials unless you set your own."
      actions={<button type="button" className="btn sm primary" onClick={() => setOpen(true)}><Icon name="edit" size={14} />Edit my logins</button>}>
      {open && <CredentialsModal user={user} onClose={() => setOpen(false)} />}
    </Section>
  )
}

function About() {
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const { data: sys, error } = useAsync(() => api.get<SystemInfo>(`${V1}/system`), [])
  if (error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>
  if (!sys) return <Loading />
  const c = sys.counts || {}
  const tiles: [string, number | undefined, string][] = [['Apps', c.apps, 'grid'], ['Services', c.services, 'server'], ['Devices', c.devices, 'smartphone'], ['People', c.users, 'users']]
  const rows: [string, React.ReactNode][] = [
    ['Version', <span key="v"><code>{sys.version}</code> · {sys.go} · {sys.os}</span>],
    ['Uptime', `${uptime(sys.uptime_s)} (since ${when(sys.started_at)})`],
    ['Base URL', <code key="b">{sys.base_url}</code>],
    ['OIDC issuer', <span key="i"><code>{sys.issuer}</code><div className="muted small">{sys.issuer}/.well-known/openid-configuration</div></span>],
    ['Auth mode', sys.auth_mode],
    ['Data', <code key="d">{sys.data_dir}</code>],
    ['Health poll', `every ${sys.health_interval_s}s · events kept ${Math.round(sys.event_retention_h / 24)}d`],
    ['Rows', <span key="r" className="small">{Object.entries(c).map(([k, v]) => `${k} ${v}`).join(' · ')}</span>],
  ]
  return (
    <div className="stack loose">
      <div className="set-about-hero">
        <img src="/favicon.svg" alt="" width={48} height={48} />
        <div>
          <h3>{sys.box_name || 'Pocket Core'}</h3>
          <p className="muted small">The hub of the Pocket apps: the registry, the event bus, single sign-on, pairing, services and the links between apps.</p>
        </div>
      </div>
      <div className="set-stats">
        {tiles.map(([label, n, icon]) => (
          <div key={label} className="set-stat"><Icon name={icon} size={16} /><span className="set-stat-n tnum">{n ?? 0}</span><span className="set-stat-l">{label}</span></div>
        ))}
      </div>
      <Section title="This server" icon="hub">
        <dl className="set-dl">
          {rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </Section>
      <div className="row wrap">
        {admin && <a className="btn" href={`${V1}/backup`}><Icon name="download" size={15} />Download database backup</a>}
        <a className="btn" href="https://github.com/abjelosevic88/pocket-homelab-core/releases" target="_blank" rel="noreferrer"><Icon name="external" size={15} />Releases</a>
      </div>
    </div>
  )
}

function OIDCClients() {
  const { toast } = useToast()
  const [sp, setSp] = useSearchParams()
  const { data, reload } = useAsync(() => api.get<OIDCClient[]>(`${V1}/oidc/clients`), [])
  const [edit, setEdit] = useState<{ id?: number; client_id?: string; name: string; redirect_uris: string; public: boolean } | null>(null)
  const [secret, setSecret] = useState<{ id: string; secret: string } | null>(null)
  const [del, setDel] = useState<OIDCClient | null>(null)
  const issuer = window.location.origin
  useEffect(() => { if (sp.get('add')) { setEdit({ name: '', redirect_uris: '', public: false }); sp.delete('add'); setSp(sp, { replace: true }) } }, [sp, setSp])
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!edit) return
    const body = { client_id: edit.client_id, name: edit.name, redirect_uris: edit.redirect_uris.split('\n').map(s => s.trim()).filter(Boolean), public: edit.public }
    const r = edit.id ? await api.put<{ client: OIDCClient; client_secret: string }>(`${V1}/oidc/clients/${edit.id}`, body) : await api.post<{ client: OIDCClient; client_secret: string }>(`${V1}/oidc/clients`, body)
    setEdit(null); reload()
    if (r.client_secret) setSecret({ id: r.client.client_id, secret: r.client_secret }); else toast('Saved')
  }
  const rotate = async (c: OIDCClient) => { const r = await api.put<{ client: OIDCClient; client_secret: string }>(`${V1}/oidc/clients/${c.id}`, { rotate_secret: true }); setSecret({ id: c.client_id, secret: r.client_secret }) }
  return (
    <div className="stack loose">
      <SignInMethods />
      <Section title="Clients" icon="shield" sub={<>Point Immich, Nextcloud, Paperless, Audiobookshelf, Kavita, Jellyfin (SSO plugin) or any Pocket app at <code>{issuer}</code> and everyone signs in with their Core account. Pocket apps that ask in their manifest get a client automatically.</>}
        actions={<button type="button" className="btn sm primary" onClick={() => setEdit({ name: '', redirect_uris: '', public: false })}><Icon name="plus" size={14} />Add client</button>}>
        {!data ? <Loading /> : data.length === 0 ? <p className="muted small">No clients yet.</p> : <div className="client-list">{data.map(c => (
          <div key={c.id} className="client-row">
            <div className="client-head">
              <div className="grow">
                <div className="client-name"><b>{c.name}</b>{c.app_id ? <span className="badge cat">app</span> : null}{!c.has_secret && <span className="badge">PKCE</span>}</div>
                <div className="mono small muted">{c.client_id}</div>
              </div>
              <div className="client-actions">{c.has_secret && <button type="button" className="btn sm" onClick={() => rotate(c)}>Secret</button>}<button type="button" className="btn sm" onClick={() => setEdit({ id: c.id, client_id: c.client_id, name: c.name, redirect_uris: c.redirect_uris.join('\n'), public: !c.has_secret })}>Edit</button><button type="button" className="btn sm ghost icon-only danger-text" onClick={() => setDel(c)} aria-label={`Delete ${c.name}`} title="Delete"><Icon name="trash" size={14} /></button></div>
            </div>
            <div className="client-redirects mono small">{c.redirect_uris.map(u => <div key={u}>{u}</div>)}</div>
          </div>))}</div>}
      </Section>
      <Section title="Discovery" icon="globe" sub="What a relying party asks for.">
        <dl className="set-dl">
          <div><dt>Issuer</dt><dd><code>{issuer}</code></dd></div>
          <div><dt>Discovery</dt><dd><code>{issuer}/.well-known/openid-configuration</code></dd></div>
          <div><dt>Scopes</dt><dd><code>openid profile email groups</code></dd></div>
        </dl>
      </Section>
      {edit && <Modal title={edit.id ? 'Edit client' : 'Add OIDC client'} onClose={() => setEdit(null)}>
        <form onSubmit={save} className="stack">
          <Field label="Name"><input required value={edit.name} onChange={e => setEdit(v => v && ({ ...v, name: e.target.value }))} placeholder="Immich" /></Field>
          <Field label="Client id" help="Optional; generated when empty"><input value={edit.client_id || ''} disabled={!!edit.id} onChange={e => setEdit(v => v && ({ ...v, client_id: e.target.value }))} /></Field>
          <Field label="Redirect URIs" help="One per line, exact match"><textarea required value={edit.redirect_uris} onChange={e => setEdit(v => v && ({ ...v, redirect_uris: e.target.value }))} placeholder={'https://photos.example.com/auth/login\napp.immich:///oauth-callback'} /></Field>
          {!edit.id && <Field label="Client type"><Select ariaLabel="Client type" value={edit.public ? 'public' : 'confidential'} onChange={v => setEdit(x => x && ({ ...x, public: v === 'public' }))} options={[{ value: 'confidential', label: 'Confidential', hint: 'client secret' }, { value: 'public', label: 'Public', hint: 'PKCE, no secret' }]} /></Field>}
          <div className="form-actions"><button type="button" className="btn" onClick={() => setEdit(null)}>Cancel</button><button className="btn primary">Save</button></div>
        </form></Modal>}
      {secret && <Modal title="Client credentials" onClose={() => setSecret(null)}>
        <div className="callout success mb">The secret is shown once. Configure the relying party with:</div>
        <dl className="kv"><dt>Issuer</dt><dd className="mono">{issuer}</dd><dt>Discovery</dt><dd className="mono">{issuer}/.well-known/openid-configuration</dd><dt>Client id</dt><dd className="mono">{secret.id}</dd><dt>Scopes</dt><dd className="mono">openid profile email groups</dd></dl>
        <div className="mt-sm"><Secret value={secret.secret} /></div>
        <div className="form-actions mt"><button type="button" className="btn primary" onClick={() => setSecret(null)}>Done</button></div></Modal>}
      {del && <Confirm title="Delete client" message={<>Delete <b>{del.name}</b>? Logins through it stop immediately.</>} onConfirm={async () => { await api.del(`${V1}/oidc/clients/${del.id}`); setDel(null); reload() }} onCancel={() => setDel(null)} confirmLabel="Delete" />}
    </div>
  )
}
