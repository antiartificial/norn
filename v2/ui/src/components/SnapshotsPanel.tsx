import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import type { RemoteSnapshot } from '../types/index.ts'

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

function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

export function SnapshotsPanel({ appId, onClose }: Props) {
  const [tab, setTab] = useState<'local' | 'remote'>('local')

  // Local state
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [loading, setLoading] = useState(true)
  const [restoring, setRestoring] = useState<string | null>(null)
  const [exporting, setExporting] = useState(false)

  // Remote state
  const [remoteSnapshots, setRemoteSnapshots] = useState<RemoteSnapshot[]>([])
  const [remoteLoading, setRemoteLoading] = useState(false)
  const [remoteLoaded, setRemoteLoaded] = useState(false)
  const [remoteUnavailable, setRemoteUnavailable] = useState(false)
  const [importing, setImporting] = useState<string | null>(null)

  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  const loadLocal = async () => {
    setLoading(true)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/snapshots`), fetchOpts)
      if (res.ok) setSnapshots(await res.json())
    } catch { /* */ }
    setLoading(false)
  }

  const loadRemote = async () => {
    if (remoteLoaded) return
    setRemoteLoading(true)
    setRemoteUnavailable(false)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/snapshots/remote`), fetchOpts)
      if (res.ok) {
        const data = await res.json()
        setRemoteSnapshots(data.snapshots ?? [])
      } else {
        setRemoteUnavailable(true)
      }
    } catch {
      setRemoteUnavailable(true)
    }
    setRemoteLoading(false)
    setRemoteLoaded(true)
  }

  useEffect(() => { loadLocal() }, [appId])

  const handleTabChange = (next: 'local' | 'remote') => {
    setTab(next)
    setMessage(null)
    if (next === 'remote') loadRemote()
  }

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

  const handleExport = async () => {
    setExporting(true)
    setMessage(null)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/snapshots/export`), {
        ...fetchOpts,
        method: 'POST',
      })
      if (res.ok) {
        setMessage({ type: 'success', text: 'Snapshot exported to S3 successfully' })
        // Invalidate remote cache so re-visiting remote tab fetches fresh list
        setRemoteLoaded(false)
      } else {
        const data = await res.json().catch(() => ({ error: 'Unknown error' }))
        setMessage({ type: 'error', text: data.error || 'Export failed' })
      }
    } catch (e) {
      setMessage({ type: 'error', text: `Export failed: ${e}` })
    }
    setExporting(false)
  }

  const handleImport = async (key: string) => {
    if (!confirm(`Import remote snapshot "${key}"? This will replace the current database.`)) return
    setImporting(key)
    setMessage(null)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/snapshots/import`), {
        ...fetchOpts,
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key }),
      })
      if (res.ok) {
        setMessage({ type: 'success', text: `Remote snapshot imported successfully` })
      } else {
        const data = await res.json().catch(() => ({ error: 'Unknown error' }))
        setMessage({ type: 'error', text: data.error || 'Import failed' })
      }
    } catch (e) {
      setMessage({ type: 'error', text: `Import failed: ${e}` })
    }
    setImporting(null)
  }

  return (
    <div className="panel-overlay">
      <div className="panel-card panel-wide">
        <div className="panel-header">
          <h4><i className="fawsb fa-database" /> Snapshots — {appId}</h4>
          <button className="btn-close" onClick={onClose}><i className="fawsb fa-xmark" /></button>
        </div>

        <div className="panel-tabs">
          <button
            className={`panel-tab-btn${tab === 'local' ? ' active' : ''}`}
            onClick={() => handleTabChange('local')}
          >
            Local
          </button>
          <button
            className={`panel-tab-btn${tab === 'remote' ? ' active' : ''}`}
            onClick={() => handleTabChange('remote')}
          >
            Remote
          </button>
          {tab === 'local' && (
            <button
              className="btn btn-small"
              style={{ marginLeft: 'auto' }}
              disabled={exporting || snapshots.length === 0}
              onClick={handleExport}
            >
              {exporting ? <span className="btn-spinner" /> : <i className="fawsb fa-cloud-arrow-up" />}
              Export to S3
            </button>
          )}
        </div>

        {message && (
          <div className={`panel-message ${message.type}`}>{message.text}</div>
        )}

        {/* Local tab */}
        {tab === 'local' && (
          <>
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
          </>
        )}

        {/* Remote tab */}
        {tab === 'remote' && (
          <>
            {remoteLoading && <div className="panel-loading"><div className="loading-spinner" /></div>}

            {!remoteLoading && remoteUnavailable && (
              <div className="panel-empty">Remote snapshots are not configured for this app</div>
            )}

            {!remoteLoading && !remoteUnavailable && remoteSnapshots.length === 0 && (
              <div className="panel-empty">No remote snapshots found</div>
            )}

            {!remoteLoading && !remoteUnavailable && remoteSnapshots.length > 0 && (
              <div className="panel-list">
                <div className="panel-list-header">
                  <span className="snap-remote-key">Key</span>
                  <span className="snap-size">Size</span>
                  <span className="snap-remote-date">Last Modified</span>
                  <span className="snap-action"></span>
                </div>
                {remoteSnapshots.map(snap => (
                  <div key={snap.key} className="panel-list-row">
                    <span className="snap-remote-key">{snap.key}</span>
                    <span className="snap-size">{formatBytes(snap.size)}</span>
                    <span className="snap-remote-date">{formatDate(snap.lastModified)}</span>
                    <span className="snap-action">
                      <button
                        className="btn btn-small"
                        disabled={importing !== null}
                        onClick={() => handleImport(snap.key)}
                      >
                        {importing === snap.key ? <span className="btn-spinner" /> : <i className="fawsb fa-cloud-arrow-down" />}
                        Import
                      </button>
                    </span>
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
