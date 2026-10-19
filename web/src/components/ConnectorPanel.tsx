import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { ConnectorAccount, ConnectorPush, ConnectorStatus, ConnectorTest, ConnectorView, HookState, ProvisionResult, RemoteAccount, Service } from '../lib/types'
import { Avatar, Loading, Modal, Switch, useAsync, useToast } from './ui'
import { Select } from './Select'

export function StatusBadge({ status }: { status: ConnectorStatus | 'skipped' }) {
  switch (status) {
    case 'ok': return <span className="badge ok">provisioned</span>
    case 'linked': return <span className="badge cat">linked</span>
    case 'error': return <span className="badge err">error</span>
    case 'skipped': return <span className="badge">skipped</span>
  }
  return <span className="badge">not yet</span>
}

function HookBadge({ state }: { state: HookState }) {
  switch (state) {
    case 'ok': return <span className="badge ok">workflows present</span>
    case 'missing': return <span className="badge warn">missing</span>
    case 'needs_key': return <span className="badge warn">needs a key</span>
    case 'error': return <span className="badge err">error</span>
  }
  return <span className="badge">off</span>
}

/** Push subscriptions: counters, then who has workflows in the app. */
function PushStatus({ push, service }: { push: ConnectorPush; service: Service }) {
  return (
    <>
      <div className="cx-stats">
        <div className="cx-stat"><span className="n">{push.received}</span><span className="l">received</span></div>
        <div className="cx-stat"><span className="n">{push.published}</span><span className="l">published</span></div>
        <div className="cx-stat"><span className="n">{push.dropped}</span><span className="l">dropped</span></div>
        <div className="cx-stat wide"><span className="n sm" title={when(push.last_received_at)}>{push.last_received_at ? ago(push.last_received_at) : 'nothing yet'}</span><span className="l">last event</span></div>
      </div>
      {push.people.length === 0 ? <p className="muted small">No connected person yet: provision people below and their workflows are created.</p> :
        <div className="cx-people">{push.people.map(p => (
          <div key={p.user_id} className={`cx-person ${p.state === 'off' ? 'off' : ''}`}>
            <Avatar name={p.name} email={p.email} />
            <div className="grow"><b>{p.name || p.email}</b><div className="muted small">{p.workflows.length ? p.workflows.map(w => w.name.replace(/^Pocket Core · /, '')).join(' + ') : p.email}</div>
              {p.detail && <div className="small" style={{ color: p.state === 'error' ? 'var(--danger)' : undefined }}>{p.detail}</div>}</div>
            <span className="muted small" title={p.last_drop ? `Last dropped: ${p.last_drop}` : undefined}>{p.received} in · {p.dropped} dropped</span>
            <HookBadge state={p.state} />
          </div>))}</div>}
      <details className="cx-more"><summary>How events work</summary>
        <p>Workflows live in each person's own {service.name} account ({service.name} runs a workflow only for its owner's photos) and are made with their own key. Uploads from someone who isn't a Core user in this service's audience, live-photo video halves, the locked folder and metadata re-scans of old photos are dropped. Exact GPS never goes on the bus, only city, state and country.</p>
      </details>
    </>
  )
}

function credLabel(c: string) { return c === 'connector' ? 'own key, made by Core' : c === 'own' ? 'own key, set by hand' : 'main key' }

/** One service's connector: test, switches, events and the people table. */
export function ConnectorModal({ service, onClose }: { service: Service; onClose: () => void }) {
  const { toast } = useToast()
  const base = `${V1}/services/${service.id}/connector`
  const { data: c, setData, reload } = useAsync(() => api.get<ConnectorView>(base), [service.id])
  const { data: people, reload: reloadPeople } = useAsync(() => api.get<ConnectorAccount[]>(`${base}/accounts`), [service.id])
  const [test, setTest] = useState<ConnectorTest | null>(null)
  const { data: remote } = useAsync(() => api.get<{ capabilities: { link?: boolean } }>(base).then(v => v.capabilities.link ? api.get<RemoteAccount[]>(`${base}/remote-accounts`).catch(() => null) : null), [service.id])
  const link = (uid: number, remoteID: string) => run(`l${uid}`, async () => {
    const r = await api.put<{ note?: string }>(`${base}/accounts/${uid}`, { remote_id: remoteID })
    toast(remoteID ? r.note || 'Linked' : 'Unlinked')
    reloadPeople()
  })
  const [busy, setBusy] = useState('')
  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key)
    try { await fn() } catch (e) { toast((e as Error).message, 'err'); reload() } finally { setBusy('') }
  }
  const save = (key: string, body: Partial<ConnectorView>, done?: string) => run(key, async () => { setData(await api.put<ConnectorView>(base, body)); toast(done || 'Saved') })
  const doTest = () => run('test', async () => setTest(await api.post<ConnectorTest>(`${base}/test`)))
  const poll = () => run('poll', async () => {
    const r = await api.post<{ ok: boolean; events?: number; baseline?: boolean; error?: string }>(`${base}/poll`)
    toast(!r.ok ? `Check failed: ${r.error}` : c?.push ? `Checked${r.events ? ` · ${r.events} new account event${r.events === 1 ? '' : 's'}` : ''}` : r.baseline ? 'Baseline recorded; changes from now on become events' : `${r.events} event${r.events === 1 ? '' : 's'} published`, r.ok ? 'ok' : 'err')
    reload()
  })
  const recreate = () => run('recreate', async () => {
    const r = await api.post<{ ok: boolean; error?: string; connector: ConnectorView }>(`${base}/hooks/recreate`)
    setData(r.connector)
    toast(r.ok ? 'Workflows re-created' : `Re-create failed: ${r.error}`, r.ok ? 'ok' : 'err')
  })
  const provision = (body: { user_id?: number; all?: boolean }) => run(body.all ? 'all' : `p${body.user_id}`, async () => {
    const r = await api.post<{ results: ProvisionResult[] }>(`${base}/provision`, body)
    const bad = r.results.filter(x => x.status === 'error')
    toast(r.results.length === 0 ? 'Nobody is missing an account' : bad.length ? `${bad.length} failed: ${bad[0].error}` : `${r.results.length} done`, bad.length ? 'err' : 'ok')
    reloadPeople()
  })
  if (!c) return <Modal title={`${service.name} · connector`} onClose={onClose} size="lg"><Loading /></Modal>
  const off = !c.enabled
  const intervals = [60, 300, 900, 3600].includes(c.poll_interval_s) ? [60, 300, 900, 3600] : [60, 300, 900, 3600, c.poll_interval_s].sort((x, y) => x - y)
  const fmtSec = (n: number) => n < 60 ? `${n} s` : n < 3600 ? `${Math.round(n / 60)} min` : `${Math.round(n / 3600)} h`
  return (
    <Modal title={`${service.name} · connector`} onClose={onClose} size="lg">
      <div className="cx-hero">
        <div className="grow">
          <div className="cx-hero-title"><span className={`pill ${off ? '' : c.last_error ? 'err' : 'ok'}`}>{off ? 'Off' : c.last_error ? 'Error' : 'On'}</span><b>Connector</b></div>
          <p className="muted small">Core manages {service.name} with the admin key saved on the service: accounts for your people, sign-in through Core, and <span className="inline-code">{c.type}.*</span> events.</p>
        </div>
        <button className="btn" onClick={doTest} disabled={busy === 'test'} title="Read-only: version and admin rights">{busy === 'test' ? 'Testing…' : 'Test'}</button>
        <Switch on={c.enabled} onChange={v => save('enabled', { enabled: v }, v ? 'Connector on' : 'Connector off')} title="Master switch" />
      </div>
      {test && (test.ok
        ? <div className="callout success mt-sm">{service.name} {test.info?.version} · {test.info?.admin ? 'admin key' : 'key'} · {test.info?.users} accounts · {test.ms} ms{test.info?.detail && <div className="mt-sm">{test.info.detail}</div>}</div>
        : <div className="callout danger mt-sm">{test.error}</div>)}
      {c.last_error && <div className="callout danger mt-sm"><b>Last error</b> · {c.last_error}</div>}
      {off && <p className="muted small mt-sm">Switched off: Core makes no accounts, stops checking and deletes its event workflows. Sign-in stays as {service.name} has it.</p>}

      <div className={`cx-sections ${off ? 'off' : ''}`}>
        {c.capabilities.provision && <section className="cx-sec">
          <header><span className="cx-ico">👤</span><div className="grow"><b>Create accounts for new people</b><span>New people under Users get a {service.name} account and their own key, saved as their credential.</span></div>
            <Switch on={c.provision_on_create} onChange={v => !off && save('prov', { provision_on_create: v })} /></header>
        </section>}

        {c.capabilities.sso && <section className="cx-sec">
          <header><span className="cx-ico">🔑</span><div className="grow"><b>Sign in with Pocket Core {busy === 'sso' && <span className="spinner sm" />}</b><span>Adds the button to {service.name}'s login. Password login stays on; only this service's audience can sign in.</span></div>
            <Switch on={c.sso_enabled} onChange={v => !off && save('sso', { sso_enabled: v }, v ? 'Sign in with Pocket Core is on' : 'Sign in with Pocket Core is off')} /></header>
          {c.sso_enabled && c.oidc_client && <div className="cx-body">
            <dl className="cx-kv"><dt>Client</dt><dd className="mono">{c.oidc_client.client_id}</dd><dt>Issuer</dt><dd className="mono">{c.issuer}</dd></dl>
            {c.redirects.length > 0 && <details className="cx-more"><summary>Redirect URLs ({c.redirects.length})</summary><ul className="mono">{c.redirects.map(r => <li key={r}>{r}</li>)}</ul></details>}
          </div>}
        </section>}

        {c.capabilities.events && <section className="cx-sec">
          <header><span className="cx-ico">⚡</span><div className="grow"><b>Events {busy === 'events' && <span className="spinner sm" />}</b>
            <span>{c.push ? <>Each upload is pushed to Core by a workflow in {service.name}: {c.push.triggers.map((t, i) => <span key={t.trigger}>{i > 0 && ' and '}<b>{t.name.replace(/^Pocket Core · /, '')}</b></span>)}. No polling.</> : <>Core checks {service.name} and publishes changes; the first check only records a baseline.</>}</span></div>
            <Switch on={c.events_enabled} onChange={v => !off && save('events', { events_enabled: v })} /></header>
          {c.events_enabled && <div className="cx-body">
            <div className="cx-toolbar">
              <span className="muted small">{c.push ? 'Health check' : 'Check'} every</span>
              <div style={{ width: 120 }}><Select ariaLabel="Check interval" value={String(c.poll_interval_s)} onChange={v => save('interval', { poll_interval_s: Number(v) })} options={intervals.map(n => ({ value: String(n), label: fmtSec(n) }))} /></div>
              <span className="muted small" title={when(c.last_poll_at)}>last {ago(c.last_poll_at)}</span>
              <span className="spacer" />
              <button className="btn sm" disabled={off || busy === 'poll' || c.polling} onClick={poll}>{busy === 'poll' || c.polling ? 'Checking…' : c.push ? 'Check now' : 'Run now'}</button>
              {c.push && <button className="btn sm ghost" disabled={off || busy === 'recreate'} onClick={recreate} title="Delete and create every workflow again, with new secrets">{busy === 'recreate' ? 'Re-creating…' : 'Re-create workflows'}</button>}
            </div>
            {c.push && <PushStatus push={c.push} service={service} />}
          </div>}
        </section>}
      </div>

      {c.capabilities.provision && <div className="cx-people-block">
        <div className="cx-block-head"><b>People</b><span className="muted small">{people ? `${people.filter(p => p.status === 'ok' || p.status === 'linked').length} of ${people.length} connected` : ''}</span><span className="spacer" />
          <button className="btn sm" disabled={off || busy === 'all'} onClick={() => provision({ all: true })} title="Everyone in the audience without an account">{busy === 'all' ? 'Working…' : 'Provision everyone missing'}</button></div>
        {!people ? <Loading /> : people.length === 0 ? <p className="muted small">Nobody is in this service's audience.</p> :
          <div className="cx-people">{people.map(p => (
            <div key={p.user_id} className={`cx-person ${p.disabled || !p.in_audience ? 'off' : ''}`}>
              <Avatar name={p.name} email={p.email} />
              <div className="grow"><b>{p.name || p.email}</b><div className="muted small">{p.email}</div>
                {p.error && <div className="small" style={{ color: 'var(--danger)' }}>{p.error}</div>}</div>
              <span className="muted small" title={p.remote_id ? `${service.name} id ${p.remote_id}` : undefined}>{credLabel(p.credential)}</span>
              {remote && p.in_audience && !p.disabled && <div style={{ width: 170 }} title={`Which ${service.name} account is ${p.name || p.email}'s`}><Select ariaLabel={`${service.name} account`} value={p.remote_id || ''} disabled={off || busy === `l${p.user_id}`} onChange={v => link(p.user_id, v)}
                options={remote.map(a => ({ value: a.id, label: a.name, hint: a.admin ? 'admin' : a.disabled ? 'disabled' : undefined }))} placeholder="Pick account…" clearLabel="Not linked" /></div>}
              {!p.in_audience && <span className="badge warn">outside audience</span>}
              {p.manual && <span className="badge" title="Linked by hand; provisioning leaves it alone">by hand</span>}
              <StatusBadge status={p.status} />
              {p.status !== 'ok' && !p.manual && p.in_audience && !p.disabled ? <button className="btn sm" disabled={off || busy === `p${p.user_id}`} onClick={() => provision({ user_id: p.user_id })}>{busy === `p${p.user_id}` ? '…' : p.status === 'linked' ? 'Re-check' : 'Provision'}</button> : <span className="cx-btn-gap" />}
            </div>))}</div>}
      </div>}
    </Modal>
  )
}
