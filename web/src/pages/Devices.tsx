import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { ago, when } from '../lib/format'
import type { AppChoice, Device, User } from '../lib/types'
import { useApp } from '../lib/app-context'
import { Card, Confirm, Empty, Loading, PageHeader, Segmented, SettingRow, errMsg, useAsync, useToast } from '../components/ui'
import PairModal from '../components/PairModal'
import { Icon } from '../components/icons'

export default function Devices() {
  const { user } = useApp()
  const admin = user?.role === 'admin'
  const [sp, setSp] = useSearchParams()
  const { data, loading, reload } = useAsync(() => api.get<Device[]>(`${V1}/devices`), [])
  const { data: users } = useAsync(() => admin ? api.get<User[]>(`${V1}/users`) : Promise.resolve([] as User[]), [admin])
  const [pair, setPair] = useState<number | 0 | null>(sp.get('pair') ? 0 : null)
  const [del, setDel] = useState<Device | null>(null)
  // a pairing started from the sidebar or the palette
  useEffect(() => { window.addEventListener('pc:paired', reload); return () => window.removeEventListener('pc:paired', reload) }, [reload])
  if (loading && !data) return <Loading />
  const list = data || []
  const who = (id: number) => (users || []).find(u => u.id === id)
  return (
    <>
      <PageHeader title="Devices" sub="Phones and Macs that paired with this box. Each holds its own token; revoke one without touching the others."
        actions={<><button type="button" className="btn primary" onClick={() => setPair(0)}><Icon name="qr" size={15} />Pair a device</button></>} />
      {list.length === 0 ? <Card><Empty icon="smartphone" title="No devices paired">Tap “Pair a device”, scan the code with Pocket Homelab, done.</Empty></Card> :
        <Card flush><table className="table">
          <thead><tr><th>Device</th>{admin && <th>User</th>}<th>Token</th><th>Last seen</th><th>Paired</th><th></th></tr></thead>
          <tbody>{list.map(d => <tr key={d.id}>
            <td><b>{d.name}</b> <span className="muted small">{d.platform}</span></td>
            {admin && <td>{who(d.user_id)?.name || who(d.user_id)?.email || d.user_id}</td>}
            <td className="mono small">{d.token_prefix}…</td>
            <td className="muted small" title={when(d.last_seen_at)}>{ago(d.last_seen_at)}{d.last_ip && <div>{d.last_ip}</div>}</td>
            <td className="muted small">{when(d.created_at)}</td>
            <td className="right"><button type="button" className="btn sm danger" onClick={() => setDel(d)}>Revoke</button></td>
          </tr>)}</tbody></table></Card>}
      <AppChoices />
      {admin && (users || []).length > 1 && <Card title="Pair for someone else" className="mt"><div className="chips">{(users || []).filter(u => u.id !== user?.id && !u.disabled).map(u => <button type="button" key={u.id} className="chip" onClick={() => setPair(u.id)}><Icon name="qr" size={13} />{u.name || u.email}</button>)}</div><p className="muted small mt-sm">The code binds the device to that account; they see the same services with their own token.</p></Card>}
      {pair !== null && <PairModal userId={pair || undefined} onClose={() => { setPair(null); sp.delete('pair'); setSp(sp); reload() }} />}
      {del && <Confirm title="Revoke device" message={<>Revoke <b>{del.name}</b>? Its token stops working for Core and every Pocket app at once. Pair again to restore.</>} onConfirm={async () => { await api.del(`${V1}/devices/${del.id}`); setDel(null); reload() }} onCancel={() => setDel(null)} />}
    </>
  )
}

/** Which app a place in Pocket Homelab uses when this person has both (Money: Pocket Money or Firefly III). */
function AppChoices() {
  const { toast } = useToast()
  const { data, reload } = useAsync(() => api.get<AppChoice[]>(`${V1}/me/choices`), [])
  const [busy, setBusy] = useState('')
  const list = data || []
  if (list.length === 0) return null
  const set = async (slot: string, provider: string) => {
    setBusy(slot)
    try { await api.put(`${V1}/me/choices/${slot}`, { provider }); reload(); toast('Saved. Devices use it after their next refresh (opening the app, or Refresh services).') }
    catch (e) { toast(errMsg(e), 'err') }
    finally { setBusy('') }
  }
  const nameOf = (c: AppChoice, v: string) => c.options.find(o => o.value === v)?.name || v
  return (
    <Card title="Apps on your devices" sub="Where you have both apps for something, choose which one Pocket Homelab uses. Automatic takes the Pocket app. The other app's address stays on the device for when you switch here." className="mt">
      {list.map(c => {
        const both = c.options.every(o => c.available[o.value])
        const help = both
          ? <>Using <b>{nameOf(c, c.effective)}</b>{c.explicit ? ', chosen here' : ' (automatic)'}.</>
          : <>Only <b>{nameOf(c, c.effective)}</b> is shared with you, so it is used whatever you choose.</>
        return (
          <SettingRow key={c.slot} title={c.title} help={help}>
            <Segmented size="sm" label={c.title} value={c.choice || ''} onChange={v => busy !== c.slot && set(c.slot, v)}
              options={[{ value: '', label: 'Automatic' }, ...c.options.map(o => ({ value: o.value, label: o.name, title: c.available[o.value] ? undefined : `${o.name} is not shared with you` }))]} />
          </SettingRow>
        )
      })}
    </Card>
  )
}
