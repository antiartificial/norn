import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface FuncExecution {
  id: string
  app: string
  process: string
  status: string
  exitCode?: number
  startedAt: string
  finishedAt?: string
  durationMs?: number
}

interface Props {
  appId: string
  processes: string[]
  onClose: () => void
}

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  return `${Math.floor(hr / 24)}d ago`
}

function statusClass(status: string): string {
  switch (status) {
    case 'complete': return 'deployed'
    case 'running': return 'running'
    default: return 'failed'
  }
}

export function FunctionPanel({ appId, processes, onClose }: Props) {
  const [selectedProcess, setSelectedProcess] = useState(processes[0] ?? '')
  const [body, setBody] = useState('')
  const [invoking, setInvoking] = useState(false)
  const [history, setHistory] = useState<FuncExecution[]>([])
  const [loading, setLoading] = useState(true)
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  const loadHistory = async () => {
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/function/history`), fetchOpts)
      if (res.ok) setHistory(await res.json())
    } catch { /* */ }
    setLoading(false)
  }

  useEffect(() => { loadHistory() }, [appId])

  const handleInvoke = async () => {
    setInvoking(true)
    setMessage(null)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/invoke`), {
        ...fetchOpts,
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ process: selectedProcess, body }),
      })
      if (res.ok) {
        const data = await res.json()
        setMessage({ type: 'success', text: `Invoked ${selectedProcess} — ID: ${data.id.slice(0, 8)}` })
        // Poll for updated history
        setTimeout(loadHistory, 2000)
        setTimeout(loadHistory, 5000)
        setTimeout(loadHistory, 10000)
      } else {
        const data = await res.json().catch(() => ({ error: 'Unknown error' }))
        setMessage({ type: 'error', text: data.error || 'Invocation failed' })
      }
    } catch (e) {
      setMessage({ type: 'error', text: `Invoke failed: ${e}` })
    }
    setInvoking(false)
  }

  return (
    <div className="panel-overlay">
      <div className="panel-card panel-wide">
        <div className="panel-header">
          <h4><i className="fawsb fa-bolt" /> Functions — {appId}</h4>
          <button className="btn-close" onClick={onClose}><i className="fawsb fa-xmark" /></button>
        </div>

        {message && (
          <div className={`panel-message ${message.type}`}>{message.text}</div>
        )}

        <div className="func-invoke-form">
          {processes.length > 1 && (
            <div className="func-field">
              <label>Process</label>
              <select
                value={selectedProcess}
                onChange={e => setSelectedProcess(e.target.value)}
                className="scale-select"
              >
                {processes.map(p => <option key={p} value={p}>{p}</option>)}
              </select>
            </div>
          )}
          {processes.length === 1 && (
            <div className="func-field-inline">
              <span className="process-badge">{selectedProcess}</span>
            </div>
          )}
          <div className="func-field">
            <label>Request Body (JSON)</label>
            <textarea
              value={body}
              onChange={e => setBody(e.target.value)}
              className="func-textarea"
              placeholder='{"key": "value"}'
              rows={3}
            />
          </div>
          <button
            className="btn btn-primary"
            disabled={invoking || !selectedProcess}
            onClick={handleInvoke}
          >
            {invoking ? <span className="btn-spinner" /> : <i className="fawsb fa-bolt" />}
            Invoke
          </button>
        </div>

        <div className="func-history">
          <h5>Execution History</h5>
          {loading && <div className="panel-loading"><div className="loading-spinner" /></div>}
          {!loading && history.length === 0 && (
            <div className="panel-empty">No executions yet</div>
          )}
          {!loading && history.length > 0 && (
            <div className="panel-list">
              <div className="panel-list-header">
                <span className="func-h-status">Status</span>
                <span className="func-h-process">Process</span>
                <span className="func-h-exit">Exit</span>
                <span className="func-h-duration">Duration</span>
                <span className="func-h-time">When</span>
              </div>
              {history.map(exec => (
                <div key={exec.id} className="panel-list-row">
                  <span className={`history-status ${statusClass(exec.status)}`}>{exec.status}</span>
                  <span className="func-h-process">{exec.process}</span>
                  <span className="func-h-exit">{exec.exitCode !== undefined ? exec.exitCode : '—'}</span>
                  <span className="func-h-duration">{exec.durationMs ? `${exec.durationMs}ms` : '...'}</span>
                  <span className="func-h-time">{relativeTime(exec.startedAt)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
