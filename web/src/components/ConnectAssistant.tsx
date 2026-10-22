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
    <p>Run the first command to save this Core URL in your assistant’s configuration under the name <code>pocket</code>. Then run the second command to sign in. Use your PC’s terminal, or the remote terminal if that is where your assistant runs.</p>
    <p><code>pocket</code> is a name created by the add command, not a preinstalled service or your username. Adding the URL does not create a Core account or grant access. Sign-in connects it to your own Core account.</p>
    <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}><code>{`${add}\n${login}`}</code></pre>
    <div className="row mt mb" style={{ flexWrap: 'wrap' }}>
      <button className="btn primary" onClick={() => copy(`${add}\n${login}`, 'setup')}>{copied === 'setup' ? 'Commands copied' : 'Copy setup commands'}</button>
      <button className="btn" onClick={() => copy(login, 'login')}>{copied === 'login' ? 'Sign-in command copied' : 'Already added? Copy sign-in command'}</button>
    </div>
    {error && <p role="alert">{error}</p>}
    <p><strong>2. Open the terminal’s link and approve in Core.</strong></p>
    <p>Sign in on any computer or phone. Review the access, then choose <strong>Approve and copy sign-in response</strong>.</p>
    <p><strong>3. Paste the response back into the waiting terminal.</strong></p>
    <p>On Core’s completion page, click <strong>Copy sign-in response</strong>. Paste that response into the original terminal’s waiting prompt and press Enter. Do not paste the authorization link from step 2 back into the terminal.</p>
    <p>The copied response starts with <code>http://localhost:…/callback?code=…</code>. It is text for your assistant to read; do not open it in your browser. Your browser and terminal can be on different machines.</p>
    <p><strong>4. Check your connected account.</strong></p>
    <p>Your assistant saves its credentials on the machine where it runs. Start a new session, then ask: <em>“Who am I in Pocket? Use pocket_whoami.”</em> Once it shows your own Core account, try <em>“Show my open tasks”</em> or <em>“Check the status of all applications I can access.”</em></p>
    <p className="muted small">No helper download or Python is needed. Sign-in stays on Core; there is no browser redirect to localhost. Access lasts up to 30 days and can be revoked below.</p>
    <details className="mt"><summary>Connection help</summary>
      <p className="mt">Use a current {title} version that supports <code>mcp login --no-browser</code>. A Core webpage cannot directly configure a terminal on another machine; your assistant starts sign-in with the commands above.</p>
      <p>If login says <code>pocket</code> is unknown, run the add command first in that same environment. Each person and each assistant installation needs its own configuration. You can choose a different connection name if you use it in both commands.</p>
      {client === 'claude' && <p><code>--scope user</code> saves Claude Code’s connection for your operating-system user across projects. The <code>oauth.scopes</code> values specify the access requested from Core; they do not choose which Core user signs in.</p>}
      <p>If <code>pocket</code> is already configured with the same access, use just the sign-in command. To change Claude Code’s requested access, remove that entry with <code>claude mcp remove --scope user pocket</code>, then copy the new setup commands. Other server entries are unaffected.</p>
      <p>If you previously configured a manual token for this entry, remove that entry first and add it again using this setup. The default name here is <code>pocket</code>; older examples used <code>pocket_core</code> or <code>pocket_account</code>.</p>
      <p>If you closed the approval page before copying, restart sign-in in your terminal. A response expires after five minutes and works only with the terminal that requested it.</p>
    </details>
  </Card>
}
