import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import type { App, AppLink } from '../lib/types'
import { AppIcon } from '../components/AppIcon'
import { Card, Empty, Loading, PageHeader, Switch, useAsync, useToast } from '../components/ui'

// Group links by (acting app → target app) so each pair reads as one sentence.
export function groupLinks(links: AppLink[]) {
  const groups: { key: string; app: string; appName: string; target: string; targetName: string; reason: string; links: AppLink[] }[] = []
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

export function LinkPair({ g, apps, children }: { g: ReturnType<typeof groupLinks>[number]; apps: App[]; children: React.ReactNode }) {
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

export default function Links() {
  const { toast } = useToast()
  const { data, loading, reload } = useAsync(() => api.get<AppLink[]>(`${V1}/links`), [])
  const { data: apps } = useAsync(() => api.get<App[]>(`${V1}/apps`), [])
  if (loading && !data) return <Loading />
  const groups = groupLinks(data || [])
  const flip = async (l: AppLink, on: boolean) => { await api.put(`${V1}/links/${l.id}`, { enabled: on }); reload(); toast(on ? 'Link on' : 'Link off for everyone') }
  return (
    <>
      <PageHeader title="Links" sub="What one Pocket app may do in another for a person. Apps ask for these in their manifest; Core hands out a five-minute token for each request, valid only in the target app." />
      {groups.length === 0 ? <Card><Empty title="No links yet">An app that needs another (Garage booking costs in Money, say) lists it under <span className="inline-code">uses</span> in its manifest; the target lists what it allows under <span className="inline-code">offers</span>.</Empty></Card> :
        <Card>{groups.map(g => (
          <LinkPair key={g.key} g={g} apps={apps || []}>
            {g.links.map(l => (
              <div key={l.id} className="link-scope">
                <div className="grow">
                  <div><b>{l.scope_name}</b> <span className="mono small muted">{l.scope}</span> {l.state !== 'on' && l.state !== 'off_admin' && stateBadge(l)}</div>
                  {l.scope_description && <div className="muted small">{l.scope_description}</div>}
                  <div className="muted small">{l.uses ? `${l.uses} tokens · last ${ago(l.last_used_at)}` : 'not used yet'}{l.opt_outs ? ` · switched off by ${l.opt_outs} ${l.opt_outs === 1 ? 'person' : 'people'}` : ''}</div>
                </div>
                <Switch on={l.enabled} onChange={on => flip(l, on)} title={l.enabled ? 'On for the box: each person can still switch it off' : 'Off for everyone'} />
              </div>))}
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
