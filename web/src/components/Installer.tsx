import { useEffect, useMemo, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import { bundledIcon } from '../lib/icons'
import { formatVersion, shortName } from '../lib/format'
import { Empty, Loading, Modal, Spinner, errMsg } from './ui'
import { Icon } from './icons'
import { useApp } from '../lib/app-context'

// One-click install and removal of Pocket apps (the installer agent on the
// server does the work; see docs/installer.md).

export interface Requires {
  docker_socket?: boolean; host_network?: boolean; privileged?: boolean; capabilities?: string[]; devices?: string[]; host_folders?: string[]; services?: string[]
}
export interface Release {
  slug: string; name: string; description?: string; repo: string; repo_url: string; version: string; tag: string
  container: string; present: boolean; installed?: boolean; installed_version?: string
  category?: string; icon?: string; page?: string; digest?: string; platforms?: string[]; requires?: Requires
  available?: boolean; running?: string; updatable?: boolean; update_note?: string
  archive?: { name: string; saved_at: string }
}
interface Catalog {
  status: { owner: string; apps_dir: string; archive_dir: string; public_url: string; proxy: boolean; source?: string; source_url?: string; platform?: string }
  apps: Release[]; running_job?: string
}
export interface InstallJob {
  id: string; kind: 'install' | 'update' | 'uninstall'; slug: string; name: string; state: 'running' | 'done' | 'failed'; log: string[]; error?: string; open_url?: string
  result?: { dir?: string; archive?: string; container?: string; public_url?: string; port?: number; version?: string; warnings?: string[] }
}

function ReleaseIcon({ slug, name, icon }: { slug: string; name: string; icon?: string }) {
  const [ok, setOk] = useState(true)
  const src = bundledIcon(slug) || icon
  return <span className="app-tile-icon inst-icon">{src && ok ? <img src={src} alt="" onError={() => setOk(false)} /> : shortName(name)[0]}</span>
}

/** Follows a job until it ends. */
function useJob(id: string | null, onEnd?: (j: InstallJob) => void) {
  const [job, setJob] = useState<InstallJob | null>(null)
  const [err, setErr] = useState('')
  const ended = useRef(onEnd)
  ended.current = onEnd
  useEffect(() => {
    if (!id) return
    let stop = false
    const tick = async () => {
      try {
        const j = await api.get<InstallJob>(`${V1}/installer/jobs/${id}`)
        if (stop) return
        setJob(j)
        if (j.state !== 'running') { ended.current?.(j); return }
      } catch (e) { if (!stop) setErr(errMsg(e)) }
      if (!stop) setTimeout(tick, 1000)
    }
    tick()
    return () => { stop = true }
  }, [id])
  return { job, err }
}

const semver = (v?: string) => (v || '').replace(/^v/, '').split('.').map(n => parseInt(n, 10))
/** a < b for X.Y.Z versions. */
export function newer(a?: string, b?: string) {
  const x = semver(a), y = semver(b)
  if (x.length !== 3 || y.length !== 3 || [...x, ...y].some(isNaN)) return false
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] < y[i]
  return false
}

/** What the app asks of the server, shown before Install. */
function RequireChips({ r }: { r?: Requires }) {
  if (!r) return null
  const chips: [string, string][] = []
  if (r.docker_socket) chips.push(['Docker socket', 'Controls Docker on this server (can start and stop containers)'])
  if (r.host_network) chips.push(['Host network', "Shares the server's network instead of its own"])
  if (r.privileged) chips.push(['Privileged', 'Runs with full access to the server'])
  for (const c of r.capabilities || []) chips.push([c, 'Linux capability ' + c])
  for (const d of r.devices || []) chips.push([d, 'Uses the device ' + d])
  for (const f of r.host_folders || []) chips.push([f, 'Mounts ' + f + ' from the server; edit its .env to point it at your folder'])
  if (!chips.length && !r.services?.length) return null
  return (
    <div className="row wrap inst-req" style={{ gap: 4 }}>
      {chips.map(([label, title]) => <span key={label} className="badge warn" title={title}>{label}</span>)}
      {r.services?.length ? <span className="badge" title={r.services.join('\n')}>+{r.services.length} container{r.services.length > 1 ? 's' : ''}</span> : null}
    </div>
  )
}

export function JobView({ job }: { job: InstallJob | null }) {
  const end = useRef<HTMLDivElement>(null)
  useEffect(() => { end.current?.scrollIntoView({ block: 'nearest' }) }, [job?.log?.length])
  if (!job) return <Loading label="Starting…" />
  const warnings = job.result?.warnings || []
  return (
    <div className="stack tight">
      <div className="inst-log" aria-live="polite">
        {(job.log || []).map((l, i) => <div key={i} className="inst-line"><Icon name="check" size={13} />{l}</div>)}
        {job.state === 'running' && <div className="inst-line muted"><Spinner size={12} />Working…</div>}
        <div ref={end} />
      </div>
      {job.state === 'failed' && <div className="callout danger"><Icon name="alert" size={15} /><span style={{ whiteSpace: "pre-wrap" }}>{job.error}</span></div>}
      {warnings.map((w, i) => <div key={i} className="callout warn"><Icon name="info" size={15} /><span>{w}</span></div>)}
      {job.state === 'done' && job.kind === 'install' && <div className="callout success"><Icon name="check-circle" size={15} /><span><b>{job.name}</b> is installed and connected to Pocket Core{job.result?.dir ? <> — files in <span className="inline-code">{job.result.dir}</span></> : null}.</span></div>}
      {job.state === 'done' && job.kind === 'update' && <div className="callout success"><Icon name="check-circle" size={15} /><span><b>{job.name}</b> is updated{job.result?.version ? <> to {formatVersion(job.result.version)}</> : null}.</span></div>}
      {job.state === 'done' && job.kind === 'uninstall' && <div className="callout success"><Icon name="check-circle" size={15} /><span><b>{job.name}</b> is removed.{job.result?.archive ? <> Its folder was archived to <span className="inline-code">{job.result.archive}</span>.</> : null}</span></div>}
    </div>
  )
}

export function InstallModal({ onClose, onInstalled }: { onClose: () => void; onInstalled: () => void }) {
  const [cat, setCat] = useState<Catalog | null>(null)
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const [starting, setStarting] = useState('')
  const [fresh, setFresh] = useState<Record<string, boolean>>({}) // slugs to install without their archived data
  const load = async (refresh = false) => {
    setRefreshing(refresh)
    try { setCat(await api.get<Catalog>(`${V1}/installer${refresh ? '?refresh=1' : ''}`)); setErr('') } catch (e) { setErr(errMsg(e)) }
    setRefreshing(false)
  }
  useEffect(() => { load() }, [])
  useEffect(() => { if (cat?.running_job && !jobId) setJobId(cat.running_job) }, [cat, jobId])
  const { job } = useJob(jobId, j => { if (j.state === 'done') onInstalled() })
  const install = async (a: Release) => {
    setStarting(a.slug)
    const restore_from = a.archive && !fresh[a.slug] ? a.archive.name : undefined
    try { const j = await api.post<InstallJob>(`${V1}/installer/install`, { slug: a.slug, name: a.name, restore_from }); setJobId(j.id) } catch (e) { setErr(errMsg(e)) }
    setStarting('')
  }
  const update = async (a: Release) => {
    setStarting(a.slug)
    try { const j = await api.post<InstallJob>(`${V1}/apps/${a.slug}/upgrade`); setJobId(j.id) } catch (e) { setErr(errMsg(e)) }
    setStarting('')
  }
  const behind = (a: Release) => !!a.installed && newer(a.installed_version || a.running, a.version)
  const list = useMemo(() => {
    const t = q.trim().toLowerCase()
    const all = (cat?.apps || []).filter(a => !t || a.name.toLowerCase().includes(t) || (a.description || '').toLowerCase().includes(t) || a.slug.includes(t) || (a.category || '').includes(t))
    return [...all.filter(behind), ...all.filter(a => !a.installed), ...all.filter(a => a.installed && !behind(a))]
  }, [cat, q])

  if (jobId) {
    const running = !job || job.state === 'running'
    return (
      <Modal title={job ? `${job.kind === 'install' ? 'Installing' : job.kind === 'update' ? 'Updating' : 'Removing'} ${job.name}` : 'Working'} onClose={onClose} size="lg"
        footer={<>
          {job?.state === 'done' && job.open_url && <a className="btn primary" href={job.open_url} target="_blank" rel="noreferrer">Open {shortName(job.name)}<Icon name="external" size={14} /></a>}
          {!running && <button type="button" className="btn" onClick={() => { setJobId(null); load() }}>Install another</button>}
          <button type="button" className="btn" onClick={onClose}>{running ? 'Hide' : 'Close'}</button>
        </>}>
        {running && <p className="muted small" style={{ marginTop: 0 }}>You can close this; the work keeps going on the server.</p>}
        <JobView job={job} />
      </Modal>
    )
  }

  const avail = list.filter(a => !a.installed).length
  const updates = list.filter(behind).length
  const signed = cat?.status.source === 'catalog'
  const box = cat?.status.source === 'box'
  return (
    <Modal title="Install an app" onClose={onClose} size="lg">
      <div className="stack tight">
        <p className="muted small" style={{ marginTop: 0 }}>
          {box ? <>Every Pocket app is ready for this Pocket Box, and new ones arrive with its nightly updates. One click installs an app and connects it here: sign-in, links to your other apps and your phones. One the box doesn't carry yet (Pocket Box Lite, or a box set up with a few bundles) is downloaded first, which takes a few minutes. Removing one switches it off; its data stays on the box, so installing it again brings everything back. </>
            : <>{signed ? <>Newest releases from the signed Pocket app catalog. </> : <>Newest releases from <a href={`https://github.com/${cat?.status.owner || ''}`} target="_blank" rel="noreferrer">github.com/{cat?.status.owner || '…'}</a>. </>}
          One click pulls the release image, starts it in its own folder{cat?.status.proxy ? ', adds its HTTPS address' : ''} and connects it here: sign-in, links to your other apps and your phones.</>}
        </p>
        <div className="row">
          <input className="grow" type="search" placeholder="Search apps" value={q} onChange={e => setQ(e.target.value)} autoFocus />
          <button type="button" className="btn" onClick={() => load(true)} disabled={refreshing} title="Check for new releases now">{refreshing ? <Spinner /> : <Icon name="refresh" size={15} />}Refresh</button>
        </div>
        {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        {!cat && !err ? <Loading label="Reading the app catalog…" /> : list.length === 0 ? <Empty icon="package" title={q ? 'Nothing matches' : 'No releases found'} /> : (
          <div className="inst-list">
            {updates > 0 && !q && <p className="muted small">{updates} update{updates > 1 ? 's' : ''} available.</p>}
            {avail === 0 && !q && <p className="muted small">Every released app is installed already.</p>}
            {list.map(a => {
              const late = behind(a)
              const off = a.available === false
              return (
                <div key={a.slug} className={`inst-row${a.installed && !late ? ' done' : ''}`}>
                  <ReleaseIcon slug={a.slug} name={a.name} icon={a.icon} />
                  <div className="grow">
                    <div className="row wrap" style={{ gap: 6 }}>
                      {a.page ? <a href={a.page} target="_blank" rel="noreferrer"><b>{a.name}</b></a> : <b>{a.name}</b>}
                      <span className="muted small">{formatVersion(a.version)}</span>
                      {a.category && <span className="badge cat">{a.category}</span>}
                    </div>
                    {a.description && <div className="muted small inst-desc">{a.description}</div>}
                    {a.installed && late && <div className="small inst-update">Running {formatVersion(a.installed_version || a.running)}{!a.updatable && a.update_note ? <span className="muted"> — {a.update_note}</span> : null}</div>}
                    {!a.installed && a.archive && !a.present && (
                      <label className="row small inst-restore" style={{ gap: 6 }}>
                        <input type="checkbox" checked={!fresh[a.slug]} onChange={e => setFresh(f => ({ ...f, [a.slug]: !e.target.checked }))} />
                        Bring back your data from {new Date(a.archive.saved_at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}
                      </label>
                    )}
                    {!a.installed && <RequireChips r={a.requires} />}
                    {!a.installed && cat?.status.public_url && <div className="muted small mono inst-url">{cat.status.public_url.replace('{slug}', a.slug).replace('{port}', '<port>')}</div>}
                  </div>
                  {a.installed ? (late && a.updatable
                    ? <button type="button" className="btn primary sm" disabled={!!starting} onClick={() => update(a)} title={`${formatVersion(a.installed_version || a.running)} → ${formatVersion(a.version)}`}>{starting === a.slug ? <Spinner /> : <Icon name="refresh" size={14} />}Update</button>
                    : <span className="pill ok" title={a.installed_version ? `Running ${formatVersion(a.installed_version)}` : undefined}>Installed</span>)
                    : a.present ? <span className="pill" title={`A container named ${a.container} already exists on the server but is not registered here. Start it with CORE_URL and the registration key, or remove it first.`}>Container exists</span>
                    : off ? <span className="pill" title={`Built for ${(a.platforms || []).join(', ')}`}>Not for {cat?.status.platform || 'this server'}</span>
                    : <button type="button" className="btn primary sm" disabled={!!starting} onClick={() => install(a)}>{starting === a.slug ? <Spinner /> : <Icon name="download" size={14} />}Install</button>}
                </div>
              )
            })}
          </div>
        )}
      </div>
    </Modal>
  )
}

/** Remove: stop it on the server, archive its folder, drop its HTTPS site, unregister. */
export function RemoveModal({ app, onClose, onRemoved }: { app: { slug: string; name: string; public_url?: string; url: string }; onClose: () => void; onRemoved: () => void }) {
  const [jobId, setJobId] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [coreOnly, setCoreOnly] = useState(false)
  const box = !!useApp().status?.box_system
  const { job } = useJob(jobId)
  // Leave the (now gone) app's page only once the result was read.
  const close = job?.state === 'done' ? onRemoved : onClose
  const start = async () => {
    setBusy(true); setErr('')
    try { const j = await api.post<InstallJob>(`${V1}/apps/${app.slug}/uninstall`); setJobId(j.id) } catch (e) { setErr(errMsg(e)) }
    setBusy(false)
  }
  const unregister = async () => { await api.del(`${V1}/apps/${app.slug}`); onRemoved() }
  const failed = !!err || job?.state === 'failed'
  const running = !!jobId && (!job || job.state === 'running')
  return (
    <Modal title={`Remove ${app.name}`} onClose={close}
      footer={<>
        {failed && <button type="button" className="btn" onClick={() => setCoreOnly(true)}>Remove from Core only…</button>}
        <span className="spacer" />
        <button type="button" className="btn" onClick={close}>{job?.state === 'done' ? 'Close' : running ? 'Hide' : 'Cancel'}</button>
        {!jobId && <button type="button" className="btn danger" onClick={start} disabled={busy}>{busy ? <Spinner /> : <Icon name="trash" size={14} />}Remove</button>}
      </>}>
      {!jobId ? (
        <div className="stack tight">
          <p style={{ marginTop: 0 }}>This takes <b>{app.name}</b> off the {box ? 'box' : 'server'}:</p>
          {box ? <ul className="inst-steps">
            <li>switches it off: it no longer runs or starts with the box</li>
            <li>keeps its data on the box — installing it again (Apps → Install an app) brings everything back</li>
            <li>unregisters it here: its sign-in, service entry, webhooks and app links</li>
          </ul> : <ul className="inst-steps">
            <li>stops and deletes its containers</li>
            <li>moves its folder, data included, to the archive — nothing is deleted, and installing it again brings the data back</li>
            {app.public_url && <li>removes its HTTPS site <span className="inline-code">{app.public_url.replace(/^https?:\/\//, '')}</span></li>}
            <li>unregisters it here: its sign-in, service entry, webhooks and app links</li>
          </ul>}
          {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        </div>
      ) : <JobView job={job} />}
      {coreOnly && <div className="callout warn mt"><Icon name="info" size={15} /><span className="grow">Only unregister it here? The app keeps running on the server and registers again on its next start.</span>
        <button type="button" className="btn sm danger" onClick={unregister}>Unregister</button></div>}
    </Modal>
  )
}
