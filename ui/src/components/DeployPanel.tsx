import type { StepLog } from '../types/index.ts'

interface Props {
  appId: string
  steps: StepLog[]
  status: string
  error?: string
  title?: string
  onClose?: () => void
}

const stepIcons: Record<string, string> = {
  build: 'fa-wrench',
  test: 'fa-bug',
  snapshot: 'fa-camera',
  migrate: 'fa-database',
  deploy: 'fa-rocket-launch',
  'create-deployment': 'fa-box',
  'create-service': 'fa-link',
  'patch-cloudflared': 'fa-cloud',
  'create-dns-route': 'fa-globe',
  'restart-cloudflared': 'fa-arrows-rotate',
  'remove-dns-route': 'fa-globe',
  'unpatch-cloudflared': 'fa-cloud',
  'delete-service': 'fa-link',
  'delete-deployment': 'fa-box',
}

export function DeployPanel({ appId, steps, status, error, title = 'Deploying', onClose }: Props) {
  const isDone = status === 'failed' || status === 'deployed' || status === 'completed'
  return (
    <div className="deploy-panel">
      <div className="deploy-panel-header">
        <h4>
          <i className="fawsb fa-clipboard-check" /> {title} {appId}
        </h4>
        {isDone && onClose && (
          <button className="btn-close" onClick={onClose}>&times;</button>
        )}
      </div>
      <div className="deploy-steps">
        {steps.map((step) => (
          <div key={step.step} className={`deploy-step ${step.status}`}>
            <i className={`fawsb ${stepIcons[step.step] ?? 'fa-circle'}`} />
            <span className="step-name">{step.step}</span>
            <span className="step-status">{step.status}</span>
            {step.durationMs != null && (
              <span className="step-duration">{(step.durationMs / 1000).toFixed(1)}s</span>
            )}
          </div>
        ))}
      </div>
      {status === 'failed' && error && (
        <div className="deploy-error">
          <i className="fawsb fa-circle-exclamation" /> {error}
        </div>
      )}
    </div>
  )
}
