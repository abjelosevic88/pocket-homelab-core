import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { Device, Pairing } from '../lib/types'
import { Modal, Secret, useToast } from './ui'
import { Icon } from './icons'

/** Mint a pairing code, show the QR, and wait for the phone to claim it. */
export default function PairModal({ onClose, userId }: { onClose: () => void; userId?: number }) {
  const { toast } = useToast()
  const [p, setP] = useState<Pairing | null>(null)
  const [claimed, setClaimed] = useState<Device | null>(null)
  const [err, setErr] = useState('')
  const [left, setLeft] = useState(0)
  useEffect(() => {
    api.post<Pairing>(`${V1}/pair`, userId ? { user_id: userId } : {}).then(setP).catch(e => setErr((e as Error).message))
  }, [userId])
  useEffect(() => {
    if (!p || claimed) return
    const t = setInterval(async () => {
      setLeft(Math.max(0, Math.round((new Date(p.expires_at).getTime() - Date.now()) / 1000)))
      try {
        const st = await api.get<{ claimed: boolean; device?: Device }>(`${V1}/pair/${p.code}`)
        if (st.claimed && st.device) { setClaimed(st.device); toast(`${st.device.name} paired`) }
      } catch { /* expired */ }
    }, 2000)
    return () => clearInterval(t)
  }, [p, claimed, toast])
  const code = p ? `${p.code.slice(0, 4)}-${p.code.slice(4)}` : ''
  return (
    <Modal title={claimed ? 'Paired!' : 'Pair a device'} onClose={onClose}>
      {err && <div className="callout danger">{err}</div>}
      {!p && !err && <p className="muted">Generating a code…</p>}
      {p && !claimed && (
        <div className="grid" style={{ gap: 14, justifyItems: 'center' }}>
          <div className="qr"><img src={p.qr_svg} alt="Pairing QR code" /></div>
          <div className="code-big">{code}</div>
          <p className="muted small" style={{ textAlign: 'center', maxWidth: 380 }}>In <b>Pocket Homelab</b> open <i>Settings → Pair with a box</i> and scan, or type the code. Every service on this box lands in the app, credentials go into the Keychain. Code expires in {left}s.</p>
          <details style={{ width: '100%' }}><summary className="muted small">Manual import instead</summary>
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
