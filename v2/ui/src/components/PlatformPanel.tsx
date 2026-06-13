import { useEffect, useState } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface PlatformSummary {
  generatedAt: string
  networkMode?: string
  services: {
    total: number
    public: number
    private: number
    local: number
    internal: number
    byType: Record<string, number>
    byStatus: Record<string, number>
  }
  deployments: {
    recent: Array<{
      app: string
      commitSha: string
      imageTag: string
      status: string
      sourceKind?: string
      sourceDirty?: boolean
      sourceChanges?: string[]
      startedAt: string
    }>
    dirty: Array<{
      app: string
      commitSha: string
      imageTag: string
      sourceChanges?: string[]
    }>
    failed: number
    successful: number
  }
  operations: {
    recent: Array<{
      id: string
      kind: string
      app?: string
      sagaId?: string
      ref?: string
      status: string
      risk?: string
      message?: string
      startedAt: string
      finishedAt?: string
    }>
    active: Array<{
      id: string
      kind: string
      app?: string
      status: string
      risk?: string
      message?: string
      startedAt: string
    }>
    byKind: Record<string, number>
    byStatus: Record<string, number>
  }
  secrets: {
    ok: number
    needsAttention: number
    migrationItems: number
    apps: Array<{
      app: string
      ok: boolean
      missingEncrypted: string[]
      encryptedUndeclared: string[]
      plainEnvWarnings: string[]
    }>
  }
  snapshots: Array<{
    app: string
    database: string
    keep: number
    count: number
    overLimit: number
    latest?: { timestamp: string; commitSha?: string }
  }>
  access: {
    totalRecent: number
    byStatus: Record<string, number>
    byClientIp: Record<string, number>
    recent: Array<{
      timestamp: string
      method: string
      path: string
      status: number
      clientIp?: string
      cfAccessEmail?: string
      durationMs: number
    }>
  }
  observability: {
    enabled: boolean
    logsEnabled: boolean
    logFormat: string
    serviceName?: string
    otlpEndpoint?: string
    bundleAvailable?: boolean
    retention?: string
  }
  warnings?: string[]
}

interface PlatformReleaseList {
  current?: string
  releases: Array<{
    sha: string
    version: string
    createdAt: string
    path: string
    current: boolean
  }>
}

interface BeaconEventList {
  total: number
  events: Array<{
    id: string
    app?: string
    type: string
    severity: string
    state?: string
    title: string
    body?: string
    dedupeKey?: string
    occurredAt: string
    acknowledgedAt?: string
    acknowledgedBy?: string
    acknowledgementNote?: string
    snoozedUntil?: string
    metadata?: Record<string, unknown>
  }>
}

export function PlatformPanel() {
  const [summary, setSummary] = useState<PlatformSummary | null>(null)
  const [releases, setReleases] = useState<PlatformReleaseList | null>(null)
  const [beaconEvents, setBeaconEvents] = useState<BeaconEventList | null>(null)
  const [busyRelease, setBusyRelease] = useState<string | null>(null)
  const [busyEvent, setBusyEvent] = useState<string | null>(null)
  const [selectedEvent, setSelectedEvent] = useState<string | null>(null)
  const [reloadNonce, setReloadNonce] = useState(0)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      try {
        const [opsRes, releasesRes, eventsRes] = await Promise.all([
          fetch(apiUrl('/api/ops/platform'), fetchOpts),
          fetch(apiUrl('/api/platform/releases'), fetchOpts),
          fetch(apiUrl('/api/events?limit=8'), fetchOpts),
        ])
        if (!opsRes.ok) throw new Error(await opsRes.text())
        if (!releasesRes.ok) throw new Error(await releasesRes.text())
        if (!eventsRes.ok) throw new Error(await eventsRes.text())
        const data = await opsRes.json()
        const releaseData = await releasesRes.json()
        const eventData = await eventsRes.json()
        if (!cancelled) {
          setSummary(data)
          setReleases(releaseData)
          setBeaconEvents(eventData)
          setError(null)
        }
      } catch (err) {
        if (!cancelled) setError(String(err))
      }
    }
    load()
    const interval = setInterval(load, 15000)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [reloadNonce])

  if (error) return <div className="error-banner"><strong>Platform error</strong>{error}</div>
  if (!summary) return <div className="ops-panel"><div className="ops-empty">Loading platform operations...</div></div>

  const snapshots = summary.snapshots ?? []
  const recentDeployments = summary.deployments.recent ?? []
  const recentOperations = summary.operations?.recent ?? []
  const activeOperations = summary.operations?.active ?? []
  const dirtyDeployments = summary.deployments.dirty ?? []
  const accessEvents = summary.access.recent ?? []
  const secretTone = summary.secrets.needsAttention > 0 ? 'warn' : 'ok'
  const dirtyTone = dirtyDeployments.length > 0 ? 'warn' : 'ok'
  const snapshotTone = snapshots.some((s) => s.overLimit > 0) ? 'warn' : 'ok'
  const platformReleases = releases?.releases ?? []
  const recentBeaconEvents = beaconEvents?.events ?? []

  async function rollbackRelease(sha: string) {
    setBusyRelease(sha)
    try {
      const res = await fetch(apiUrl(`/api/platform/releases/${encodeURIComponent(sha)}/rollback`), {
        ...fetchOpts,
        method: 'POST',
      })
      if (!res.ok) throw new Error(await res.text())
    } catch (err) {
      setError(String(err))
    } finally {
      setBusyRelease(null)
    }
  }

  async function eventAction(id: string, action: 'ack' | 'snooze' | 'open') {
    setBusyEvent(id)
    try {
      const body = action === 'snooze' ? { duration: '1h', note: 'snoozed from platform panel' } : {}
      const res = await fetch(apiUrl(`/api/events/${encodeURIComponent(id)}/${action}`), {
        ...fetchOpts,
        method: 'POST',
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error(await res.text())
      setReloadNonce((value) => value + 1)
    } catch (err) {
      setError(String(err))
    } finally {
      setBusyEvent(null)
    }
  }

  return (
    <div className="ops-panel">
      <div className="ops-header">
        <div>
          <h2>Norn Platform</h2>
          <p>{summary.networkMode || 'network unknown'} &middot; generated {formatTime(summary.generatedAt)}</p>
        </div>
        <span className={`ops-status ${summary.observability.enabled ? 'ok' : 'warn'}`}>
          {summary.observability.enabled ? 'otel enabled' : 'otel disabled'}
        </span>
      </div>

      <div className="ops-metrics">
        <Metric label="Services" value={String(summary.services.total)} />
        <Metric label="Active Ops" value={String(activeOperations.length)} tone={activeOperations.length > 0 ? 'warn' : 'ok'} />
        <Metric label="Public" value={String(summary.services.public)} tone={summary.services.public > 0 ? 'warn' : 'ok'} />
        <Metric label="Dirty Deploys" value={String(dirtyDeployments.length)} tone={dirtyTone} />
        <Metric label="Secrets" value={`${summary.secrets.ok}/${summary.secrets.ok + summary.secrets.needsAttention}`} tone={secretTone} />
        <Metric label="Secret Moves" value={String(summary.secrets.migrationItems || 0)} tone={(summary.secrets.migrationItems || 0) > 0 ? 'warn' : 'ok'} />
        <Metric label="Snapshots" value={String(snapshots.length)} tone={snapshotTone} />
        <Metric label="Access" value={String(summary.access.totalRecent)} />
      </div>

      <div className="ops-two">
        <section className="ops-section">
          <h3>Observability</h3>
          <div className="ops-kv">
            <span>enabled</span><strong>{String(summary.observability.enabled)}</strong>
            <span>logs</span><strong>{String(summary.observability.logsEnabled)}</strong>
            <span>format</span><strong>{summary.observability.logFormat}</strong>
            <span>service</span><strong>{summary.observability.serviceName || '-'}</strong>
            <span>otlp</span><strong>{summary.observability.otlpEndpoint || '-'}</strong>
            <span>bundle</span><strong>{summary.observability.bundleAvailable ? 'available' : '-'}</strong>
            <span>retention</span><strong>{summary.observability.retention || '-'}</strong>
          </div>
        </section>

        <section className="ops-section">
          <h3>Service Exposure</h3>
          <div className="ops-kv">
            <span>public</span><strong>{summary.services.public}</strong>
            <span>private</span><strong>{summary.services.private}</strong>
            <span>local</span><strong>{summary.services.local}</strong>
            <span>internal</span><strong>{summary.services.internal}</strong>
            <span>types</span><strong>{joinCounts(summary.services.byType)}</strong>
          </div>
        </section>
      </div>

      <section className="ops-section">
        <h3>Snapshot Lifecycle</h3>
        {snapshots.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head">
              <span>App</span><span>Database</span><span>Count</span><span>Keep</span><span>Over</span><span>Latest</span><span>Commit</span>
            </div>
            {snapshots.map((snapshot) => (
              <div className="ops-row" key={`${snapshot.app}:${snapshot.database}`}>
                <span>{snapshot.app}</span><span>{snapshot.database}</span><span>{snapshot.count}</span><span>{snapshot.keep}</span><span>{snapshot.overLimit}</span><span>{snapshot.latest?.timestamp || '-'}</span><span>{short(snapshot.latest?.commitSha)}</span>
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No snapshot-backed apps found</div>}
      </section>

      <section className="ops-section">
        <h3>Recent Deployments</h3>
        {recentDeployments.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head ops-row-wide">
              <span>App</span><span>Status</span><span>Commit</span><span>Source</span><span>Image</span><span>Started</span><span>Changes</span>
            </div>
            {recentDeployments.slice(0, 8).map((deployment) => (
              <div className="ops-row ops-row-wide" key={`${deployment.app}:${deployment.startedAt}`}>
                <span>{deployment.app}</span><span>{deployment.status}</span><span>{short(deployment.commitSha)}</span><span>{deployment.sourceKind || '-'}{deployment.sourceDirty ? '*' : ''}</span><span>{deployment.imageTag || '-'}</span><span>{formatTime(deployment.startedAt)}</span><span>{deployment.sourceChanges?.length ?? 0}</span>
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No deployments recorded</div>}
      </section>

      <section className="ops-section">
        <h3>Operations</h3>
        {recentOperations.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head ops-row-wide">
              <span>Time</span><span>Status</span><span>Kind</span><span>App</span><span>Ref</span><span>Risk</span><span>Message</span>
            </div>
            {recentOperations.slice(0, 8).map((operation) => (
              <div className="ops-row ops-row-wide" key={operation.id}>
                <span>{formatTime(operation.startedAt)}</span><span>{operation.status}</span><span>{operation.kind}</span><span>{operation.app || '-'}</span><span>{short(operation.ref)}</span><span>{operation.risk || '-'}</span><span>{operation.message || '-'}</span>
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No operations recorded</div>}
      </section>

      <section className="ops-section">
        <h3>Platform Releases</h3>
        {platformReleases.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head ops-row-wide">
              <span>Created</span><span>Version</span><span>SHA</span><span>Status</span><span>Path</span><span>Action</span>
            </div>
            {platformReleases.slice(0, 8).map((release) => (
              <div className="ops-row ops-row-wide" key={release.sha}>
                <span>{formatTime(release.createdAt)}</span><span>{release.version}</span><span>{short(release.sha)}</span><span>{release.current ? 'current' : '-'}</span><span>{release.path}</span><span>{release.current ? '-' : (
                  <button className="btn btn-small" disabled={busyRelease === release.sha} onClick={() => rollbackRelease(release.sha)}>
                    {busyRelease === release.sha ? 'starting' : 'rollback'}
                  </button>
                )}</span>
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No platform releases found</div>}
      </section>

      <section className="ops-section">
        <h3>Beacon Events</h3>
        {recentBeaconEvents.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head ops-row-events">
              <span>Time</span><span>Severity</span><span>State</span><span>Type</span><span>App</span><span>Title</span><span>Action</span>
            </div>
            {recentBeaconEvents.map((event) => (
              <div key={event.id}>
                <div className="ops-row ops-row-events">
                  <span>{formatTime(event.occurredAt)}</span><span>{event.severity}</span><span>{event.state || 'open'}</span><span>{event.type}</span><span>{event.app || '-'}</span><button className="ops-link" onClick={() => setSelectedEvent(selectedEvent === event.id ? null : event.id)}>{event.title}</button><span className="ops-actions">
                    {(event.state || 'open') === 'acknowledged' ? (
                      <button className="btn btn-small" disabled={busyEvent === event.id} onClick={() => eventAction(event.id, 'open')}>open</button>
                    ) : (
                      <button className="btn btn-small" disabled={busyEvent === event.id} onClick={() => eventAction(event.id, 'ack')}>ack</button>
                    )}
                    {(event.state || 'open') !== 'snoozed' && <button className="btn btn-small" disabled={busyEvent === event.id} onClick={() => eventAction(event.id, 'snooze')}>snooze</button>}
                  </span>
                </div>
                {selectedEvent === event.id && (
                  <div className="ops-event-detail">
                    <div><span>ID</span><strong>{event.id}</strong></div>
                    <div><span>Dedupe</span><strong>{event.dedupeKey || '-'}</strong></div>
                    <div><span>Ack</span><strong>{event.acknowledgedAt ? `${formatTime(event.acknowledgedAt)} ${event.acknowledgedBy || ''}` : '-'}</strong></div>
                    <div><span>Snoozed</span><strong>{event.snoozedUntil ? formatTime(event.snoozedUntil) : '-'}</strong></div>
                    {event.body && <p>{event.body}</p>}
                    {event.acknowledgementNote && <p>{event.acknowledgementNote}</p>}
                    {event.metadata && <p>{formatMetadata(event.metadata)}</p>}
                  </div>
                )}
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No Beacon events recorded</div>}
      </section>

      <section className="ops-section">
        <h3>Access</h3>
        {accessEvents.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head">
              <span>Time</span><span>Status</span><span>Method</span><span>Path</span><span>Client</span><span>User</span><span>MS</span>
            </div>
            {accessEvents.slice(0, 8).map((event, i) => (
              <div className="ops-row" key={`${event.timestamp}:${event.path}:${i}`}>
                <span>{formatTime(event.timestamp)}</span><span>{event.status}</span><span>{event.method}</span><span>{event.path}</span><span>{event.clientIp || '-'}</span><span>{event.cfAccessEmail || '-'}</span><span>{event.durationMs}</span>
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No access events recorded</div>}
      </section>

      {(summary.warnings && summary.warnings.length > 0) && (
        <section className="ops-section ops-warnings">
          <h3>Warnings</h3>
          {summary.warnings.map((warning) => <p key={warning}>{warning}</p>)}
        </section>
      )}
    </div>
  )
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: 'ok' | 'warn' | 'bad' }) {
  return (
    <div className={`ops-metric ${tone || ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}

function formatTime(value?: string) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

function short(value?: string) {
  if (!value) return '-'
  return value.length > 10 ? value.slice(0, 10) : value
}

function joinCounts(values: Record<string, number>) {
  const parts = Object.entries(values).map(([key, value]) => `${key} ${value}`)
  return parts.length > 0 ? parts.join(', ') : '-'
}

function formatMetadata(values: Record<string, unknown>) {
  const parts = Object.entries(values).map(([key, value]) => `${key}: ${String(value)}`)
  return parts.length > 0 ? parts.join(' · ') : '-'
}
