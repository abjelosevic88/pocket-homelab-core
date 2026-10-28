// The Pocket Box itself — time zone, network, remote access, console password, disk — through the
// box agent. The setup wizard shows these one at a time, Settings → System all together.
import { useEffect, useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { BoxSystem, WifiNetwork } from '../lib/types'
import { errMsg, Loading, Spinner, useToast } from './ui'
import { Icon } from './icons'
import { Select } from './Select'

export const BOX = `${V1}/box/system`

export function useBoxStatus() {
  const [st, setSt] = useState<BoxSystem | null>(null)
  const [err, setErr] = useState('')
  const reload = async () => {
    try { setSt(await api.get<BoxSystem>(`${BOX}/status`)); setErr('') } catch (e) { setErr(errMsg(e)) }
  }
  useEffect(() => { reload() }, [])
  return { st, err, reload }
}

function Problem({ text }: { text: string }) {
  return text ? <div className="callout danger"><Icon name="alert" size={15} /><span>{text}</span></div> : null
}

/** The zone the browser is in, offered first. */
export function browserZone(): string {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || '' } catch { return '' }
}

export function TimezonePanel({ current, onSaved }: { current: string; onSaved?: (tz: string) => void }) {
  const { toast } = useToast()
  const [zones, setZones] = useState<string[]>([])
  const [tz, setTz] = useState(current === 'UTC' && browserZone() ? browserZone() : current)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => { api.get<{ timezones: string[] }>(`${BOX}/timezones`).then(r => setZones(r.timezones)).catch(e => setErr(errMsg(e))) }, [])
  const options = useMemo(() => zones.map(z => ({ value: z, label: z.replace(/_/g, ' '), group: z.includes('/') ? z.split('/')[0] : 'Other' })), [zones])
  const save = async () => {
    setBusy(true); setErr('')
    try {
      await api.put(`${BOX}/timezone`, { timezone: tz })
      toast('Time zone saved. The apps restart for a moment to pick it up.')
      onSaved?.(tz)
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  return (
    <div className="stack tight">
      <div className="row wrap">
        <div style={{ flex: '1 1 260px' }}><Select value={tz} onChange={setTz} options={options} placeholder="Search a city or region…" ariaLabel="Time zone" disabled={!zones.length} /></div>
        <button type="button" className="btn primary" disabled={busy || !tz || tz === current} onClick={save}>{busy ? <Spinner /> : null}Save</button>
      </div>
      <p className="muted small">Now: <b>{current}</b>{tz !== current && browserZone() === tz ? `. Your device is in ${tz}` : ''}.</p>
      <Problem text={err} />
    </div>
  )
}

export function NetworkPanel({ st, onChanged }: { st: BoxSystem; onChanged?: () => void }) {
  const { toast } = useToast()
  const [nets, setNets] = useState<WifiNetwork[] | null>(null)
  const [scanning, setScanning] = useState(false)
  const [pick, setPick] = useState<WifiNetwork | null>(null)
  const [pw, setPw] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const wifi = st.network.wifi
  const scan = async () => {
    setScanning(true); setErr('')
    try { setNets((await api.get<{ networks: WifiNetwork[] }>(`${BOX}/wifi`)).networks) } catch (e) { setErr(errMsg(e)) } finally { setScanning(false) }
  }
  useEffect(() => { if (wifi.available) scan() }, [wifi.available])
  const join = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!pick) return
    setBusy(true); setErr('')
    try {
      await api.post(`${BOX}/wifi`, { ssid: pick.ssid, password: pw })
      toast(`Joined ${pick.ssid}`)
      setPick(null); setPw(''); onChanged?.(); scan()
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  return (
    <div className="stack tight">
      <div className="box-addrs">
        {st.network.addresses.length ? st.network.addresses.map(a => (
          <div key={a.interface + a.address} className="box-addr"><Icon name={a.interface.startsWith('w') ? 'wifi' : 'nodes'} size={15} /><span>{a.interface.startsWith('w') ? 'Wi-Fi' : 'Cable'}</span><code>{a.address}</code></div>
        )) : <p className="muted small">No network address yet.</p>}
      </div>
      {!wifi.available && <p className="muted small">This box has no Wi-Fi, so it stays on the network by cable.</p>}
      {wifi.available && <>
        <div className="row"><b className="grow">Wi-Fi{wifi.connected ? ` · ${wifi.connected}` : ''}</b><button type="button" className="btn sm ghost" onClick={scan} disabled={scanning}>{scanning ? <Spinner /> : <Icon name="refresh" size={14} />}Scan</button></div>
        {nets && !nets.length && <p className="muted small">No networks in range.</p>}
        {nets && nets.length > 0 && <div className="box-wifi-list">
          {nets.map(n => (
            <button type="button" key={n.ssid} className={`box-wifi ${pick?.ssid === n.ssid ? 'on' : ''}`} onClick={() => { setPick(n); setPw('') }} disabled={n.active}>
              <Icon name="wifi" size={15} /><span className="grow">{n.ssid}</span>
              {n.active ? <span className="badge accent">Connected</span> : <span className="muted xsmall">{n.signal}%{n.secure ? ' · locked' : ''}</span>}
            </button>
          ))}
        </div>}
        {pick && <form onSubmit={join} className="row wrap">
          {pick.secure && <input type="password" placeholder={`Password for ${pick.ssid}`} value={pw} onChange={e => setPw(e.target.value)} autoFocus style={{ flex: '1 1 220px' }} />}
          <button className="btn primary" disabled={busy || (pick.secure && !pw)}>{busy ? <Spinner /> : null}Join {pick.ssid}</button>
        </form>}
        <p className="muted small">Join from here while the cable is still in; the box keeps the cable as well if you leave it plugged.</p>
      </>}
      <Problem text={err} />
    </div>
  )
}

export function TailscalePanel({ st, onChanged }: { st: BoxSystem; onChanged?: () => void }) {
  const [ts, setTs] = useState(st.tailscale)
  const [key, setKey] = useState('')
  const [withKey, setWithKey] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => setTs(st.tailscale), [st.tailscale])
  // While someone signs in on Tailscale's page, watch for the box to come online.
  useEffect(() => {
    if (!ts.auth_url || ts.state === 'Running') return
    const id = window.setInterval(async () => {
      try { const s = await api.get<BoxSystem>(`${BOX}/status`); setTs(s.tailscale); if (s.tailscale.state === 'Running') onChanged?.() } catch { /* keep waiting */ }
    }, 3000)
    return () => window.clearInterval(id)
  }, [ts.auth_url, ts.state, onChanged])
  const up = async (e?: React.FormEvent) => {
    e?.preventDefault()
    setBusy(true); setErr('')
    try {
      const r = await api.post<BoxSystem['tailscale']>(`${BOX}/tailscale`, withKey ? { auth_key: key } : {})
      setTs(r)
      if (r.auth_url && r.state !== 'Running') window.open(r.auth_url, '_blank', 'noopener')
      if (r.state === 'Running') onChanged?.()
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const down = async () => {
    setBusy(true); setErr('')
    try { setTs(await api.del<BoxSystem['tailscale']>(`${BOX}/tailscale`)); onChanged?.() } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  if (ts.state === 'Running') return (
    <div className="stack tight">
      <div className="callout"><Icon name="check" size={15} /><span>On your tailnet{ts.tailnet ? ` ${ts.tailnet}` : ''} as <b>{ts.name || st.hostname}</b>{ts.addresses?.length ? ` (${ts.addresses[0]})` : ''}. Devices on the same tailnet reach the box from anywhere.</span></div>
      <div className="row"><button type="button" className="btn sm danger-text" onClick={down} disabled={busy}>Leave the tailnet</button></div>
      <Problem text={err} />
    </div>
  )
  return (
    <div className="stack tight">
      <p className="muted small">Tailscale connects the box to your own devices wherever they are, without opening anything on your router. It's free for personal use.</p>
      {ts.auth_url && <div className="callout"><Icon name="external" size={15} /><span>Finish signing in on <a href={ts.auth_url} target="_blank" rel="noopener noreferrer">Tailscale's page</a>; this updates by itself when the box is in.</span></div>}
      {withKey ? (
        <form onSubmit={up} className="row wrap">
          <input value={key} onChange={e => setKey(e.target.value)} placeholder="tskey-auth-…" style={{ flex: '1 1 240px' }} autoFocus />
          <button className="btn primary" disabled={busy || !key.trim()}>{busy ? <Spinner /> : null}Join</button>
          <button type="button" className="btn ghost" onClick={() => setWithKey(false)}>Back</button>
        </form>
      ) : (
        <div className="row wrap">
          <button type="button" className="btn primary" onClick={() => up()} disabled={busy}>{busy ? <Spinner /> : <Icon name="globe" size={15} />}Sign in to Tailscale</button>
          <button type="button" className="btn ghost" onClick={() => setWithKey(true)}>Use an auth key</button>
        </div>
      )}
      <Problem text={err} />
    </div>
  )
}

export function PasswordPanel({ st, onSaved }: { st: BoxSystem; onSaved?: () => void }) {
  const { toast } = useToast()
  const [pw, setPw] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (pw !== again) { setErr('The two passwords differ.'); return }
    setBusy(true); setErr('')
    try { await api.put(`${BOX}/password`, { password: pw }); setPw(''); setAgain(''); toast('Console password saved'); onSaved?.() } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="stack tight">
      <p className="muted small">For the account <code>pocket</code> at the box's own screen and keyboard (or serial cable), for repairs. {st.password_set ? 'A password is set.' : 'Until you set one, nobody can sign in there.'}</p>
      <div className="row wrap">
        <input type="password" placeholder="New password" value={pw} onChange={e => setPw(e.target.value)} autoComplete="new-password" minLength={8} style={{ flex: '1 1 180px' }} />
        <input type="password" placeholder="Again" value={again} onChange={e => setAgain(e.target.value)} autoComplete="new-password" minLength={8} style={{ flex: '1 1 180px' }} />
        <button className="btn primary" disabled={busy || pw.length < 8 || !again}>{busy ? <Spinner /> : null}{st.password_set ? 'Change' : 'Set password'}</button>
      </div>
      <Problem text={err} />
    </form>
  )
}

export function DiskPanel({ st }: { st: BoxSystem }) {
  const d = st.encryption
  if (!d.encrypted) return <div className="callout"><Icon name="info" size={15} /><span>The disk is not encrypted: this box had no TPM chip when it was installed. Keep it somewhere safe.</span></div>
  return (
    <div className="callout"><Icon name="lock" size={15} /><span>
      The disk is encrypted{d.tpm ? ' and unlocks itself with this box’s TPM chip, so it starts without a password' : ''}. Taken out of the box, it can’t be read.
      {d.recovery_key ? ' Keep the recovery key from the installation somewhere safe: it is the only way in if the board is replaced.' : ''}
    </span></div>
  )
}

export function BoxLoading({ err }: { err: string }) {
  return err ? <Problem text={err} /> : <Loading />
}
