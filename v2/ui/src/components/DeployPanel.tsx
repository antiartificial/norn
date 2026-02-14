import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import type { StepEvent } from '../App.tsx'
import type { SagaEvent } from '../types/index.ts'

interface DeployStep {
  step: string
  status: string
  events?: StepEvent[]
}

interface Props {
  appId: string
  steps: DeployStep[]
  status: string
  error?: string
  sagaId?: string
  onClose: () => void
  onRetry: () => void
}

const KNOWN_STEPS = ['clone', 'build', 'test', 'snapshot', 'migrate', 'submit', 'healthy', 'cleanup']

const stepIcons: Record<string, string> = {
  clone: 'fa-clone',
  build: 'fa-wrench',
  test: 'fa-bug',
  snapshot: 'fa-camera',
  migrate: 'fa-database',
  submit: 'fa-rocket-launch',
  healthy: 'fa-heart-pulse',
  cleanup: 'fa-broom',
}

function formatElapsed(ms: number): string {
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  const rem = Math.floor(s % 60)
  return `${m}m ${rem}s`
}

function formatEventTime(ts: number | string): string {
  const d = new Date(ts)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function DeployPanel({ appId, steps, status, error, sagaId, onClose, onRetry }: Props) {
  const isDone = status === 'deployed' || status === 'failed'
  const isSuccess = status === 'deployed'
  const [startedAt] = useState(() => Date.now())
  const [now, setNow] = useState(Date.now())
  const [expanded, setExpanded] = useState<string | null>(null)
  const [sagaEvents, setSagaEvents] = useState<SagaEvent[] | null>(null)
  const [sagaLoading, setSagaLoading] = useState(false)

  useEffect(() => {
    if (isDone) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [isDone])

  const elapsed = now - startedAt

  // Fetch saga events when expanding a step on a completed deploy
  useEffect(() => {
    if (!expanded || !isDone || !sagaId) return
    if (sagaEvents !== null) return // already fetched
    setSagaLoading(true)
    fetch(apiUrl(`/api/saga/${sagaId}`), fetchOpts)
      .then(r => r.json())
      .then((events: SagaEvent[]) => {
        setSagaEvents(events)
        setSagaLoading(false)
      })
      .catch(() => setSagaLoading(false))
  }, [expanded, isDone, sagaId, sagaEvents])

  const stepMap = new Map(steps.map(s => [s.step, s]))
  const currentIdx = KNOWN_STEPS.findIndex(s => !stepMap.has(s)) - 1

  function getEventsForStep(stepName: string): StepEvent[] {
    // During live deploy: use WS events from step
    const step = stepMap.get(stepName)
    if (!isDone && step?.events?.length) {
      return step.events
    }
    // After deploy: use saga events filtered by step
    if (isDone && sagaEvents) {
      return sagaEvents
        .filter(e =>
          e.metadata?.step === stepName &&
          e.action !== 'step.start' &&
          e.action !== 'step.complete' &&
          e.action !== 'step.failed'
        )
        .map(e => ({
          message: e.message,
          timestamp: new Date(e.timestamp).getTime(),
          allocId: e.metadata?.allocId,
          node: e.metadata?.node,
          allocStatus: e.metadata?.allocStatus,
        }))
    }
    // Live events even while deploy is still running
    if (step?.events?.length) {
      return step.events
    }
    return []
  }

  function toggleExpand(stepName: string) {
    setExpanded(prev => prev === stepName ? null : stepName)
  }

  return (
    <div className={`deploy-panel${isSuccess ? ' deploy-success' : ''}`}>
      <div className="deploy-panel-header">
        <h4>
          <i className={`fawsb ${isSuccess ? 'fa-circle-check' : 'fa-clipboard-check'}`} />{' '}
          {isSuccess ? 'Deployed' : 'Deploying'} {appId}
        </h4>
        <div className="deploy-panel-actions">
          <span className={`deploy-total-time${!isDone ? ' step-duration-live' : ''}`}>
            {formatElapsed(elapsed)}
          </span>
          {isDone && (
            <button className="btn-close" onClick={onClose}>&times;</button>
          )}
        </div>
      </div>
      <div className="deploy-steps">
        {KNOWN_STEPS.map((stepName, i) => {
          const stepStatus = stepMap.get(stepName)?.status
          const isActive = !isDone && stepStatus === 'running'
          const isComplete = stepStatus === 'complete' || stepStatus === 'done'
          const isFailed = stepStatus === 'failed'
          const isPending = !stepStatus && i > currentIdx
          const isExpanded = expanded === stepName
          const events = isExpanded ? getEventsForStep(stepName) : []
          const hasEvents = (stepMap.get(stepName)?.events?.length ?? 0) > 0

          let icon = stepIcons[stepName] ?? 'fa-circle'
          let className = 'deploy-step'
          if (isComplete) {
            icon = 'fa-circle-check'
            className += ' step-done'
          } else if (isFailed) {
            icon = 'fa-circle-xmark'
            className += ' failed'
          } else if (isActive) {
            className += ' step-active'
          } else if (isPending) {
            className += ' step-pending'
          }
          if (isExpanded) {
            className += ' step-expanded'
          }

          return (
            <div key={stepName}>
              <div
                className={className}
                onClick={() => !isPending && toggleExpand(stepName)}
                style={{ cursor: isPending ? 'default' : 'pointer' }}
              >
                {isActive ? (
                  <span className="btn-spinner" />
                ) : (
                  <i className={`fawsb ${icon}`} />
                )}
                <span className="step-name">{stepName}</span>
                <span className="step-status">
                  {isComplete ? 'done' : isFailed ? 'failed' : isActive ? 'running' : ''}
                </span>
                {!isPending && (hasEvents || isComplete || isFailed || isActive) && (
                  <span className="step-expand">
                    <i className={`fawsb fa-chevron-${isExpanded ? 'up' : 'down'}`} />
                  </span>
                )}
              </div>
              {isExpanded && (
                <div className="step-events">
                  {sagaLoading && isDone && (
                    <div className="step-event-empty">Loading...</div>
                  )}
                  {events.length > 0 ? (
                    events.map((evt, j) => (
                      <div key={j} className="step-event">
                        <span className="step-event-dot" />
                        <span className="step-event-time">{formatEventTime(evt.timestamp)}</span>
                        <span className="step-event-msg">{evt.message}</span>
                      </div>
                    ))
                  ) : (
                    !sagaLoading && <div className="step-event-empty">No events</div>
                  )}
                </div>
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
      {status === 'failed' && (
        <div className="deploy-panel-footer">
          <button className="btn btn-primary" onClick={onRetry}>
            <i className="fawsb fa-arrows-rotate" /> Retry
          </button>
          <button className="btn" onClick={onClose}>Close</button>
        </div>
      )}
    </div>
  )
}
