import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Card, Field } from '../components/ui'

type Connection = { id: string; client_name: string; created_at: string; expires_at: string }
type Connections = { enabled: boolean; url: string; connections: Connection[] }

export default function AIConnections() {
  const [data, setData] = useState<Connections | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [copied, setCopied] = useState(false)
  const refresh = async () => setData(await api.get<Connections>(`${V1}/me/ai-connections`))
  useEffect(() => { refresh().catch(e => setError(e.message)) }, [])
  const disconnect = async (id: string) => {
    setBusy(id); setError('')
    try { await api.del(`${V1}/me/ai-connections/${encodeURIComponent(id)}`); await refresh() }
    catch (e) { setError((e as Error).message) }
    finally { setBusy('') }
  }
  const copy = async () => {
    try { await navigator.clipboard.writeText(data!.url); setCopied(true) }
    catch { setError('Select and copy the URL below.') }
  }
  return <>
    <h1>AI connections</h1>
    <p className="muted">Let your assistant check the status of Pocket apps shared with your account.</p>
    {error && <div className="callout danger" role="alert">{error}</div>}
    {!data && !error && <p>Loading connections…</p>}
    {data && <div className="grid" style={{ gap: 20 }}>
      <Card title="Connect an assistant">
        {data.enabled ? <>
          <p>Add this URL as a remote MCP server in your assistant, choose account sign-in, and approve access using your Core account.</p>
          <Field label="MCP server URL"><input readOnly value={data.url} onFocus={e => e.target.select()} /></Field>
          <button className="btn mt" onClick={copy}>{copied ? 'Copied' : 'Copy URL'}</button>
          <p>Access includes Core availability and status checks for your permitted Pocket apps, including apps shared with you later. Personal content and changes are excluded.</p>
          <p className="muted">Your assistant needs to support remote MCP with OAuth. Connections expire after 30 days; reconnect when prompted.</p>
        </> : <p>Account connections are disabled on this Core. Ask your administrator to enable MCP account sign-in.</p>}
      </Card>
      <Card title="Your connections">
        {data.connections.length === 0 ? <p>No assistants connected yet.</p> : data.connections.map(c => <div key={c.id} className="row" style={{ justifyContent: 'space-between', flexWrap: 'wrap', gap: 12, padding: '12px 0' }}>
          <div><strong>{c.client_name}</strong><div className="muted small">Connected {new Date(c.created_at).toLocaleDateString()} · {new Date(c.expires_at) <= new Date() ? 'Expired' : 'Expires'} {new Date(c.expires_at).toLocaleDateString()}</div></div>
          <button className="btn danger" disabled={!!busy} onClick={() => disconnect(c.id)}>{busy === c.id ? 'Disconnecting…' : 'Disconnect'}</button>
        </div>)}
      </Card>
    </div>}
  </>
}
