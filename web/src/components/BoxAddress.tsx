// How the Pocket Box gets its padlock, through the box address service (BOX_ADDRESS): Pocket's free
// address (<random>.box.pockethomelab.net), the owner's own domain on Cloudflare ("Sign in with
// Cloudflare" or an API token), or the box's own certificate. The landing page at http://pocket.local
// asks the same thing before anyone has signed in; here an admin can change it at any time.
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { BoxAddress, BoxAddressMode, CloudflareZone } from '../lib/types'
import { Confirm, errMsg, Loading, Spinner } from './ui'
import { Icon } from './icons'

export const ADDRESS = `${V1}/box/address`

const MODES: { id: Exclude<BoxAddressMode, 'off'>; title: string; text: string; tag?: string; icon: string; facts: string[] }[] = [
  { id: 'pocket', icon: 'zap', title: "Pocket's free address", tag: 'Recommended', text: 'A name like x7k2m9.box.pockethomelab.net that points at the box\'s home address. Every browser trusts it.', facts: ['Ready in a minute', 'Nothing to install', 'Free'] },
  { id: 'cloudflare', icon: 'globe', title: 'My own domain on Cloudflare', text: 'Like pocket.yourname.com. Sign in with Cloudflare and the box looks after the DNS records and the certificate itself.', facts: ['Your own name', 'Nothing through Pocket'] },
  { id: 'own-ca', icon: 'shield', title: "The box's own certificate", text: 'No name on the internet. Install the box\'s certificate once on each phone and computer.', facts: ['Works offline', 'Fully private'] },
  { id: 'plain', icon: 'home', title: 'Only on this network', text: 'Each app at this machine\'s address and its own port, like 192.168.1.20:8300. No padlock, so phones may warn and some features (installing as an app, the camera) stay off.', facts: ['Works offline', 'Nothing to set up'] },
]

const WAITING = ['asking', 'certificate', 'waiting']
// Held back for now (a limit, no internet, a sign-in to renew): it is tried again by itself.
const STUCK = ['limited', 'failed', 'offline', 'signin']

function Problem({ text }: { text: string }) {
  return text ? <div className="callout danger"><Icon name="alert" size={15} /><span>{text}</span></div> : null
}

function describe(a: BoxAddress): React.ReactNode {
  const st = a.status?.state || ''
  if (a.mode === 'off') return 'Public names are turned off on this box.'
  if (a.mode === null && a.plain_url) return <>Not chosen yet: Pocket Core is at <b>{a.plain_url}</b> on this network, without a padlock, until you pick one below.</>
  if (a.mode === null) return 'Not chosen yet: the box is on its own certificate until you pick one below.'
  if (a.mode === 'plain') return a.domain ? <>Moving back to this network's addresses…</> : <>The apps are at this machine's address with their own ports (Pocket Core: <b>{a.plain_url}</b>), without a padlock.</>
  if (a.mode === 'own-ca') return <>The box uses its own certificate. Devices need it installed once (from <a href="http://pocket.local" target="_blank" rel="noopener noreferrer">pocket.local</a>).</>
  const want = a.mode === 'cloudflare' && a.cloudflare ? a.cloudflare.domain : ''
  if (a.domain && (!want || a.domain === want)) return <>The apps are at <a href={`https://core.${a.domain}`}><b>{a.domain}</b></a> with a certificate every browser trusts{a.mode === 'cloudflare' ? ', on your own Cloudflare domain' : ', Pocket\'s free address'}.</>
  if (st === 'limited') return <><b>Can't get {want ? want : 'its secure address'} yet.</b> {a.status?.message} {a.plain_url
    ? <>Until then Pocket stays at <b>{a.plain_url}</b> on this network.</>
    : <>Until then the box works with its own certificate (install it from <a href="http://pocket.local" target="_blank" rel="noopener noreferrer">pocket.local</a>).</>}</>
  if (WAITING.includes(st)) return <>Getting {want ? <b>{want}</b> : 'the address'} ready… This takes a minute or two.</>
  return <>Getting {want ? <b>{want}</b> : 'the address'} ready{a.status?.message ? `: ${a.status.message}` : '.'}</>
}

/** The box's address: what it is now, and choosing another. */
export function AddressPanel({ onChosen, startOpen }: { onChosen?: () => void; startOpen?: boolean }) {
  const [a, setA] = useState<BoxAddress | null>(null)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<BoxAddressMode>('pocket')
  const [busy, setBusy] = useState(false)
  // Cloudflare: how the box signs in, then which domain and name.
  const [useToken, setUseToken] = useState(false)
  const [token, setToken] = useState('')
  const [zones, setZones] = useState<CloudflareZone[] | null>(null)
  const [signin, setSignin] = useState<{ pending?: string; token?: string }>({})
  const [zone, setZone] = useState('')
  const [sub, setSub] = useState('pocket')
  const [confirm, setConfirm] = useState<null | (() => void)>(null)

  const pickZones = (zs: CloudflareZone[]) => {
    setZones(zs)
    setZone(zs[0]?.id || '')
    if (!zs.length) setErr("This sign-in can't change any domain. Add your domain to Cloudflare first, or give the token access to it.")
  }
  const load = async () => {
    try {
      const got = await api.get<BoxAddress>(ADDRESS)
      setA(got); setErr('')
      // In the setup wizard, a box that has no address yet shows the choices straight away.
      if (startOpen && got.mode === null && got.can_choose) setOpen(true)
    } catch (e) { setErr(errMsg(e)) }
  }
  useEffect(() => {
    // Back from "Sign in with Cloudflare": Cloudflare → names.pockethomelab.com → here, with the code.
    const q = new URLSearchParams(window.location.search)
    const code = q.get('code'), state = q.get('state'), cfErr = q.get('error')
    if (code || cfErr) {
      for (const k of ['code', 'state', 'error', 'error_description']) q.delete(k)
      window.history.replaceState(null, '', window.location.pathname + (q.toString() ? `?${q}` : '') + window.location.hash)
      setOpen(true); setMode('cloudflare')
      if (cfErr) setErr(`Cloudflare didn't sign you in (${q.get('error_description') || cfErr}).`)
      else if (code && state) {
        setBusy(true)
        api.post<{ pending: string; zones: CloudflareZone[] }>(`${ADDRESS}/cloudflare/finish`, { code, state })
          .then(r => { setSignin({ pending: r.pending }); pickZones(r.zones) })
          .catch(e => setErr(errMsg(e))).finally(() => setBusy(false))
      }
    }
    load()
  }, [])
  // While the box gets its new address, follow it.
  const following = !!a && a.mode !== 'own-ca' && a.mode !== null && (a.mode === 'plain' ? !!a.domain : WAITING.includes(a.status?.state || '') || (a.mode === 'cloudflare' && !!a.cloudflare && a.domain !== a.cloudflare.domain))
  // Held back: look again now and then, so the page shows it once it worked (by itself, or after
  // a limit was lifted) without a reload.
  const stuck = !!a && (a.mode === 'pocket' || a.mode === 'cloudflare') && STUCK.includes(a.status?.state || '') && (!a.domain || (a.mode === 'cloudflare' && !!a.cloudflare && a.domain !== a.cloudflare.domain))
  useEffect(() => {
    if (!following && !stuck) return
    const id = window.setInterval(load, following ? 4000 : 10000)
    return () => window.clearInterval(id)
  }, [following, stuck])
  const [retrying, setRetrying] = useState(false)
  const retry = async () => {
    setRetrying(true); setErr('')
    try { setA(await api.post<BoxAddress>(`${ADDRESS}/retry`)); window.setTimeout(load, 5000) } catch (e) { setErr(errMsg(e)) } finally { window.setTimeout(() => setRetrying(false), 5000) }
  }

  if (!a) return err ? <Problem text={err} /> : <Loading />

  const zoneName = zones?.find(z => z.id === zone)?.name || ''
  const cleanSub = sub.trim().toLowerCase()

  const save = async (body: Record<string, unknown>) => {
    setBusy(true); setErr('')
    try {
      setA(await api.put<BoxAddress>(ADDRESS, body))
      setOpen(false); setZones(null); setToken(''); setSignin({})
      onChosen?.()
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  // Moving off the address this page is open on: say so first.
  const guarded = (body: Record<string, unknown>) => {
    const leaving = a.domain && window.location.hostname.endsWith(a.domain)
    if (leaving) setConfirm(() => () => { setConfirm(null); save(body) })
    else save(body)
  }
  const choose = () => {
    if (mode === 'cloudflare') return
    guarded({ mode })
  }
  const startOAuth = async () => {
    setBusy(true); setErr('')
    try {
      const r = await api.post<{ url: string }>(`${ADDRESS}/cloudflare/start`, { return_url: window.location.href.split('#')[0] })
      window.location.href = r.url
    } catch (e) { setErr(errMsg(e)); setBusy(false) }
  }
  const checkToken = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try { const r = await api.post<{ zones: CloudflareZone[] }>(`${ADDRESS}/cloudflare/zones`, { token: token.trim() }); setSignin({ token: token.trim() }); pickZones(r.zones) }
    catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }

  const st = a.status?.state || ''
  const now = a.domain && a.mode !== 'own-ca' && a.mode !== 'plain'
  const elsewhere = now && !window.location.hostname.endsWith(a.domain) && !following
  return (
    <div className="stack tight">
      <div className={`callout ${st === 'signin' || st === 'failed' ? 'danger' : st === 'limited' && !now ? 'warn' : ''}`}>
        <Icon name={following ? 'refresh' : now ? 'lock' : st === 'limited' ? 'alert' : 'info'} size={15} />
        <span>{describe(a)}</span>
      </div>
      {a.problem && a.problem.state === 'signin' && <Problem text={a.problem.message} />}
      {elsewhere && <p className="muted small">You're on {window.location.hostname}. <a href={`https://core.${a.domain}${window.location.pathname}`}>Open Pocket Core at its address</a>.</p>}
      {!open && a.can_choose && <div className="row">
        {stuck && a.plain_url && <button type="button" className="btn sm primary" onClick={retry} disabled={retrying}>{retrying ? <Spinner /> : <Icon name="refresh" size={14} />}Try again now</button>}
        <button type="button" className="btn sm" onClick={() => { setOpen(true); setMode(a.mode && a.mode !== 'off' ? a.mode : 'pocket') }}>{a.mode ? 'Change' : 'Choose'}</button>
      </div>}
      {open && <div className="stack tight">
        <div className="stack tight" role="radiogroup" aria-label="How the box gets its padlock">
          {MODES.filter(m => a.modes ? a.modes.includes(m.id) : m.id !== 'plain').map(m => (
            <label key={m.id} className={`address-choice ${mode === m.id ? 'on' : ''}`}>
              <input type="radio" name="box-address" checked={mode === m.id} onChange={() => { setMode(m.id); setErr('') }} />
              <span className={`address-ico ${m.id}`}><Icon name={m.icon} size={19} /></span>
              <span className="grow">
                <b>{m.title}</b>{m.tag && <span className="badge ok">{m.tag}</span>}{a.mode === m.id && <span className="badge">Now</span>}
                <span className="address-text">{m.text}</span>
                <span className="address-facts">{m.facts.map(f => <span key={f}><Icon name="check" size={11} />{f}</span>)}</span>
              </span>
              <span className="address-radio" aria-hidden="true" />
            </label>
          ))}
        </div>
        {mode === 'cloudflare' && <div className="stack tight address-cf">
          <p className="muted small">You need a domain whose DNS is on Cloudflare (the free plan is enough). The box adds two records, <code>name.yourdomain</code> and <code>*.name.yourdomain</code>, pointing at its home address, keeps them current and renews the certificate itself. Some routers block names that point into the home network; if yours does, the box's own address keeps working.</p>
          {!zones && (a.options.oauth && !useToken ? <>
            <div className="row wrap">
              <button type="button" className="btn primary" onClick={startOAuth} disabled={busy}>{busy ? <Spinner /> : <Icon name="key" size={15} />}Sign in with Cloudflare</button>
              <button type="button" className="btn ghost" onClick={() => setUseToken(true)}>Use an API token</button>
            </div>
            <p className="muted small">Cloudflare asks you to allow “Pocket Box” to read your domains and edit their DNS. You can take that back any time under My Profile → Access Management in Cloudflare.</p>
          </> : <form onSubmit={checkToken} className="stack tight">
            <p className="small"><b>1.</b> <a href={a.options.token_link} target="_blank" rel="noopener noreferrer">Create a token in Cloudflare <Icon name="external" size={12} /></a>. The permissions are filled in already (Zone · Read and DNS · Edit); press <i>Continue to summary</i>, then <i>Create Token</i>.</p>
            <p className="small"><b>2.</b> Paste it here:</p>
            <div className="row wrap">
              <input value={token} onChange={e => setToken(e.target.value)} placeholder="The token Cloudflare shows once" autoComplete="off" spellCheck={false} style={{ flex: '1 1 260px' }} />
              <button className="btn primary" disabled={busy || !token.trim()}>{busy ? <Spinner /> : null}Next</button>
              {a.options.oauth && <button type="button" className="btn ghost" onClick={() => setUseToken(false)}>Back</button>}
            </div>
          </form>)}
          {zones && zones.length > 0 && <div className="stack tight">
            <div className="row wrap">
              <label className="stack tight" style={{ flex: '1 1 200px' }}><span className="small"><b>Domain</b></span>
                <select value={zone} onChange={e => setZone(e.target.value)}>{zones.map(z => <option key={z.id} value={z.id}>{z.name}</option>)}</select></label>
              <label className="stack tight" style={{ flex: '1 1 160px' }}><span className="small"><b>Name for the box</b></span>
                <input value={sub} onChange={e => { setSub(e.target.value); setErr('') }} autoComplete="off" spellCheck={false} /></label>
            </div>
            <p className="muted small">Pocket Core will be at <b>https://core.{cleanSub || 'pocket'}.{zoneName}</b>, every other app next to it.</p>
            <div className="row wrap">
              <button type="button" className="btn primary" disabled={busy || !zone || !cleanSub} onClick={() => guarded({ mode: 'cloudflare', zone_id: zone, sub: cleanSub, ...signin })}>{busy ? <Spinner /> : null}Use {cleanSub || 'pocket'}.{zoneName}</button>
              <button type="button" className="btn ghost" onClick={() => { setZones(null); setSignin({}); setErr('') }}>Sign in differently</button>
            </div>
          </div>}
        </div>}
        <Problem text={err} />
        <div className="row wrap">
          {mode !== 'cloudflare' && <button type="button" className="btn primary" onClick={choose} disabled={busy || mode === a.mode}>{busy ? <Spinner /> : null}{mode === 'own-ca' ? "Use the box's own certificate" : mode === 'plain' ? "Use this network's addresses" : "Use Pocket's address"}</button>}
          {!(startOpen && a.mode === null) && <button type="button" className="btn ghost" onClick={() => { setOpen(false); setErr(''); setZones(null) }}>Cancel</button>}
        </div>
      </div>}
      {!open && <Problem text={err} />}
      {confirm && <Confirm title="Move the box to another address" danger={false} confirmLabel="Move"
        message={<>The apps restart on the new address, so this page goes away for a moment. Bookmarks to <b>{a.domain}</b> stop working; {a.plain_url ? <><b>{a.plain_url}</b> always reaches Pocket Core on this network.</> : <><b>pocket.local</b> always finds the box.</>}</>}
        onConfirm={confirm} onCancel={() => setConfirm(null)} />}
    </div>
  )
}
