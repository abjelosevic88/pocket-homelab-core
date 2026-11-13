import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { bundledIcon } from '../lib/icons'
import { shortName } from '../lib/format'
import type { Release } from '../components/Installer'
import { Icon } from '../components/icons'
import { Loading, Spinner, errMsg } from '../components/ui'

// First-run setup of a Core that is not on a Pocket Box (install.sh on any Linux machine): pick the
// apps to start with, as Pocket Box's bundles or one by one, and Core installs them one after the
// other (internal/server/setup.go). On a Pocket Box, BoxWizard does this.

interface Bundle { id: string; name: string; description: string; apps: string[] }
interface QueueItem { slug: string; name: string; state: 'waiting' | 'installing' | 'done' | 'failed'; error?: string }
interface Setup { pending: boolean; bundles: { base: string[]; bundles: Bundle[] }; install: { running: boolean; items: QueueItem[] } }

type StepId = 'apps' | 'install' | 'done'
const STEPS: { id: StepId; icon: string; title: string; sub: string }[] = [
  { id: 'apps', icon: 'grid', title: 'Choose your apps', sub: 'Start with a bundle, then tick or untick single apps. Everything else stays one click away under Apps → Install app.' },
  { id: 'install', icon: 'download', title: 'Installing', sub: 'One app after the other. You can close this page: the installs carry on, and each app shows up under Apps when it is ready.' },
  { id: 'done', icon: 'check', title: 'All set', sub: 'Your apps are ready. Each one signs you in with your Pocket Core account.' },
]
const DEFAULT_BUNDLE = 'essentials'

function AppIcon({ a }: { a: Release }) {
  const [ok, setOk] = useState(true)
  const src = bundledIcon(a.slug) || a.icon
  return <span className="setup-app-icon">{src && ok ? <img src={src} alt="" onError={() => setOk(false)} /> : shortName(a.name)[0]}</span>
}

// The service the catalog lists first ("immich-server (ghcr.io/immich-app/immich-server:…)"), as
// people know it: Immich, SearXNG, ONLYOFFICE…
const KNOWN: Record<string, string> = { immich: 'Immich', searxng: 'SearXNG', onlyoffice: 'ONLYOFFICE', gotenberg: 'Gotenberg', drawio: 'draw.io', navidrome: 'Navidrome', jellyfin: 'Jellyfin', bazarr: 'Bazarr', libretranslate: 'LibreTranslate', guacd: 'Guacamole', sockpuppetbrowser: 'a browser' }
function serviceName(svc: string): string {
  const [key, img = ''] = svc.replace(')', '').split(' (')
  const fromImage = img.split(':')[0].split('/').pop() || ''
  for (const n of [key.split('-')[0], fromImage.split('-')[0], fromImage]) if (KNOWN[n]) return KNOWN[n]
  return key
}

/** What an app brings along, so a big one (Immich, ONLYOFFICE…) is no surprise. */
function extra(a: Release): string {
  const r = a.requires
  if (!r) return ''
  if (r.services?.length) return `+ ${serviceName(r.services[0])}`
  if (r.docker_socket || r.host_network || r.privileged) return 'needs extra access'
  return ''
}

function AppTile({ a, on, toggle }: { a: Release; on: boolean; toggle: (slug: string) => void }) {
  return (
    <label className={`setup-app ${on || a.present ? 'on' : ''} ${a.present ? 'have' : ''}`} title={a.description}>
      <input type="checkbox" checked={on || a.present} disabled={a.present} onChange={() => toggle(a.slug)} />
      <AppIcon a={a} />
      <span className="grow">
        <b>{shortName(a.name)}</b>
        <span>{a.present ? 'Installed' : extra(a) || a.description}</span>
      </span>
      <span className="setup-check"><Icon name="check" size={12} /></span>
    </label>
  )
}

export default function SetupWizard() {
  const navigate = useNavigate()
  const { refresh, status } = useApp()
  const [setup, setSetup] = useState<Setup | null>(null)
  const [apps, setApps] = useState<Release[] | null>(null)
  const [err, setErr] = useState('')
  const [step, setStep] = useState<StepId>('apps')
  const [bundles, setBundles] = useState<Set<string>>(new Set())
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [all, setAll] = useState(false) // the whole catalog, to tick single apps

  // The bundles, the catalog, and whether installs are already under way (a reload mid-install).
  useEffect(() => {
    Promise.all([api.get<Setup>(`${V1}/first-apps`), api.get<{ apps: Release[] }>(`${V1}/installer`)])
      .then(([s, cat]) => {
        setSetup(s)
        setApps(cat.apps.filter(a => a.slug !== 'core' && a.available !== false))
        if (s.install.items.length) { setStep(s.install.running ? 'install' : 'done'); return }
        const first = s.bundles.bundles.find(b => b.id === DEFAULT_BUNDLE) || s.bundles.bundles[0]
        if (first) { setBundles(new Set([first.id])); setPicked(new Set(first.apps)) }
      })
      .catch(e => setErr(errMsg(e)))
  }, [])

  // While installing: follow the queue.
  useEffect(() => {
    if (step !== 'install') return
    let stop = false
    const tick = async () => {
      try {
        const s = await api.get<Setup>(`${V1}/first-apps`)
        if (stop) return
        setSetup(s)
        if (!s.install.running) { setStep('done'); return }
      } catch { /* a restart of Core: ask again */ }
      if (!stop) setTimeout(tick, 2000)
    }
    tick()
    return () => { stop = true }
  }, [step])

  const known = useMemo(() => new Map((apps || []).map(a => [a.slug, a])), [apps])
  const categories = useMemo(() => {
    const by = new Map<string, Release[]>()
    for (const a of [...(apps || [])].sort((x, y) => x.name.localeCompare(y.name))) {
      const c = a.category || 'other'
      by.set(c, [...(by.get(c) || []), a])
    }
    return [...by.entries()].sort(([a], [b]) => a.localeCompare(b))
  }, [apps])
  const chosen = [...picked].filter(s => known.has(s) && !known.get(s)!.present)
  const installed = (apps || []).filter(a => a.present).map(a => a.slug)

  const toggleBundle = (b: Bundle) => {
    const on = new Set(bundles)
    const next = new Set(picked)
    if (on.has(b.id)) {
      on.delete(b.id)
      // Untick the bundle's apps unless another chosen bundle has them too.
      const keep = new Set((setup?.bundles.bundles || []).filter(o => on.has(o.id)).flatMap(o => o.apps))
      b.apps.forEach(s => { if (!keep.has(s)) next.delete(s) })
    } else {
      on.add(b.id)
      b.apps.forEach(s => next.add(s))
    }
    setBundles(on)
    setPicked(next)
  }
  const toggleApp = (slug: string) => {
    const next = new Set(picked)
    if (next.has(slug)) next.delete(slug); else next.add(slug)
    setPicked(next)
  }

  const install = async () => {
    setBusy(true); setErr('')
    try {
      await api.post(`${V1}/first-apps/install`, { apps: chosen.map(s => ({ slug: s, name: known.get(s)!.name })) })
      setStep('install')
    } catch (e) { setErr(errMsg(e)) }
    setBusy(false)
  }
  const finish = async (to: string) => {
    setBusy(true)
    try { await api.post(`${V1}/first-apps/done`) } catch { /* it is offered again next time, no harm */ }
    await refresh()
    navigate(to, { replace: true })
  }

  const items = setup?.install.items || []
  const doneN = items.filter(i => i.state === 'done').length
  const failed = items.filter(i => i.state === 'failed')
  const at = STEPS.findIndex(s => s.id === step)
  const cur = STEPS[at]
  const pct = step === 'done' ? 100 : step === 'install' && items.length ? Math.round(33 + 67 * (doneN + failed.length) / items.length) : 15

  return (
    <div className="wz">
      <div className="wz-shell">
        <aside className="wz-rail">
          <div className="wz-brand">
            <img src="/favicon.svg" alt="" width={34} height={34} />
            <div>
              <b>Set up {status?.box_name || 'Pocket'}</b>
              <span>{step === 'done' ? 'Done' : step === 'install' ? `${doneN} of ${items.length} apps ready` : 'Pick your apps'}</span>
            </div>
          </div>
          <div className="wz-progress" aria-hidden="true"><i style={{ width: `${pct}%` }} /></div>
          <ol className="wz-steps" aria-label="Steps">
            {STEPS.map((s, n) => (
              <li key={s.id} className={n === at ? 'on' : n < at ? 'done' : ''}>
                <button type="button" disabled aria-current={n === at ? 'step' : undefined} title={s.title}>
                  <span className="wz-dot">{n < at ? <Icon name="check" size={13} /> : <Icon name={s.icon} size={14} />}</span>
                  <span className="wz-label"><b>{s.title}</b></span>
                </button>
              </li>
            ))}
          </ol>
          <p className="wz-rail-note">Apps can be added and removed any time under Apps.</p>
        </aside>

        <main className="wz-main">
          <section className="wz-step" key={step}>
            <header className="wz-head">
              <span className={`wz-tile ${step === 'done' ? 'ok' : ''}`}><Icon name={cur.icon} size={22} /></span>
              <div>
                <p className="wz-eyebrow">{step === 'done' ? 'Finished' : `Step ${at + 1} of ${STEPS.length - 1}`}</p>
                <h1>{cur.title}</h1>
              </div>
            </header>
            <p className="wz-sub">{cur.sub}</p>
            <div className="wz-body">
              {err && <div className="callout danger">{err}</div>}
              {!setup || !apps ? (!err && <Loading />) : <>
                {step === 'apps' && <>
                  <div className="setup-bundles">
                    {setup.bundles.bundles.map(b => (
                      <label key={b.id} className={`address-choice setup-bundle ${bundles.has(b.id) ? 'on' : ''}`}>
                        <input type="checkbox" checked={bundles.has(b.id)} onChange={() => toggleBundle(b)} />
                        <span className="setup-check"><Icon name="check" size={13} /></span>
                        <span className="grow">
                          <b>{b.name}</b>
                          <span className="address-text">{b.description}</span>
                          <span className="address-facts">{b.apps.filter(s => known.has(s)).length} apps</span>
                        </span>
                      </label>
                    ))}
                  </div>
                  <div className="setup-cat">
                    <div className="row setup-cat-head">
                      <h3 className="grow">{all ? 'All apps' : `Your apps · ${chosen.length} to install`}</h3>
                      <button type="button" className="btn ghost sm" onClick={() => setAll(!all)}>
                        <Icon name={all ? 'chevron-up' : 'plus'} size={14} />{all ? 'Show only mine' : 'Add or remove single apps'}
                      </button>
                    </div>
                    {!all && (chosen.length || installed.length ? <div className="setup-apps">
                      {[...chosen, ...installed].map(slug => known.get(slug)!).map(a => <AppTile key={a.slug} a={a} on={picked.has(a.slug)} toggle={toggleApp} />)}
                    </div> : <p className="muted small">Nothing picked yet: choose a bundle above, or add single apps.</p>)}
                  </div>
                  {all && categories.map(([cat, list]) => (
                    <div key={cat} className="setup-cat">
                      <h3>{cat.replace(/-/g, ' ')}</h3>
                      <div className="setup-apps">
                        {list.map(a => <AppTile key={a.slug} a={a} on={picked.has(a.slug)} toggle={toggleApp} />)}
                      </div>
                    </div>
                  ))}
                </>}
                {step !== 'apps' && <ul className="wz-summary setup-queue">
                  {items.map(it => (
                    <li key={it.slug}><span className="setup-queue-row">
                      <span className={`wz-sum-ico ${it.state === 'done' ? 'ok' : ''}`}>
                        {it.state === 'installing' ? <Spinner /> : <Icon name={it.state === 'done' ? 'check' : it.state === 'failed' ? 'alert' : 'clock'} size={14} />}
                      </span>
                      <span className="grow"><b>{it.name}</b><span>{
                        it.state === 'installing' ? 'Installing…' : it.state === 'waiting' ? 'Waiting' : it.state === 'done' ? 'Ready' : `Didn't install: ${it.error || 'see Apps → Install app'}`
                      }</span></span>
                    </span></li>
                  ))}
                </ul>}
                {step === 'done' && failed.length > 0 && <p className="muted small">Try the ones that didn't install again under Apps → Install app; the log there says what went wrong.</p>}
              </>}
            </div>
          </section>
          <nav className="wz-nav">
            <span className="grow" />
            {step === 'apps' && <button type="button" className="btn primary lg" onClick={install} disabled={busy || !chosen.length}>
              {busy ? <Spinner /> : null}{chosen.length ? `Install ${chosen.length} app${chosen.length === 1 ? '' : 's'}` : 'Pick at least one app'}{!busy && <Icon name="chevron-right" size={15} />}
            </button>}
            {step === 'install' && <button type="button" className="btn ghost" onClick={() => finish('/apps')} disabled={busy}>Continue while it installs</button>}
            {step === 'done' && <button type="button" className="btn primary lg" onClick={() => finish('/')} disabled={busy}>
              {busy ? <Spinner /> : null}Open Pocket Core{!busy && <Icon name="chevron-right" size={15} />}
            </button>}
          </nav>
        </main>
      </div>
      {step === 'apps' && <button type="button" className="btn ghost sm wz-later" onClick={() => finish('/apps')} disabled={busy}>Pick apps later</button>}
    </div>
  )
}
