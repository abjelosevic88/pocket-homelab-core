// The Pocket Box itself — time zone, network, remote access, console password, disk — through the
// box agent. The setup wizard shows these one at a time, Settings → System all together.
import { useEffect, useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { BoxSystem, BoxUpdates, WifiNetwork } from '../lib/types'
import { Confirm, errMsg, Loading, Spinner, Switch, useToast } from './ui'
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

/** The time now in a zone, ticking. */
function useClock(tz: string) {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => { const id = window.setInterval(() => setNow(new Date()), 15000); return () => window.clearInterval(id) }, [])
  try {
    return {
      time: new Intl.DateTimeFormat(undefined, { timeZone: tz, hour: '2-digit', minute: '2-digit' }).format(now),
      date: new Intl.DateTimeFormat(undefined, { timeZone: tz, weekday: 'short', day: 'numeric', month: 'short' }).format(now),
    }
  } catch { return { time: '', date: '' } }
}

export function TimezonePanel({ current, onSaved }: { current: string; onSaved?: (tz: string) => void }) {
  const { toast } = useToast()
  const [zones, setZones] = useState<string[]>([])
  const device = browserZone()
  const [tz, setTz] = useState(current === 'UTC' && device ? device : current)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const clock = useClock(tz)
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
  const city = (tz.split('/').pop() || tz).replace(/_/g, ' ')
  return (
    <div className="stack tight">
      <div className="box-clock">
        <div className="box-clock-time">{clock.time}</div>
        <div className="grow">
          <b>{city}</b>
          <span>{clock.date}{tz.includes('/') ? ` · ${tz.split('/')[0].replace(/_/g, ' ')}` : ''}</span>
        </div>
        {tz === current ? <span className="badge ok">Now</span> : <span className="badge warn">Not saved</span>}
      </div>
      <div className="row wrap">
        <div style={{ flex: '1 1 260px' }}><Select value={tz} onChange={setTz} options={options} placeholder="Search a city or region…" ariaLabel="Time zone" disabled={!zones.length} /></div>
        <button type="button" className="btn primary" disabled={busy || !tz || tz === current} onClick={save}>{busy ? <Spinner /> : null}Save</button>
      </div>
      {device && device !== tz && <button type="button" className="box-chip" onClick={() => setTz(device)}><Icon name="smartphone" size={13} />Use this device's zone: {device.replace(/_/g, ' ')}</button>}
      {tz !== current && <p className="muted small">The box is on <b>{current.replace(/_/g, ' ')}</b> until you save.</p>}
      <Problem text={err} />
    </div>
  )
}

/** Four bars, lit by the signal strength. */
function Signal({ pct }: { pct: number }) {
  const lit = pct >= 75 ? 4 : pct >= 50 ? 3 : pct >= 25 ? 2 : 1
  return (
    <svg className="box-signal" width="18" height="14" viewBox="0 0 18 14" aria-label={`Signal ${pct}%`} role="img">
      {[0, 1, 2, 3].map(n => <rect key={n} x={n * 4.6} y={10 - n * 3.2} width="3.2" height={4 + n * 3.2} rx="1" className={n < lit ? 'on' : ''} />)}
    </svg>
  )
}

export function NetworkPanel({ st, onChanged }: { st: BoxSystem; onChanged?: () => void }) {
  const { toast } = useToast()
  const [nets, setNets] = useState<WifiNetwork[] | null>(null)
  const [scanning, setScanning] = useState(false)
  const [pick, setPick] = useState<WifiNetwork | null>(null)
  const [pw, setPw] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [all, setAll] = useState(false)
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
  // Strongest first (the connected one on top); a long list shows the few nearest until asked.
  const FEW = 6
  const sorted = useMemo(() => [...(nets || [])].sort((a, b) => Number(b.active) - Number(a.active) || b.signal - a.signal), [nets])
  const shown = all ? sorted : sorted.filter((n, k) => k < FEW || n.ssid === pick?.ssid)
  const cable = st.network.addresses.filter(a => !a.interface.startsWith('w'))
  const air = st.network.addresses.filter(a => a.interface.startsWith('w'))
  return (
    <div className="stack">
      <div className="box-links">
        <div className={`box-link ${cable.length ? 'on' : ''}`}>
          <span className="box-link-ico"><Icon name="nodes" size={17} /></span>
          <div className="grow"><b>Cable</b><span>{cable.length ? cable.map(a => a.address).join(', ') : 'Not plugged in'}</span></div>
          {cable.length > 0 && <span className="badge ok">Connected</span>}
        </div>
        <div className={`box-link ${wifi.connected ? 'on' : ''}`}>
          <span className="box-link-ico"><Icon name="wifi" size={17} /></span>
          <div className="grow"><b>Wi-Fi</b><span>{!wifi.available ? 'No Wi-Fi in this box' : wifi.connected ? `${wifi.connected}${air.length ? ` · ${air.map(a => a.address).join(', ')}` : ''}` : 'Not joined'}</span></div>
          {wifi.connected && <span className="badge ok">Connected</span>}
        </div>
      </div>
      {!wifi.available && <p className="muted small">This box has no Wi-Fi, so it stays on the network by cable.</p>}
      {wifi.available && <div className="stack tight">
        <div className="row"><b className="grow box-section-title">Networks nearby</b><button type="button" className="btn sm ghost" onClick={scan} disabled={scanning}>{scanning ? <Spinner /> : <Icon name="refresh" size={14} />}{scanning ? 'Scanning…' : 'Scan again'}</button></div>
        {!nets && scanning && <div className="box-wifi-list">{[0, 1, 2].map(n => <div key={n} className="box-wifi skeleton" />)}</div>}
        {nets && !nets.length && <p className="muted small">No networks in range.</p>}
        {nets && nets.length > 0 && <div className="box-wifi-list">
          {shown.map(n => (
            <div key={n.ssid} className={`box-wifi-item ${pick?.ssid === n.ssid ? 'on' : ''}`}>
              <button type="button" className="box-wifi" onClick={() => { setPick(pick?.ssid === n.ssid ? null : n); setPw(''); setErr('') }} disabled={n.active} aria-expanded={pick?.ssid === n.ssid}>
                <Signal pct={n.signal} />
                <span className="grow">{n.ssid}</span>
                {n.active ? <span className="badge ok">Connected</span> : n.secure ? <Icon name="lock" size={13} /> : <span className="muted xsmall">Open</span>}
              </button>
              {pick?.ssid === n.ssid && <form onSubmit={join} className="box-wifi-join">
                {n.secure && <div className="box-pw">
                  <input type={showPw ? 'text' : 'password'} placeholder={`Password for ${n.ssid}`} value={pw} onChange={e => setPw(e.target.value)} autoFocus autoComplete="off" />
                  <button type="button" className="btn ghost icon-only sm" onClick={() => setShowPw(!showPw)} aria-label={showPw ? 'Hide password' : 'Show password'}><Icon name={showPw ? 'eye-off' : 'eye'} size={15} /></button>
                </div>}
                <button className="btn primary" disabled={busy || (n.secure && !pw)}>{busy ? <Spinner /> : null}Join</button>
              </form>}
            </div>
          ))}
          {sorted.length > shown.length && <button type="button" className="btn sm ghost box-wifi-more" onClick={() => setAll(true)}><Icon name="chevron-down" size={14} />Show all {sorted.length} networks</button>}
          {all && sorted.length > FEW && <button type="button" className="btn sm ghost box-wifi-more" onClick={() => setAll(false)}><Icon name="chevron-up" size={14} />Show fewer</button>}
        </div>}
        <p className="muted small">Join while the cable is still in. The box keeps using the cable too if you leave it plugged in.</p>
      </div>}
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
      <div className="box-hero ok">
        <span className="box-hero-ico"><Icon name="check" size={20} /></span>
        <div className="grow"><b>On your tailnet{ts.tailnet ? ` ${ts.tailnet}` : ''}</b>
          <span>Devices on the same tailnet reach the box from anywhere.</span>
          <div className="box-chips"><span className="box-chip static"><Icon name="server" size={13} />{ts.name || st.hostname}</span>{ts.addresses?.[0] && <span className="box-chip static"><code>{ts.addresses[0]}</code></span>}</div>
        </div>
      </div>
      <div className="row"><button type="button" className="btn sm danger-ghost" onClick={down} disabled={busy}>Leave the tailnet</button></div>
      <Problem text={err} />
    </div>
  )
  return (
    <div className="stack">
      <ul className="box-perks">
        <li><span><Icon name="smartphone" size={16} /></span><div><b>Your phone and laptop, anywhere</b>Open the apps away from home as if you were on the home Wi-Fi.</div></li>
        <li><span><Icon name="shield" size={16} /></span><div><b>Nothing opened on your router</b>Only your own devices get in; the box stays hidden from the internet.</div></li>
        <li><span><Icon name="zap" size={16} /></span><div><b>Free for personal use</b>Install the Tailscale app on each device and sign in with the same account.</div></li>
      </ul>
      {ts.auth_url && <div className="callout info"><Icon name="external" size={15} /><span>Finish signing in on <a href={ts.auth_url} target="_blank" rel="noopener noreferrer">Tailscale's page</a>; this updates by itself when the box is in.</span></div>}
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
  const [show, setShow] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (pw !== again) { setErr('The two passwords differ.'); return }
    setBusy(true); setErr('')
    try { await api.put(`${BOX}/password`, { password: pw }); setPw(''); setAgain(''); toast('Console password saved'); onSaved?.() } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const long = pw.length >= 8
  const same = !!again && pw === again
  return (
    <form onSubmit={save} className="stack">
      <div className="box-tiles">
        <div className="box-tile"><span>Username</span><b><code>pocket</code></b></div>
        <div className={`box-tile ${st.password_set ? 'ok' : ''}`}><span>Login</span><b><Icon name={st.password_set ? 'check' : 'lock'} size={14} />{st.password_set ? 'Password set' : 'Locked'}</b></div>
      </div>
      <p className="muted small">It can change anything on the box, so give it a password of its own. The box's screen never shows it.</p>
      <div className="stack tight">
        <div className="box-pw">
          <input type={show ? 'text' : 'password'} placeholder={st.password_set ? 'New password' : 'Password'} value={pw} onChange={e => setPw(e.target.value)} autoComplete="new-password" minLength={8} aria-label="New password" />
          <button type="button" className="btn ghost icon-only sm" onClick={() => setShow(!show)} aria-label={show ? 'Hide password' : 'Show password'}><Icon name={show ? 'eye-off' : 'eye'} size={15} /></button>
        </div>
        <input type={show ? 'text' : 'password'} placeholder="The same again" value={again} onChange={e => setAgain(e.target.value)} autoComplete="new-password" minLength={8} aria-label="The same again" />
        <div className="box-checks">
          <span className={long ? 'ok' : ''}><Icon name={long ? 'check' : 'circle'} size={13} />At least 8 characters</span>
          <span className={same ? 'ok' : ''}><Icon name={same ? 'check' : 'circle'} size={13} />Both the same</span>
        </div>
      </div>
      <div className="row"><button className="btn primary" disabled={busy || !long || !same}>{busy ? <Spinner /> : null}{st.password_set ? 'Change password' : 'Set password'}</button></div>
      <Problem text={err} />
    </form>
  )
}

export function DiskPanel({ st }: { st: BoxSystem }) {
  const d = st.encryption
  if (!d.encrypted) return (
    <div className="box-hero warn">
      <span className="box-hero-ico"><Icon name="alert" size={20} /></span>
      <div className="grow"><b>The disk is not encrypted</b><span>This box had no TPM chip when it was installed. Keep it somewhere safe.</span></div>
    </div>
  )
  return (
    <div className="stack tight">
      <div className="box-hero ok">
        <span className="box-hero-ico"><Icon name="shield" size={20} /></span>
        <div className="grow"><b>Your data is encrypted</b><span>Taken out of the box, the disk can't be read.</span></div>
      </div>
      <ul className="box-perks">
        {d.tpm && <li><span><Icon name="zap" size={16} /></span><div><b>Starts by itself</b>The box's TPM chip unlocks the disk, so no password at power-on.</div></li>}
        {d.recovery_key && <li><span><Icon name="key" size={16} /></span><div><b>Keep the recovery key safe</b>The one from the installation is the only way in if the board is ever replaced.</div></li>}
        <li><span><Icon name="refresh" size={16} /></span><div><b>Backups</b>Pocket Backups copies the apps' data elsewhere; set it up from its app.</div></li>
      </ul>
    </div>
  )
}

export function BoxLoading({ err }: { err: string }) {
  return err ? <Problem text={err} /> : <Loading />
}

const RESULT: Record<string, string> = {
  'up-to-date': 'Up to date', updated: 'Updated', 'rolled-back': 'Went back', skipped: 'Skipped a release', error: 'Problem',
  paused: 'Paused', checking: 'Checking…', installing: 'Installing…',
}

/** Nightly updates with rollback: this release, the last run, check now, on/off, restart to finish. */
export function UpdatesPanel() {
  const { toast } = useToast()
  const [u, setU] = useState<BoxUpdates | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [restart, setRestart] = useState(false)
  const load = async () => { try { setU(await api.get<BoxUpdates>(`${BOX}/updates`)); setErr('') } catch (e) { setErr(errMsg(e)) } }
  useEffect(() => { load() }, [])
  // While an update runs, follow it.
  useEffect(() => {
    if (!u?.running) return
    const id = window.setInterval(load, 4000)
    return () => window.clearInterval(id)
  }, [u?.running])
  if (!u) return err ? <Problem text={err} /> : <Loading />
  const check = async () => { setBusy(true); setErr(''); try { setU(await api.post<BoxUpdates>(`${BOX}/updates`)); toast('Looking for a new release') } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) } }
  const auto = async (on: boolean) => { try { setU(await api.put<BoxUpdates>(`${BOX}/updates`, { auto: on })) } catch (e) { setErr(errMsg(e)) } }
  const last = u.last || {}
  const when = last.checked_at ? new Date(last.checked_at).toLocaleString() : ''
  return (
    <div className="stack tight">
      <div className="row wrap">
        <span className="grow">This box runs <b>Pocket Box {u.release.version || '?'}</b>{u.release.rev ? <span className="muted"> ({u.release.rev})</span> : null}</span>
        <button type="button" className="btn sm" onClick={check} disabled={busy || u.running}>{u.running || busy ? <Spinner /> : <Icon name="refresh" size={14} />}{u.running ? 'Updating…' : 'Check now'}</button>
      </div>
      {last.result && <div className={`callout ${last.result === 'rolled-back' || last.result === 'error' ? 'danger' : ''}`}>
        <Icon name={last.result === 'rolled-back' || last.result === 'error' ? 'alert' : 'check'} size={15} />
        <span><b>{RESULT[last.result] || last.result}.</b> {last.message}{when ? <span className="muted"> {when}</span> : null}
          {last.failing?.length ? <><br /><span className="small">Did not come up: {last.failing.join(', ')}</span></> : null}</span>
      </div>}
      {u.restart_needed && <div className="callout"><Icon name="info" size={15} /><span>A new system kernel is installed; it takes over at the next restart. The nightly update restarts the box by itself.</span>
        <button type="button" className="btn sm" onClick={() => setRestart(true)}>Restart now</button></div>}
      <div className="row"><span className="grow">Update every night and go back automatically if something does not start</span><Switch on={u.auto} onChange={auto} label="Automatic updates" /></div>
      <Problem text={err} />
      {restart && <Confirm title="Restart the box" message="Every app is unavailable for a minute or two while the box restarts." confirmLabel="Restart"
        onConfirm={async () => { setRestart(false); try { await api.post(`${BOX}/restart`); toast('Restarting…') } catch (e) { setErr(errMsg(e)) } }} onCancel={() => setRestart(false)} />}
    </div>
  )
}
