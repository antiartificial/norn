import { useState, useEffect } from 'react'
import type { CronExecution, CronState } from '../types/index.ts'
import { Tooltip } from './Tooltip.tsx'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface Props {
  appId: string
  cronState?: CronState
  onClose: () => void
  onTrigger: (appId: string) => void
  onPause: (appId: string) => void
  onResume: (appId: string) => void
  onScheduleUpdate: (appId: string, schedule: string) => void
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

function statusBadge(status: CronExecution['status']) {
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

export function CronPanel({ appId, cronState, onClose, onTrigger, onPause, onResume, onScheduleUpdate }: Props) {
  const [executions, setExecutions] = useState<CronExecution[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const [editingSchedule, setEditingSchedule] = useState(false)
  const [scheduleInput, setScheduleInput] = useState(cronState?.schedule ?? '')

  useEffect(() => {
    fetch(apiUrl(`/api/apps/${appId}/cron/history`), fetchOpts)
      .then(r => r.json())
      .then(data => {
        setExecutions(data.executions ?? [])
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }, [appId])

  const handleScheduleSave = () => {
    if (scheduleInput.trim()) {
      onScheduleUpdate(appId, scheduleInput.trim())
      setEditingSchedule(false)
    }
  }

  return (
    <div className="cron-panel">
      <div className="cron-panel-header">
        <h4><i className="fawsb fa-clock" /> Cron: {appId}</h4>
        <button className="btn-close" onClick={onClose}>&times;</button>
      </div>

      <div className="cron-schedule-section">
        <div className="cron-schedule-row">
          <span className="cron-schedule-label">Schedule</span>
          {editingSchedule ? (
            <div className="cron-schedule-edit">
              <input
                type="text"
                value={scheduleInput}
                onChange={e => setScheduleInput(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && handleScheduleSave()}
                placeholder="*/5 * * * *"
                autoFocus
              />
              <button className="btn" onClick={handleScheduleSave}>
                <i className="fawsb fa-check" />
              </button>
              <button className="btn" onClick={() => setEditingSchedule(false)}>
                <i className="fawsb fa-xmark" />
              </button>
            </div>
          ) : (
            <span className="cron-schedule" onClick={() => { setScheduleInput(cronState?.schedule ?? ''); setEditingSchedule(true) }}>
              <code>{cronState?.schedule || 'not set'}</code>
              <i className="fawsb fa-pen" />
            </span>
          )}
        </div>
        {cronState?.nextRunAt && (
          <div className="cron-schedule-row">
            <span className="cron-schedule-label">Next run</span>
            <Tooltip text={new Date(cronState.nextRunAt).toLocaleString()}>
              <span className="cron-next-run">{timeAgo(cronState.nextRunAt).replace(' ago', '') === '0s' ? 'soon' : new Date(cronState.nextRunAt).toLocaleTimeString()}</span>
            </Tooltip>
          </div>
        )}
      </div>

      <div className="cron-actions">
        <Tooltip text="Run the cron job immediately">
          <button className="btn btn-primary" onClick={() => onTrigger(appId)}>
            <i className="fawsb fa-play" /> Run Now
          </button>
        </Tooltip>
        {cronState?.paused ? (
          <Tooltip text="Resume scheduled runs">
            <button className="btn" onClick={() => onResume(appId)}>
              <i className="fawsb fa-play" /> Resume
            </button>
          </Tooltip>
        ) : (
          <Tooltip text="Pause scheduled runs">
            <button className="btn" onClick={() => onPause(appId)}>
              <i className="fawsb fa-pause" /> Pause
            </button>
          </Tooltip>
        )}
        {cronState?.paused && (
          <span className="cron-paused-badge">
            <i className="fawsb fa-pause" /> paused
          </span>
        )}
      </div>

      <div className="cron-history">
        <h5>Execution History</h5>
        {loading && <div className="cron-history-loading">Loading...</div>}
        {!loading && executions.length === 0 && (
          <div className="cron-history-empty">No executions yet</div>
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
