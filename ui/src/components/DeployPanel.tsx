import type { StepLog } from '../types/index.ts'

interface Props {
  appId: string
  steps: StepLog[]
  status: string
  error?: string
}

const stepIcons: Record<string, string> = {
  build: 'fa-hammer',
  test: 'fa-vial',
  snapshot: 'fa-camera',
  migrate: 'fa-database',
  deploy: 'fa-rocket',
}

export function DeployPanel({ appId, steps, status, error }: Props) {
  return (
    <div className="deploy-panel">
      <h4>
        <i className="fa-solid fa-list-check" /> Deploying {appId}
      </h4>
      <div className="deploy-steps">
        {steps.map((step) => (
          <div key={step.step} className={`deploy-step ${step.status}`}>
            <i className={`fa-solid ${stepIcons[step.step] ?? 'fa-circle'}`} />
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
          <i className="fa-solid fa-triangle-exclamation" /> {error}
        </div>
      )}
    </div>
  )
}
