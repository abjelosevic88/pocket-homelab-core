import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import { useApp } from '../lib/app-context'
import { Card, Confirm, CopyField, Empty, Field, Loading, Modal, PageHeader, Segmented, SettingRow, Switch, errMsg, useAsync, useToast } from '../components/ui'
import { Icon } from '../components/icons'

interface RemoteApp { slug: string; name: string; kind: 'app' | 'service'; direct: string; address: string; exposed: boolean; device_default: boolean; passkey: boolean; sensitive: boolean }
interface RemoteKey { id: number; app: string; created_at: string; last_seen_at: string; has_passkey: boolean }
interface RemoteDevice { id: number; user_id: number; user_name: string; user_email: string; name: string; created_at: string; last_seen_at: string; last_ip: string; apps: Record<string, boolean>; keys: RemoteKey[] }
interface RemoteStatus { connected: boolean; since?: string; last_error?: string; sessions: number }
interface RemoteAdmin {
  enabled: boolean; provider: 'cloudflare' | 'custom'; pattern: string; relay_url: string; has_secret: boolean
  cf_account: string; cf_worker: string; has_cf_token: boolean; cf_deployed: boolean; cf_current: boolean; cf_domains: string[] | null; cf_syncing: boolean; cf_error: string; cf_ready: string[] | null
  portal: string; box_url: string; status: RemoteStatus; apps: RemoteApp[]; devices: RemoteDevice[]
}
interface RemoteMine { enabled: boolean; connected: boolean; portal: string; apps: RemoteApp[]; devices: RemoteDevice[] }

export default function Remote() {
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const { data: adm, reload: reloadAdmin } = useAsync(() => admin ? api.get<RemoteAdmin>(`${V1}/remote`) : Promise.resolve(null), [admin])
  const { data: mine, loading, reload: reloadMine } = useAsync(() => api.get<RemoteMine>(`${V1}/me/remote`), [])
  const [pair, setPair] = useState(false)
  const reload = () => { reloadMine(); if (admin) reloadAdmin() }
  // The relay status changes on its own (connecting, reconnecting): keep it fresh.
  useEffect(() => {
    if (!admin) return
    const t = setInterval(() => { if (!document.hidden) reloadAdmin() }, 5000)
    return () => clearInterval(t)
  }, [admin, reloadAdmin])
  if (loading && !mine) return <Loading />
  if (admin && !adm) return <Loading />
  const enabled = admin ? !!adm?.enabled : !!mine?.enabled
  const apps = admin ? (adm?.apps || []) : (mine?.apps || [])
  const devices = admin ? (adm?.devices || []) : (mine?.devices || [])
  return (
    <>
      <PageHeader title="Remote access" sub="Use your apps from anywhere without Tailscale. Traffic goes through a relay you control and is encrypted end to end between your browser and Core: the relay never sees what you do."
        actions={enabled ? <button type="button" className="btn primary" onClick={() => setPair(true)}><Icon name="qr" size={15} />Pair a device</button> : undefined} />
      {admin && adm && <RelayCard d={adm} onChange={reloadAdmin} />}
      {admin && adm && <AppsCard d={adm} onChange={reloadAdmin} />}
      {!admin && !enabled && <Card><Empty icon="globe" title="Remote access is off">An admin can switch it on here in Core.</Empty></Card>}
      {enabled && <DevicesCard devices={devices} apps={apps.filter(a => a.exposed)} admin={admin} portal={admin ? adm!.portal : mine!.portal} onChange={reload} onPair={() => setPair(true)} />}
      {pair && <RemotePairModal apps={apps.filter(a => a.exposed)} onClose={() => { setPair(false); reload() }} />}
    </>
  )
}

function StatusPill({ d }: { d: RemoteAdmin }) {
  if (!d.enabled) return <span className="pill">Off</span>
  if (d.status.connected) return <span className="pill ok" title={d.status.since ? `Since ${when(d.status.since)}` : undefined}>Connected · {d.status.sessions} open</span>
  return <span className="pill err" title={d.status.last_error}>Not connected</span>
}

function RelayCard({ d, onChange }: { d: RemoteAdmin; onChange: () => void }) {
  const { toast } = useToast()
  const [provider, setProvider] = useState(d.provider)
  const [pattern, setPattern] = useState(d.pattern)
  const [relayURL, setRelayURL] = useState(d.relay_url)
  const [account, setAccount] = useState(d.cf_account)
  const [token, setToken] = useState('')
  const [worker, setWorker] = useState(d.cf_worker)
  const [busy, setBusy] = useState('')
  const [secret, setSecret] = useState('')
  const [reset, setReset] = useState(false)
  useEffect(() => { setProvider(d.provider); setPattern(d.pattern); setRelayURL(d.relay_url); setAccount(d.cf_account); setWorker(d.cf_worker) }, [d.provider, d.pattern, d.relay_url, d.cf_account, d.cf_worker]) // not on every status poll: that would wipe what is being typed
  const dirty = provider !== d.provider || pattern !== d.pattern || relayURL !== d.relay_url || account !== d.cf_account || worker !== d.cf_worker || token !== ''
  const run = async (what: string, f: () => Promise<unknown>, ok?: string) => {
    setBusy(what)
    try { await f(); if (ok) toast(ok); onChange() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  const save = () => run('save', async () => { await api.put(`${V1}/remote`, { provider, pattern, relay_url: relayURL, cf_account: account, cf_worker: worker, cf_token: token }); setToken('') }, 'Saved.')
  const example = (pattern || '{app}.remote.example.org')
  const showSecret = () => run('secret', async () => setSecret((await api.get<{ secret: string }>(`${V1}/remote/secret`)).secret))
  const portalHost = d.portal.replace(/^https:\/\//, '')
  const deployBlock = !pattern ? 'Enter the address pattern and save, then deploy.' : !d.has_cf_token && !token ? 'Enter the API token and save, then deploy.' : dirty ? 'Save your changes first, then deploy.' : ''
  return (
    <Card title="Relay" actions={<StatusPill d={d} />}>
      <SettingRow title="Remote access" help={d.enabled ? 'On. Paired devices reach the apps switched on below.' : 'Off. Nothing on this box is reachable through the relay.'}>
        <Switch on={d.enabled} label="Remote access" disabled={!!busy || (!d.enabled && !d.pattern)} title={!d.pattern ? 'Set the address pattern first' : undefined}
          onChange={on => run('enable', () => api.put(`${V1}/remote`, { enabled: on }), on ? 'Remote access is on.' : 'Remote access is off. Open sessions were closed.')} />
      </SettingRow>
      {d.status.last_error && d.enabled && !d.status.connected && <div className="callout danger mt-sm">{d.status.last_error}</div>}
      <div className="grid mt" style={{ gap: 14 }}>
        <Field label="Where the relay runs" group>
          <Segmented value={provider} onChange={v => setProvider(v)} options={[{ value: 'cloudflare', label: 'Cloudflare' }, { value: 'custom', label: 'Self-hosted' }]} />
        </Field>
        <Field label="Address pattern" help={<>Each app gets its own address, {'{app}'} is replaced by its name: <span className="mono">{example.replace('{app}', 'money')}</span>. The launcher is <span className="mono">{example.replace('{app}', 'pocket')}</span>.</>}>
          <input value={pattern} onChange={e => setPattern(e.target.value)} placeholder="e.g. {app}.remote.example.org" spellCheck={false} autoComplete="off" />
        </Field>
        {provider === 'cloudflare' ? <>
          <p className="muted small">Core uploads a small Worker to <b>your own</b> Cloudflare account (the free Workers plan is enough) and adds one address per app you switch on. The API token needs <i>Account · Workers Scripts · Edit</i> and, on the zone, <i>Workers Routes · Edit</i>, <i>DNS · Edit</i> and <i>Zone · Read</i>.</p>
          <div className="grid" style={{ gap: 14, gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))' }}>
            <Field label="Account ID"><input value={account} onChange={e => setAccount(e.target.value)} spellCheck={false} autoComplete="off" /></Field>
            <Field label="API token"><input type="password" value={token} onChange={e => setToken(e.target.value)} placeholder={d.has_cf_token ? '•••••••• stored' : ''} autoComplete="new-password" /></Field>
            <Field label="Worker name"><input value={worker} onChange={e => setWorker(e.target.value)} spellCheck={false} /></Field>
          </div>
          <div className="row" style={{ flexWrap: 'wrap', gap: 8 }}>
            <button type="button" className="btn" disabled={!!busy || dirty || !d.pattern || !d.has_cf_token} title={deployBlock || undefined}
              onClick={() => run('deploy', () => api.post(`${V1}/remote/deploy`), 'Uploaded to Cloudflare.')}>
              <Icon name="upload" size={15} />{busy === 'deploy' ? 'Uploading…' : d.cf_deployed ? 'Upload again' : 'Deploy to Cloudflare'}</button>
            {d.cf_deployed && <span className={`pill ${d.cf_current ? 'ok' : 'warn'}`}>{d.cf_current ? 'Worker up to date' : 'Newer Worker available'}</span>}
          </div>
          {deployBlock && !busy && <p className="muted small">{deployBlock}</p>}
          {!!d.cf_domains?.length && <div><div className="muted small" style={{ marginBottom: 6 }}>Addresses at Cloudflare</div>
            <div className="chips">{d.cf_domains.map(h => <a key={h} className="chip sm mono" href={`https://${h}`} target="_blank" rel="noreferrer">{h}</a>)}</div></div>}
        </> : <>
          <p className="muted small">Run the relay on any server with a public address, behind a TLS proxy that answers for the whole pattern (a wildcard certificate). It is the same image as Core:</p>
          <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{`docker run -d --name pocket-relay --restart unless-stopped -p 8080:8080 \\
  -e RELAY_SECRET=${secret || '<relay secret>'} \\
  -e RELAY_PORTAL=${d.portal || 'https://pocket.remote.example.org'} \\
  ghcr.io/abjelosevic88/pocket-homelab-core relay`}</pre>
          <Field label="Relay address (where Core connects)" help={<>Leave empty to use the launcher address{portalHost ? <> (<span className="mono">{portalHost}</span>)</> : ''}.</>}>
            <input value={relayURL} onChange={e => setRelayURL(e.target.value)} placeholder={d.portal || 'https://pocket.remote.example.org'} spellCheck={false} />
          </Field>
          <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
            {secret ? <div className="grow"><CopyField value={secret} label="Relay secret" /></div> : <button type="button" className="btn" disabled={!d.has_secret || !!busy} onClick={showSecret}><Icon name="key" size={15} />Show relay secret</button>}
            <button type="button" className="btn ghost" disabled={!!busy} onClick={() => run('rotate', async () => setSecret((await api.post<{ secret: string }>(`${V1}/remote/secret`)).secret), 'New secret. Restart the relay with it.')}>New secret</button>
          </div>
        </>}
        {dirty && <div className="form-actions">
          <button type="button" className="btn primary" disabled={!!busy} onClick={save}>{busy === 'save' ? 'Saving…' : 'Save changes'}</button>
        </div>}
      </div>
      <div style={{ marginTop: 14 }} />
      <SettingRow title="Reset remote access" help="Core gets a new key and forgets every paired device; everyone pairs again. For when a device or the relay may be compromised.">
        <button type="button" className="btn sm danger-ghost" disabled={!!busy} onClick={() => setReset(true)}>Reset…</button>
      </SettingRow>
      {reset && <Confirm title="Reset remote access" confirmLabel="Reset"
        message={<>Core makes a new key and forgets <b>every paired device</b>. Everyone pairs again. Use this if a device or the relay may be compromised.</>}
        onConfirm={() => { setReset(false); run('reset', () => api.post(`${V1}/remote/reset`), 'Reset. Every device must pair again.') }} onCancel={() => setReset(false)} />}
    </Card>
  )
}

function AppsCard({ d, onChange }: { d: RemoteAdmin; onChange: () => void }) {
  const { toast } = useToast()
  // Switches flip at once; the server answers fast and Cloudflare catches up
  // in the background. Overrides hold the new value until a reload shows it.
  const [local, setLocal] = useState<Record<string, Partial<RemoteApp>>>({})
  useEffect(() => {
    setLocal(prev => {
      const next: typeof prev = {}
      for (const [slug, o] of Object.entries(prev)) {
        const a = d.apps.find(x => x.slug === slug)
        if (a && Object.entries(o).some(([k, v]) => a[k as keyof RemoteApp] !== v)) next[slug] = o
      }
      return next
    })
  }, [d])
  const set = async (a: RemoteApp, patch: Partial<RemoteApp>) => {
    setLocal(prev => ({ ...prev, [a.slug]: { ...prev[a.slug], ...patch } }))
    try {
      await api.put(`${V1}/remote/apps/${a.slug}`, patch)
      onChange()
    } catch (e) {
      setLocal(prev => { const { [a.slug]: _, ...rest } = prev; return rest })
      toast(errMsg(e), 'err')
    }
  }
  const apps = d.apps.map(a => ({ ...a, ...local[a.slug] }))
  const exposed = apps.filter(a => a.exposed).length
  const cf = d.provider === 'cloudflare' && d.cf_deployed
  const where = (a: RemoteApp) => {
    const host = a.address.replace(/^https:\/\//, '')
    if (!cf) return <div className="muted small mono">{host}</div>
    if (!(d.cf_domains || []).includes(host)) return <div className="muted small remote-pending"><span className="spinner" style={{ width: 11, height: 11 }} />Adding {host} at Cloudflare…</div>
    if (!(d.cf_ready || []).includes(host)) return <div className="muted small remote-pending"><span className="spinner" style={{ width: 11, height: 11 }} />Waiting for the certificate of {host} (a minute or two)</div>
    return <div className="muted small mono">{host}</div>
  }
  return (
    <Card title="Apps" sub={`${exposed} of ${apps.length} on. Switch on the apps you want away from home; every paired device gets them (switch one off for a single device under Devices). An app that is off has no remote address at all.`} className="mt">
      {d.cf_error && !d.cf_syncing && <div className="callout danger" style={{ marginBottom: 12 }}>Cloudflare did not update the addresses: {d.cf_error}</div>}
      {([['app', 'Pocket apps'], ['service', 'Services']] as const).map(([kind, title]) => {
        const list = apps.filter(a => (a.kind || 'app') === kind)
        if (!list.length) return null
        return <div key={kind} className="remote-group">
          <h3 className="remote-group-title">{title}{kind === 'service' && <span className="muted small"> · devices sign in with their own login for each service</span>}</h3>
          <div className="remote-apps">{list.map(a => <div key={a.slug} className="remote-app">
        <div className="remote-app-name"><b>{a.name}</b>{a.sensitive && <span className="badge warn" title={a.slug === 'ai' ? 'Runs commands on this server' : 'Manages the whole box'}>sensitive</span>}
          {a.exposed && a.address && where(a)}</div>
        <div className="remote-app-switches">
          {a.exposed && <label className="remote-passkey" title="Ask for Face ID, Touch ID or the device PIN when the app is opened (again after 15 minutes without use)">
            <input type="checkbox" checked={a.passkey} onChange={e => set(a, { passkey: e.target.checked })} />Passkey</label>}
          <Switch on={a.exposed} disabled={!d.pattern} label={`${a.name} remotely`} title={!d.pattern ? 'Set the address pattern first' : a.exposed ? 'On' : 'Off'}
            onChange={v => set(a, v ? { exposed: true, device_default: true } : { exposed: false })} />
        </div>
      </div>)}</div>
        </div>
      })}
    </Card>
  )
}

function DevicesCard({ devices, apps, admin, portal, onChange, onPair }: { devices: RemoteDevice[]; apps: RemoteApp[]; admin: boolean; portal: string; onChange: () => void; onPair: () => void }) {
  const { toast } = useToast()
  const [del, setDel] = useState<RemoteDevice | null>(null)
  const [rename, setRename] = useState<{ d: RemoteDevice; name: string } | null>(null)
  const [busy, setBusy] = useState(0)
  const setApp = async (d: RemoteDevice, slug: string, on: boolean) => {
    setBusy(d.id)
    try { await api.put(`${V1}/me/remote/devices/${d.id}`, { apps: { [slug]: on } }); onChange() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(0) }
  }
  const appName = (slug: string) => slug === 'pocket' ? 'Launcher' : apps.find(a => a.slug === slug)?.name || slug
  return (
    <Card title={admin ? 'Devices' : 'Your devices'} sub={<>Open <a href={portal} target="_blank" rel="noreferrer" className="mono">{portal.replace(/^https:\/\//, '')}</a> on a paired device to see your apps.</>} className="mt">
      {devices.length === 0 ? <Empty icon="smartphone" title="No devices paired">Tap “Pair a device” and scan the code with your phone. Each device gets its own key, one per app address, that never leaves it.</Empty> :
        <div className="grid" style={{ gap: 12 }}>
          {devices.map(d => <div key={d.id} className="card" style={{ margin: 0 }}>
            <div className="card-head remote-device-head"><div className="grow">
              <h2 style={{ fontSize: 15 }}>{d.name}</h2>
              <p className="muted small">{admin && <>{d.user_name || d.user_email} · </>}paired {when(d.created_at)}{d.last_seen_at && <> · last seen {ago(d.last_seen_at)}{d.last_ip && <> from {d.last_ip}</>}</>}</p>
            </div><div className="row">
              <button type="button" className="btn sm ghost" onClick={() => setRename({ d, name: d.name })}>Rename</button>
              <button type="button" className="btn sm danger" onClick={() => setDel(d)}>Remove</button>
            </div></div>
            <div className="card-body">
              {apps.length === 0 ? <p className="muted small">No apps are reachable remotely yet.</p> :
                apps.map(a => {
                  const on = d.apps[a.slug] ?? a.device_default
                  const key = d.keys.find(k => k.app === a.slug)
                  return <SettingRow key={a.slug} title={a.name}
                    help={key ? <>Opened {ago(key.last_seen_at || key.created_at)}{key.has_passkey ? ' · passkey set up' : ''}</> : a.passkey ? 'Asks for a passkey' : undefined}>
                    <Switch size="sm" on={on} disabled={busy === d.id} label={`${a.name} on ${d.name}`} onChange={v => setApp(d, a.slug, v)} />
                  </SettingRow>
                })}
              {d.keys.some(k => k.app !== 'pocket' && !apps.find(a => a.slug === k.app)) && <p className="muted small mt-sm">Also paired: {d.keys.filter(k => k.app !== 'pocket' && !apps.find(a => a.slug === k.app)).map(k => appName(k.app)).join(', ')} (not reachable now).</p>}
            </div>
          </div>)}
        </div>}
      <div className="form-actions mt"><button type="button" className="btn" onClick={onPair}><Icon name="qr" size={15} />Pair a device</button></div>
      {del && <Confirm title="Remove device" message={<>Remove <b>{del.name}</b>? Every key it holds stops working at once and open sessions close. Pair again to restore.</>}
        onConfirm={async () => { try { await api.del(`${V1}/me/remote/devices/${del.id}`) } catch (e) { toast(errMsg(e), 'err') } setDel(null); onChange() }} onCancel={() => setDel(null)} />}
      {rename && <Modal title="Rename device" size="sm" onClose={() => setRename(null)} footer={<>
        <button type="button" className="btn" onClick={() => setRename(null)}>Cancel</button>
        <button type="button" className="btn primary" disabled={!rename.name.trim()} onClick={async () => {
          try { await api.put(`${V1}/me/remote/devices/${rename.d.id}`, { name: rename.name.trim() }); setRename(null); onChange() } catch (e) { toast(errMsg(e), 'err') }
        }}>Save</button></>}>
        <Field label="Name"><input autoFocus value={rename.name} maxLength={60} onChange={e => setRename({ ...rename, name: e.target.value })} /></Field>
      </Modal>}
    </Card>
  )
}

function RemotePairModal({ apps, onClose }: { apps: RemoteApp[]; onClose: () => void }) {
  const [p, setP] = useState<{ url: string; qr_svg: string; expires_at: string } | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [left, setLeft] = useState(600)
  // The apps the new device starts with; each can be changed later under Devices.
  const [on, setOn] = useState<Record<string, boolean>>(() => Object.fromEntries(apps.map(a => [a.slug, a.device_default])))
  // Only what differs from the defaults is kept for the device.
  const changed = Object.fromEntries(apps.filter(a => !!on[a.slug] !== a.device_default).map(a => [a.slug, !!on[a.slug]]))
  const make = async () => {
    setBusy(true); setErr('')
    try { setP(await api.post<{ url: string; qr_svg: string; expires_at: string }>(`${V1}/me/remote/pair`, { apps: changed })) } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  useEffect(() => { if (!apps.length) make() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!p) return
    const t = setInterval(() => setLeft(Math.max(0, Math.round((new Date(p.expires_at).getTime() - Date.now()) / 1000))), 1000)
    return () => clearInterval(t)
  }, [p])
  const count = apps.filter(a => on[a.slug]).length
  // An unused link should not outlive the dialog.
  const close = () => { if (p) api.del(`${V1}/me/remote/pair`).catch(() => {}); onClose() }
  return (
    <Modal title="Pair a device for remote access" onClose={close}
      footer={!p && apps.length > 0 ? <>
        <button type="button" className="btn" onClick={close}>Cancel</button>
        <button type="button" className="btn primary" disabled={busy} onClick={make}><Icon name="qr" size={15} />{busy ? 'Making a code…' : 'Make pairing code'}</button>
      </> : undefined}>
      {err && <div className="callout danger">{err}</div>}
      {!p && apps.length > 0 && <div className="grid" style={{ gap: 6 }}>
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
          <p className="muted small">Which apps this device can open ({count} of {apps.length}). Change them any time under Devices.</p>
          <div className="row" style={{ gap: 4 }}>
            <button type="button" className="btn sm ghost" onClick={() => setOn(Object.fromEntries(apps.map(a => [a.slug, true])))}>All</button>
            <button type="button" className="btn sm ghost" onClick={() => setOn(Object.fromEntries(apps.map(a => [a.slug, false])))}>None</button>
          </div>
        </div>
        {apps.map(a => <SettingRow key={a.slug} title={a.name} help={a.kind === 'service' ? 'Service · signs in with its own login' : undefined}>
          <Switch size="sm" on={!!on[a.slug]} label={a.name} onChange={v => setOn(prev => ({ ...prev, [a.slug]: v }))} />
        </SettingRow>)}
      </div>}
      {!p && !apps.length && !err && <p className="muted">Making a code…</p>}
      {p && <div className="grid" style={{ gap: 14, justifyItems: 'center' }}>
        <div className="qr"><img src={p.qr_svg} alt="Pairing QR code" /></div>
        <p className="muted small" style={{ textAlign: 'center', maxWidth: 400 }}>Scan with the phone's camera, or open the link on the device you want to pair. The page creates a key that never leaves that device. {left > 0 ? <>The code works once and expires in {Math.floor(left / 60)}:{String(left % 60).padStart(2, '0')}.</> : <b>The code expired; close and make a new one.</b>}</p>
        <div style={{ width: '100%' }}><CopyField value={p.url} label="Pairing link" /></div>
        {apps.length > 0 && <p className="muted small" style={{ textAlign: 'center' }}>Starts with {count === apps.length ? 'every app' : count === 0 ? 'no apps' : apps.filter(a => on[a.slug]).map(a => a.name).join(', ')}. <button type="button" className="btn sm ghost" onClick={() => setP(null)}>Change</button></p>}
        <p className="muted small" style={{ textAlign: 'center', maxWidth: 400 }}>Tip: on iPhone, add the launcher and each app to the Home Screen from Safari's Share menu.</p>
      </div>}
    </Modal>
  )
}
