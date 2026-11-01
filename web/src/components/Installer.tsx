import { useEffect, useMemo, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import { bundledIcon } from '../lib/icons'
import { formatVersion, shortName } from '../lib/format'
import { Empty, Loading, Modal, Spinner, errMsg } from './ui'
import { Icon } from './icons'

// One-click install and removal of Pocket apps (the installer agent on the
// server does the work; see docs/installer.md).

export interface Release {
  slug: string; name: string; description?: string; repo: string; repo_url: string; version: string; tag: string
  container: string; present: boolean; installed?: boolean; installed_version?: string
}
interface Catalog { status: { owner: string; apps_dir: string; archive_dir: string; public_url: string; proxy: boolean }; apps: Release[]; running_job?: string }
export interface InstallJob {
  id: string; kind: 'install' | 'uninstall'; slug: string; name: string; state: 'running' | 'done' | 'failed'; log: string[]; error?: string; open_url?: string
  result?: { dir?: string; archive?: string; container?: string; public_url?: string; port?: number; version?: string; warnings?: string[] }
}

function ReleaseIcon({ slug, name }: { slug: string; name: string }) {
  const [ok, setOk] = useState(true)
  const src = bundledIcon(slug)
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

export function JobView({ job }: { job: InstallJob | null }) {
  const end = useRef<HTMLDivElement>(null)
  useEffect(() => { end.current?.scrollIntoView({ block: 'nearest' }) }, [job?.log.length])
  if (!job) return <Loading label="Starting…" />
  const warnings = job.result?.warnings || []
  return (
    <div className="stack tight">
      <div className="inst-log" aria-live="polite">
        {job.log.map((l, i) => <div key={i} className="inst-line"><Icon name="check" size={13} />{l}</div>)}
        {job.state === 'running' && <div className="inst-line muted"><Spinner size={12} />Working…</div>}
        <div ref={end} />
      </div>
      {job.state === 'failed' && <div className="callout danger"><Icon name="alert" size={15} /><span style={{ whiteSpace: "pre-wrap" }}>{job.error}</span></div>}
      {warnings.map((w, i) => <div key={i} className="callout warn"><Icon name="info" size={15} /><span>{w}</span></div>)}
      {job.state === 'done' && job.kind === 'install' && <div className="callout success"><Icon name="check-circle" size={15} /><span><b>{job.name}</b> is installed and connected to Pocket Core{job.result?.dir ? <> — files in <span className="inline-code">{job.result.dir}</span></> : null}.</span></div>}
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
  const load = async (refresh = false) => {
    setRefreshing(refresh)
    try { setCat(await api.get<Catalog>(`${V1}/installer${refresh ? '?refresh=1' : ''}`)); setErr('') } catch (e) { setErr(errMsg(e)) }
    setRefreshing(false)
  }
  useEffect(() => { load() }, [])
  useEffect(() => { if (cat?.running_job && !jobId) setJobId(cat.running_job) }, [cat, jobId])
  const { job } = useJob(jobId, j => { if (j.state === 'done') onInstalled() })
  const install = async (slug: string) => {
    setStarting(slug)
    try { const j = await api.post<InstallJob>(`${V1}/installer/install`, { slug }); setJobId(j.id) } catch (e) { setErr(errMsg(e)) }
    setStarting('')
  }
  const list = useMemo(() => {
    const t = q.trim().toLowerCase()
    const all = (cat?.apps || []).filter(a => !t || a.name.toLowerCase().includes(t) || (a.description || '').toLowerCase().includes(t) || a.slug.includes(t))
    return [...all.filter(a => !a.installed), ...all.filter(a => a.installed)]
  }, [cat, q])

  if (jobId) {
    const running = !job || job.state === 'running'
    return (
      <Modal title={job ? `${job.kind === 'install' ? 'Installing' : 'Removing'} ${job.name}` : 'Installing'} onClose={onClose} size="lg"
        footer={<>
          {job?.state === 'done' && job.open_url && <a className="btn primary" href={job.open_url} target="_blank" rel="noreferrer">Open {shortName(job.name)}<Icon name="external" size={14} /></a>}
          {!running && <button type="button" className="btn" onClick={() => { setJobId(null); load() }}>Install another</button>}
          <button type="button" className="btn" onClick={onClose}>{running ? 'Hide' : 'Close'}</button>
        </>}>
        {running && <p className="muted small" style={{ marginTop: 0 }}>You can close this; the install keeps going on the server.</p>}
        <JobView job={job} />
      </Modal>
    )
  }

  const avail = list.filter(a => !a.installed).length
  return (
    <Modal title="Install an app" onClose={onClose} size="lg">
      <div className="stack tight">
        <p className="muted small" style={{ marginTop: 0 }}>
          Newest releases from <a href={`https://github.com/${cat?.status.owner || ''}`} target="_blank" rel="noreferrer">github.com/{cat?.status.owner || '…'}</a>.
          One click pulls the release image, starts it in its own folder{cat?.status.proxy ? ', adds its HTTPS address' : ''} and connects it here: sign-in, links to your other apps and your phones.
        </p>
        <div className="row">
          <input className="grow" type="search" placeholder="Search apps" value={q} onChange={e => setQ(e.target.value)} autoFocus />
          <button type="button" className="btn" onClick={() => load(true)} disabled={refreshing} title="Check GitHub for new releases now">{refreshing ? <Spinner /> : <Icon name="refresh" size={15} />}Refresh</button>
        </div>
        {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        {!cat && !err ? <Loading label="Reading releases from GitHub…" /> : list.length === 0 ? <Empty icon="package" title={q ? 'Nothing matches' : 'No releases found'} /> : (
          <div className="inst-list">
            {avail === 0 && !q && <p className="muted small">Every released app is installed already.</p>}
            {list.map(a => (
              <div key={a.slug} className={`inst-row${a.installed ? ' done' : ''}`}>
                <ReleaseIcon slug={a.slug} name={a.name} />
                <div className="grow">
                  <div className="row wrap" style={{ gap: 6 }}><b>{a.name}</b><span className="muted small">{formatVersion(a.version)}</span></div>
                  {a.description && <div className="muted small inst-desc">{a.description}</div>}
                  {!a.installed && cat?.status.public_url && <div className="muted small mono">{cat.status.public_url.replace('{slug}', a.slug)}</div>}
                </div>
                {a.installed ? <span className="pill ok">Installed{a.installed_version ? ` · ${formatVersion(a.installed_version)}` : ''}</span>
                  : a.present ? <span className="pill" title={`A container named ${a.container} already exists on the server but is not registered here. Start it with CORE_URL and the registration key, or remove it first.`}>Container exists</span>
                  : <button type="button" className="btn primary sm" disabled={!!starting} onClick={() => install(a.slug)}>{starting === a.slug ? <Spinner /> : <Icon name="download" size={14} />}Install</button>}
              </div>
            ))}
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
          <p style={{ marginTop: 0 }}>This takes <b>{app.name}</b> off the server:</p>
          <ul className="inst-steps">
            <li>stops and deletes its containers</li>
            <li>moves its folder, data included, to the archive — nothing is deleted</li>
            {app.public_url && <li>removes its HTTPS site <span className="inline-code">{app.public_url.replace(/^https?:\/\//, '')}</span></li>}
            <li>unregisters it here: its sign-in, service entry, webhooks and app links</li>
          </ul>
          {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        </div>
      ) : <JobView job={job} />}
      {coreOnly && <div className="callout warn mt"><Icon name="info" size={15} /><span className="grow">Only unregister it here? The app keeps running on the server and registers again on its next start.</span>
        <button type="button" className="btn sm danger" onClick={unregister}>Unregister</button></div>}
    </Modal>
  )
}
