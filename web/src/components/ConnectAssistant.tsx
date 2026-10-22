import { useEffect, useState } from 'react'
import { Card, Field } from './ui'
import { Select } from './Select'

// These commands are displayed/copied, never executed by the browser. Quote all
// variable values so unusual self-hosted URLs cannot become shell instructions.
const shellQuote = (value: string) => "'" + value.replace(/'/g, "'\\''") + "'"

export default function ConnectAssistant({ url, appTools }: { url: string; appTools: boolean }) {
  const [client, setClient] = useState('claude')
  const [scope, setScope] = useState('status:read apps:read apps:write')
  const [copied, setCopied] = useState('')
  const [error, setError] = useState('')
  const access = appTools ? scope : 'status:read'
  const title = client === 'claude' ? 'Claude Code' : 'Codex'
  const add = client === 'claude'
    ? `claude mcp add-json --scope user pocket ${shellQuote(JSON.stringify({ type: 'http', url, oauth: { scopes: access } }))}`
    : `codex mcp add pocket --url ${shellQuote(url)}`
  const login = client === 'claude'
    ? 'claude mcp login pocket --no-browser'
    : `codex mcp login pocket --no-browser --scopes ${access.split(' ').join(',')}`
  useEffect(() => { setCopied(''); setError('') }, [client, scope])
  const copy = async (value: string, kind: string) => {
    try { await navigator.clipboard.writeText(value); setCopied(kind); setError('') }
    catch { setError('Select and copy the commands below.') }
  }
  return <Card title="Connect your assistant" sub="Sign in with your Core account using your assistant’s built-in MCP login.">
    <div className="row mb" style={{ flexWrap: 'wrap' }} aria-label="Choose your assistant">
      <button className={`btn ${client === 'claude' ? 'primary' : ''}`} aria-pressed={client === 'claude'} onClick={() => setClient('claude')}>Claude Code</button>
      <button className={`btn ${client === 'codex' ? 'primary' : ''}`} aria-pressed={client === 'codex'} onClick={() => setClient('codex')}>Codex terminal</button>
    </div>
    <Field label="Requested access"><Select ariaLabel="Requested access" value={access} onChange={setScope} options={[
      { value: 'status:read', label: 'Status checks only' },
      ...(appTools ? [{ value: 'status:read apps:read', label: 'Read application content' }, { value: 'status:read apps:read apps:write', label: 'Read and create/update items' }] : []),
    ]} /></Field>
    <p className="mt"><strong>1. Copy and run these commands in your {title} terminal.</strong></p>
    <p>The first adds Core. The second starts sign-in and prints a link. Use the machine where your assistant runs.</p>
    <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}><code>{`${add}\n${login}`}</code></pre>
    <div className="row mt mb" style={{ flexWrap: 'wrap' }}>
      <button className="btn primary" onClick={() => copy(`${add}\n${login}`, 'setup')}>{copied === 'setup' ? 'Commands copied' : 'Copy setup commands'}</button>
      <button className="btn" onClick={() => copy(login, 'login')}>{copied === 'login' ? 'Sign-in command copied' : 'Already added? Copy sign-in command'}</button>
    </div>
    {error && <p role="alert">{error}</p>}
    <p><strong>2. Open the terminal’s link and approve in Core.</strong></p>
    <p>Sign in on any computer or phone. Review the access, then choose <strong>Approve and copy sign-in response</strong>.</p>
    <p><strong>3. Paste the response back into the waiting terminal.</strong></p>
    <p>Click <strong>Copy sign-in response</strong> on Core’s approval page. Paste it into {title} and press Enter. Your assistant saves the connection. Start a new session, then ask: <em>“Check the status of all applications I can access.”</em></p>
    <p className="muted small">No helper download or Python is needed. Sign-in stays on Core; there is no browser redirect to localhost. Access lasts up to 30 days and can be revoked below.</p>
    <details className="mt"><summary>Connection help</summary>
      <p className="mt">Use a current {title} version that supports <code>mcp login --no-browser</code>. A Core webpage cannot directly configure a terminal on another machine; your assistant starts sign-in with the commands above.</p>
      <p>If <code>pocket</code> is already configured with the same access, use just the sign-in command. To change Claude Code’s requested access, remove that entry with <code>claude mcp remove --scope user pocket</code>, then copy the new setup commands. Other server entries are unaffected.</p>
      <p>If you previously configured a manual token for this entry, remove that entry first and add it again using this setup. The default name here is <code>pocket</code>; older examples used <code>pocket_core</code> or <code>pocket_account</code>.</p>
      <p>If you closed the approval page before copying, restart sign-in in your terminal. A response expires after five minutes and works only with the terminal that requested it.</p>
    </details>
  </Card>
}
