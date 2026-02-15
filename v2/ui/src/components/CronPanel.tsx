import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface CronRun {
  jobId: string
  status: string
  startedAt: string
}

interface CronEntry {
  process: string
  schedule: string
  paused: boolean
  runs: CronRun[]
}

interface Props {
  appId: string
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
    case 'running': return 'running'
    case 'dead': return 'deployed'
    default: return 'failed'
  }
}

export function CronPanel({ appId, onClose }: Props) {
  const [entries, setEntries] = useState<CronEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<string | null>(null)
  const [editing, setEditing] = useState<{ process: string; schedule: string } | null>(null)
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  const load = async () => {
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/cron/history`), fetchOpts)
      if (res.ok) setEntries(await res.json())
    } catch { /* */ }
    setLoading(false)
  }

  useEffect(() => { load() }, [appId])

  const action = async (endpoint: string, process: string, body?: object) => {
    setBusy(process)
    setMessage(null)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/cron/${endpoint}`), {
        ...fetchOpts,
        method: endpoint === 'schedule' ? 'PUT' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ process, ...body }),
      })
      if (res.ok) {
        setMessage({ type: 'success', text: `${endpoint} succeeded for ${process}` })
        await load()
      } else {
        const data = await res.json().catch(() => ({ error: 'Unknown error' }))
        setMessage({ type: 'error', text: data.error || `${endpoint} failed` })
      }
    } catch (e) {
      setMessage({ type: 'error', text: `${endpoint} failed: ${e}` })
    }
    setBusy(null)
  }

  const handleScheduleSave = async () => {
    if (!editing) return
    await action('schedule', editing.process, { schedule: editing.schedule })
    setEditing(null)
  }

  return (
    <div className="panel-overlay">
      <div className="panel-card panel-wide">
        <div className="panel-header">
          <h4><i className="fawsb fa-clock" /> Cron Jobs — {appId}</h4>
          <button className="btn-close" onClick={onClose}><i className="fawsb fa-xmark" /></button>
        </div>

        {message && (
          <div className={`panel-message ${message.type}`}>{message.text}</div>
        )}

        {loading && <div className="panel-loading"><div className="loading-spinner" /></div>}

        {!loading && entries.length === 0 && (
          <div className="panel-empty">No cron jobs found for this app</div>
        )}

        {!loading && entries.map(entry => (
          <div key={entry.process} className="cron-entry">
            <div className="cron-entry-header">
              <div className="cron-entry-title">
                <span className={`health-dot ${entry.paused ? 'red' : 'green'}`} />
                <span className="cron-process">{entry.process}</span>
                <span className="cron-schedule">{entry.schedule}</span>
                {entry.paused && <span className="cron-paused-badge">paused</span>}
              </div>
              <div className="cron-entry-actions">
                <button
                  className="btn btn-small"
                  disabled={busy !== null}
                  onClick={() => action('trigger', entry.process)}
                  title="Trigger now"
                >
                  {busy === entry.process ? <span className="btn-spinner" /> : <i className="fawsb fa-play" />}
                  Trigger
                </button>
                {entry.paused ? (
                  <button
                    className="btn btn-small"
                    disabled={busy !== null}
                    onClick={() => action('resume', entry.process)}
                  >
                    <i className="fawsb fa-circle-play" /> Resume
                  </button>
                ) : (
                  <button
                    className="btn btn-small btn-danger"
                    disabled={busy !== null}
                    onClick={() => action('pause', entry.process)}
                  >
                    <i className="fawsb fa-circle-pause" /> Pause
                  </button>
                )}
                <button
                  className="btn btn-small"
                  onClick={() => setEditing(
                    editing?.process === entry.process
                      ? null
                      : { process: entry.process, schedule: entry.schedule }
                  )}
                >
                  <i className="fawsb fa-pen" />
                </button>
              </div>
            </div>

            {editing?.process === entry.process && (
              <div className="cron-schedule-edit">
                <input
                  type="text"
                  value={editing.schedule}
                  onChange={e => setEditing({ ...editing, schedule: e.target.value })}
                  className="scale-input"
                  placeholder="*/5 * * * *"
                  autoFocus
                />
                <button className="btn btn-primary btn-small" onClick={handleScheduleSave}>
                  Save
                </button>
                <button className="btn btn-small" onClick={() => setEditing(null)}>
                  Cancel
                </button>
              </div>
            )}

            {entry.runs && entry.runs.length > 0 && (
              <div className="cron-runs">
                {entry.runs.slice(0, 5).map(run => (
                  <div key={run.jobId} className="cron-run-row">
                    <span className={`history-status ${statusClass(run.status)}`}>{run.status}</span>
                    <span className="cron-run-time">{relativeTime(run.startedAt)}</span>
                    <span className="cron-run-id">{run.jobId.split('/').pop()}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
