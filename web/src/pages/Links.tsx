import { useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, formatVersion } from '../lib/format'
import type { App, AppLink } from '../lib/types'
import { AppIcon } from '../components/AppIcon'
import { Icon } from '../components/icons'
import { bundledIcon } from '../lib/icons'
import { Card, Collapse, Confirm, Empty, Loading, Modal, PageHeader, Segmented, Switch, useAsync, useToast } from '../components/ui'
import { appOffers, directCount, groupLinks, isService, pairState, scopeLabel, writes, type LinkGroup } from '../components/linkModel'
import { KeyLine } from '../components/ConnectorPanel'

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
      <PairModes g={g} />
    </div>
  )
}

// How each person's connection is really made, as the acting app reports it.
function PairModes({ g }: { g: LinkGroup }) {
  const people = g.links[0]?.mode_people || []
  if (!people.length) return null
  const word = { core: 'Pocket Core', direct: 'Direct', off: 'Off' } as const
  return (
    <div className="lv-modes">
      <span className="muted small">How people connect:</span>
      {people.map(m => (
        <span key={m.email} className={`lv-mode ${m.mode}`} title={m.mode === 'direct' ? 'A URL and key pasted into the app: the switches above don\'t control it; disconnect it in the app' : `${word[m.mode]} (reported ${ago(m.updated_at)})`}>
          {m.name || m.email} · {word[m.mode]}
        </span>))}
    </div>
  )
}

function ScopeRow({ l, flip }: { l: AppLink; flip: (l: AppLink, on: boolean) => void }) {
  return (
    <div className="link-scope">
      <div className="grow">
        <div><b>{scopeLabel(l)}</b> <span className="mono small muted">{l.scope}</span> {l.state !== 'on' && l.state !== 'off_admin' && stateBadge(l)}</div>
        {l.scope_description && <div className="muted small">{l.scope_description}</div>}
        <div className="muted small">{l.uses ? `${l.uses} tokens · last ${ago(l.last_used_at)}` : 'not used yet'}{l.opt_outs ? ` · switched off by ${l.opt_outs} ${l.opt_outs === 1 ? 'person' : 'people'}` : ''}</div>
        <KeyRotationNote l={l} />
      </div>
      <Switch on={l.enabled} onChange={on => flip(l, on)} title={l.enabled ? 'On for the box: each person can still switch it off' : 'Off for everyone'} />
    </div>
  )
}

// What switching a service link off does to the keys its connector made.
function KeyRotationNote({ l }: { l: AppLink }) {
  if (!l.key_rotation) return null
  return (
    <>
      <div className="muted small">{l.key_rotation === 'replaces'
        ? `Switching off replaces the keys Core's connector made in ${l.target_name}, so copies apps kept stop working.`
        : `Core can't replace keys in ${l.target_name} automatically: a key its connector made keeps working there after a switch-off.`}</div>
      {(l.key_replacements || []).map(r => <KeyLine key={r.email} who={r.name || r.email} replacedAt={r.replaced_at} error={r.error} note={r.note} />)}
    </>
  )
}

// One readable list instead of a diagram: a card per app, opened into what it
// changes, what it only sees, and which services it signs in to (or, read the
// other way round, who changes, sees or signs in to it).
type Side = 'uses' | 'reached'
type Filter = 'all' | 'off' | 'bad' | 'direct'
type Section = { key: string; title: string; pairs: LinkGroup[] }
type Subject = { slug: string; name: string; service: boolean; sections: Section[]; pairs: LinkGroup[]; idle: { scope: string; name?: string }[] }

const short = (name: string) => name.replace(/^Pocket /, '')
const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`
const SIDES: { value: Side; label: string; title: string }[] = [
  { value: 'uses', label: 'What each app uses', title: 'One card per app: what it may change, see, or sign in to elsewhere' },
  { value: 'reached', label: 'Who reaches each app', title: 'One card per app or service: which apps may change or see it' },
]

function Ico({ slug, name, apps }: { slug: string; name: string; apps: App[] }) {
  const a = apps.find(x => x.slug === slug)
  if (a) return <AppIcon a={a} small />
  const icon = isService(slug) ? bundledIcon(slug.slice(8)) : ''
  return <span className="feed-ico">{icon ? <img src={icon} alt="" /> : short(name)[0]}</span>
}

function matches(f: Filter, g: LinkGroup) {
  const st = pairState(g)
  return f === 'all' || (f === 'off' && (st === 'off' || st === 'partial')) || (f === 'bad' && st === 'bad') || (f === 'direct' && directCount(g) > 0)
}

function subjects(groups: LinkGroup[], side: Side, apps: App[]): Subject[] {
  const by = new Map<string, LinkGroup[]>()
  for (const g of groups) { const k = side === 'uses' ? g.app : g.target; by.set(k, [...(by.get(k) || []), g]) }
  const out: Subject[] = []
  for (const [slug, pairs] of by) {
    const name = side === 'uses' ? pairs[0].appName : pairs[0].targetName
    const service = isService(slug)
    const other = (g: LinkGroup) => side === 'uses' ? g.targetName : g.appName
    const sort = (l: LinkGroup[]) => [...l].sort((a, b) => other(a).localeCompare(other(b)))
    const pocket = pairs.filter(g => !isService(g.target))
    const sections: Section[] = side === 'uses' ? [
      { key: 'changes', title: 'Changes things in', pairs: sort(pocket.filter(writes)) },
      { key: 'sees', title: 'Only looks at', pairs: sort(pocket.filter(g => !writes(g))) },
      { key: 'signs', title: 'Signs in to, with each person’s own account', pairs: sort(pairs.filter(g => isService(g.target))) },
    ] : service ? [
      { key: 'signs', title: 'Apps that sign in to it with each person’s own account', pairs: sort(pairs) },
    ] : [
      { key: 'changes', title: 'Apps that can change things in it', pairs: sort(pairs.filter(writes)) },
      { key: 'sees', title: 'Apps that can only look', pairs: sort(pairs.filter(g => !writes(g))) },
    ]
    const used = new Set(pairs.flatMap(g => g.links.map(l => l.scope)))
    const idle = side === 'reached' ? appOffers(apps.find(a => a.slug === slug)).filter(o => !used.has(o.scope)) : []
    out.push({ slug, name, service, sections: sections.filter(s => s.pairs.length), pairs, idle })
  }
  return out.sort((a, b) => short(a.name).localeCompare(short(b.name)))
}

// "Changes 3 apps · looks at 12 · signs in to 9 services"
function summary(s: Subject, side: Side) {
  const n = (k: string) => s.sections.find(x => x.key === k)?.pairs.length || 0
  if (side === 'reached') return s.service ? `${plural(n('signs'), 'app signs', 'apps sign')} in to it`
    : [n('changes') && `${plural(n('changes'), 'app')} can change it`, n('sees') && (n('changes') ? `${n('sees')} can look` : `${plural(n('sees'), 'app')} can look`)].filter(Boolean).join(' · ')
  return [n('changes') && `Changes ${plural(n('changes'), 'app')}`, n('sees') && `looks at ${n('sees')}`, n('signs') && `signs in to ${plural(n('signs'), 'service')}`]
    .filter(Boolean).join(' · ').replace(/^l/, 'L').replace(/^s/, 'S')
}

const stateWord = { on: '', partial: 'Partly off', off: 'Off', bad: "Can't work" } as const

function PairChip({ g, side, apps, onOpen, both }: { g: LinkGroup; side: Side; apps: App[]; onOpen: () => void; both?: boolean }) {
  const slug = side === 'uses' ? g.target : g.app, name = side === 'uses' ? g.targetName : g.appName
  const st = pairState(g), direct = directCount(g)
  const what = isService(g.target) ? '' : g.links.map(scopeLabel).join(', ')
  return (
    <button type="button" className={`lk-chip ${st}`} onClick={onOpen}
      title={`${g.appName} → ${g.targetName}${g.reason ? `\n${g.reason}` : ''}\n${g.links.map(l => `${l.enabled ? '✓' : '✕'} ${scopeLabel(l)}`).join('\n')}${direct ? `\n↗ Direct for ${direct}: Core can't switch this` : ''}`}>
      <Ico slug={slug} name={name} apps={apps} />
      <span className="lk-chip-text"><b>{both ? `${short(g.appName)} → ${short(g.targetName)}` : short(name)}</b>{what && <span className="lk-chip-what">{what}</span>}</span>
      {st !== 'on' && <span className={`lk-tag ${st}`}>{stateWord[st]}</span>}
      {direct > 0 && <span className="lk-tag direct" title={`${direct} connect directly with a pasted key`}>↗{direct}</span>}
    </button>
  )
}

// An opened card's link: the other app on top, one switch per permission under it.
function PairBox({ g, side, apps, onOpen, flip }: { g: LinkGroup; side: Side; apps: App[]; onOpen: () => void; flip: (l: AppLink, on: boolean) => void }) {
  const slug = side === 'uses' ? g.target : g.app, name = side === 'uses' ? g.targetName : g.appName
  const st = pairState(g), direct = directCount(g)
  return (
    <div className={`lk-box ${st}`}>
      <button type="button" className="lk-box-head" onClick={onOpen} title={`Details: ${g.appName} → ${g.targetName}`}>
        <Ico slug={slug} name={name} apps={apps} /><b>{short(name)}</b>
        {st === 'bad' && <span className="lk-tag bad">{stateWord.bad}</span>}
        {direct > 0 && <span className="lk-tag direct" title={`${direct} connect directly with a pasted key: these switches don't control that`}>↗{direct}</span>}
        <Icon name="chevron-right" size={14} className="lk-box-go" />
      </button>
      {g.links.map(l => (
        <div key={l.id} className={`lk-perm${l.enabled ? '' : ' off'}`}>
          <span className="grow" title={l.scope_description || l.scope}>{isService(g.target) ? 'Use each person’s account' : scopeLabel(l)}</span>
          <Switch size="sm" on={l.enabled} onChange={on => flip(l, on)} title={l.enabled ? 'On for the box: each person can still switch it off' : 'Off for everyone'} />
        </div>))}
    </div>
  )
}

function SubjectCard({ s, side, apps, open, toggle, onOpen, setAll, flip }: {
  s: Subject; side: Side; apps: App[]; open: boolean; toggle: () => void; onOpen: (g: LinkGroup) => void; setAll: (pairs: LinkGroup[], on: boolean, what: string) => void; flip: (l: AppLink, on: boolean) => void
}) {
  const off = s.pairs.filter(g => ['off', 'partial'].includes(pairState(g))).length
  const bad = s.pairs.filter(g => pairState(g) === 'bad').length
  const direct = s.pairs.filter(g => directCount(g) > 0).length
  const a = apps.find(x => x.slug === s.slug)
  return (
    <div className={`lk-card${open ? ' open' : ''}`}>
      <button type="button" className="lk-head" onClick={toggle} aria-expanded={open}>
        <Ico slug={s.slug} name={s.name} apps={apps} />
        <span className="lk-head-text">
          <b>{s.name}</b>{a && <span className="muted small"> {formatVersion(a.version)}</span>}
          <span className="lk-head-sum">{summary(s, side)}</span>
        </span>
        {!open && <span className="lk-stack" aria-hidden="true">
          {s.pairs.slice(0, 8).map(g => { const o = side === 'uses' ? [g.target, g.targetName] : [g.app, g.appName]; return <Ico key={g.key} slug={o[0]} name={o[1]} apps={apps} /> })}
          {s.pairs.length > 8 && <span className="lk-stack-more">+{s.pairs.length - 8}</span>}
        </span>}
        <span className="lk-head-tags">
          {bad > 0 && <span className="badge danger" title="Links that can't work">{bad} can't work</span>}
          {off > 0 && <span className="badge" title="Links switched off for the box">{off} off</span>}
          {direct > 0 && <span className="badge warn" title="People connected with a pasted URL and key: these switches don't control them">↗ {direct}</span>}
        </span>
        <Icon name="chevron-down" size={16} className="lk-chev" />
      </button>
      <Collapse open={open}>
        <div className="lk-body">
          {s.sections.map(sec => {
            const allOn = sec.pairs.every(g => g.links.every(l => l.enabled))
            return (
              <section key={sec.key} className="lk-sec">
                <div className="lk-sec-head">
                  <h4 className="lc-h">{sec.title} <span className="muted">· {sec.pairs.length}</span></h4>
                  {sec.pairs.length > 1 && <button type="button" className="linkish small" onClick={() => setAll(sec.pairs, !allOn, `${sec.title.toLowerCase()} (${s.name})`)}>{allOn ? 'Switch all off' : 'Switch all on'}</button>}
                </div>
                <div className="lk-boxes">{sec.pairs.map(g => <PairBox key={g.key} g={g} side={side} apps={apps} onOpen={() => onOpen(g)} flip={flip} />)}</div>
              </section>)
          })}
          {open && s.idle.length > 0 && <p className="muted small lk-idle">Offered, no app uses it yet: {s.idle.map(o => o.name || o.scope).join(', ')}</p>}
        </div>
      </Collapse>
    </div>
  )
}

export default function Links() {
  const { toast } = useToast()
  const { data, loading, reload } = useAsync(() => api.get<AppLink[]>(`${V1}/links`), [])
  const { data: apps } = useAsync(() => api.get<App[]>(`${V1}/apps`), [])
  const [side, setSideState] = useState<Side>(() => localStorage.getItem('core.links.side') === 'reached' ? 'reached' : 'uses')
  const [filter, setFilter] = useState<Filter>('all')
  const [q, setQ] = useState('')
  const [openSet, setOpenSet] = useState<Set<string>>(new Set())
  const [closedSet, setClosedSet] = useState<Set<string>>(new Set()) // while searching or filtering, cards start open
  const [picked, setPicked] = useState<string | null>(null)
  const [bulk, setBulk] = useState<{ pairs: LinkGroup[]; what: string } | null>(null)
  const groups = useMemo(() => groupLinks(data || []), [data])
  const all = useMemo(() => subjects(groups, side, apps || []), [groups, side, apps])
  if (loading && !data) return <Loading />

  const setSide = (s: Side) => { localStorage.setItem('core.links.side', s); setSideState(s); setOpenSet(new Set()) }
  const flip = async (l: AppLink, on: boolean) => { await api.put(`${V1}/links/${l.id}`, { enabled: on }); reload(); toast(on ? 'Link on' : 'Link off for everyone') }
  const switchAll = async (pairs: LinkGroup[], on: boolean) => {
    const todo = pairs.flatMap(g => g.links).filter(l => l.enabled !== on)
    for (const l of todo) await api.put(`${V1}/links/${l.id}`, { enabled: on })
    reload(); toast(on ? `${plural(todo.length, 'link')} on` : `${plural(todo.length, 'link')} off for everyone`)
  }
  const setAll = (pairs: LinkGroup[], on: boolean, what: string) => on ? switchAll(pairs, true) : setBulk({ pairs, what })

  // A card shows when its own name matches (all its pairs), or only the pairs whose other side or scopes match.
  const needle = q.trim().toLowerCase()
  const hit = (g: LinkGroup) => !needle || [side === 'uses' ? g.targetName : g.appName, ...g.links.map(scopeLabel)].some(t => t.toLowerCase().includes(needle))
  const shown = all.map(s => {
    const self = !needle || s.name.toLowerCase().includes(needle)
    const keep = (g: LinkGroup) => matches(filter, g) && (self || hit(g))
    return { ...s, sections: s.sections.map(sec => ({ ...sec, pairs: sec.pairs.filter(keep) })).filter(sec => sec.pairs.length), pairs: s.pairs.filter(keep) }
  }).filter(s => s.pairs.length)
  const narrowed = !!needle || filter !== 'all'
  const isOpen = (slug: string) => narrowed ? !closedSet.has(slug) : openSet.has(slug)
  const flipIn = (set: React.Dispatch<React.SetStateAction<Set<string>>>, slug: string) => set(cur => { const n = new Set(cur); if (n.has(slug)) n.delete(slug); else n.add(slug); return n })
  const toggle = (slug: string) => flipIn(narrowed ? setClosedSet : setOpenSet, slug)

  const count = (f: Filter) => groups.filter(g => matches(f, g)).length
  const FILTERS: { value: Filter; label: string; n: number; title: string }[] = [
    { value: 'all', label: 'All', n: groups.length, title: 'Every connection between two apps' },
    { value: 'off', label: 'Off', n: count('off'), title: 'Switched off for the box, fully or partly' },
    { value: 'bad', label: "Can't work", n: count('bad'), title: 'The other app no longer offers this' },
    { value: 'direct', label: 'Direct', n: count('direct'), title: 'Someone connects with a URL and key pasted into the app: Core can see these but not switch them' },
  ]
  const pair = picked ? groups.find(g => g.key === picked) : undefined
  const bad = groups.filter(g => pairState(g) === 'bad')
  const pocketCards = shown.filter(s => !s.service), serviceCards = shown.filter(s => s.service)
  const card = (s: Subject) => <SubjectCard key={s.slug} s={s} side={side} apps={apps || []} open={isOpen(s.slug)} toggle={() => toggle(s.slug)} onOpen={g => setPicked(g.key)} setAll={setAll} flip={flip} />
  const ico = (slug: string, name: string) => <Ico slug={slug} name={name} apps={apps || []} />

  return (
    <>
      <PageHeader title="Links" sub="What one Pocket app may do in another app, or in a service, on a person’s behalf. Each person can still switch a link off for themselves in Settings." />
      {groups.length === 0 ? <Card><Empty icon="nodes" title="No links yet">An app that needs another (Garage booking costs in Money, say) lists it under <span className="inline-code">uses</span> in its manifest; the target lists what it allows under <span className="inline-code">offers</span>.</Empty></Card> : <>
        {bad.length > 0 && filter === 'all' && !needle && (
          <div className="lk-alert">
            <b>{plural(bad.length, "link can't", "links can't")} work right now</b>
            <div className="lk-chips">{bad.map(g => <PairChip key={g.key} g={g} side="uses" both apps={apps || []} onOpen={() => setPicked(g.key)} />)}</div>
          </div>)}
        <div className="toolbar lk-toolbar">
          <Segmented size="sm" value={side} onChange={setSide} options={SIDES} />
          <div className="search"><Icon name="search" size={15} /><input placeholder="Find an app, service or permission…" value={q} onChange={e => { setQ(e.target.value); setClosedSet(new Set()) }} /></div>
        </div>
        <div className="lk-filters">
          {FILTERS.filter(f => f.value === 'all' || f.n > 0).map(f => (
            <button key={f.value} type="button" className={`chip lk-filter ${f.value}${filter === f.value ? ' active' : ''}`} title={f.title} onClick={() => { setFilter(f.value); setClosedSet(new Set()) }}>
              {f.label}<span className="lk-filter-n">{f.n}</span>
            </button>))}
          {!narrowed && <button type="button" className="linkish small lk-expand" onClick={() => setOpenSet(openSet.size ? new Set() : new Set(shown.map(s => s.slug)))}>{openSet.size ? 'Collapse all' : 'Expand all'}</button>}
        </div>
        {shown.length === 0 ? <Card><Empty title="Nothing matches">No link matches this search and filter.</Empty></Card> : <>
          {side === 'reached' && pocketCards.length > 0 && serviceCards.length > 0 && <h3 className="lk-group">Pocket apps</h3>}
          <div className="lk-list">{pocketCards.map(card)}</div>
          {serviceCards.length > 0 && <><h3 className="lk-group">Services</h3><div className="lk-list">{serviceCards.map(card)}</div></>}
        </>}
        <p className="muted small lk-legend">
          <span className="lk-tag partial">Partly off</span> some permissions are switched off · <span className="lk-tag off">Off</span> switched off for everyone · <span className="lk-tag direct">↗</span> someone connects with a pasted key, which Core can't switch. Tap a link for its switches.
        </p>
      </>}
      {pair && (
        <Modal className="lv-modal" onClose={() => setPicked(null)} title={<span className="lv-modal-title">{ico(pair.app, pair.appName)}{pair.appName}<span className="muted">→</span>{ico(pair.target, pair.targetName)}{pair.targetName}</span>}>
          <LinkPair g={pair} apps={apps || []}>{pair.links.map(l => <ScopeRow key={l.id} l={l} flip={flip} />)}</LinkPair>
        </Modal>)}
      {bulk && <Confirm title="Switch these links off?" confirmLabel="Switch off"
        message={<>This switches off {plural(bulk.pairs.length, 'connection')} ({bulk.what}) for everyone on the box. Features that rely on them stop working until you switch them back on.</>}
        onCancel={() => setBulk(null)} onConfirm={() => { const b = bulk; setBulk(null); switchAll(b.pairs, false) }} />}
    </>
  )
}
/** Everyone's own switches (Settings). */
export function MyLinks() {
  const { toast } = useToast()
  const { data, reload } = useAsync(() => api.get<AppLink[]>(`${V1}/me/links`), [])
  const { data: apps } = useAsync(() => api.get<App[]>(`${V1}/apps`).catch(() => [] as App[]), [])
  const groups = groupLinks(data || [])
  if (!data) return <Loading />
  if (groups.length === 0) return <Card><Empty icon="nodes" title="No app acts for you">When a Pocket app gets a link to another app, it shows up here with a switch.</Empty></Card>
  const flip = async (l: AppLink, on: boolean) => { await api.put(`${V1}/me/links/${l.id}`, { enabled: on }); reload(); toast(on ? 'Allowed' : 'Switched off for you') }
  return (
    <Card>
      {groups.map(g => (
        <LinkPair key={g.key} g={g} apps={apps || []}>
          {g.links.map(l => (
            <div key={l.id} className="link-scope">
              <div className="grow">
                <div><b>{scopeLabel(l)}</b> {l.state === 'off_admin' && <span className="badge">Off for the box</span>}</div>
                {l.scope_description && <div className="muted small">{l.scope_description}</div>}
              </div>
              <Switch on={!!l.on_for_me} onChange={on => flip(l, on)} />
            </div>))}
        </LinkPair>))}
    </Card>
  )
}
