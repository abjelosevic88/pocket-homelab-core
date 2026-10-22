import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../lib/api'
import { useApp } from '../lib/app-context'
import { Card, Field, PageHeader } from '../components/ui'

type Pairing = { name: string; scope: string; expires_in_days: number; expires_at: string }

export default function AIPairing() {
  const { user } = useApp()
  const [code, setCode] = useState('')
  const [pairing, setPairing] = useState<Pairing | null>(null)
  const [completion, setCompletion] = useState('')
  const [denied, setDenied] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const inspect = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setError('')
    try { setPairing(await api.post<Pairing>('/mcp/pair/inspect', { user_code: code })) }
    catch (e) { setError((e as Error).message) }
    finally { setBusy(false) }
  }
  const decide = async (approve: boolean) => {
    setBusy(true); setError('')
    try {
      const result = await api.post<{ completion_code: string }>(`/mcp/pair/${approve ? 'approve' : 'deny'}`, { user_code: code })
      if (approve) setCompletion(result.completion_code)
      else setDenied(true)
    } catch (e) { setError((e as Error).message) }
    finally { setBusy(false) }
  }
  const copy = async () => {
    try { await navigator.clipboard.writeText(completion); setCopied(true) }
    catch { setError('Select and copy the completion code below.') }
  }
  // Client-side navigation can arrive from a document without this route's
  // frame-ancestors header. Never render approval controls inside a frame.
  if (window.self !== window.top) return <p>Open AI connections in a separate browser tab to approve this connection.</p>
  return <>
    <PageHeader title="Connect your terminal" sub="Approve your assistant here, then finish in the terminal where you started." />
    {error && <div className="callout danger mb" role="alert">{error}</div>}
    <Card title={completion ? 'Return to your terminal' : denied ? 'Connection declined' : pairing ? 'Review this connection' : 'Enter your pairing code'}>
      {completion ? <>
        <p role="status">Access approved. Paste this completion code into the terminal that showed your pairing code. The terminal will finish connecting your assistant.</p>
        <Field label="Completion code"><input readOnly value={completion} autoComplete="off" onFocus={e => e.target.select()} /></Field>
        <button className="btn primary mt" onClick={copy}>{copied ? 'Copied' : 'Copy completion code'}</button>
        <p>This code works once, only with your original terminal request. Finish before {new Date(pairing!.expires_at).toLocaleTimeString()}. If you close this page before copying, start again in your terminal.</p>
      </> : denied ? <p>No access was granted. You can close this page.</p> : pairing ? <>
        <p><strong>{pairing.name}</strong> is requesting access as <strong>{user?.email}</strong>.</p>
        <p>Pairing code: <strong>{code.toUpperCase()}</strong>. Confirm this matches the code in the terminal you started.</p>
        <p><strong>{pairing.scope.includes('apps:write') ? 'Read and create/update items' : pairing.scope.includes('apps:read') ? 'Read application content' : 'Status checks only'}</strong> in your permitted apps. Deletion and service administration are unavailable.</p>
        <p>Access lasts {pairing.expires_in_days} {pairing.expires_in_days === 1 ? 'day' : 'days'} and follows your account permissions, including apps shared with you later. You can disconnect it at any time from AI connections.</p>
        <p>Only approve a request you started yourself.</p>
        <div className="row" style={{ flexWrap: 'wrap', gap: 12 }}>
          <button className="btn primary" disabled={busy} onClick={() => decide(true)}>{busy ? 'Please wait…' : 'Approve connection'}</button>
          <button className="btn" disabled={busy} onClick={() => decide(false)}>Deny</button>
          <button className="btn ghost" disabled={busy} onClick={() => { setPairing(null); setError('') }}>Use a different code</button>
        </div>
      </> : <form className="stack" onSubmit={inspect}>
        <p>Enter the code printed by Pocket Connect in your terminal. You can use this page on a different computer or phone.</p>
        <Field label="Pairing code"><input required value={code} onChange={e => setCode(e.target.value.toUpperCase())} maxLength={11} placeholder="ABCDE-23456" autoComplete="off" autoCapitalize="characters" spellCheck={false} /></Field>
        <button className="btn primary" disabled={busy}>{busy ? 'Checking…' : 'Review connection'}</button>
      </form>}
    </Card>
    <p><Link to="/ai-connections">Back to AI connections</Link></p>
  </>
}
