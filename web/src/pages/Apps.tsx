import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { App, OIDCClient, Settings, Webhook } from '../lib/types'
import { useApp } from '../lib/app-context'
import { Card, Confirm, Dot, Empty, Field, Loading, Modal, PageHeader, Secret, useAsync, useToast } from '../components/ui'

export default function Apps() {
  const { slug } = useParams()
  const { user } = useApp()
  const { data, loading, reload } = useAsync(() => api.get<App[]>(`${V1}/apps`), [])
  const [settings, setSettings] = useState<Settings | null>(null)
  useEffect(() => { if (user?.role === 'admin') api.get<Settings>(`${V1}/settings`).then(setSettings).catch(() => {}) }, [user])
  if (slug) return <AppDetail slug={slug} onChange={reload} />
  if (loading && !data) return <Loading />
  const apps = data || []
  return (
    <>
      <PageHeader title="Apps" sub="Pocket apps that registered themselves with this box." actions={<button className="btn" onClick={reload}>Refresh</button>} />
      {apps.length === 0 ? <Card><Empty title="No apps registered">Give each Pocket app <span className="inline-code">CORE_URL</span> and <span className="inline-code">CORE_REGISTRATION_KEY</span>; it registers on start and shows up here.</Empty></Card> :
        <Card flush>
          <table className="table">
            <thead><tr><th></th><th>App</th><th>Version</th><th>URL</th><th>Prefix</th><th>Last seen</th><th></th></tr></thead>
            <tbody>{apps.map(a => <tr key={a.id}>
              <td><Dot status={a.status} /></td>
              <td><Link to={`/apps/${a.slug}`}><b>{a.name}</b></Link><div className="muted small">{a.slug}{a.description ? ` · ${a.description}` : ''}</div></td>
              <td>{a.version || '—'}</td>
              <td className="mono small">{a.url}{a.public_url && a.public_url !== a.url && <div className="muted">{a.public_url}</div>}</td>
              <td className="mono small">{a.env_prefix}_URL</td>
              <td className="muted small" title={when(a.last_seen_at)}>{ago(a.last_seen_at || a.last_check_at)}{a.status === 'down' && <div className="bad">{a.last_error}</div>}</td>
              <td className="right"><Link className="btn sm" to={`/apps/${a.slug}`}>Open</Link></td>
            </tr>)}</tbody>
          </table>
        </Card>}
      {settings && <Card title="Register an app" className="mt">
        <p className="muted small">An app announces itself with one POST on start. Apps built with the Go SDK only need two variables:</p>
        <pre>{`CORE_URL=${window.location.origin}\nCORE_REGISTRATION_KEY=${settings.registration_key}`}</pre>
        <p className="muted small">The first registration returns a per-app token the app stores; the key is never needed again. Rotate it under Settings if it leaks. Full manifest format: <code>docs/manifest.md</code>.</p>
      </Card>}
    </>
  )
}

interface Detail { app: App; webhooks: Webhook[]; oidc_client: OIDCClient | null }

function AppDetail({ slug, onChange }: { slug: string; onChange: () => void }) {
  const { toast } = useToast()
  const navigate = useNavigate()
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const { data, loading, error, reload } = useAsync(() => api.get<Detail>(`${V1}/apps/${slug}`), [slug])
  const [edit, setEdit] = useState(false)
  const [confirm, setConfirm] = useState<'delete' | 'rotate' | null>(null)
  const [newToken, setNewToken] = useState('')
  const [form, setForm] = useState<Partial<App>>({})
  if (loading && !data) return <Loading />
  if (error || !data) return <div className="callout danger">{error || 'Not found'}</div>
  const { app: a, webhooks, oidc_client: oc } = data
  const check = async () => { await api.post(`${V1}/apps/${slug}/check`); await reload(); toast('Checked') }
  const save = async (e: React.FormEvent) => { e.preventDefault(); await api.put(`${V1}/apps/${slug}`, form); setEdit(false); await reload(); onChange(); toast('Saved') }
  const rotate = async () => { const r = await api.post<{ token: string }>(`${V1}/apps/${slug}/rotate`); setNewToken(r.token); setConfirm(null) }
  const del = async () => { await api.del(`${V1}/apps/${slug}`); onChange(); navigate('/apps') }
  const man = a.manifest as { publishes?: string[]; subscribes?: { topics: string; url: string }[] }
  return (
    <>
      <PageHeader title={<span className="row"><Dot status={a.status} />{a.name}</span>} sub={<span>{a.slug} · {a.description || 'no description'}</span>}
        actions={<><Link className="btn" to="/apps">← Apps</Link><button className="btn" onClick={check}>Check now</button>{admin && <button className="btn" onClick={() => { setForm({ name: a.name, description: a.description, url: a.url, public_url: a.public_url, health_path: a.health_path, env_prefix: a.env_prefix, icon: a.icon }); setEdit(true) }}>Edit</button>}</>} />
      <div className="two-col">
        <Card title="Registration">
          <dl className="kv">
            <dt>Status</dt><dd>{a.status}{a.last_error && <span className="bad"> — {a.last_error}</span>}</dd>
            <dt>Version</dt><dd>{a.version || '—'}</dd>
            <dt>URL</dt><dd className="mono">{a.url}</dd>
            <dt>Public URL</dt><dd className="mono">{a.public_url || <span className="muted">same as URL</span>}</dd>
            <dt>Health</dt><dd className="mono">{a.health_path}</dd>
            <dt>Env prefix</dt><dd className="mono">{a.env_prefix}_URL · {a.env_prefix}_API_KEY</dd>
            <dt>Token</dt><dd className="mono">{a.token_prefix}…</dd>
            <dt>Last seen</dt><dd>{when(a.last_seen_at) || 'never'}</dd>
            <dt>Last check</dt><dd>{when(a.last_check_at) || 'never'}</dd>
            <dt>Registered</dt><dd>{when(a.created_at)}</dd>
          </dl>
          {admin && <div className="form-actions mt"><button className="btn" onClick={() => setConfirm('rotate')}>Rotate token</button><button className="btn danger" onClick={() => setConfirm('delete')}>Remove app</button></div>}
          {newToken && <div className="mt"><div className="callout success mb-sm">New app token — set it in the app's environment or delete its token file so it re-registers with the key.</div><Secret value={newToken} /></div>}
        </Card>
        <div className="grid" style={{ gap: 16 }}>
          <Card title="Events">
            <div className="small"><b>Publishes:</b> {man.publishes?.length ? man.publishes.map(t => <span key={t} className="badge cat" style={{ marginRight: 4 }}>{t}</span>) : <span className="muted">not declared</span>}</div>
            <div className="small mt-sm"><b>Webhooks:</b></div>
            {webhooks.length === 0 ? <p className="muted small">none</p> : <table className="table compact"><tbody>{webhooks.map(w => <tr key={w.id}><td className="mono small">{w.topics}</td><td className="mono small">{w.url}</td><td>{w.enabled ? <span className="ok">on</span> : <span className="muted">off</span>}</td></tr>)}</tbody></table>}
            <p className="muted small mt-sm">Manage and test webhooks under <Link to="/events">Events</Link>.</p>
          </Card>
          <Card title="Single sign-on">
            {oc ? <dl className="kv"><dt>Client id</dt><dd className="mono">{oc.client_id}</dd><dt>Secret</dt><dd>{oc.has_secret ? 'set' : 'public client (PKCE)'}</dd><dt>Redirects</dt><dd className="mono">{oc.redirect_uris.join('\n')}</dd><dt>Issuer</dt><dd className="mono">{window.location.origin}</dd></dl>
              : <p className="muted small">This app did not ask for an OIDC client in its manifest. Add one under Settings → Single sign-on if it supports OpenID Connect.</p>}
          </Card>
        </div>
      </div>
      <Card title="Manifest" className="mt"><pre>{JSON.stringify(a.manifest, null, 2)}</pre></Card>
      {edit && <Modal title="Edit app" onClose={() => setEdit(false)}>
        <form onSubmit={save} className="grid" style={{ gap: 12 }}>
          <Field label="Name"><input value={form.name || ''} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} /></Field>
          <Field label="Description"><input value={form.description || ''} onChange={e => setForm(f => ({ ...f, description: e.target.value }))} /></Field>
          <Field label="URL" help="How Core and sibling apps reach it"><input value={form.url || ''} onChange={e => setForm(f => ({ ...f, url: e.target.value }))} /></Field>
          <Field label="Public URL" help="What paired phones open; empty = same as URL"><input value={form.public_url || ''} onChange={e => setForm(f => ({ ...f, public_url: e.target.value }))} /></Field>
          <div className="two-col tight"><Field label="Health path"><input value={form.health_path || ''} onChange={e => setForm(f => ({ ...f, health_path: e.target.value }))} /></Field><Field label="Env prefix"><input value={form.env_prefix || ''} onChange={e => setForm(f => ({ ...f, env_prefix: e.target.value }))} /></Field></div>
          <Field label="Icon" help="Absolute URL or a path under the app URL"><input value={form.icon || ''} onChange={e => setForm(f => ({ ...f, icon: e.target.value }))} /></Field>
          <div className="form-actions"><button type="button" className="btn" onClick={() => setEdit(false)}>Cancel</button><button className="btn primary">Save</button></div>
        </form>
      </Modal>}
      {confirm === 'delete' && <Confirm title="Remove app" message={<>Remove <b>{a.name}</b> from the registry? Its service entry, webhooks and SSO client go with it. The app itself keeps running and can register again.</>} onConfirm={del} onCancel={() => setConfirm(null)} />}
      {confirm === 'rotate' && <Confirm title="Rotate app token" message="The current token stops working immediately. You will need to give the app the new one." onConfirm={rotate} onCancel={() => setConfirm(null)} danger={false} />}
    </>
  )
}
