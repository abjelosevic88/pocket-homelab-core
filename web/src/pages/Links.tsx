import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import type { App, AppLink } from '../lib/types'
import { AppIcon } from '../components/AppIcon'
import { Card, Empty, Loading, PageHeader, Segmented, Switch, useAsync, useToast } from '../components/ui'
import { appOffers, LinkCanvas, type CanvasSel, type LinkGroup } from '../components/LinkCanvas'

// Group links by (acting app → target app) so each pair reads as one sentence.
export function groupLinks(links: AppLink[]) {
  const groups: LinkGroup[] = []
  for (const l of links) {
    const key = `${l.app_slug}→${l.target_slug}`
    let g = groups.find(x => x.key === key)
    if (!g) { g = { key, app: l.app_slug, appName: l.app_name, target: l.target_slug, targetName: l.target_name, reason: l.reason, links: [] }; groups.push(g) }
    g.links.push(l)
  }
  return groups
}

const stateBadge = (l: AppLink) => {
  switch (l.state) {
    case 'on': return <span className="badge ok">On</span>
    case 'off_admin': return <span className="badge">Off for the box</span>
    case 'off_person': return <span className="badge">Off for you</span>
    case 'target_missing': return <span className="badge warn" title={l.detail}>{l.target_slug} not registered</span>
    case 'not_offered': return <span className="badge warn" title={l.detail}>Not offered</span>
    default: return <span className="badge warn" title={l.detail}>{l.detail}</span>
  }
}

export function LinkPair({ g, apps, children }: { g: LinkGroup; apps: App[]; children: React.ReactNode }) {
  const a = apps.find(x => x.slug === g.app)
  const t = apps.find(x => x.slug === g.target)
  return (
    <div className="link-pair">
      <div className="link-pair-head">
        {a && <AppIcon a={a} small />}<b>{g.appName}</b><span className="muted">→</span>{t && <AppIcon a={t} small />}<b>{g.targetName}</b>
      </div>
      {g.reason && <p className="muted small link-reason">{g.reason}</p>}
      <div className="link-scopes">{children}</div>
    </div>
  )
}

function ScopeRow({ l, flip }: { l: AppLink; flip: (l: AppLink, on: boolean) => void }) {
  return (
    <div className="link-scope">
      <div className="grow">
        <div><b>{l.scope_name}</b> <span className="mono small muted">{l.scope}</span> {l.state !== 'on' && l.state !== 'off_admin' && stateBadge(l)}</div>
        {l.scope_description && <div className="muted small">{l.scope_description}</div>}
        <div className="muted small">{l.uses ? `${l.uses} tokens · last ${ago(l.last_used_at)}` : 'not used yet'}{l.opt_outs ? ` · switched off by ${l.opt_outs} ${l.opt_outs === 1 ? 'person' : 'people'}` : ''}</div>
      </div>
      <Switch on={l.enabled} onChange={on => flip(l, on)} title={l.enabled ? 'On for the box: each person can still switch it off' : 'Off for everyone'} />
    </div>
  )
}

// What a tapped card stands for: what the app may do elsewhere, and what others may do in it.
function AppDetail({ slug, groups, apps, flip, onPick }: { slug: string; groups: LinkGroup[]; apps: App[]; flip: (l: AppLink, on: boolean) => void; onPick: (s: CanvasSel) => void }) {
  const a = apps.find(x => x.slug === slug)
  const out = groups.filter(g => g.app === slug), inn = groups.filter(g => g.target === slug)
  const used = new Set(inn.flatMap(g => g.links.map(l => l.scope)))
  const idle = appOffers(a).filter(o => !used.has(o.scope))
  return (
    <>
      <div className="link-pair-head">{a && <AppIcon a={a} small />}<b>{a?.name || slug}</b>{a && <span className="muted small">v{a.version}</span>}</div>
      {out.length === 0 && inn.length === 0 && idle.length === 0 && <p className="muted small">No links: this app neither uses nor offers anything in another app.</p>}
      {out.length > 0 && <h4 className="lc-h">Acts in</h4>}
      {out.map(g => <LinkPair key={g.key} g={g} apps={apps}>{g.links.map(l => <ScopeRow key={l.id} l={l} flip={flip} />)}</LinkPair>)}
      {inn.length > 0 && <h4 className="lc-h">Lets these apps act in it</h4>}
      {inn.map(g => <LinkPair key={g.key} g={g} apps={apps}>{g.links.map(l => <ScopeRow key={l.id} l={l} flip={flip} />)}</LinkPair>)}
      {idle.length > 0 && <>
        <h4 className="lc-h">Offers, not used by any app yet</h4>
        <div className="link-scopes">{idle.map(o => (
          <div key={o.scope} className="link-scope"><div className="grow"><div><b>{o.name || o.scope}</b> <span className="mono small muted">{o.scope}</span></div>{o.description && <div className="muted small">{o.description}</div>}</div></div>))}</div>
      </>}
      {(out.length > 0 || inn.length > 0) && <p className="muted small lc-hint">Tap an arrow in the diagram to see just that pair. <button type="button" className="linkish" onClick={() => onPick(null)}>Clear</button></p>}
    </>
  )
}

export default function Links() {
  const { toast } = useToast()
  const { data, loading, reload } = useAsync(() => api.get<AppLink[]>(`${V1}/links`), [])
  const { data: apps } = useAsync(() => api.get<App[]>(`${V1}/apps`), [])
  const [mode, setModeState] = useState<'diagram' | 'list'>(() => localStorage.getItem('core.links.view') === 'list' ? 'list' : 'diagram')
  const [sel, setSel] = useState<CanvasSel>(null)
  if (loading && !data) return <Loading />
  const groups = groupLinks(data || [])
  const setMode = (m: 'diagram' | 'list') => { localStorage.setItem('core.links.view', m); setModeState(m) }
  const flip = async (l: AppLink, on: boolean) => { await api.put(`${V1}/links/${l.id}`, { enabled: on }); reload(); toast(on ? 'Link on' : 'Link off for everyone') }
  const pair = sel?.kind === 'pair' ? groups.find(g => g.key === sel.key) : undefined
  return (
    <>
      <PageHeader title="Links" sub="What one Pocket app may do in another for a person. Apps ask for these in their manifest; Core hands out a five-minute token for each request, valid only in the target app."
        actions={<Segmented size="sm" value={mode} onChange={setMode} options={[{ value: 'diagram', label: 'Diagram' }, { value: 'list', label: 'List' }]} />} />
      {mode === 'diagram' ? <>
        <Card flush className="lc-card-wrap"><LinkCanvas groups={groups} apps={apps || []} sel={sel} onSelect={setSel} /></Card>
        <Card>
          {pair ? <LinkPair g={pair} apps={apps || []}>{pair.links.map(l => <ScopeRow key={l.id} l={l} flip={flip} />)}</LinkPair>
            : sel?.kind === 'app' ? <AppDetail slug={sel.slug} groups={groups} apps={apps || []} flip={flip} onPick={setSel} />
            : <p className="muted small lc-hint">{groups.length === 0
              ? <>No links yet. An app that needs another (Garage booking costs in Money, say) lists it under <span className="inline-code">uses</span> in its manifest; the target lists what it allows under <span className="inline-code">offers</span>.</>
              : <>Tap an arrow to see and switch its permissions, or an app to see everything it uses and offers. Drag cards to rearrange; drag the background to move around, scroll or pinch to zoom.</>}</p>}
        </Card>
      </> : groups.length === 0 ? <Card><Empty title="No links yet">An app that needs another (Garage booking costs in Money, say) lists it under <span className="inline-code">uses</span> in its manifest; the target lists what it allows under <span className="inline-code">offers</span>.</Empty></Card> :
        <Card>{groups.map(g => (
          <LinkPair key={g.key} g={g} apps={apps || []}>
            {g.links.map(l => <ScopeRow key={l.id} l={l} flip={flip} />)}
          </LinkPair>))}</Card>}
    </>
  )
}

/** Everyone's own switches (Settings). */
export function MyLinks() {
  const { toast } = useToast()
  const { data, reload } = useAsync(() => api.get<AppLink[]>(`${V1}/me/links`), [])
  const { data: apps } = useAsync(() => api.get<App[]>(`${V1}/apps`).catch(() => [] as App[]), [])
  const groups = groupLinks(data || [])
  if (!data || groups.length === 0) return null
  const flip = async (l: AppLink, on: boolean) => { await api.put(`${V1}/me/links/${l.id}`, { enabled: on }); reload(); toast(on ? 'Allowed' : 'Switched off for you') }
  return (
    <Card title="Apps acting for me">
      <p className="muted small">Pocket apps that can do things in another app as you. Switching one off here stops it at once, for you only.</p>
      {groups.map(g => (
        <LinkPair key={g.key} g={g} apps={apps || []}>
          {g.links.map(l => (
            <div key={l.id} className="link-scope">
              <div className="grow">
                <div><b>{l.scope_name}</b> {l.state === 'off_admin' && <span className="badge">Off for the box</span>}</div>
                {l.scope_description && <div className="muted small">{l.scope_description}</div>}
              </div>
              <Switch on={!!l.on_for_me} onChange={on => flip(l, on)} />
            </div>))}
        </LinkPair>))}
    </Card>
  )
}
