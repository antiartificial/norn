import { useEffect, useState } from 'react'
import { Button, Modal } from './ui/index.ts'

export interface CreateAppInput { name: string; kind: 'endpoint' | 'worker'; port?: number }

export function CreateAppModal({ open, busy, error, onClose, onCreate }: {
  open: boolean
  busy: boolean
  error: string | null
  onClose: () => void
  onCreate: (input: CreateAppInput) => void
}) {
  const [name, setName] = useState('')
  const [kind, setKind] = useState<CreateAppInput['kind']>('endpoint')
  const [port, setPort] = useState(8080)
  useEffect(() => {
    if (!open) { setName(''); setKind('endpoint'); setPort(8080) }
  }, [open])
  const validName = /^[a-z0-9][a-z0-9-]*$/.test(name)
  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    if (validName) onCreate({ name, kind, ...(kind === 'endpoint' ? { port } : {}) })
  }
  return (
    <Modal
      open={open}
      title="Create app"
      onClose={onClose}
      footer={<><Button type="button" onClick={onClose}>Cancel</Button><Button variant="primary" type="submit" form="create-app-form" loading={busy}>Create draft</Button></>}
    >
      <form id="create-app-form" className="create-app-form" onSubmit={submit}>
        <p className="create-app-intro">Start with a safe InfraSpec. Deployment is off until you explicitly enable it after adding source and build settings.</p>
        <label htmlFor="create-app-name">Name</label>
        <input id="create-app-name" autoFocus autoComplete="off" required pattern="[a-z0-9][a-z0-9-]*" value={name} onChange={(event) => setName(event.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))} placeholder="orders-api" />
        <span className="field-hint">Lowercase letters, numbers, and hyphens.</span>
        <fieldset>
          <legend>Template</legend>
          <label><input type="radio" name="kind" checked={kind === 'endpoint'} onChange={() => setKind('endpoint')} /> Endpoint</label>
          <label><input type="radio" name="kind" checked={kind === 'worker'} onChange={() => setKind('worker')} /> Worker</label>
        </fieldset>
        {kind === 'endpoint' && <><label htmlFor="create-app-port">Container port</label><input id="create-app-port" type="number" min="1" max="65535" required value={port} onChange={(event) => setPort(Number(event.target.value))} /></>}
        <div className="create-app-safety" role="note"><strong>Deployment disabled</strong><span>The draft will appear in Apps but cannot be deployed or recovered yet.</span></div>
        {error && <p className="form-error" role="alert">{error}</p>}
      </form>
    </Modal>
  )
}
