import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { BoxAddress, BoxSystem } from '../lib/types'
import { BOX, BoxLoading, DiskPanel, NetworkPanel, PasswordPanel, TailscalePanel, TimezonePanel, useBoxStatus } from '../components/BoxSystem'
import { ADDRESS, AddressPanel } from '../components/BoxAddress'
import { Icon } from '../components/icons'
import { Spinner } from '../components/ui'

type Step = { id: string; icon: string; title: string; short: string; sub: string; optional?: boolean }

const STEPS: Step[] = [
  { id: 'address', icon: 'lock', title: 'Secure address', short: 'Address', sub: 'How phones and computers get a padlock for the box. Pocket\'s free address is ready in a minute; your own domain on Cloudflare works as well.' },
  { id: 'time', icon: 'clock', title: 'Time zone', short: 'Time', sub: 'So reminders, schedules and “today” are right in every app.' },
  { id: 'network', icon: 'wifi', title: 'Network', short: 'Network', sub: 'The box is online by cable. Join Wi-Fi too if it should work without one.', optional: true },
  { id: 'remote', icon: 'globe', title: 'Away from home', short: 'Remote', sub: 'Reach the box from your phone and laptop anywhere, through Tailscale.', optional: true },
  { id: 'console', icon: 'terminal', title: 'Console password', short: 'Console', sub: 'The login on the box\'s own screen (username pocket), only for repairs. Not your Pocket account. Leave it locked if you never plug in a screen.', optional: true },
  { id: 'disk', icon: 'shield', title: 'Your data', short: 'Data', sub: 'How the box keeps it safe.' },
  { id: 'done', icon: 'check', title: 'All set', short: 'Done', sub: 'Here is how the box is set up. Tap a line to change it; everything stays under Settings → System as well.' },
]

/** One line of the summary at the end: what each step ended up as. */
function summary(id: string, st: BoxSystem, addr: BoxAddress | null): { text: string; ok: boolean } {
  switch (id) {
    case 'address':
      if (!addr || addr.mode === null) return { text: 'Not chosen yet', ok: false }
      if (addr.mode === 'own-ca') return { text: 'The box\'s own certificate', ok: true }
      if (addr.mode === 'off') return { text: 'Public names off', ok: true }
      return addr.domain ? { text: addr.domain, ok: true } : { text: 'Getting it ready…', ok: true }
    case 'time': return { text: st.timezone.replace(/_/g, ' '), ok: st.timezone !== 'UTC' }
    case 'network': return st.network.wifi.connected ? { text: `Cable and Wi-Fi (${st.network.wifi.connected})`, ok: true } : { text: st.network.addresses.length ? 'Cable' : 'No address yet', ok: st.network.addresses.length > 0 }
    case 'remote': return st.tailscale.state === 'Running' ? { text: `On Tailscale as ${st.tailscale.name || st.hostname}`, ok: true } : { text: 'Only at home', ok: false }
    case 'console': return { text: st.password_set ? 'Password set' : 'Locked', ok: true }
    case 'disk': return { text: st.encryption.encrypted ? (st.encryption.tpm ? 'Encrypted, unlocks with the TPM' : 'Encrypted') : 'Not encrypted', ok: st.encryption.encrypted }
  }
  return { text: '', ok: true }
}

/** First-run setup of the Pocket Box, shown to the admin right after the first account is made. */
export default function BoxWizard() {
  const navigate = useNavigate()
  const { refresh, status } = useApp()
  const { st, err, reload } = useBoxStatus()
  const [i, setI] = useState(0)
  const [seen, setSeen] = useState(0)
  const [finishing, setFinishing] = useState(false)
  const [addr, setAddr] = useState<BoxAddress | null>(null)
  const steps = STEPS.filter(s => s.id !== 'address' || status?.box_address)
  const step = steps[Math.min(i, steps.length - 1)]
  const last = step.id === 'done'
  const total = steps.length - 1
  const go = (n: number) => { setI(n); setSeen(s => Math.max(s, n)); window.scrollTo({ top: 0 }) }

  // The summary reads the address as it is now.
  useEffect(() => {
    if (!last) return
    if (status?.box_address) api.get<BoxAddress>(ADDRESS).then(setAddr).catch(() => setAddr(null))
    reload()
  }, [last])

  const finish = async () => {
    setFinishing(true)
    try { await api.post(`${BOX}/wizard`) } catch { /* the wizard can be finished from Settings too */ }
    await refresh()
    navigate('/', { replace: true })
  }
  const next = () => (last ? finish() : go(i + 1))
  const pct = last ? 100 : Math.round(((i + 1) / (total + 1)) * 100)
  const left = total - i

  return (
    <div className="wz">
      <div className="wz-shell">
        <aside className="wz-rail">
          <div className="wz-brand">
            <img src="/favicon.svg" alt="" width={34} height={34} />
            <div>
              <b>Set up {status?.box_name || 'your box'}</b>
              <span>{last ? 'Done' : `${left} step${left === 1 ? '' : 's'} to go`}</span>
            </div>
          </div>
          <div className="wz-progress" aria-hidden="true"><i style={{ width: `${pct}%` }} /></div>
          <ol className="wz-steps" aria-label="Steps">
            {steps.map((s, n) => {
              const done = n !== i && n <= seen
              return (
                <li key={s.id} className={n === i ? 'on' : done ? 'done' : ''}>
                  <button type="button" onClick={() => go(n)} aria-current={n === i ? 'step' : undefined} title={s.title}>
                    <span className="wz-dot">{done ? <Icon name="check" size={13} /> : <Icon name={s.icon} size={14} />}</span>
                    <span className="wz-label"><b>{s.title}</b>{s.optional && <em>Optional</em>}</span>
                  </button>
                </li>
              )
            })}
          </ol>
          <p className="wz-rail-note">Everything here can be changed later under Settings → System.</p>
        </aside>

        <main className="wz-main">
          <section className="wz-step" key={step.id}>
            <header className="wz-head">
              <span className={`wz-tile ${last ? 'ok' : ''}`}><Icon name={step.icon} size={22} /></span>
              <div>
                <p className="wz-eyebrow">{last ? 'Finished' : `Step ${i + 1} of ${total}`}{step.optional ? ' · optional' : ''}</p>
                <h1>{step.title}</h1>
              </div>
            </header>
            <p className="wz-sub">{step.sub}</p>
            <div className="wz-body">
              {!st ? <BoxLoading err={err} /> : <>
                {step.id === 'address' && <AddressPanel startOpen />}
                {step.id === 'time' && <TimezonePanel current={st.timezone} onSaved={() => { reload(); go(i + 1) }} />}
                {step.id === 'network' && <NetworkPanel st={st} onChanged={reload} />}
                {step.id === 'remote' && <TailscalePanel st={st} onChanged={reload} />}
                {step.id === 'console' && <PasswordPanel st={st} onSaved={() => { reload(); go(i + 1) }} />}
                {step.id === 'disk' && <DiskPanel st={st} />}
                {step.id === 'done' && <ul className="wz-summary">
                  {steps.filter(s => s.id !== 'done').map(s => {
                    const sm = summary(s.id, st, addr)
                    return (
                      <li key={s.id}><button type="button" onClick={() => go(steps.indexOf(s))}>
                        <span className={`wz-sum-ico ${sm.ok ? 'ok' : ''}`}><Icon name={sm.ok ? 'check' : s.icon} size={14} /></span>
                        <span className="grow"><b>{s.title}</b><span>{sm.text}</span></span>
                        <Icon name="chevron-right" size={15} />
                      </button></li>
                    )
                  })}
                </ul>}
              </>}
            </div>
          </section>
          <nav className="wz-nav">
            {i > 0 ? <button type="button" className="btn ghost" onClick={() => go(i - 1)}><Icon name="chevron-left" size={15} />Back</button> : <span />}
            <span className="grow" />
            {step.optional && <button type="button" className="btn ghost" onClick={next}>Skip</button>}
            <button type="button" className="btn primary lg" onClick={next} disabled={finishing}>
              {finishing ? <Spinner /> : null}{last ? 'Open Pocket Core' : 'Continue'}{!finishing && <Icon name="chevron-right" size={15} />}
            </button>
          </nav>
        </main>
      </div>
      {!last && <button type="button" className="btn ghost sm wz-later" onClick={finish} disabled={finishing}>Do this later</button>}
    </div>
  )
}
