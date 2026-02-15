import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface Snapshot {
  filename: string
  database: string
  timestamp: string
  size: number
}

interface Props {
  appId: string
  onClose: () => void
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const k = bytes / 1024
  if (k < 1024) return `${k.toFixed(1)} KB`
  const m = k / 1024
  if (m < 1024) return `${m.toFixed(1)} MB`
  return `${(m / 1024).toFixed(1)} GB`
}

function formatTimestamp(ts: string): string {
  // ts is like "20250613T143022"
  if (ts.length < 15) return ts
  const date = ts.slice(0, 4) + '-' + ts.slice(4, 6) + '-' + ts.slice(6, 8)
  const time = ts.slice(9, 11) + ':' + ts.slice(11, 13) + ':' + ts.slice(13, 15)
  return `${date} ${time}`
}

export function SnapshotsPanel({ appId, onClose }: Props) {
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [loading, setLoading] = useState(true)
  const [restoring, setRestoring] = useState<string | null>(null)
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  const load = async () => {
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/snapshots`), fetchOpts)
      if (res.ok) setSnapshots(await res.json())
    } catch { /* */ }
    setLoading(false)
  }

  useEffect(() => { load() }, [appId])

  const handleRestore = async (ts: string) => {
    if (!confirm(`Restore snapshot ${ts}? This will replace the current database.`)) return
    setRestoring(ts)
    setMessage(null)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/snapshots/${ts}/restore`), {
        ...fetchOpts,
        method: 'POST',
      })
      if (res.ok) {
        setMessage({ type: 'success', text: `Snapshot ${ts} restored successfully` })
      } else {
        const data = await res.json().catch(() => ({ error: 'Unknown error' }))
        setMessage({ type: 'error', text: data.error || 'Restore failed' })
      }
    } catch (e) {
      setMessage({ type: 'error', text: `Restore failed: ${e}` })
    }
    setRestoring(null)
  }

  return (
    <div className="panel-overlay">
      <div className="panel-card panel-wide">
        <div className="panel-header">
          <h4><i className="fawsb fa-database" /> Snapshots — {appId}</h4>
          <button className="btn-close" onClick={onClose}><i className="fawsb fa-xmark" /></button>
        </div>

        {message && (
          <div className={`panel-message ${message.type}`}>{message.text}</div>
        )}

        {loading && <div className="panel-loading"><div className="loading-spinner" /></div>}

        {!loading && snapshots.length === 0 && (
          <div className="panel-empty">No snapshots found for this app</div>
        )}

        {!loading && snapshots.length > 0 && (
          <div className="panel-list">
            <div className="panel-list-header">
              <span className="snap-ts">Timestamp</span>
              <span className="snap-db">Database</span>
              <span className="snap-size">Size</span>
              <span className="snap-action"></span>
            </div>
            {snapshots.map(snap => (
              <div key={snap.filename} className="panel-list-row">
                <span className="snap-ts">{formatTimestamp(snap.timestamp)}</span>
                <span className="snap-db">{snap.database}</span>
                <span className="snap-size">{formatBytes(snap.size)}</span>
                <span className="snap-action">
                  <button
                    className="btn btn-danger btn-small"
                    disabled={restoring !== null}
                    onClick={() => handleRestore(snap.timestamp)}
                  >
                    {restoring === snap.timestamp ? <span className="btn-spinner" /> : <i className="fawsb fa-arrow-rotate-left" />}
                    Restore
                  </button>
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
