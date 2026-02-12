import { useState, useEffect } from 'react'
import type { FuncExecution } from '../types/index.ts'
import { Tooltip } from './Tooltip.tsx'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface Props {
  appId: string
  onClose: () => void
  onInvoke: (appId: string) => void
}

function timeAgo(iso: string): string {
  if (!iso) return ''
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
}

function statusBadge(status: FuncExecution['status']) {
  const map: Record<string, { cls: string; label: string; icon: string }> = {
    running: { cls: 'cron-status-badge running', label: 'running', icon: 'fa-spinner' },
    succeeded: { cls: 'cron-status-badge succeeded', label: 'ok', icon: 'fa-check' },
    failed: { cls: 'cron-status-badge failed', label: 'failed', icon: 'fa-xmark' },
    timed_out: { cls: 'cron-status-badge timed-out', label: 'timeout', icon: 'fa-clock' },
  }
  const s = map[status] ?? map['failed']
  return (
    <span className={s.cls}>
      <i className={`fawsb ${s.icon}`} /> {s.label}
    </span>
  )
}

export function FunctionPanel({ appId, onClose }: Props) {
  const [executions, setExecutions] = useState<FuncExecution[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const [requestBody, setRequestBody] = useState('')
  const [invoking, setInvoking] = useState(false)

  const fetchHistory = () => {
    fetch(apiUrl(`/api/apps/${appId}/function/history`), fetchOpts)
      .then(r => r.json())
      .then(data => {
        setExecutions(data.executions ?? [])
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }

  useEffect(() => { fetchHistory() }, [appId])

  const handleInvoke = async () => {
    setInvoking(true)
    await fetch(apiUrl(`/api/apps/${appId}/invoke`), {
      ...fetchOpts,
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: requestBody || '{}',
    })
    setInvoking(false)
    // Refresh history after a short delay
    setTimeout(fetchHistory, 1000)
  }

  return (
    <div className="cron-panel">
      <div className="cron-panel-header">
        <h4><i className="fawsb fa-bolt" /> Function: {appId}</h4>
        <button className="btn-close" onClick={onClose}>&times;</button>
      </div>

      <div className="cron-schedule-section">
        <div className="cron-schedule-row">
          <span className="cron-schedule-label">Request Body</span>
          <textarea
            className="func-request-body"
            value={requestBody}
            onChange={e => setRequestBody(e.target.value)}
            placeholder='{"key": "value"}'
            rows={3}
            style={{ width: '100%', fontFamily: 'monospace', fontSize: '12px', padding: '8px', borderRadius: '4px', border: '1px solid var(--border)', background: 'var(--bg-secondary)', color: 'var(--text-primary)', resize: 'vertical' }}
          />
        </div>
      </div>

      <div className="cron-actions">
        <Tooltip text="Invoke the function with the request body">
          <button className="btn btn-primary" onClick={handleInvoke} disabled={invoking}>
            {invoking ? <span className="btn-spinner" /> : <i className="fawsb fa-play" />} Invoke
          </button>
        </Tooltip>
      </div>

      <div className="cron-history">
        <h5>Invocation History</h5>
        {loading && <div className="cron-history-loading">Loading...</div>}
        {!loading && executions.length === 0 && (
          <div className="cron-history-empty">No invocations yet</div>
        )}
        {executions.map(exec => (
          <div key={exec.id} className="cron-execution-row" onClick={() => setExpandedId(expandedId === exec.id ? null : exec.id)}>
            <div className="cron-exec-summary">
              {statusBadge(exec.status)}
              <span className="cron-exec-exit">exit {exec.exitCode}</span>
              <span className="cron-exec-duration">{formatDuration(exec.durationMs)}</span>
              <Tooltip text={new Date(exec.startedAt).toLocaleString()}>
                <span className="cron-exec-time">{timeAgo(exec.startedAt)}</span>
              </Tooltip>
              <span className="cron-exec-expand">
                <i className={`fawsb ${expandedId === exec.id ? 'fa-chevron-up' : 'fa-chevron-down'}`} />
              </span>
            </div>
            {expandedId === exec.id && exec.output && (
              <div className="cron-output">
                <pre>{exec.output}</pre>
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
