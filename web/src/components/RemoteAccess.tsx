// Tailscale for a Core installed with install.sh: the installer agent runs Pocket's own Tailscale
// container and hands back Tailscale's sign-in link (internal/installer/tailscale.go). A Pocket Box
// has its own panel (BoxSystem's TailscalePanel).
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { RemoteStatus } from '../lib/types'
import { errMsg, Loading, Spinner } from './ui'
import { Icon } from './icons'

export const REMOTE = `${V1}/box/remote`

export function RemotePanel({ onChanged }: { onChanged?: (s: RemoteStatus) => void }) {
  const [ts, setTs] = useState<RemoteStatus | null>(null)
  const [key, setKey] = useState('')
  const [withKey, setWithKey] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = async () => {
    try { const s = await api.get<RemoteStatus>(REMOTE); setTs(s); onChanged?.(s); return s } catch (e) { setErr(errMsg(e)); return null }
  }
  useEffect(() => { load() }, [])
  // While someone signs in on Tailscale's page, watch for the machine to come online.
  const waiting = !!ts && (ts.state === 'needs-login' || ts.state === 'starting')
  useEffect(() => {
    if (!waiting) return
    const id = window.setInterval(load, 3000)
    return () => window.clearInterval(id)
  }, [waiting])

  const up = async (e?: React.FormEvent) => {
    e?.preventDefault()
    setBusy(true); setErr('')
    try {
      const r = await api.post<RemoteStatus>(`${REMOTE}/up`, withKey ? { authkey: key.trim() } : {})
      setTs(r); onChanged?.(r)
      if (r.auth_url && r.state !== 'running') window.open(r.auth_url, '_blank', 'noopener')
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const down = async () => {
    setBusy(true); setErr('')
    try { const r = await api.post<RemoteStatus>(`${REMOTE}/down`); setTs(r); onChanged?.(r) } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }

  if (!ts) return err ? <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div> : <Loading />
  const problem = err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>

  if (ts.state === 'host') return (
    <div className="callout"><Icon name="check" size={15} /><span>Tailscale already runs on this machine ({ts.ips?.join(', ')}), so devices on your tailnet reach Pocket from anywhere as it is.</span></div>
  )
  if (ts.state === 'running') return (
    <div className="stack tight">
      <div className="box-hero ok">
        <span className="box-hero-ico"><Icon name="check" size={20} /></span>
        <div className="grow"><b>On your tailnet</b>
          <span>Phones and laptops signed in to the same Tailscale account reach Pocket from anywhere.</span>
          <div className="box-chips">{ts.name && <span className="box-chip static"><Icon name="server" size={13} />{ts.name}</span>}{ts.ips?.[0] && <span className="box-chip static"><code>{ts.ips[0]}</code></span>}</div>
        </div>
      </div>
      <div className="row"><button type="button" className="btn sm danger-ghost" onClick={down} disabled={busy}>{busy ? <Spinner /> : null}Leave the tailnet</button></div>
      {problem}
    </div>
  )
  return (
    <div className="stack tight">
      <p className="muted small">Tailscale is a free private network between your own devices. Connect this machine once, install the Tailscale app on your phone with the same account, and Pocket works away from home too.</p>
      {ts.state === 'needs-login' && ts.auth_url && <div className="callout"><Icon name="refresh" size={15} /><span>Waiting for you to sign in on Tailscale's page. <a href={ts.auth_url} target="_blank" rel="noopener noreferrer">Open it again</a>.</span></div>}
      {ts.state === 'starting' && <div className="callout"><Spinner /><span>Starting Tailscale…</span></div>}
      {withKey ? <form className="row wrap" onSubmit={up}>
        <input value={key} onChange={e => setKey(e.target.value)} placeholder="tskey-auth-…" autoComplete="off" spellCheck={false} style={{ flex: '1 1 240px' }} />
        <button className="btn primary" disabled={busy || !key.trim()}>{busy ? <Spinner /> : null}Connect</button>
        <button type="button" className="btn ghost" onClick={() => setWithKey(false)}>Back</button>
      </form> : <div className="row wrap">
        <button type="button" className="btn primary" onClick={() => up()} disabled={busy}>{busy ? <Spinner /> : <Icon name="globe" size={15} />}Connect Tailscale</button>
        <button type="button" className="btn ghost" onClick={() => setWithKey(true)}>Use an auth key</button>
      </div>}
      {problem}
    </div>
  )
}
