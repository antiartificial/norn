import { useState, useEffect, useRef } from 'react'
import { Tooltip } from './Tooltip.tsx'
import type { StepLog } from '../types/index.ts'

interface Props {
  appId: string
  steps: StepLog[]
  status: string
  error?: string
  title?: string
  onClose?: () => void
  onRetry?: () => void
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

function formatElapsed(ms: number): string {
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  const rem = Math.floor(s % 60)
  return `${m}m ${rem}s`
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

function lastNLines(text: string, n: number): string {
  const lines = text.split('\n').filter(l => l.trim() !== '')
  return lines.slice(-n).join('\n')
}

const STUCK_THRESHOLD_MS = 120_000 // 2 minutes

export function DeployPanel({ appId, steps, status, error, title = 'Deploying', onClose, onRetry }: Props) {
  const isDone = status === 'failed' || status === 'deployed' || status === 'completed'
  const isSuccess = isDone && status !== 'failed'
  const [copied, setCopied] = useState(false)
  const [now, setNow] = useState(Date.now())
  const [expandedSteps, setExpandedSteps] = useState<Set<string>>(new Set())
  const prevStepsRef = useRef<StepLog[]>(steps)

  useEffect(() => {
    if (isDone) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [isDone])

  // Auto-expand active step, auto-collapse when step completes, keep failed expanded
  useEffect(() => {
    const prev = prevStepsRef.current
    prevStepsRef.current = steps

    setExpandedSteps(cur => {
      const next = new Set(cur)

      for (const step of steps) {
        const prevStep = prev.find(s => s.step === step.step)

        // Failed step: always expand
        if (step.status === 'failed') {
          next.add(step.step)
          continue
        }

        // Step just got output (transitioned from active to done): auto-collapse
        if (prevStep && !prevStep.output && step.output && step.status !== 'failed') {
          next.delete(step.step)
          continue
        }
      }

      // Auto-expand current active step (last step when not done)
      if (!isDone && steps.length > 0) {
        const activeStepName = steps[steps.length - 1].step
        // Only auto-expand if it doesn't have output yet (still running)
        const activeStepData = steps[steps.length - 1]
        if (!activeStepData.output) {
          next.add(activeStepName)
        }
      }

      return next
    })
  }, [steps, isDone])

  const toggleStep = (stepName: string) => {
    setExpandedSteps(cur => {
      const next = new Set(cur)
      if (next.has(stepName)) {
        next.delete(stepName)
      } else {
        next.add(stepName)
      }
      return next
    })
  }

  // Total elapsed: first step start → now (or final duration sum when done)
  const totalElapsed = steps.length > 0 && steps[0].startedAt != null
    ? (isDone
        ? steps.reduce((sum, s) => sum + (s.durationMs ?? 0), 0)
          || (now - steps[0].startedAt)
        : now - steps[0].startedAt)
    : null

  // Detect stuck: active step running > threshold
  const activeStep = !isDone && steps.length > 0 ? steps[steps.length - 1] : null
  const activeElapsed = activeStep?.startedAt != null ? now - activeStep.startedAt : 0
  const isStuck = activeElapsed > STUCK_THRESHOLD_MS

  const handleCopy = () => {
    const text = formatOutput(appId, title, steps, status, error)
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className={`deploy-panel${isSuccess ? ' deploy-success' : ''}`}>
      <div className="deploy-panel-header">
        <h4>
          <i className={`fawsb ${isSuccess ? 'fa-circle-check' : 'fa-clipboard-check'}`} />{' '}
          {isSuccess ? `${title === 'Deploying' ? 'Deployed' : title.replace(/ing$/, 'ed')}` : title} {appId}
        </h4>
        <div className="deploy-panel-actions">
          {totalElapsed != null && (
            <span className={`deploy-total-time${!isDone ? ' step-duration-live' : ''}`}>
              {formatElapsed(totalElapsed)}
            </span>
          )}
          {isStuck && onRetry && (
            <Tooltip text="Step appears stuck — cancel and retry">
              <button className="btn btn-danger deploy-retry" onClick={onRetry}>
                <i className="fawsb fa-arrows-rotate" /> Retry
              </button>
            </Tooltip>
          )}
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
          const isLast = i === steps.length - 1
          const isActive = !isDone && isLast
          const stepDone = isSuccess || (!isLast && !isDone)
          const isFailed = step.status === 'failed'
          const elapsed = step.durationMs != null
            ? step.durationMs
            : step.startedAt != null
              ? (steps[i + 1]?.startedAt ?? now) - step.startedAt
              : null

          const isExpanded = expandedSteps.has(step.step)
          const hasOutput = !!step.output

          return (
            <div key={step.step} className="deploy-step-wrapper">
              <div
                className={`deploy-step ${step.status}${isChild ? ' step-child' : ''}${isActive ? ' step-active' : ''}${stepDone ? ' step-done' : ''}${hasOutput ? ' step-expandable' : ''}`}
                onClick={hasOutput ? () => toggleStep(step.step) : undefined}
              >
                {hasOutput && (
                  <i className={`fawsb step-chevron ${isExpanded ? 'fa-chevron-down' : 'fa-chevron-right'}`} />
                )}
                <i className={`fawsb ${stepDone ? 'fa-circle-check' : stepIcons[step.step] ?? 'fa-circle'}`} />
                <span className="step-name">{step.step}</span>
                <span className="step-status">{stepDone ? 'done' : step.status}</span>
                {elapsed != null && (
                  <span className={`step-duration${isActive ? ' step-duration-live' : ''}`}>{formatElapsed(elapsed)}</span>
                )}
              </div>
              {isExpanded && hasOutput && (
                <pre className={`step-output${isFailed ? ' step-output-failed' : ''}`}>
                  {lastNLines(step.output!, 5)}
                </pre>
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
