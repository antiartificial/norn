import { useState, useEffect } from 'react'
import type { Artifact, Snapshot } from '../types/index.ts'
import { Tooltip } from './Tooltip.tsx'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface Props {
  appId: string
  currentImage: string
  hasPostgres: boolean
  onClose: () => void
  onPromote: (imageTag: string) => void
}

function timeAgo(iso: string): string {
  if (!iso) return ''
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <Tooltip text={copied ? 'Copied!' : 'Copy command'}>
      <button className="btn-icon" onClick={copy}>
        <i className={`fawsb ${copied ? 'fa-check' : 'fa-copy'}`} />
      </button>
    </Tooltip>
  )
}

export function RollbackModal({ appId, currentImage, hasPostgres, onClose, onPromote }: Props) {
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [loadingArtifacts, setLoadingArtifacts] = useState(true)
  const [loadingSnapshots, setLoadingSnapshots] = useState(hasPostgres)
  const [restoring, setRestoring] = useState<string | null>(null)
  const [restoreResult, setRestoreResult] = useState<{ ok: boolean; message: string } | null>(null)

  useEffect(() => {
    fetch(apiUrl(`/api/apps/${appId}/artifacts`), fetchOpts)
      .then(r => r.json())
      .then(data => { setArtifacts(data ?? []); setLoadingArtifacts(false) })
      .catch(() => setLoadingArtifacts(false))

    if (hasPostgres) {
      fetch(apiUrl(`/api/apps/${appId}/snapshots`), fetchOpts)
        .then(r => r.json())
        .then(data => { setSnapshots(data ?? []); setLoadingSnapshots(false) })
        .catch(() => setLoadingSnapshots(false))
    }
  }, [appId, hasPostgres])

  const handleRestore = async (ts: string, filename: string) => {
    if (!window.confirm(`Restore database from ${filename}? This will overwrite the current database.`)) return
    setRestoring(ts)
    setRestoreResult(null)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/snapshots/${encodeURIComponent(ts)}/restore`), { ...fetchOpts, method: 'POST' })
      if (res.ok) {
        setRestoreResult({ ok: true, message: `Restored ${filename}` })
      } else {
        const text = await res.text()
        setRestoreResult({ ok: false, message: text })
      }
    } catch (err) {
      setRestoreResult({ ok: false, message: String(err) })
    } finally {
      setRestoring(null)
    }
  }

  return (
    <div className="welcome-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="rollback-modal">
        <div className="rollback-modal-header">
          <h4><i className="fawsb fa-arrow-rotate-left" /> Rollback — {appId}</h4>
          <button className="btn-close" onClick={onClose}>&times;</button>
        </div>

        <div className="rollback-section">
          <div className="rollback-section-label">
            <i className="fawsb fa-box" /> Artifacts
          </div>
          {loadingArtifacts ? (
            <div className="rollback-loading">Loading artifacts...</div>
          ) : artifacts.length === 0 ? (
            <div className="rollback-empty">No artifacts found</div>
          ) : (
            <div className="rollback-list">
              {artifacts.map((a) => {
                const isCurrent = a.imageTag === currentImage
                return (
                  <div key={a.imageTag} className={`artifact-row${isCurrent ? ' artifact-current' : ''}`}>
                    <span className="artifact-sha">{a.commitSha.slice(0, 7)}</span>
                    <span className="artifact-tag">{a.imageTag}</span>
                    <span className={`artifact-status artifact-status-${a.status}`}>{a.status}</span>
                    <span className="artifact-time">{timeAgo(a.deployedAt)}</span>
                    {isCurrent ? (
                      <span className="artifact-current-label">current</span>
                    ) : (
                      <button
                        className="btn artifact-promote"
                        onClick={() => { onPromote(a.imageTag); onClose() }}
                      >
                        <i className="fawsb fa-arrow-up" /> Promote
                      </button>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {hasPostgres && (
          <div className="rollback-section">
            <div className="rollback-section-label">
              <i className="fawsb fa-database" /> Database Snapshots
            </div>
            {restoreResult && (
              <div className={`rollback-result ${restoreResult.ok ? 'rollback-result-ok' : 'rollback-result-err'}`}>
                {restoreResult.message}
              </div>
            )}
            {loadingSnapshots ? (
              <div className="rollback-loading">Loading snapshots...</div>
            ) : snapshots.length === 0 ? (
              <div className="rollback-empty">No snapshots found</div>
            ) : (
              <div className="rollback-list">
                {snapshots.map((s) => (
                  <div key={s.filename} className="snapshot-row">
                    <span className="snapshot-db">{s.database}</span>
                    <span className="snapshot-sha">{s.commitSha.slice(0, 7)}</span>
                    <span className="snapshot-time">{timeAgo(s.timestamp)}</span>
                    <span className="snapshot-size">{formatBytes(s.sizeBytes)}</span>
                    <CopyButton text={`pg_restore --clean -d ${s.database} snapshots/${s.filename}`} />
                    <button
                      className="btn snapshot-restore"
                      disabled={restoring !== null}
                      onClick={() => handleRestore(s.timestamp, s.filename)}
                    >
                      {restoring === s.timestamp ? <span className="btn-spinner" /> : <i className="fawsb fa-arrow-rotate-left" />} Restore
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
