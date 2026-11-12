// Downloads on the Pocket Box, through the box downloads service (BOX_DOWNLOADS). The box runs and
// wires Radarr, Sonarr, Prowlarr, SABnzbd, qBittorrent (Lidarr, Bazarr) by itself; the owner only brings a
// Usenet provider and an indexer's API key, and/or switches torrents on (optionally through a WireGuard
// VPN), and for subtitles the languages and an optional OpenSubtitles.com login. Either Usenet or
// torrents is enough; with both the box tries Usenet first. Settings → System → Downloads
// (/settings/system#downloads, where Pocket Requests sends people) and the setup wizard's optional
// Downloads step share this panel.
import { useEffect, useMemo, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { BoxDownloads, DownloadsStatus, IndexerPreset } from '../lib/types'
import { Confirm, errMsg, Field, Spinner, Switch, useToast } from './ui'
import { Icon } from './icons'
import { Select } from './Select'

export const DOWNLOADS = `${V1}/box/downloads`

/** The downloads service's answer, loaded once (only when `on`). */
export function useBoxDownloads(on = true) {
  const [dl, setDl] = useState<BoxDownloads | null>(null)
  const [err, setErr] = useState('')
  const reload = async () => {
    try { setDl(await api.get<BoxDownloads>(DOWNLOADS)); setErr('') } catch (e) { setErr(errMsg(e)) }
  }
  useEffect(() => { if (on) reload() }, [on])
  return { dl, setDl, err, reload }
}

type Kind = 'usenet' | 'torrent'
const kindOf = (x: { kind?: string }): Kind => (x.kind === 'torrent' ? 'torrent' : 'usenet')
const okIndexers = (dl: BoxDownloads, kind: Kind) => dl.indexers.filter(x => kindOf(x) === kind && x.status === 'ok').length

/** Where each way of downloading stands (the box's `ready` = either one works). */
function routes(dl: BoxDownloads) {
  const t = dl.torrents
  const provider = dl.usenet.configured && dl.usenet.status === 'ok'
  const torrentsOn = !!t?.enabled && t.status === 'ok'
  return {
    provider, usenetIndexer: okIndexers(dl, 'usenet') > 0, usenet: provider && okIndexers(dl, 'usenet') > 0,
    torrentsOn, torrentIndexer: okIndexers(dl, 'torrent') > 0, torrents: torrentsOn && okIndexers(dl, 'torrent') > 0,
  }
}

/** One line for the wizard's summary, like "Eweka, 2 indexers · torrents via VPN". */
export function downloadsSummary(dl: BoxDownloads | null): { text: string; ok: boolean } {
  if (!dl) return { text: 'Not set up yet', ok: false }
  const r = routes(dl)
  const t = dl.torrents
  const n = dl.indexers.filter(x => x.status === 'ok').length
  const count = `${n} indexer${n === 1 ? '' : 's'}`
  const via = t?.enabled ? (t.vpn.configured ? 'torrents via VPN' : 'torrents without a VPN') : ''
  if (dl.ready) {
    if (r.usenet) return { text: `${providerName(dl)}, ${count}${via ? ` · ${via}` : ''}`, ok: true }
    return { text: `${via.charAt(0).toUpperCase()}${via.slice(1)}, ${count}`, ok: true }
  }
  if (!dl.usenet.configured && !dl.indexers.length && !t?.enabled) return { text: 'Not set up yet', ok: false }
  if (dl.usenet.configured && dl.usenet.status === 'error') return { text: 'The Usenet provider has a problem', ok: false }
  if (t?.enabled && t.status === 'error') return { text: 'Torrents have a problem', ok: false }
  if (!n) return { text: dl.indexers.length ? 'No indexer works yet' : 'Indexer still missing', ok: false }
  if (r.provider) return { text: 'Add a Usenet indexer', ok: false }
  if (r.torrentsOn) return { text: 'Add a torrent site', ok: false }
  return { text: t ? 'Usenet provider or torrents still missing' : 'Usenet provider still missing', ok: false }
}

const SERVICE: Record<string, string> = {
  radarr: 'Films (Radarr)', sonarr: 'Shows (Sonarr)', lidarr: 'Music (Lidarr)', prowlarr: 'Indexers (Prowlarr)', sabnzbd: 'Downloader (SABnzbd)', bazarr: 'Subtitles (Bazarr)',
  qbittorrent: 'Torrents (qBittorrent)',
}

// Subtitle languages on offer (ISO 639-1), named in the browser's language.
const LANGS = ['en', 'sr', 'hr', 'bs', 'sl', 'mk', 'sq', 'de', 'fr', 'es', 'it', 'pt', 'nl', 'sv', 'no', 'da', 'fi', 'is', 'pl', 'cs', 'sk', 'hu', 'ro', 'bg', 'el', 'tr', 'ru', 'uk', 'lt', 'lv', 'et', 'ar', 'he', 'fa', 'hi', 'th', 'vi', 'id', 'ms', 'zh', 'ja', 'ko']
function langName(code: string): string {
  try { return new Intl.DisplayNames(undefined, { type: 'language' }).of(code) || code } catch { return code }
}

function Problem({ text }: { text: string }) {
  return text ? <div className="callout danger"><Icon name="alert" size={15} /><span>{text}</span></div> : null
}

function StatusBadge({ status }: { status: DownloadsStatus }) {
  if (status === 'ok') return <span className="badge ok"><Icon name="check" size={11} />Working</span>
  if (status === 'error') return <span className="badge danger"><Icon name="alert" size={11} />Not working</span>
  return <span className="badge">Not tested yet</span>
}

function providerName(dl: BoxDownloads): string {
  return dl.presets.providers.find(p => p.host === dl.usenet.host)?.name || dl.usenet.host
}

function PasswordInput({ value, onChange, placeholder, label }: { value: string; onChange: (v: string) => void; placeholder: string; label: string }) {
  const [show, setShow] = useState(false)
  return (
    <div className="box-pw">
      <input type={show ? 'text' : 'password'} value={value} onChange={e => onChange(e.target.value)} placeholder={placeholder} autoComplete="new-password" aria-label={label} spellCheck={false} />
      <button type="button" className="btn ghost icon-only sm" onClick={() => setShow(!show)} aria-label={show ? 'Hide' : 'Show'}><Icon name={show ? 'eye-off' : 'eye'} size={15} /></button>
    </div>
  )
}

function Check({ ok, children }: { ok: boolean; children: React.ReactNode }) {
  return <span className={ok ? 'ok' : ''}><Icon name={ok ? 'check' : 'circle'} size={13} />{children}</span>
}

/** Everything about downloads on the box: ready or not, Usenet, torrents, the indexers, subtitles. */
export function DownloadsPanel({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const r = routes(dl)
  const t = dl.torrents
  // qBittorrent is stopped while torrents are off: not "starting".
  const starting = dl.services.filter(s => !s.ready && (s.type !== 'qbittorrent' || t?.enabled))
  const started = r.provider || r.usenetIndexer || r.torrentsOn || r.torrentIndexer
  let readyText = 'Pocket Requests can now find and download the films and shows you ask for.'
  if (r.usenet && r.torrents) readyText += ' Usenet first; torrents when Usenet has nothing.'
  if (r.torrents && t?.vpn.configured && t.vpn.status !== 'up') readyText += ' The VPN is down, so torrents are paused until it is back.'
  else if (r.torrents && !r.usenet) readyText += t?.vpn.configured ? ' Torrents go only through your VPN.' : ' Torrents run without a VPN, so others sharing a file see your home address.'
  if (dl.subtitles.installed) readyText += ' Subtitles come by themselves.'
  return (
    <div className="stack loose dl">
      {dl.ready ? (
        <div className="box-hero ok">
          <span className="box-hero-ico"><Icon name="check" size={20} /></span>
          <div className="grow"><b>Downloads are ready</b><span>{readyText}</span></div>
        </div>
      ) : (
        <div className="box-hero">
          <span className="box-hero-ico"><Icon name="download" size={20} /></span>
          <div className="grow"><b>{started ? 'Almost there' : t ? 'Choose how the box downloads' : 'Two things to finish'}</b>
            <span>{t
              ? 'Usenet is a paid service: fast and private. Torrents are free, but others sharing a file see your home address unless you use a VPN. One of them is enough; with both, the box tries Usenet first.'
              : 'The box has everything else installed and connected. It needs your Usenet provider and one indexer; both are services you sign up for yourself.'}</span>
            <div className="dl-routes">
              <div className="box-checks"><b>Usenet</b><Check ok={r.provider}>A provider that works</Check><Check ok={r.usenetIndexer}>A Usenet indexer</Check></div>
              {t && <div className="box-checks"><b>Torrents</b><Check ok={r.torrentsOn}>Switched on</Check><Check ok={r.torrentIndexer}>A torrent site</Check></div>}
            </div>
          </div>
        </div>
      )}
      {starting.length > 0 && <div className="callout"><Icon name="clock" size={15} /><span>Still starting: {starting.map(s => SERVICE[s.type] || s.type).join(', ')}. Give it a minute, then open this page again.</span></div>}
      <UsenetPart dl={dl} onChange={onChange} />
      {t && <TorrentsPart dl={dl} onChange={onChange} />}
      <IndexersPart dl={dl} onChange={onChange} />
      {dl.subtitles.installed && <SubtitlesPart dl={dl} onChange={onChange} />}
    </div>
  )
}

function PartHead({ icon, title, text }: { icon: string; title: string; text: React.ReactNode }) {
  return (
    <div className="dl-head">
      <span className="dl-head-ico"><Icon name={icon} size={15} /></span>
      <div className="grow"><b>{title}</b><span>{text}</span></div>
    </div>
  )
}

const CUSTOM = 'custom'

function UsenetPart({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const { toast } = useToast()
  const u = dl.usenet
  const providers = dl.presets.providers
  const known = providers.find(p => p.host === u.host)
  const [open, setOpen] = useState(!u.configured)
  const [preset, setPreset] = useState(u.configured ? (known?.id || CUSTOM) : '')
  const [host, setHost] = useState(u.host)
  const [port, setPort] = useState(String(u.port || 563))
  const [ssl, setSsl] = useState(u.configured ? u.ssl : true)
  const [server, setServer] = useState(false) // show the server fields for a preset
  const [username, setUsername] = useState(u.username)
  const [password, setPassword] = useState('')
  const [connections, setConnections] = useState(String(u.connections || 20))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [removing, setRemoving] = useState(false)

  const pick = (id: string) => {
    setPreset(id); setErr('')
    const p = providers.find(x => x.id === id)
    if (p) { setHost(p.host); setPort(String(p.port)); setSsl(p.ssl); setServer(false) }
    else if (id === CUSTOM && known) { setHost(''); setPort('563'); setSsl(true) }
  }
  const options = useMemo(() => [...providers.map(p => ({ value: p.id, label: p.name, hint: p.host })), { value: CUSTOM, label: 'Another provider', hint: 'type in its server' }], [providers])
  const keepsPassword = u.configured && host.trim() === u.host
  const portN = Number(port)
  const connN = Number(connections)
  const valid = !!preset && !!host.trim() && portN > 0 && portN < 65536 && !!username.trim() && (keepsPassword || !!password) && connN >= 1 && connN <= 100

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try {
      const body: Record<string, unknown> = { host: host.trim(), port: portN, ssl, username: username.trim(), connections: connN }
      if (password) body.password = password
      const got = await api.put<BoxDownloads>(`${DOWNLOADS}/usenet`, body)
      onChange(got)
      setPassword('')
      if (got.usenet.status === 'ok') { setOpen(false); toast('The Usenet provider works') }
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const remove = async () => {
    setRemoving(false); setBusy(true); setErr('')
    try {
      const got = await api.del<BoxDownloads>(`${DOWNLOADS}/usenet`)
      onChange(got)
      setOpen(true); setPreset(''); setHost(''); setUsername(''); setPassword('')
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }

  return (
    <div className="stack tight">
      <PartHead icon="server" title="Usenet provider" text="The company you pay for Usenet, like Eweka or Newshosting. Their welcome email has your username and password." />
      {u.configured && <div className={`box-link ${u.status === 'ok' ? 'on' : ''}`}>
        <span className="box-link-ico"><Icon name={u.status === 'error' ? 'alert' : 'server'} size={17} /></span>
        <div className="grow"><b>{providerName(dl)}</b><span>{u.username} · {u.connections} connection{u.connections === 1 ? '' : 's'}{known ? '' : ` · port ${u.port}${u.ssl ? ', secure' : ''}`}</span></div>
        <StatusBadge status={u.status} />
      </div>}
      {u.configured && u.status === 'error' && <Problem text={u.message || 'The box could not sign in to this server.'} />}
      {u.configured && !open && <div className="row wrap">
        <button type="button" className="btn sm" onClick={() => { setOpen(true); setPreset(known?.id || CUSTOM); setHost(u.host); setPort(String(u.port)); setSsl(u.ssl); setUsername(u.username); setConnections(String(u.connections)) }}><Icon name="edit" size={14} />Change</button>
        <button type="button" className="btn sm danger-ghost" onClick={() => setRemoving(true)} disabled={busy}>Remove</button>
      </div>}
      {open && <form onSubmit={save} className="stack tight dl-form">
        <Field label="Which provider do you have?">
          <Select value={preset} onChange={pick} options={options} placeholder="Pick your provider…" ariaLabel="Usenet provider" />
        </Field>
        {preset && <>
          {preset !== CUSTOM && !server && <p className="muted small">Server {host}, port {port}{ssl ? ', secure' : ''}. <button type="button" className="link-btn" onClick={() => setServer(true)}>Change</button></p>}
          {(preset === CUSTOM || server) && <div className="dl-server">
            <Field label="Server" help="From the provider's welcome email, like news.example.com."><input value={host} onChange={e => setHost(e.target.value)} placeholder="news.example.com" autoComplete="off" spellCheck={false} /></Field>
            <Field label="Port"><input value={port} onChange={e => setPort(e.target.value.replace(/\D/g, ''))} inputMode="numeric" style={{ width: 90 }} /></Field>
            <Field label="Secure (SSL)" group><Switch on={ssl} onChange={v => { setSsl(v); if (port === '563' || port === '119') setPort(v ? '563' : '119') }} label="Secure connection (SSL)" /></Field>
          </div>}
          <div className="two-col tight">
            <Field label="Username"><input value={username} onChange={e => setUsername(e.target.value)} autoComplete="off" spellCheck={false} /></Field>
            <Field label="Password"><PasswordInput value={password} onChange={setPassword} label="Password" placeholder={keepsPassword ? 'Leave empty to keep the saved one' : ''} /></Field>
          </div>
          <Field label="Connections" help="How many downloads at once your plan allows; the welcome email or your account page says. 20 suits most plans.">
            <input value={connections} onChange={e => setConnections(e.target.value.replace(/\D/g, ''))} inputMode="numeric" style={{ width: 90 }} />
          </Field>
        </>}
        <Problem text={err} />
        <div className="row wrap">
          <button className="btn primary" disabled={busy || !valid}>{busy ? <><Spinner />Testing…</> : 'Save and test'}</button>
          {u.configured && <button type="button" className="btn ghost" onClick={() => { setOpen(false); setErr(''); setPassword('') }} disabled={busy}>Cancel</button>}
        </div>
      </form>}
      {!open && <Problem text={err} />}
      {removing && <Confirm title="Remove the Usenet provider" message="Nothing downloads until you add one again. Films and shows already on the box stay." confirmLabel="Remove"
        onConfirm={remove} onCancel={() => setRemoving(false)} />}
    </div>
  )
}

const CUSTOM_TORRENT = 'custom-torrent'
const NEED_LABEL: Record<string, string> = { api_key: 'API key', username: 'Username', password: 'Password', passkey: 'Passkey', cookie: 'Cookie' }
const needLabel = (k: string) => NEED_LABEL[k] || (k.charAt(0).toUpperCase() + k.slice(1)).replace(/_/g, ' ')
const bare = (u: string) => u.replace(/^https?:\/\//, '').replace(/\/$/, '')

function IndexersPart({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const { toast } = useToast()
  const t = dl.torrents
  const taken = new Set(dl.indexers.map(x => x.preset).filter(Boolean))
  const [open, setOpen] = useState(dl.indexers.length === 0)
  const [preset, setPreset] = useState('')
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [key, setKey] = useState('')
  const [needs, setNeeds] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [removing, setRemoving] = useState<{ id: number; name: string } | null>(null)
  const p: IndexerPreset | undefined = dl.presets.indexers.find(x => x.id === preset)
  const torrentPick = preset === CUSTOM_TORRENT || (!!p && kindOf(p) === 'torrent')
  // Usenet first, then torrent sites (only on a box with torrents), each with its own "another".
  const options = useMemo(() => {
    const free = dl.presets.indexers.filter(x => !taken.has(x.id))
    const group = (k: Kind) => (t ? (k === 'usenet' ? 'Usenet indexers' : 'Torrent sites') : undefined)
    const usenet = free.filter(x => kindOf(x) === 'usenet').map(x => ({ value: x.id, label: x.name, hint: x.signup ? bare(x.signup) : '', group: group('usenet') }))
    const out = [...usenet, { value: CUSTOM, label: 'Another Usenet indexer', hint: 'type in its address', group: group('usenet') }]
    if (t) {
      const torrent = free.filter(x => kindOf(x) === 'torrent').sort((a, b) => Number(!!b.public) - Number(!!a.public) || a.name.localeCompare(b.name))
        .map(x => ({ value: x.id, label: x.name, hint: x.public ? 'public · nothing to type in' : 'private · needs an account', group: group('torrent') }))
      out.push(...torrent, { value: CUSTOM_TORRENT, label: 'Another torrent indexer', hint: 'Torznab address', group: group('torrent') })
    }
    return out
  }, [dl.presets.indexers, dl.indexers, t])
  const need = p && kindOf(p) === 'torrent' && !p.public ? (p.needs?.length ? p.needs : ['api_key']) : []
  const okUrl = /^https?:\/\/\S+$/.test(url.trim())
  let valid = false
  if (p && kindOf(p) === 'usenet') valid = !!key.trim()
  else if (p) valid = need.every(k => !!(needs[k] || '').trim())
  else if (preset === CUSTOM) valid = !!key.trim() && !!name.trim() && okUrl
  else if (preset === CUSTOM_TORRENT) valid = !!name.trim() && okUrl

  const reset = () => { setPreset(''); setName(''); setUrl(''); setKey(''); setNeeds({}) }
  const add = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try {
      let body: Record<string, unknown>
      if (p && kindOf(p) === 'usenet') body = { preset: p.id, api_key: key.trim() }
      else if (p) { body = { kind: 'torrent', preset: p.id }; for (const k of need) body[k] = k === 'password' ? needs[k] : (needs[k] || '').trim() }
      else if (preset === CUSTOM_TORRENT) body = { kind: 'torrent', name: name.trim(), url: url.trim(), ...(key.trim() ? { api_key: key.trim() } : {}) }
      else body = { name: name.trim(), url: url.trim(), api_key: key.trim() }
      const got = await api.post<BoxDownloads>(`${DOWNLOADS}/indexers`, body)
      onChange(got)
      // How the test went: the new one is the indexer that was not there before.
      const before = new Set(dl.indexers.map(x => x.id))
      const added = got.indexers.find(x => !before.has(x.id))
      // A failed test still adds it: the list shows why, with Remove next to it.
      setOpen(false); reset()
      if (!added || added.status === 'ok') toast(`${added?.name || 'The indexer'} works`)
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const remove = async () => {
    if (!removing) return
    const id = removing.id
    setRemoving(null); setErr('')
    try {
      const got = await api.del<BoxDownloads>(`${DOWNLOADS}/indexers/${id}`)
      onChange(got)
      if (!got.indexers.length) setOpen(true)
    } catch (e) { setErr(errMsg(e)) }
  }

  const groups: { kind: Kind; title: string }[] = t && dl.indexers.some(x => kindOf(x) === 'torrent') ? [{ kind: 'usenet', title: 'Usenet' }, { kind: 'torrent', title: 'Torrents' }] : [{ kind: 'usenet', title: '' }, { kind: 'torrent', title: '' }]
  return (
    <div className="stack tight">
      <PartHead icon="search" title="Indexers" text={t
        ? 'Websites that list what can be downloaded, so the box finds what you ask for. Usenet needs a Usenet indexer (most have a free plan); for torrents, public sites need nothing at all.'
        : 'Websites that list what is on Usenet, so the box can find what you ask for. You need at least one; most have a free plan, and a paid one costs a few euros a year.'} />
      {groups.map(g => {
        const items = dl.indexers.filter(x => kindOf(x) === g.kind)
        if (!items.length) return null
        return (
          <div key={g.kind} className="stack tight">
            {g.title && <b className="box-section-title">{g.title}</b>}
            {items.map(x => {
              const idle = g.kind === 'torrent' && !t?.enabled
              return (
                <div key={x.id} className="dl-item">
                  <div className={`box-link ${x.status === 'ok' && !idle ? 'on' : ''}`}>
                    <span className="box-link-ico"><Icon name={x.status === 'error' ? 'alert' : g.kind === 'torrent' ? 'share' : 'search'} size={17} /></span>
                    <div className="grow"><b>{x.name}{x.public && <span className="badge">Public</span>}</b>
                      {x.status === 'error' && x.message ? <span className="dl-err">{x.message}</span> : idle ? <span>Not used while torrents are off</span> : x.status === 'ok' ? <span>Answering searches</span> : null}</div>
                    <StatusBadge status={x.status} />
                    <button type="button" className="btn ghost icon-only sm" onClick={() => setRemoving({ id: x.id, name: x.name })} aria-label={`Remove ${x.name}`} title="Remove"><Icon name="trash" size={14} /></button>
                  </div>
                </div>
              )
            })}
          </div>
        )
      })}
      {!open && <div className="row"><button type="button" className="btn sm" onClick={() => { setOpen(true); setErr('') }}><Icon name="plus" size={14} />Add an indexer</button></div>}
      {open && <form onSubmit={add} className="stack tight dl-form">
        <Field label={t ? 'Which indexer or torrent site?' : 'Which indexer?'}>
          <Select value={preset} onChange={v => { setPreset(v); setErr(''); setNeeds({}) }} options={options} placeholder="Pick one…" ariaLabel="Indexer" />
        </Field>
        {p && kindOf(p) === 'usenet' && <ol className="dl-steps">
          {p.signup && <li>Make an account at <a href={p.signup} target="_blank" rel="noopener noreferrer">{bare(p.signup)} <Icon name="external" size={12} /></a> if you have none.</li>}
          <li>Copy the <b>API key</b> from your profile or account page there.</li>
          <li>Paste it here:</li>
        </ol>}
        {p && kindOf(p) === 'torrent' && p.public && <div className="callout info"><Icon name="check" size={15} /><span><b>{p.name}</b> is public: nothing to type in.</span></div>}
        {p && kindOf(p) === 'torrent' && !p.public && <p className="small">{p.name} is private: it needs your account there{p.signup ? <> (<a href={p.signup} target="_blank" rel="noopener noreferrer">{bare(p.signup)} <Icon name="external" size={12} /></a>)</> : null}.</p>}
        {need.length > 0 && <div className="two-col tight">
          {need.map(k => <Field key={k} label={needLabel(k)}>{k === 'password'
            ? <PasswordInput value={needs[k] || ''} onChange={v => setNeeds({ ...needs, [k]: v })} label={needLabel(k)} placeholder="" />
            : <input value={needs[k] || ''} onChange={e => setNeeds({ ...needs, [k]: e.target.value })} autoComplete="off" spellCheck={false} />}</Field>)}
        </div>}
        {torrentPick && !t?.enabled && <p className="muted small">Torrents are off: the box uses this site once you switch them on above.</p>}
        {(preset === CUSTOM || preset === CUSTOM_TORRENT) && <div className="two-col tight">
          <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} placeholder={preset === CUSTOM ? 'My indexer' : 'My tracker'} autoComplete="off" /></Field>
          <Field label="Address" help={preset === CUSTOM ? 'Its Newznab API address, like https://api.example.com.' : 'Its Torznab address, like https://tracker.example.com/api.'}><input value={url} onChange={e => setUrl(e.target.value)} placeholder="https://" autoComplete="off" spellCheck={false} /></Field>
        </div>}
        {(p && kindOf(p) === 'usenet' || preset === CUSTOM || preset === CUSTOM_TORRENT) && <Field label={preset === CUSTOM_TORRENT ? 'API key (if it has one)' : 'API key'}><input value={key} onChange={e => setKey(e.target.value)} placeholder="Paste the API key" autoComplete="off" spellCheck={false} /></Field>}
        <Problem text={err} />
        <div className="row wrap">
          <button className="btn primary" disabled={busy || !valid}>{busy ? <><Spinner />Testing…</> : 'Add and test'}</button>
          {dl.indexers.length > 0 && <button type="button" className="btn ghost" onClick={() => { setOpen(false); setErr(''); reset() }} disabled={busy}>Cancel</button>}
        </div>
      </form>}
      {!open && <Problem text={err} />}
      {removing && <Confirm title={`Remove ${removing.name}`} message="The box stops searching it. You can add it again any time." confirmLabel="Remove"
        onConfirm={remove} onCancel={() => setRemoving(null)} />}
    </div>
  )
}

function TorrentsPart({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const { toast } = useToast()
  const t = dl.torrents!
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [askOn, setAskOn] = useState(false)
  const [ratio, setRatio] = useState(String(t.seeding.ratio))
  const [days, setDays] = useState(String(t.seeding.days))
  useEffect(() => { setRatio(String(t.seeding.ratio)); setDays(String(t.seeding.days)) }, [t.seeding.ratio, t.seeding.days])
  const put = async (body: Record<string, unknown>) => {
    setBusy(true); setErr('')
    try { const got = await api.put<BoxDownloads>(`${DOWNLOADS}/torrents`, body); onChange(got); return got } catch (e) { setErr(errMsg(e)); return null } finally { setBusy(false) }
  }
  const turn = async (on: boolean) => {
    setAskOn(false)
    const got = await put({ enabled: on })
    if (got) toast(!on ? 'Torrents are off' : got.torrents?.status === 'error' ? 'Torrents are on, but not working yet' : 'Torrents are on')
  }
  const r = Number(ratio.replace(',', '.')), d = Number(days)
  const seedValid = r > 0 && r <= 100 && Number.isInteger(d) && d >= 1 && d <= 365
  const seedChanged = r !== t.seeding.ratio || d !== t.seeding.days
  const state = !t.enabled ? 'Off' : t.status === 'error' ? 'On, but not working' : t.vpn.configured ? (t.vpn.status === 'up' ? 'On, only through your VPN' : 'On, paused until the VPN is back') : 'On, without a VPN'
  return (
    <div className="stack tight">
      <PartHead icon="share" title="Torrents" text="Free: the box downloads from other people sharing the same file, and shares it back for a while. Off until you switch it on." />
      <div className={`box-link ${t.enabled && t.status === 'ok' ? 'on' : ''}`}>
        <span className="box-link-ico"><Icon name={t.enabled && t.status === 'error' ? 'alert' : 'share'} size={17} /></span>
        <div className="grow"><b>Use torrents</b><span>{state}</span></div>
        {t.enabled && t.status !== 'off' && (t.status === 'ok' && t.vpn.configured && t.vpn.status !== 'up' ? <span className="badge warn"><Icon name="pause" size={11} />Paused</span> : <StatusBadge status={t.status === 'ok' ? 'ok' : 'error'} />)}
        <Switch on={t.enabled} disabled={busy} label="Use torrents" onChange={on => (on && !t.vpn.configured ? setAskOn(true) : turn(on))} />
      </div>
      {t.enabled && t.status === 'error' && <Problem text={t.message || 'qBittorrent is not answering.'} />}
      {!t.vpn.configured && <div className={`callout ${t.enabled ? 'warn' : ''}`}><Icon name="alert" size={15} />
        <span><b>{t.enabled ? 'Your home address is visible:' : 'Before you switch them on:'}</b> everyone sharing the same file sees the box's internet address, and in many countries rights holders write to your internet provider about films and shows. A VPN hides it; add one below.</span></div>}
      <VpnBlock dl={dl} onChange={onChange} />
      {t.enabled && <div className="dl-os">
        <span className="small"><b>Sharing back</b><br /><span className="muted">After a download, the box keeps sharing it until it has sent this many times its size, or for this many days, whichever comes first.</span></span>
        <div className="row wrap dl-seed">
          <Field label="Times its size"><input value={ratio} onChange={e => setRatio(e.target.value.replace(/[^\d.,]/g, ''))} inputMode="decimal" style={{ width: 90 }} /></Field>
          <Field label="Days"><input value={days} onChange={e => setDays(e.target.value.replace(/\D/g, ''))} inputMode="numeric" style={{ width: 90 }} /></Field>
          {seedChanged && <button type="button" className="btn primary" disabled={busy || !seedValid} onClick={async () => { if (await put({ enabled: t.enabled, seeding: { ratio: r, days: d } })) toast('Saved') }}>{busy ? <Spinner /> : null}Save</button>}
        </div>
      </div>}
      <Problem text={err} />
      {askOn && <Confirm title="Use torrents without a VPN?" confirmLabel="Switch on anyway"
        message="Everyone sharing a file will see your home internet address. You can add a VPN first (below) and switch torrents on afterwards."
        onConfirm={() => turn(true)} onCancel={() => setAskOn(false)} />}
    </div>
  )
}

function VpnBlock({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const { toast } = useToast()
  const t = dl.torrents!
  const v = t.vpn
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [removing, setRemoving] = useState(false)
  const file = useRef<HTMLInputElement>(null)
  const looksRight = /\[Interface\]/i.test(text) && /\[Peer\]/i.test(text)
  const pick = async (f?: File) => {
    if (!f) return
    if (f.size > 32 << 10) { setErr("That file is too big for a WireGuard configuration."); return }
    setText(await f.text()); setErr('')
  }
  const save = async () => {
    setBusy(true); setErr('')
    try {
      const got = await api.put<BoxDownloads>(`${DOWNLOADS}/vpn`, { wireguard: text })
      onChange(got)
      if (got.torrents?.vpn.status === 'up') { setOpen(false); setText(''); toast('The VPN is connected') }
      else setErr(got.torrents?.vpn.message || "The VPN didn't come up with this configuration.")
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const remove = async () => {
    setRemoving(false); setBusy(true); setErr('')
    try { onChange(await api.del<BoxDownloads>(`${DOWNLOADS}/vpn`)) } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  return (
    <div className="dl-os">
      <div className="row wrap">
        <span className="grow small"><b>VPN</b> <span className="muted">(recommended for torrents)</span><br />
          <span className="muted">{v.configured
            ? <>Torrents go out only through {v.endpoint ? <b>{v.endpoint}</b> : 'your VPN'}. When it is down they stop, so your address never shows.</>
            : 'Most VPN services (Mullvad, Proton VPN, AirVPN, IVPN…) let you download a WireGuard configuration file from your account.'}</span></span>
        {v.configured && (v.status === 'up' ? <span className="badge ok"><Icon name="check" size={11} />Connected</span> : <span className="badge danger"><Icon name="alert" size={11} />Down</span>)}
        {!open && <button type="button" className="btn sm" onClick={() => { setOpen(true); setErr('') }} disabled={busy}>{v.configured ? 'Replace' : 'Add a VPN'}</button>}
        {v.configured && !open && <button type="button" className="btn sm danger-ghost" onClick={() => setRemoving(true)} disabled={busy}>Remove</button>}
      </div>
      {v.configured && v.status !== 'up' && !open && <Problem text={v.message || 'The VPN is down, so torrents are paused.'} />}
      {open && <div className="stack tight">
        <ol className="dl-steps">
          <li>In your VPN's account, make a <b>WireGuard</b> configuration for a server near you (some call it “manual setup” or “router”).</li>
          <li>Download the <code>.conf</code> file, or copy its text.</li>
          <li>Upload or paste it here:</li>
        </ol>
        <div className="row"><button type="button" className="btn sm" onClick={() => file.current?.click()}><Icon name="upload" size={14} />Choose the .conf file</button>
          <input ref={file} type="file" accept=".conf,text/plain" hidden onChange={e => { pick(e.target.files?.[0]); e.target.value = '' }} /></div>
        <textarea className="dl-conf" value={text} onChange={e => { setText(e.target.value); setErr('') }} rows={7} spellCheck={false} aria-label="WireGuard configuration"
          placeholder={'[Interface]\nPrivateKey = …\nAddress = …\n\n[Peer]\nPublicKey = …\nEndpoint = …'} />
        {text.trim() && !looksRight && <p className="muted small">This doesn't look like a WireGuard configuration: it should have an [Interface] and a [Peer] part.</p>}
        <Problem text={err} />
        <div className="row wrap">
          <button type="button" className="btn primary" disabled={busy || !looksRight} onClick={save}>{busy ? <><Spinner />Connecting…</> : 'Save and connect'}</button>
          <button type="button" className="btn ghost" onClick={() => { setOpen(false); setText(''); setErr('') }} disabled={busy}>Cancel</button>
        </div>
      </div>}
      {!open && v.configured && <Problem text={err} />}
      {removing && <Confirm title="Remove the VPN" confirmLabel="Remove"
        message={t.enabled ? 'Torrents keep running, without the VPN: others sharing a file will see your home address.' : 'The box deletes the configuration.'}
        onConfirm={remove} onCancel={() => setRemoving(false)} />}
    </div>
  )
}

function SubtitlesPart({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const { toast } = useToast()
  const s = dl.subtitles
  const [langs, setLangs] = useState<string[]>(s.languages)
  const [osOpen, setOsOpen] = useState(false)
  const [osUser, setOsUser] = useState(s.opensubtitles.username)
  const [osPw, setOsPw] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => setLangs(s.languages), [s.languages.join(',')])
  const options = useMemo(() => [...new Set([...LANGS, ...s.languages])].filter(c => !langs.includes(c)).map(c => ({ value: c, label: langName(c), hint: c })).sort((a, b) => a.label.localeCompare(b.label)), [langs, s.languages])
  const langsChanged = langs.join(',') !== s.languages.join(',')
  const osChanged = osOpen && !!osUser.trim() && !!osPw
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try {
      const body: Record<string, unknown> = { languages: langs }
      if (osChanged) body.opensubtitles = { username: osUser.trim(), password: osPw }
      onChange(await api.put<BoxDownloads>(`${DOWNLOADS}/subtitles`, body))
      setOsPw(''); setOsOpen(false)
      toast('Subtitles saved')
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="stack tight">
      <PartHead icon="text" title="Subtitles" text="The box looks for subtitles in these languages for everything it downloads, in this order." />
      <div className="box-chips dl-langs">
        {langs.map(c => <button key={c} type="button" className="box-chip" onClick={() => setLangs(langs.filter(x => x !== c))} aria-label={`Remove ${langName(c)}`} title="Remove">{langName(c)}<Icon name="x" size={12} /></button>)}
        {!langs.length && <span className="muted small">No language yet: add at least one.</span>}
      </div>
      <div style={{ maxWidth: 300 }}><Select value="" onChange={c => c && setLangs([...langs, c])} options={options} placeholder="Add a language…" ariaLabel="Add a subtitle language" /></div>
      <div className="dl-os">
        <div className="row wrap">
          <span className="grow small"><b>OpenSubtitles.com</b> <span className="muted">(optional)</span><br />
            <span className="muted">{s.opensubtitles.configured ? <>Signed in as <b>{s.opensubtitles.username}</b>.</> : 'A free account there finds more subtitles each day.'}</span></span>
          {!osOpen && <button type="button" className="btn sm" onClick={() => { setOsOpen(true); setOsUser(s.opensubtitles.username) }}>{s.opensubtitles.configured ? 'Change' : 'Sign in'}</button>}
        </div>
        {osOpen && <div className="stack tight">
          <div className="two-col tight">
            <Field label="Username"><input value={osUser} onChange={e => setOsUser(e.target.value)} autoComplete="off" spellCheck={false} /></Field>
            <Field label="Password"><PasswordInput value={osPw} onChange={setOsPw} label="OpenSubtitles.com password" placeholder="" /></Field>
          </div>
          <p className="muted small">No account yet? <a href="https://www.opensubtitles.com/en/users/sign_up" target="_blank" rel="noopener noreferrer">Make one at opensubtitles.com <Icon name="external" size={12} /></a> (opensubtitles.org accounts don't work here).</p>
        </div>}
      </div>
      <Problem text={err} />
      {(langsChanged || osOpen) && <div className="row wrap">
        <button className="btn primary" disabled={busy || !langs.length || (osOpen && !osChanged && !langsChanged)}>{busy ? <Spinner /> : null}Save</button>
        <button type="button" className="btn ghost" onClick={() => { setLangs(s.languages); setOsOpen(false); setOsPw(''); setErr('') }} disabled={busy}>Cancel</button>
      </div>}
    </form>
  )
}
