import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../lib/api'
import { useApp } from '../lib/app-context'
import { BOX, BoxLoading, DiskPanel, NetworkPanel, PasswordPanel, TailscalePanel, TimezonePanel, useBoxStatus } from '../components/BoxSystem'
import { Icon } from '../components/icons'
import { Spinner } from '../components/ui'

type Step = { id: string; icon: string; title: string; sub: string }

const STEPS: Step[] = [
  { id: 'time', icon: 'clock', title: 'Time zone', sub: 'So reminders, schedules and “today” are right in every app.' },
  { id: 'network', icon: 'wifi', title: 'Network', sub: 'The box is online by cable. Join Wi-Fi too if it should work without one.' },
  { id: 'remote', icon: 'globe', title: 'Away from home', sub: 'Reach the box from your phone and laptop anywhere, through Tailscale.' },
  { id: 'console', icon: 'terminal', title: 'Console password', sub: 'Only for repairs at the box itself. Leave it locked if you never plug in a screen.' },
  { id: 'disk', icon: 'disk', title: 'Your data', sub: 'How the box keeps it safe.' },
]

/** First-run setup of the Pocket Box, shown to the admin right after the first account is made. */
export default function BoxWizard() {
  const navigate = useNavigate()
  const { refresh, status } = useApp()
  const { st, err, reload } = useBoxStatus()
  const [i, setI] = useState(0)
  const [finishing, setFinishing] = useState(false)
  const step = STEPS[i]
  const last = i === STEPS.length - 1

  const finish = async () => {
    setFinishing(true)
    try { await api.post(`${BOX}/wizard`) } catch { /* the wizard can be finished from Settings too */ }
    await refresh()
    navigate('/', { replace: true })
  }
  const next = () => (last ? finish() : setI(i + 1))

  return (
    <div className="login-wrap">
      <div className="login-card wizard-card">
        <div className="login-brand">
          <img src="/favicon.svg" alt="" width={40} height={40} />
          <h1>Set up {status?.box_name || 'your box'}</h1>
          <p className="login-tagline">A few things about the box itself. Everything here can be changed later under Settings → System.</p>
        </div>
        <ol className="wizard-steps" aria-label="Steps">
          {STEPS.map((s, n) => (
            <li key={s.id} className={n === i ? 'on' : n < i ? 'done' : ''}>
              <button type="button" onClick={() => setI(n)} aria-current={n === i ? 'step' : undefined} title={s.title}>
                {n < i ? <Icon name="check" size={13} /> : <Icon name={s.icon} size={13} />}<span>{s.title}</span>
              </button>
            </li>
          ))}
        </ol>
        <section className="wizard-step" key={step.id}>
          <h2><Icon name={step.icon} size={18} />{step.title}</h2>
          <p className="muted">{step.sub}</p>
          {!st ? <BoxLoading err={err} /> : <>
            {step.id === 'time' && <TimezonePanel current={st.timezone} onSaved={() => { reload(); setI(i + 1) }} />}
            {step.id === 'network' && <NetworkPanel st={st} onChanged={reload} />}
            {step.id === 'remote' && <TailscalePanel st={st} onChanged={reload} />}
            {step.id === 'console' && <PasswordPanel st={st} onSaved={() => { reload(); setI(i + 1) }} />}
            {step.id === 'disk' && <DiskPanel st={st} />}
          </>}
        </section>
        <div className="wizard-nav">
          {i > 0 ? <button type="button" className="btn ghost" onClick={() => setI(i - 1)}><Icon name="chevron-left" size={15} />Back</button> : <span />}
          <span className="grow" />
          <button type="button" className={`btn ${last ? 'primary' : ''}`} onClick={next} disabled={finishing}>
            {finishing ? <Spinner /> : null}{last ? 'Finish' : 'Next'}{!last && <Icon name="chevron-right" size={15} />}
          </button>
        </div>
      </div>
      <button type="button" className="btn ghost sm wizard-later" onClick={finish} disabled={finishing}>Do this later</button>
    </div>
  )
}
