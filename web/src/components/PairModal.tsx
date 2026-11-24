import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { Device, Pairing } from '../lib/types'
import { Modal, Secret, Switch, copyText, useToast } from './ui'
import { useApp } from '../lib/app-context'
import { Icon } from './icons'

/** Mint a pairing code, show the QR, and wait for the phone to claim it. */
export default function PairModal({ onClose, userId }: { onClose: () => void; userId?: number }) {
  const { toast } = useToast()
  const { user } = useApp()
  // Only the Pocket Core app needs to manage the box; every other device acts as a member would.
  const canOffer = user?.role === 'admin' && !userId
  const [manage, setManage] = useState(false)
  const [p, setP] = useState<Pairing | null>(null)
  const [claimed, setClaimed] = useState<Device | null>(null)
  const [err, setErr] = useState('')
  const [left, setLeft] = useState(0)
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    setP(null)
    api.post<Pairing>(`${V1}/pair`, { ...(userId ? { user_id: userId } : {}), ...(manage ? { can_manage: true } : {}) }).then(setP).catch(e => setErr((e as Error).message))
  }, [userId, manage])
  useEffect(() => {
    if (!p || claimed) return
    const tick = () => setLeft(Math.max(0, Math.round((new Date(p.expires_at).getTime() - Date.now()) / 1000)))
    tick()
    const clock = setInterval(tick, 1000)
    const t = setInterval(async () => {
      try {
        const st = await api.get<{ claimed: boolean; device?: Device }>(`${V1}/pair/${p.code}`)
        if (st.claimed && st.device) { setClaimed(st.device); toast(`${st.device.name} paired`) }
      } catch { /* expired */ }
    }, 2000)
    return () => { clearInterval(t); clearInterval(clock) }
  }, [p, claimed, toast])
  const code = p ? `${p.code.slice(0, 4)}-${p.code.slice(4)}` : ''
  return (
    <Modal title={claimed ? 'Paired!' : 'Pair a device'} onClose={onClose}>
      {err && <div className="callout danger">{err}</div>}
      {!p && !err && <p className="muted">Generating a code…</p>}
      {p && !claimed && (
        <div className="grid" style={{ gap: 14, justifyItems: 'center' }}>
          <div className="qr"><img src={p.qr_svg} alt="Pairing QR code" /></div>
          <div className="pair-code">
            <button type="button" className={`pair-code-chip ${copied ? 'copied' : ''}`} title="Copy the code" aria-label={`Copy the code ${code}`}
              onClick={async () => {
                if (await copyText(code)) { setCopied(true); setTimeout(() => setCopied(false), 1500) } else toast('Could not copy', 'err')
              }}>
              <span className="code-big">{code}</span>
              <span className="pair-code-copy"><Icon name={copied ? 'check' : 'copy'} size={16} /></span>
            </button>
            <span className="muted small">{copied ? 'Copied' : `Expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`}</span>
          </div>
          <p className="muted small" style={{ textAlign: 'center', maxWidth: 380, margin: 0 }}>In <b>Pocket Homelab</b> open <i>Settings → Pair with a box</i> and scan, or type the code. Every service on this box lands in the app, credentials go into the Keychain.</p>
          {canOffer && <label className="pair-option">
            <span className="grow">
              <span className="setting-title">Can manage this box</span>
              <span className="setting-help">Only for the Pocket Core app</span>
            </span>
            <Switch size="sm" on={manage} onChange={setManage} label="Can manage this box" />
          </label>}
          <details className="pair-manual"><summary>Manual import instead</summary>
            <p className="muted small mt-sm">Older client builds import an env file: download it from <b>Services → Download env</b> after signing in on that device, or paste this link into the client:</p>
            <Secret value={p.url} /></details>
        </div>
      )}
      {claimed && <div className="grid" style={{ gap: 10 }}>
        <div className="callout success"><Icon name="check-circle" size={16} /><span><b>{claimed.name}</b> ({claimed.platform || 'device'}) received the profile and its own token.</span></div>
        <p className="muted small">Revoke it any time under Devices.</p>
        <div className="form-actions"><button type="button" className="btn primary" onClick={onClose}>Done</button></div>
      </div>}
    </Modal>
  )
}
