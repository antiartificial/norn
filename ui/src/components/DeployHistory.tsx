import { useState } from 'react'
import { useDeployments } from '../hooks/useDeployments.ts'
import type { Deployment, StepLog } from '../types/index.ts'

interface Props {
  apps: string[]
  onClose: () => void
}

const statuses = ['', 'queued', 'building', 'testing', 'deploying', 'deployed', 'failed', 'rolled_back']

function statusLabel(s: string): string {
  if (!s) return 'All statuses'
  return s.replace('_', ' ')
}

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const d = Math.floor(hr / 24)
  return `${d}d ago`
}

function duration(started: string, finished?: string): string {
  if (!finished) return '...'
  const ms = new Date(finished).getTime() - new Date(started).getTime()
  if (ms < 1000) return `${ms}ms`
  const sec = Math.floor(ms / 1000)
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60)
  const s = sec % 60
  return `${min}m ${s}s`
}

function statusClass(status: string): string {
  switch (status) {
    case 'deployed': return 'deployed'
    case 'failed':
    case 'rolled_back': return 'failed'
    case 'queued': return 'queued'
    default: return 'running'
  }
}

function StepRow({ step }: { step: StepLog }) {
  const isDone = step.status === 'deployed' || step.status === 'done'
  const isFailed = step.status === 'failed'
  return (
    <div className={`deploy-step ${isDone ? 'step-done' : ''} ${isFailed ? 'failed' : ''} step-child`}>
      <span className="step-name">{step.step}</span>
      <span className="step-status">{step.status}</span>
      {step.durationMs ? (
        <span className="step-duration">{step.durationMs < 1000 ? `${step.durationMs}ms` : `${(step.durationMs / 1000).toFixed(1)}s`}</span>
      ) : null}
    </div>
  )
}

function DeployRow({ deploy }: { deploy: Deployment }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <div className="history-row-wrapper">
      <div className={`history-row`} onClick={() => setExpanded(!expanded)}>
        <span className={`history-status ${statusClass(deploy.status)}`}>
          {deploy.status.replace('_', ' ')}
        </span>
        <span className="history-app">{deploy.app}</span>
        <span className="history-sha">{deploy.commitSha.slice(0, 7)}</span>
        <span className="history-tag">{deploy.imageTag}</span>
        <span className="history-duration">{duration(deploy.startedAt, deploy.finishedAt)}</span>
        <span className="history-time">{relativeTime(deploy.startedAt)}</span>
        <span className="history-expand">{expanded ? '\u25B4' : '\u25BE'}</span>
      </div>
      {expanded && (
        <div className="history-detail">
          {deploy.steps && deploy.steps.length > 0 ? (
            <div className="deploy-steps">
              {deploy.steps.map((s, i) => <StepRow key={i} step={s} />)}
            </div>
          ) : (
            <div className="history-no-steps">No step details</div>
          )}
          {deploy.error && <div className="deploy-error">{deploy.error}</div>}
        </div>
      )}
    </div>
  )
}

export function DeployHistory({ apps, onClose }: Props) {
  const { deployments, total, loading, filters, setApp, setStatus, nextPage, prevPage } = useDeployments()

  const page = Math.floor(filters.offset / filters.limit) + 1
  const totalPages = Math.ceil(total / filters.limit)

  return (
    <div className="history-panel">
      <div className="history-panel-header">
        <h4>Deploy History</h4>
        <span className="history-total">{total} deployment{total !== 1 ? 's' : ''}</span>
        <button className="btn-close" onClick={onClose}>&times;</button>
      </div>

      <div className="history-filters">
        <select
          className="history-select"
          value={filters.app}
          onChange={e => setApp(e.target.value)}
        >
          <option value="">All apps</option>
          {apps.map(a => <option key={a} value={a}>{a}</option>)}
        </select>

        <select
          className="history-select"
          value={filters.status}
          onChange={e => setStatus(e.target.value)}
        >
          {statuses.map(s => (
            <option key={s} value={s}>{statusLabel(s)}</option>
          ))}
        </select>
      </div>

      {loading && (
        <div className="history-loading">
          <div className="loading-spinner" />
        </div>
      )}

      {!loading && deployments.length === 0 && (
        <div className="history-empty">No deployments found</div>
      )}

      {!loading && deployments.length > 0 && (
        <>
          <div className="history-list">
            {deployments.map(d => <DeployRow key={d.id} deploy={d} />)}
          </div>

          {totalPages > 1 && (
            <div className="history-pagination">
              <button className="btn" disabled={page <= 1} onClick={prevPage}>Prev</button>
              <span className="history-page">{page} / {totalPages}</span>
              <button className="btn" disabled={page >= totalPages} onClick={nextPage}>Next</button>
            </div>
          )}
        </>
      )}
    </div>
  )
}
