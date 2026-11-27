// Pocket UI kit — synced from pocket-homelab-core/sdk/ui by scripts/sync-sdk.py. Change it there, not here.
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Select } from './Select'

interface ModelCatalog {
  models: { id: string; name: string; description?: string }[]
  default_model: string
  default_name: string
}

/** Personal to this app. Empty follows the current model in Pocket AI. */
export function PocketModelSelect({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    let active = true
    setLoading(true); setError('')
    api.get<ModelCatalog>(`${V1}/ai/models`)
      .then(data => { if (active) setCatalog(data) })
      .catch(e => { if (active) setError((e as Error).message.replace(/^HTTP \d+:\s*/, '')) })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [revision])
  const models = catalog?.models || []
  const missing = !!value && !!catalog && !models.some(m => m.id === value)
  const options = [
    { value: '', label: `Use Pocket AI default${catalog ? ` · ${catalog.default_name}` : ''}` },
    ...models.map(m => ({ value: m.id, label: m.name, hint: m.description })),
    ...(value && !models.some(m => m.id === value) ? [{ value, label: `${value}${missing ? ' (unavailable)' : ' (saved)'}` }] : []),
  ]
  return (
    <div className="field">
      <span className="label">Pocket AI model</span>
      <Select ariaLabel="Pocket AI model" value={value} onChange={onChange} options={options} disabled={loading} />
      <span className="help">Used when Pocket AI handles text generation. This choice applies only to your account in this app. The default follows your model in Pocket AI settings.</span>
      {loading && <span className="help" role="status">Loading models from Pocket AI…</span>}
      {missing && <span className="help" role="status">Your saved model is no longer listed. Choose another model or use the Pocket AI default.</span>}
      {error && <div className="row wrap"><span className="help" role="status">{error}</span><button type="button" className="btn sm" onClick={() => setRevision(v => v + 1)}>Retry models</button></div>}
    </div>
  )
}
