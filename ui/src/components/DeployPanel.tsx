import { useState } from 'react'
import { Tooltip } from './Tooltip.tsx'
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
  clone: 'fa-clone',
  build: 'fa-wrench',
  test: 'fa-bug',
  cleanup: 'fa-broom',
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

const stepGroups: Record<string, string> = {
  'clone': 'build',
  'build': 'build',
  'test': 'build',
  'cleanup': 'build',
  'snapshot': 'data',
  'migrate': 'data',
  'deploy': 'deploy',
  'create-deployment': 'infra',
  'create-service': 'infra',
  'patch-cloudflared': 'network',
  'create-dns-route': 'network',
  'restart-cloudflared': 'network',
  'remove-dns-route': 'network',
  'unpatch-cloudflared': 'network',
  'delete-service': 'infra',
  'delete-deployment': 'infra',
}

function formatOutput(appId: string, title: string, steps: StepLog[], status: string, error?: string): string {
  const lines: string[] = []
  lines.push(`=== ${title} ${appId} ===`)
  lines.push('')

  for (const step of steps) {
    const dur = step.durationMs != null ? `${(step.durationMs / 1000).toFixed(1)}s` : ''
    const padStep = step.step.padEnd(22)
    const padDur = dur.padStart(8)
    lines.push(`[${step.status.padEnd(10)}]  ${padStep}${padDur}`)
    if (step.output) {
      for (const line of step.output.split('\n')) {
        lines.push(`  > ${line}`)
      }
    }
  }

  lines.push('')
  lines.push(`--- Status: ${status} ---`)
  if (error) {
    lines.push(`Error: ${error}`)
  }
  return lines.join('\n')
}

export function DeployPanel({ appId, steps, status, error, title = 'Deploying', onClose }: Props) {
  const isDone = status === 'failed' || status === 'deployed' || status === 'completed'
  const [copied, setCopied] = useState(false)

  const handleCopy = () => {
    const text = formatOutput(appId, title, steps, status, error)
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="deploy-panel">
      <div className="deploy-panel-header">
        <h4>
          <i className="fawsb fa-clipboard-check" /> {title} {appId}
        </h4>
        <div className="deploy-panel-actions">
          <Tooltip text={copied ? 'Copied!' : 'Copy output'}>
            <button className="btn-icon" onClick={handleCopy}>
              <i className={`fawsb ${copied ? 'fa-check' : 'fa-copy'}`} />
            </button>
          </Tooltip>
          {isDone && onClose && (
            <button className="btn-close" onClick={onClose}>&times;</button>
          )}
        </div>
      </div>
      <div className="deploy-steps">
        {steps.map((step, i) => {
          const group = stepGroups[step.step]
          const prevGroup = i > 0 ? stepGroups[steps[i - 1].step] : null
          const isChild = group === prevGroup && i > 0
          const isActive = !isDone && step.status === status
          return (
            <div key={step.step}
                 className={`deploy-step ${step.status}${isChild ? ' step-child' : ''}${isActive ? ' step-active' : ''}`}>
              <i className={`fawsb ${stepIcons[step.step] ?? 'fa-circle'}`} />
              <span className="step-name">{step.step}</span>
              <span className="step-status">{step.status}</span>
              {step.durationMs != null && (
                <span className="step-duration">{(step.durationMs / 1000).toFixed(1)}s</span>
              )}
            </div>
          )
        })}
      </div>
      {status === 'failed' && error && (
        <div className="deploy-error">
          <i className="fawsb fa-circle-exclamation" /> {error}
        </div>
      )}
    </div>
  )
}
