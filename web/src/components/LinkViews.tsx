import { useState } from 'react'
import type { App, AppLink } from '../lib/types'
import { AppIcon } from './AppIcon'
import { bundledIcon } from '../lib/icons'
import { Switch } from './ui'
import { appOffers, directCount, pairState, scopeLabel, type CanvasSel, type LinkGroup } from './LinkCanvas'

// Other ways to read the same links: a grid (who acts in whom), a focus on one
// app (what it uses, what uses it), and access by data (who can reach what).

const isRead = (scope: string) => /:read$/.test(scope)
const short = (name: string) => name.replace(/^Pocket /, '')
const stateWord = { on: 'On', partial: 'Partly off', off: 'Off', bad: "Can't work" } as const

function Ico({ slug, name, apps }: { slug: string; name: string; apps: App[] }) {
  const a = apps.find(x => x.slug === slug)
  if (a) return <AppIcon a={a} small />
  const icon = slug.startsWith('service:') ? bundledIcon(slug.slice(8)) : ''
  return <span className="feed-ico">{icon ? <img src={icon} alt="" /> : short(name)[0]}</span>
}

// A green check when allowed, a red cross when switched off, amber when only some are on.
type MarkState = 'yes' | 'no' | 'some'
const markOf = (links: AppLink[]): MarkState => {
  const on = links.filter(l => l.enabled && ['on', 'off_person'].includes(l.state)).length
  return on === 0 ? 'no' : on === links.length ? 'yes' : 'some'
}
export function Mark({ s, title }: { s: MarkState; title?: string }) {
  const t = title || (s === 'yes' ? 'Allowed' : s === 'no' ? 'Not allowed' : 'Partly allowed')
  return <span className={`lv-mark ${s}`} title={t} aria-label={t}>{s === 'no' ? '✕' : '✓'}</span>
}

// One word per pair: "changes" when the app may change anything there (it can
// see it too), else "sees"; the mark covers all its scopes. With every write
// switched off but a read still on, it only sees.
function verbs(links: AppLink[]) {
  const writesOn = links.some(l => !isRead(l.scope) && l.enabled)
  const hasWrites = links.some(l => !isRead(l.scope))
  const word = writesOn || (hasWrites && markOf(links) === 'no') ? 'changes' : 'sees'
  return <span className={`lv-verb ${markOf(links)}`}><Mark s={markOf(links)} /> {word}</span>
}

/** One line on top of every view: how many links, and how they stand. */
export function LinkSummary({ groups }: { groups: LinkGroup[] }) {
  const links = groups.flatMap(g => g.links)
  const apps = new Set(groups.flatMap(g => [g.app, g.target]))
  const on = links.filter(l => l.enabled && l.state === 'on').length
  const off = links.filter(l => !l.enabled).length
  const bad = links.filter(l => !['on', 'off_admin', 'off_person'].includes(l.state)).length
  const tokens = links.reduce((a, l) => a + l.uses, 0)
  const direct = groups.reduce((a, g) => a + directCount(g), 0)
  if (!links.length) return null
  return (
    <div className="lv-summary">
      <span><b>{links.length}</b> links between <b>{apps.size}</b> apps</span>
      <span className="lv-stat on"><b>{on}</b> on</span>
      {off > 0 && <span className="lv-stat off"><b>{off}</b> off</span>}
      {bad > 0 && <span className="lv-stat bad"><b>{bad}</b> can't work</span>}
      {direct > 0 && <span className="lv-stat direct" title="Connections made with a URL and key pasted into the app: Core sees them but can't switch them off"><b>{direct}</b> on Direct</span>}
      <span className="muted"><b>{tokens}</b> tokens issued</span>
    </div>
  )
}

/** Grid: apps that act down the side, apps they act in across the top. */
export function MatrixView({ groups, apps, sel, onSelect }: { groups: LinkGroup[]; apps: App[]; sel: CanvasSel; onSelect: (s: CanvasSel) => void }) {
  const count = (slug: string, side: 'app' | 'target') => groups.filter(g => g[side] === slug).length
  const actors = [...new Map(groups.map(g => [g.app, g.appName])).entries()].sort((a, b) => count(b[0], 'app') - count(a[0], 'app') || a[1].localeCompare(b[1]))
  const targets = [...new Map(groups.map(g => [g.target, g.targetName])).entries()].sort((a, b) => count(b[0], 'target') - count(a[0], 'target') || a[1].localeCompare(b[1]))
  return (
    <div className="lv-grid-wrap">
      <table className="lv-grid">
        <thead>
          <tr>
            <th className="lv-corner"><span className="muted small">acts in →</span></th>
            {targets.map(([slug, name]) => (
              <th key={slug} className="lv-col">
                <button type="button" className="lv-head" onClick={() => onSelect({ kind: 'app', slug })} title={name}>
                  <Ico slug={slug} name={name} apps={apps} /><span>{short(name)}</span>
                </button>
              </th>))}
          </tr>
        </thead>
        <tbody>
          {actors.map(([slug, name]) => (
            <tr key={slug}>
              <th className="lv-row">
                <button type="button" className="lv-head row" onClick={() => onSelect({ kind: 'app', slug })}>
                  <Ico slug={slug} name={name} apps={apps} /><span>{short(name)}</span>
                </button>
              </th>
              {targets.map(([t]) => {
                const g = groups.find(x => x.app === slug && x.target === t)
                if (!g) return <td key={t} className="lv-empty">·</td>
                const st = pairState(g)
                const picked = sel?.kind === 'pair' && sel.key === g.key
                return (
                  <td key={t}>
                    <button type="button" className={`lv-cell ${st}${picked ? ' picked' : ''}`} onClick={() => onSelect(picked ? null : { kind: 'pair', key: g.key })}
                      title={`${g.appName} → ${g.targetName}\n${g.links.map(l => `${scopeLabel(l)}${l.enabled ? '' : ' (off)'}`).join('\n')}`}>
                      {verbs(g.links)}
                      {st !== 'on' && <span className="lv-cell-state">{stateWord[st]}</span>}
                      {directCount(g) > 0 && <span className="lv-direct" title="Pasted URL and key in the app: Core can't switch this">↗ Direct {directCount(g)}</span>}
                    </button>
                  </td>)
              })}
            </tr>))}
        </tbody>
      </table>
    </div>
  )
}

function CompactScopes({ links, flip }: { links: AppLink[]; flip: (l: AppLink, on: boolean) => void }) {
  return (
    <div className="lv-scopes">
      {links.map(l => (
        <div key={l.id} className={`lv-scope${l.enabled ? '' : ' off'}`}>
          <Mark s={markOf([l])} />
          <span className="grow" title={l.scope_description || l.scope}>{scopeLabel(l)}</span>
          <Switch on={l.enabled} onChange={on => flip(l, on)} title={l.enabled ? 'On for the box' : 'Off for everyone'} />
        </div>))}
    </div>
  )
}

/** Focus: one app in the middle, what lets it in on the left, what it uses on the right. */
export function FocusView({ groups, apps, flip }: { groups: LinkGroup[]; apps: App[]; flip: (l: AppLink, on: boolean) => void }) {
  const linked = [...new Map(groups.flatMap(g => [[g.app, g.appName], [g.target, g.targetName]] as [string, string][])).entries()]
    .map(([slug, name]) => ({ slug, name, n: groups.filter(g => g.app === slug || g.target === slug).length }))
    .sort((a, b) => b.n - a.n || a.name.localeCompare(b.name))
  const [focus, setFocusRaw] = useState(() => localStorage.getItem('core.links.focus') || '')
  const cur = linked.find(x => x.slug === focus) || linked[0]
  const setFocus = (s: string) => { localStorage.setItem('core.links.focus', s); setFocusRaw(s) }
  if (!cur) return null
  const app = apps.find(a => a.slug === cur.slug)
  const uses = groups.filter(g => g.app === cur.slug)
  const usedBy = groups.filter(g => g.target === cur.slug)
  const unused = appOffers(app).filter(o => !usedBy.some(g => g.links.some(l => l.scope === o.scope)))
  const side = (list: LinkGroup[], dir: 'in' | 'out') => list.map(g => {
    const other = dir === 'out' ? g.target : g.app, otherName = dir === 'out' ? g.targetName : g.appName
    return (
      <div key={g.key} className={`lv-peer ${pairState(g)}`}>
        <button type="button" className="lv-peer-head" onClick={() => setFocus(other)} title={`Focus on ${otherName}`}>
          <Ico slug={other} name={otherName} apps={apps} /><b>{short(otherName)}</b><span className="lv-peer-go">›</span>
        </button>
        <p className="muted small lv-peer-why">{dir === 'out' ? `${short(cur.name)} may, in ${short(otherName)}:` : `${short(otherName)} may, in ${short(cur.name)}:`}</p>
        <CompactScopes links={g.links} flip={flip} />
        {directCount(g) > 0 && <p className="small lv-direct-line">↗ Direct for {(g.links[0].mode_people || []).filter(m => m.mode === 'direct').map(m => m.name || m.email).join(', ')}: a pasted URL and key, which these switches don't control.</p>}
        {g.reason && <p className="muted small lv-reason">{g.reason}</p>}
      </div>)
  })
  return (
    <div className="lv-focus">
      <div className="lv-picker">
        {linked.map(x => (
          <button type="button" key={x.slug} className={`lc-chip${x.slug === cur.slug ? ' picked' : ''}`} onClick={() => setFocus(x.slug)}>
            {(() => { const a = apps.find(y => y.slug === x.slug); return a ? <AppIcon a={a} small /> : <i>{short(x.name)[0]}</i> })()}{short(x.name)}<span className="lc-chip-n">{x.n}</span>
          </button>))}
      </div>
      <div className="lv-stage">
        <section className="lv-side in">
          <h4 className="lc-h">Let in by {short(cur.name)} <span className="muted">· {usedBy.length}</span></h4>
          {usedBy.length ? side(usedBy, 'in') : <p className="muted small">No app acts in {short(cur.name)}.</p>}
          {unused.length > 0 && <div className="lv-unused">
            <p className="muted small">Offered, nobody uses yet:</p>
            {unused.map(o => <div key={o.scope} className="lv-scope off"><Mark s="no" title="Nobody uses this yet" /><span className="grow">{o.name || o.scope}</span></div>)}
          </div>}
        </section>
        <div className="lv-center">
          <span className="lv-arrow in" aria-hidden="true">→</span>
          <div className="lv-hub">
            {app ? <AppIcon a={app} /> : <span className="mini-ico">{short(cur.name)[0]}</span>}
            <b>{cur.name}</b>
            <span className="muted small">{usedBy.length ? `${usedBy.length} let in` : ''}{usedBy.length && uses.length ? ' · ' : ''}{uses.length ? `uses ${uses.length}` : ''}</span>
          </div>
          <span className="lv-arrow out" aria-hidden="true">→</span>
        </div>
        <section className="lv-side out">
          <h4 className="lc-h">Uses <span className="muted">· {uses.length}</span></h4>
          {uses.length ? side(uses, 'out') : <p className="muted small">{short(cur.name)} doesn't act in other apps.</p>}
        </section>
      </div>
    </div>
  )
}

/** Access: your data in each app, and which apps may see or change it. */
export function AccessView({ groups, apps, sel, onSelect }: { groups: LinkGroup[]; apps: App[]; sel: CanvasSel; onSelect: (s: CanvasSel) => void }) {
  const targets = [...new Set(groups.map(g => g.target))]
  for (const a of apps) if (appOffers(a).length && !targets.includes(a.slug)) targets.push(a.slug)
  const name = (slug: string) => apps.find(a => a.slug === slug)?.name || groups.find(g => g.target === slug)?.targetName || slug
  targets.sort((a, b) => name(a).localeCompare(name(b)))
  return (
    <div className="lv-access">
      {targets.map(t => {
        const app = apps.find(a => a.slug === t)
        const inbound = groups.filter(g => g.target === t)
        const scopes = new Map<string, { name: string; desc: string }>()
        for (const o of appOffers(app)) scopes.set(o.scope, { name: o.name || o.scope, desc: o.description || '' })
        for (const l of inbound.flatMap(g => g.links)) if (!scopes.has(l.scope)) scopes.set(l.scope, { name: scopeLabel(l), desc: l.scope_description })
        const ordered = [...scopes.entries()].sort((a, b) => Number(isRead(b[0])) - Number(isRead(a[0])) || a[1].name.localeCompare(b[1].name))
        const who = new Set(inbound.map(g => g.app)).size
        return (
          <section key={t} className="lv-data">
            <header>
              <Ico slug={t} name={name(t)} apps={apps} />
              <div><b>Your {short(name(t))}</b><div className="muted small">{who ? `${who} ${who === 1 ? 'app' : 'apps'} can reach it` : 'no app reaches it yet'}</div></div>
            </header>
            {ordered.map(([scope, info]) => {
              const holders = inbound.filter(g => g.links.some(l => l.scope === scope))
              return (
                <div key={scope} className="lv-right">
                  <div className="lv-right-what">
                    <Mark s={holders.some(g => markOf(g.links.filter(l => l.scope === scope)) === 'yes') ? 'yes' : 'no'} title={holders.length ? undefined : 'No app holds this'} />
                    <span title={info.desc || scope}>{info.name}</span>
                  </div>
                  <div className="lv-holders">
                    {holders.length ? holders.map(g => {
                      const l = g.links.find(x => x.scope === scope)!
                      const st = !['on', 'off_admin', 'off_person'].includes(l.state) ? 'bad' : l.enabled ? 'on' : 'off'
                      const picked = sel?.kind === 'pair' && sel.key === g.key
                      return (
                        <button type="button" key={g.key} className={`lv-holder ${st}${picked ? ' picked' : ''}`} onClick={() => onSelect(picked ? null : { kind: 'pair', key: g.key })}
                          title={st === 'on' ? `${g.appName} may ${info.name.toLowerCase()}` : st === 'off' ? `${g.appName}: switched off` : l.detail}>
                          <Ico slug={g.app} name={g.appName} apps={apps} />{short(g.appName)}{directCount(g) > 0 && <span className="lv-direct" title="Some people connect directly">↗</span>}
                        </button>)
                    }) : <span className="muted small">nobody</span>}
                  </div>
                </div>)
            })}
          </section>)
      })}
    </div>
  )
}
