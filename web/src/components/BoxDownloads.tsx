// Downloads on the Pocket Box, through the box downloads service (BOX_DOWNLOADS). The box runs and
// wires Radarr, Sonarr, Prowlarr, SABnzbd (Lidarr, Bazarr) by itself; the owner only brings a Usenet
// provider and an indexer's API key, and for subtitles the languages and an optional
// OpenSubtitles.com login. Settings → System → Downloads (/settings/system#downloads, where Pocket
// Requests sends people) and the setup wizard's optional Downloads step share this panel.
import { useEffect, useMemo, useState } from 'react'
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

/** One line for the wizard's summary. */
export function downloadsSummary(dl: BoxDownloads | null): { text: string; ok: boolean } {
  if (!dl) return { text: 'Not set up yet', ok: false }
  const usenet = dl.usenet.configured && dl.usenet.status === 'ok'
  const indexers = dl.indexers.filter(x => x.status === 'ok').length
  if (dl.ready) return { text: `${providerName(dl)}, ${indexers} indexer${indexers === 1 ? '' : 's'}`, ok: true }
  if (!dl.usenet.configured && !dl.indexers.length) return { text: 'Not set up yet', ok: false }
  if (!usenet) return { text: dl.usenet.configured ? 'The Usenet provider has a problem' : 'Usenet provider still missing', ok: false }
  return { text: dl.indexers.length ? 'No indexer works yet' : 'Indexer still missing', ok: false }
}

const SERVICE: Record<string, string> = {
  radarr: 'Films (Radarr)', sonarr: 'Shows (Sonarr)', lidarr: 'Music (Lidarr)', prowlarr: 'Indexers (Prowlarr)', sabnzbd: 'Downloader (SABnzbd)', bazarr: 'Subtitles (Bazarr)',
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

/** Everything about downloads on the box: ready or not, the provider, the indexers, subtitles. */
export function DownloadsPanel({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const usenetOk = dl.usenet.configured && dl.usenet.status === 'ok'
  const indexerOk = dl.indexers.some(x => x.status === 'ok')
  const starting = dl.services.filter(s => !s.ready)
  return (
    <div className="stack loose dl">
      {dl.ready ? (
        <div className="box-hero ok">
          <span className="box-hero-ico"><Icon name="check" size={20} /></span>
          <div className="grow"><b>Downloads are ready</b><span>Pocket Requests can now find and download the films and shows you ask for.{dl.subtitles.installed ? ' Subtitles come by themselves.' : ''}</span></div>
        </div>
      ) : (
        <div className="box-hero">
          <span className="box-hero-ico"><Icon name="download" size={20} /></span>
          <div className="grow"><b>{usenetOk || indexerOk ? 'One thing left' : 'Two things to finish'}</b>
            <span>{usenetOk ? 'Add an indexer that works and the box can start.' : indexerOk ? (dl.usenet.configured ? 'Your Usenet provider has to work too; see below.' : 'Add your Usenet provider and the box can start.')
              : 'The box has everything else installed and connected. It needs your Usenet provider and one indexer; both are services you sign up for yourself.'}</span>
            <div className="box-checks dl-checks">
              <span className={usenetOk ? 'ok' : ''}><Icon name={usenetOk ? 'check' : 'circle'} size={13} />A Usenet provider that works</span>
              <span className={indexerOk ? 'ok' : ''}><Icon name={indexerOk ? 'check' : 'circle'} size={13} />An indexer that works</span>
            </div>
          </div>
        </div>
      )}
      {starting.length > 0 && <div className="callout"><Icon name="clock" size={15} /><span>Still starting: {starting.map(s => SERVICE[s.type] || s.type).join(', ')}. Give it a minute, then open this page again.</span></div>}
      <UsenetPart dl={dl} onChange={onChange} />
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

function IndexersPart({ dl, onChange }: { dl: BoxDownloads; onChange: (dl: BoxDownloads) => void }) {
  const { toast } = useToast()
  const taken = new Set(dl.indexers.map(x => x.preset).filter(Boolean))
  const free = dl.presets.indexers.filter(p => !taken.has(p.id))
  const [open, setOpen] = useState(dl.indexers.length === 0)
  const [preset, setPreset] = useState('')
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [removing, setRemoving] = useState<{ id: number; name: string } | null>(null)
  const p: IndexerPreset | undefined = dl.presets.indexers.find(x => x.id === preset)
  const options = useMemo(() => [...free.map(x => ({ value: x.id, label: x.name, hint: x.signup.replace(/^https?:\/\//, '').replace(/\/$/, '') })), { value: CUSTOM, label: 'Another indexer', hint: 'type in its address' }], [free])
  const valid = !!key.trim() && (!!p || (preset === CUSTOM && !!name.trim() && /^https?:\/\/\S+$/.test(url.trim())))

  const add = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try {
      const body = p ? { preset: p.id, api_key: key.trim() } : { name: name.trim(), url: url.trim(), api_key: key.trim() }
      const got = await api.post<BoxDownloads>(`${DOWNLOADS}/indexers`, body)
      onChange(got)
      // How the test went: the new one is the indexer that was not there before.
      const before = new Set(dl.indexers.map(x => x.id))
      const added = got.indexers.find(x => !before.has(x.id))
      // A failed test still adds it: the list shows why, with Remove next to it.
      setOpen(false); setPreset(''); setName(''); setUrl(''); setKey('')
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

  return (
    <div className="stack tight">
      <PartHead icon="search" title="Indexers" text="Websites that list what is on Usenet, so the box can find what you ask for. You need at least one; most have a free plan, and a paid one costs a few euros a year." />
      {dl.indexers.length > 0 && <div className="stack tight">
        {dl.indexers.map(x => (
          <div key={x.id} className="dl-item">
            <div className={`box-link ${x.status === 'ok' ? 'on' : ''}`}>
              <span className="box-link-ico"><Icon name={x.status === 'error' ? 'alert' : 'search'} size={17} /></span>
              <div className="grow"><b>{x.name}</b>{x.status === 'error' && x.message ? <span className="dl-err">{x.message}</span> : x.status === 'ok' ? <span>Answering searches</span> : null}</div>
              <StatusBadge status={x.status} />
              <button type="button" className="btn ghost icon-only sm" onClick={() => setRemoving({ id: x.id, name: x.name })} aria-label={`Remove ${x.name}`} title="Remove"><Icon name="trash" size={14} /></button>
            </div>
          </div>
        ))}
      </div>}
      {!open && <div className="row"><button type="button" className="btn sm" onClick={() => { setOpen(true); setErr('') }}><Icon name="plus" size={14} />Add an indexer</button></div>}
      {open && <form onSubmit={add} className="stack tight dl-form">
        <Field label="Which indexer?">
          <Select value={preset} onChange={v => { setPreset(v); setErr('') }} options={options} placeholder="Pick an indexer…" ariaLabel="Indexer" />
        </Field>
        {p && <ol className="dl-steps">
          <li>Make an account at <a href={p.signup} target="_blank" rel="noopener noreferrer">{p.signup.replace(/^https?:\/\//, '').replace(/\/$/, '')} <Icon name="external" size={12} /></a> if you have none.</li>
          <li>Copy the <b>API key</b> from your profile or account page there.</li>
          <li>Paste it here:</li>
        </ol>}
        {preset === CUSTOM && <div className="two-col tight">
          <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} placeholder="My indexer" autoComplete="off" /></Field>
          <Field label="Address" help="Its Newznab API address, like https://api.example.com."><input value={url} onChange={e => setUrl(e.target.value)} placeholder="https://" autoComplete="off" spellCheck={false} /></Field>
        </div>}
        {preset && <Field label="API key"><input value={key} onChange={e => setKey(e.target.value)} placeholder="Paste the API key" autoComplete="off" spellCheck={false} /></Field>}
        <Problem text={err} />
        <div className="row wrap">
          <button className="btn primary" disabled={busy || !valid}>{busy ? <><Spinner />Testing…</> : 'Add and test'}</button>
          {dl.indexers.length > 0 && <button type="button" className="btn ghost" onClick={() => { setOpen(false); setErr('') }} disabled={busy}>Cancel</button>}
        </div>
      </form>}
      {!open && <Problem text={err} />}
      {removing && <Confirm title={`Remove ${removing.name}`} message="The box stops searching it. You can add it again any time with the same API key." confirmLabel="Remove"
        onConfirm={remove} onCancel={() => setRemoving(null)} />}
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
