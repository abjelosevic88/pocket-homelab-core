import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { Card, Field, Loading, PageHeader } from '../components/ui'
import { Select } from '../components/Select'
import { Icon } from '../components/icons'

type Connection = { id: string; client_name: string; created_at: string; expires_at: string; scope: string; kind: string }
type Connections = { enabled: boolean; app_tools_enabled: boolean; url: string; connections: Connection[] }

type CreatedToken = { token: string; url: string; connection: Connection }

export default function AIConnections() {
  const [data, setData] = useState<Connections | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [copied, setCopied] = useState(false)
  const [name, setName] = useState('Codex')
  const [scope, setScope] = useState('status:read apps:read apps:write')
  const [days, setDays] = useState(30)
  const [created, setCreated] = useState<CreatedToken | null>(null)
  const [tokenCopied, setTokenCopied] = useState(false)
  const [terminalClient, setTerminalClient] = useState('codex')
  const createToken = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy('create'); setError(''); setTokenCopied(false)
    try {
      const result = await api.post<CreatedToken>(`${V1}/me/ai-connections/token`, { name, scope: data?.app_tools_enabled ? scope : 'status:read', expires_in_days: days, approved: true })
      setCreated(result)
      await refresh()
    } catch (e) { setError((e as Error).message) }
    finally { setBusy('') }
  }
  const copyToken = async () => {
    try { await navigator.clipboard.writeText(created!.token); setTokenCopied(true) }
    catch { setError('Select and copy the connection token below.') }
  }
  const refresh = async () => setData(await api.get<Connections>(`${V1}/me/ai-connections`))
  useEffect(() => { refresh().catch(e => setError(e.message)) }, [])
  const disconnect = async (id: string) => {
    setBusy(id); setError('')
    try { await api.del(`${V1}/me/ai-connections/${encodeURIComponent(id)}`); await refresh(); if (created?.connection.id === id) setCreated(null) }
    catch (e) { setError((e as Error).message) }
    finally { setBusy('') }
  }
  const copy = async () => {
    try { await navigator.clipboard.writeText(data!.url); setCopied(true) }
    catch { setError('Select and copy the URL below.') }
  }
  return <>
    <PageHeader title="AI connections" sub="Connect your assistant to the Pocket apps shared with your account." />
    {error && <div className="callout danger mb" role="alert"><Icon name="alert" size={15} /><span>{error}</span></div>}
    {!data && !error && <Loading label="Loading connections…" />}
    {data && <div className="grid" style={{ gap: 20 }}>
      {data.enabled && <Card title="Connect from your terminal">
        <p>Run Pocket Connect on the machine where you use Codex or Claude Code. It gives you a link and pairing code. Approve access here, then paste the completion code back into your terminal to configure MCP automatically.</p>
        <Field label="Assistant"><Select ariaLabel="Assistant" value={terminalClient} onChange={setTerminalClient} options={[{ value: 'codex', label: 'Codex' }, { value: 'claude', label: 'Claude Code' }]} /></Field>
        <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{`curl -fsS ${window.location.origin}/mcp/connect.py -o pocket-connect.py\npython3 pocket-connect.py --url ${window.location.origin} --client ${terminalClient}${data.app_tools_enabled ? '' : ' --access status'}`}</pre>
        <p>Requires Python 3.11 or newer. {data.app_tools_enabled ? <>The default requests reading and creating/updating items for 30 days. Use <code>--access read</code> or <code>--access status</code> for less access.</> : <>This command requests status checks for 30 days.</>} Use <code>--days 1</code> or <code>--days 7</code> for a shorter connection.</p>
        <Link className="btn primary" to="/ai-connections/pair">Enter pairing code</Link>
        <p className="muted">No localhost callback or environment variable is needed. Restart your assistant after pairing. Credentials are saved in its private user configuration.</p>
      </Card>}
      {data.enabled && <Card title="Connect without redirects">
        <p>Use a connection token when your assistant runs on another computer, a server or in the cloud. This setup stays on Core and does not redirect to localhost.</p>
        {!created ? <form onSubmit={createToken} className="stack">
          <Field label="Connection name"><input value={name} onChange={e => setName(e.target.value)} maxLength={80} required autoComplete="off" /></Field>
          <Field label="Access"><Select ariaLabel="Access" value={data.app_tools_enabled ? scope : 'status:read'} onChange={setScope} options={[
            { value: 'status:read', label: 'Status checks only' },
            ...(data.app_tools_enabled ? [{ value: 'status:read apps:read', label: 'Read application content' }, { value: 'status:read apps:read apps:write', label: 'Read and create/update items' }] : []),
          ]} /></Field>
          <Field label="Expires after"><Select ariaLabel="Expires after" value={String(days)} onChange={v => setDays(Number(v))} options={[{ value: '1', label: '1 day' }, { value: '7', label: '7 days' }, { value: '30', label: '30 days' }]} /></Field>
          <p>This grants the selected access to your permitted apps, including apps shared with you later. Deletion and service administration are unavailable.</p>
          <button className="btn primary" type="submit" disabled={!!busy}>{busy === 'create' ? 'Creating…' : 'Create token with this access'}</button>
        </form> : <>
          <p role="status">Token created. Copy it now; Core cannot show it again. It expires {new Date(created.connection.expires_at).toLocaleDateString()}. Keep it in your assistant's secret settings.</p>
          <Field label="Connection token"><input type="password" readOnly value={created.token} autoComplete="off" onFocus={e => e.target.select()} /></Field>
          <button type="button" className="btn" onClick={copyToken}>{tokenCopied ? 'Token copied' : 'Copy token'}</button>
          <p>On the machine running Codex, set <code>POCKET_ACCOUNT_MCP_TOKEN</code> to this token in its environment or secret manager, then run:</p>
          <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{`codex mcp add pocket_account --url ${created.url} --bearer-token-env-var POCKET_ACCOUNT_MCP_TOKEN`}</pre>
          <p>Start a new Codex session with that environment variable available. You do not need <code>codex mcp login</code>. Other clients can send this token as a Bearer authorization header.</p>
          <button type="button" className="btn mt" onClick={() => { setCreated(null); setTokenCopied(false) }}>I've saved the token</button>
        </>}
      </Card>}
      <Card title="Connect with browser sign-in">
        {data.enabled ? <>
          <p>Add this URL as a remote MCP server in your assistant, choose account sign-in, and approve access using your Core account.</p>
          <Field label="MCP server URL"><input readOnly value={data.url} onFocus={e => e.target.select()} /></Field>
          <button type="button" className="btn mt" onClick={copy}>{copied ? 'Copied' : 'Copy URL'}</button>
          <p>{data.app_tools_enabled ? 'Choose status checks, reading application content, or reading and creating/updating items when connecting your assistant. Deletion and service administration are unavailable.' : 'Access includes Core availability and status checks for your permitted Pocket apps. Personal content and changes are excluded.'} Access follows your account permissions, including apps shared with you later.</p>
          {data.app_tools_enabled && <p className="muted">Existing status connections keep their original access. To enable application tools, reconnect and approve application access. In Codex, use <code style={{ overflowWrap: 'anywhere' }}>codex mcp login pocket_account --scopes status:read,apps:read,apps:write</code> after adding this URL.</p>}
          <p className="muted">Your assistant needs to support remote MCP with OAuth. Connections expire after 30 days; reconnect when prompted.</p>
        </> : <p>Account connections are disabled on this Core. Ask your administrator to enable MCP account sign-in.</p>}
      </Card>
      <Card title="Your connections">
        {data.connections.length === 0 ? <p>No assistants connected yet.</p> : data.connections.map(c => <div key={c.id} className="row" style={{ justifyContent: 'space-between', flexWrap: 'wrap', gap: 12, padding: '12px 0' }}>
          <div><strong>{c.client_name}</strong><span className="muted small"> · {c.kind === 'token' ? 'Connection token' : 'Browser sign-in'}</span><div className="small">{c.scope?.includes("apps:write") ? "Read and create/update items" : c.scope?.includes("apps:read") ? "Read application content" : "Status checks only"}</div><div className="muted small">Connected {new Date(c.created_at).toLocaleDateString()} · {new Date(c.expires_at) <= new Date() ? 'Expired' : 'Expires'} {new Date(c.expires_at).toLocaleDateString()}</div></div>
          <button type="button" className="btn danger" disabled={!!busy} onClick={() => disconnect(c.id)}>{busy === c.id ? 'Disconnecting…' : 'Disconnect'}</button>
        </div>)}
      </Card>
    </div>}
  </>
}
