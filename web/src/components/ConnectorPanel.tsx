import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { ConnectorAccount, ConnectorPush, ConnectorStatus, ConnectorTest, ConnectorView, HookState, ProvisionResult, Service } from '../lib/types'
import { Loading, Modal, Switch, useAsync, useToast } from './ui'

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

/** Push subscriptions: who has workflows in the app and what arrived. */
function PushStatus({ push, service }: { push: ConnectorPush; service: Service }) {
  return (
    <div className="mt-sm">
      <div className="muted small">
        {push.last_received_at ? <span title={when(push.last_received_at)}>Last event received {ago(push.last_received_at)}</span> : 'No event received yet'}
        {' · '}{push.received} received · {push.published} published · {push.dropped} dropped
        {push.checked_at && <> · workflows checked <span title={when(push.checked_at)}>{ago(push.checked_at)}</span></>}
      </div>
      {push.people.length === 0 ? <p className="muted small mt-sm">No connected person yet: provision people below, then their workflows are created.</p> :
        <table className="table compact mt-sm"><tbody>{push.people.map(p => <tr key={p.user_id} className={p.state === 'off' ? 'muted' : ''}>
          <td><b>{p.name || p.email}</b>{p.name && p.email && <div className="muted small">{p.email}</div>}</td>
          <td><HookBadge state={p.state} />
            {p.detail && <div className="small" style={{ color: p.state === 'error' ? 'var(--danger)' : undefined, maxWidth: 360 }}>{p.detail}</div>}
            {p.workflows.length > 0 && <div className="muted small mono" title={p.workflows.map(w => `${w.name}: ${w.remote_workflow_id}`).join('\n')}>{p.workflows.map(w => w.name.replace(/^Pocket Core · /, '')).join(' · ')}</div>}</td>
          <td className="small muted right">{p.last_received_at ? <span title={when(p.last_received_at)}>{ago(p.last_received_at)}</span> : '—'}
            <div title={p.last_drop ? `Last dropped: ${p.last_drop}` : undefined}>{p.received} in · {p.published} out · {p.dropped} dropped</div></td>
        </tr>)}</tbody></table>}
      <p className="muted small mt-sm">Workflows live in each person's own {service.name} account (Immich runs a workflow only for its owner's photos), made with their own key. Assets of someone who is not a Core user in this service's audience, live-photo video halves, the locked folder and metadata re-scans of old assets are dropped. Exact GPS never goes on the bus, only city, state and country.</p>
    </div>
  )
}

function credLabel(c: string) { return c === 'connector' ? 'their own key, made by Core' : c === 'own' ? 'their own, set by hand' : 'box-wide' }

/** One service's connector: test, switches, events and the people table. */
export function ConnectorModal({ service, onClose }: { service: Service; onClose: () => void }) {
  const { toast } = useToast()
  const base = `${V1}/services/${service.id}/connector`
  const { data: c, setData, reload } = useAsync(() => api.get<ConnectorView>(base), [service.id])
  const { data: people, reload: reloadPeople } = useAsync(() => api.get<ConnectorAccount[]>(`${base}/accounts`), [service.id])
  const [test, setTest] = useState<ConnectorTest | null>(null)
  const [busy, setBusy] = useState('')
  const [interval, setIntervalS] = useState<string>('')
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
  return (
    <Modal title={`${service.name} · connector`} onClose={onClose} size="lg">
      <p className="muted small">Core acts on {service.name} with the admin credential stored on the service: it can make each person's account and hand them their own key, turn on <b>Sign in with Pocket Core</b>, and publish what happens there as <span className="inline-code">{c.type}.*</span> events. Nothing runs until you switch it on.</p>

      <div className="row mt-sm"><button className="btn" onClick={doTest} disabled={busy === 'test'}>{busy === 'test' ? 'Testing…' : 'Test'}</button><span className="muted small">Read-only: version and the admin read the connector needs.</span></div>
      {test && (test.ok
        ? <div className="callout success mt-sm">{c.type === 'immich' ? 'Immich' : c.type} {test.info?.version} · {test.info?.admin ? 'admin credential' : 'credential'} · {test.info?.users} accounts · {test.ms} ms{test.info?.detail && <div className="mt-sm">{test.info.detail}</div>}</div>
        : <div className="callout danger mt-sm">{test.error}</div>)}

      <table className="table compact mt">
        <tbody>
          <tr><td style={{ width: 44 }}><Switch on={c.enabled} onChange={v => save('enabled', { enabled: v }, v ? 'Connector on' : 'Connector off')} /></td>
            <td><b>Connector on</b><div className="muted small">The master switch. Off: Core makes no accounts, stops checking and deletes its event workflows; sign-in stays as the app has it.</div></td></tr>
          {c.capabilities.provision && <tr className={off ? 'muted' : ''}><td><Switch on={c.provision_on_create} onChange={v => !off && save('prov', { provision_on_create: v })} /></td>
            <td><b>Create accounts for new people</b><div className="muted small">When you add a person under Users, Core makes (or links, if the email exists) their {service.name} account in the background and stores their own key as their credential. Only people in this service's audience.</div></td></tr>}
          {c.capabilities.sso && <tr className={off ? 'muted' : ''}><td><Switch on={c.sso_enabled} onChange={v => !off && save('sso', { sso_enabled: v }, v ? 'Sign in with Pocket Core is on' : 'Sign in with Pocket Core is off')} /></td>
            <td><b>Sign in with Pocket Core</b>{busy === 'sso' && <span className="spinner sm" style={{ marginLeft: 8 }} />}
              <div className="muted small">Creates an OIDC client for {service.name} and writes it into the app's settings (a backup of the app's config is saved in Core's data dir first). Password login stays on. Only this service's audience can sign in.</div>
              {c.redirects.length > 0 && <div className="muted small mono mt-sm">{c.redirects.join(' · ')}</div>}
              {c.oidc_client && <div className="muted small mt-sm">Client <span className="mono">{c.oidc_client.client_id}</span> · issuer <span className="mono">{c.issuer}</span></div>}
            </td></tr>}
          {c.capabilities.events && <tr className={off ? 'muted' : ''}><td><Switch on={c.events_enabled} onChange={v => !off && save('events', { events_enabled: v })} /></td>
            <td><b>Events</b>{busy === 'events' && <span className="spinner sm" style={{ marginLeft: 8 }} />}
              {c.push
                ? <div className="muted small">{service.name} sends each upload to Core through a workflow (no polling): on creates {c.push.triggers.map((t, i) => <span key={t.trigger}>{i > 0 && ' and '}<b>{t.name}</b></span>)} in {c.push.per_user ? "each connected person's account" : service.name}, off deletes them. Every interval Core checks they still exist (re-creating any that were deleted) and looks for new {service.name} accounts.</div>
                : <div className="muted small">Poll {service.name} and publish changes on the bus. The first poll only records a baseline.</div>}
              <div className="row mt-sm">
                <span className="small muted">{c.push ? 'check every' : 'every'}</span>
                <input type="number" min={60} step={30} style={{ width: 90 }} value={interval || String(c.poll_interval_s)} onChange={e => setIntervalS(e.target.value)} />
                <span className="small muted">seconds</span>
                {interval && Number(interval) !== c.poll_interval_s && <button className="btn sm" onClick={() => { save('interval', { poll_interval_s: Number(interval) }); setIntervalS('') }}>Save</button>}
                <button className="btn sm" disabled={off || !c.events_enabled || busy === 'poll' || c.polling} onClick={poll}>{busy === 'poll' || c.polling ? 'Checking…' : c.push ? 'Check now' : 'Run now'}</button>
                {c.push && <button className="btn sm" disabled={off || !c.events_enabled || busy === 'recreate'} onClick={recreate} title="Delete and create every workflow again, with new secrets">{busy === 'recreate' ? 'Re-creating…' : 'Re-create workflows'}</button>}
              </div>
              <div className="muted small mt-sm" title={when(c.last_poll_at)}>Last check {ago(c.last_poll_at)}</div>
              {c.push && (c.events_enabled || c.push.people.length > 0) && <PushStatus push={c.push} service={service} />}
            </td></tr>}
        </tbody>
      </table>
      {c.last_error && <div className="callout danger mt-sm"><b>Last error</b> · {c.last_error}</div>}

      {c.capabilities.provision && <>
        <div className="row mt" style={{ justifyContent: 'space-between' }}>
          <b>People</b>
          <button className="btn sm" disabled={off || busy === 'all'} onClick={() => provision({ all: true })} title="Everyone in the audience without an account">{busy === 'all' ? 'Working…' : 'Provision everyone missing'}</button>
        </div>
        {!people ? <Loading /> : people.length === 0 ? <p className="muted small">Nobody is in this service's audience.</p> :
          <table className="table compact mt-sm"><tbody>{people.map(p => <tr key={p.user_id} className={p.disabled || !p.in_audience ? 'muted' : ''}>
            <td><b>{p.name || p.email}</b>{p.name && <div className="muted small">{p.email}</div>}</td>
            <td><StatusBadge status={p.status} />{!p.in_audience && <span className="badge warn" style={{ marginLeft: 6 }}>outside audience</span>}
              {p.error && <div className="small" style={{ color: 'var(--danger)', maxWidth: 380 }}>{p.error}</div>}</td>
            <td className="small muted">{credLabel(p.credential)}{p.remote_id && <div className="mono">{p.remote_id}</div>}</td>
            <td className="right">{p.status !== 'ok' && p.in_audience && !p.disabled && <button className="btn sm" disabled={off || busy === `p${p.user_id}`} onClick={() => provision({ user_id: p.user_id })}>{busy === `p${p.user_id}` ? '…' : p.status === 'linked' ? 'Re-check' : 'Provision'}</button>}</td>
          </tr>)}</tbody></table>}
      </>}
    </Modal>
  )
}
