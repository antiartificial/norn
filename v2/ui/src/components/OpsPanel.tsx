import { useEffect, useState } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface OpsSummary {
  generatedAt: string
  webUrl?: string
  workerUrl?: string
  app?: {
    nomadStatus: string
    healthy: boolean
    allocations?: unknown[]
  }
  worker?: {
    status: string
    dry_run: boolean
    policy: {
      totals: {
        namespaces: number
        mutation_enabled: number
        provider_backed: number
        missing_provider_keys: number
        warnings: number
        errors: number
      }
      namespaces: Array<{
        namespace: string
        mode: string
        policy_preset: string
        evaluator: string
        dry_run: boolean
        mutation_allowed: boolean
        ok: boolean
      }>
    }
  }
  providerGate: {
    ready: boolean
    reason?: string
    providerBacked: number
    mutationEnabled: number
    missingProviderKeys: number
    warnings: number
    errors: number
  }
  queue: {
    total: number
    error?: string
  }
  workerRuns: Array<{
    cycle_id: string
    generated_at: string
    evaluator: string
    dry_run: boolean
    scanned: number
    applied: number
    errors: number
    decisions?: Array<{
      review_id: string
      type: string
      node_id?: string
      action: string
      applied: boolean
      reason?: string
      feedback_event_id?: string
      review_decision_event_id?: string
    }>
  }>
  feedbackEvents: Array<{
    event_id: string
    tx_time: string
    action: string
    confidence: number
    node_id: string
    reason: string
  }>
  rollbacks: Array<{
    event_id: string
    rolled_back_event_id: string
    node_id: string
    action: string
    previous_confidence: number
    restored_confidence: number
    reason: string
    owner: string
    tx_time: string
  }>
  snapshots: Array<{
    timestamp: string
    commitSha?: string
    size: number
  }>
  deployments: Array<{
    commitSha: string
    imageTag: string
    status: string
    startedAt: string
  }>
  secrets?: {
    ok: boolean
    declared: string[]
    encrypted: string[]
  }
  warnings?: string[]
}

export function OpsPanel() {
  const [summary, setSummary] = useState<OpsSummary | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyRollback, setBusyRollback] = useState<string | null>(null)

  const loadSummary = () => {
    let cancelled = false
    setLoading(true)
    fetch(apiUrl('/api/ops/contextdb'), fetchOpts)
      .then(async (res) => {
        if (!res.ok) throw new Error(await res.text())
        return res.json()
      })
      .then((data) => {
        if (!cancelled) {
          setSummary(data)
          setError(null)
        }
      })
      .catch((err) => {
        if (!cancelled) setError(String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }

  useEffect(() => {
    return loadSummary()
  }, [])

  if (loading) {
    return <div className="ops-panel"><div className="ops-empty">Loading ContextDB operations...</div></div>
  }
  if (error) {
    return <div className="error-banner"><strong>Ops error</strong>{error}</div>
  }
  if (!summary) {
    return <div className="ops-panel"><div className="ops-empty">No operations summary available</div></div>
  }

  const policy = summary.worker?.policy
  const latestRun = summary.workerRuns?.[0]
  const latestDeployment = summary.deployments?.[0]
  const rollbackTargets = new Set(summary.rollbacks.map((receipt) => receipt.rolled_back_event_id))

  const rollbackFeedback = async (eventID: string) => {
    if (!confirm(`Rollback feedback event ${short(eventID)}?`)) return
    setBusyRollback(eventID)
    try {
      const res = await fetch(apiUrl(`/api/ops/contextdb/feedback/${eventID}/rollback`), {
        ...fetchOpts,
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reason: 'operator rollback from norn ops', owner: 'norn-ui' }),
      })
      if (!res.ok) throw new Error(await res.text())
      loadSummary()
    } catch (err) {
      setError(String(err))
    } finally {
      setBusyRollback(null)
    }
  }

  return (
    <div className="ops-panel">
      <div className="ops-header">
        <div>
          <h2>ContextDB Ops</h2>
          <p>{summary.webUrl || 'web unavailable'} &middot; {summary.workerUrl || 'worker unavailable'}</p>
        </div>
        <span className={`ops-status ${summary.providerGate.ready ? 'ok' : 'warn'}`}>
          {summary.providerGate.ready ? 'provider ready' : summary.providerGate.reason || 'guarded'}
        </span>
      </div>

      <div className="ops-metrics">
        <Metric label="App" value={summary.app?.healthy ? 'healthy' : summary.app?.nomadStatus || 'unknown'} tone={summary.app?.healthy ? 'ok' : 'warn'} />
        <Metric label="Worker" value={summary.worker ? `${summary.worker.status}${summary.worker.dry_run ? ' dry-run' : ''}` : 'missing'} tone={summary.worker ? 'ok' : 'bad'} />
        <Metric label="Queue" value={String(summary.queue.total ?? 0)} tone={summary.queue.error ? 'bad' : 'ok'} />
        <Metric label="Snapshots" value={String(summary.snapshots.length)} />
        <Metric label="Secrets" value={summary.secrets?.ok ? 'ok' : 'check'} tone={summary.secrets?.ok ? 'ok' : 'warn'} />
        <Metric label="Mutation" value={String(summary.providerGate.mutationEnabled)} tone={summary.providerGate.mutationEnabled > 0 ? 'warn' : 'ok'} />
      </div>

      {policy && (
        <section className="ops-section">
          <h3>Policy</h3>
          <div className="ops-table">
            <div className="ops-row ops-row-head">
              <span>Namespace</span><span>Mode</span><span>Preset</span><span>Evaluator</span><span>Dry</span><span>Mutate</span><span>OK</span>
            </div>
            {policy.namespaces.map((ns) => (
              <div className="ops-row" key={`${ns.namespace}:${ns.mode}`}>
                <span>{ns.namespace}</span><span>{ns.mode}</span><span>{ns.policy_preset}</span><span>{ns.evaluator}</span><span>{String(ns.dry_run)}</span><span>{String(ns.mutation_allowed)}</span><span>{String(ns.ok)}</span>
              </div>
            ))}
          </div>
          <p className="ops-footnote">
            provider-backed {policy.totals.provider_backed}, missing keys {policy.totals.missing_provider_keys}, warnings {policy.totals.warnings}, errors {policy.totals.errors}
          </p>
        </section>
      )}

      <div className="ops-two">
        <section className="ops-section">
          <h3>Latest Run</h3>
          {latestRun ? (
            <div className="ops-kv">
              <span>time</span><strong>{formatTime(latestRun.generated_at)}</strong>
              <span>evaluator</span><strong>{latestRun.evaluator}</strong>
              <span>dry run</span><strong>{String(latestRun.dry_run)}</strong>
              <span>scanned</span><strong>{latestRun.scanned}</strong>
              <span>applied</span><strong>{latestRun.applied}</strong>
              <span>errors</span><strong>{latestRun.errors}</strong>
            </div>
          ) : <div className="ops-empty">No worker runs recorded</div>}
        </section>

        <section className="ops-section">
          <h3>Latest Deploy</h3>
          {latestDeployment ? (
            <div className="ops-kv">
              <span>status</span><strong>{latestDeployment.status}</strong>
              <span>commit</span><strong>{short(latestDeployment.commitSha)}</strong>
              <span>image</span><strong>{latestDeployment.imageTag}</strong>
              <span>started</span><strong>{formatTime(latestDeployment.startedAt)}</strong>
            </div>
          ) : <div className="ops-empty">No deployments recorded</div>}
        </section>
      </div>

      <section className="ops-section">
        <h3>Worker Decisions</h3>
        {summary.workerRuns.some((run) => (run.decisions?.length ?? 0) > 0) ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head ops-row-wide">
              <span>Run</span><span>Type</span><span>Action</span><span>Applied</span><span>Node</span><span>Feedback</span><span>Reason</span>
            </div>
            {summary.workerRuns.flatMap((run) => (run.decisions ?? []).map((decision, i) => (
              <div className="ops-row ops-row-wide" key={`${run.cycle_id}:${decision.review_id}:${i}`}>
                <span>{short(run.cycle_id)}</span><span>{decision.type}</span><span>{decision.action}</span><span>{String(decision.applied)}</span><span>{short(decision.node_id)}</span><span>{short(decision.feedback_event_id)}</span><span>{decision.reason || '-'}</span>
              </div>
            )))}
          </div>
        ) : <div className="ops-empty">No worker decisions in recent runs</div>}
      </section>

      <section className="ops-section">
        <h3>Audit Events</h3>
        {summary.feedbackEvents.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head ops-row-audit">
              <span>Time</span><span>Action</span><span>Confidence</span><span>Node</span><span>Event</span><span>Rollback</span><span>Reason</span>
            </div>
            {summary.feedbackEvents.slice(0, 6).map((event, i) => (
              <div className="ops-row ops-row-audit" key={`${event.tx_time}:${event.node_id}:${i}`}>
                <span>{formatTime(event.tx_time)}</span><span>{event.action}</span><span>{event.confidence.toFixed(2)}</span><span>{short(event.node_id)}</span><span>{short(event.event_id)}</span>
                <span>
                  {rollbackTargets.has(event.event_id) ? 'rolled back' : (
                    <button className="btn btn-small" disabled={busyRollback === event.event_id} onClick={() => rollbackFeedback(event.event_id)}>
                      {busyRollback === event.event_id ? '...' : 'Rollback'}
                    </button>
                  )}
                </span>
                <span>{event.reason || '-'}</span>
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No feedback audit events found</div>}
      </section>

      <section className="ops-section">
        <h3>Rollback Receipts</h3>
        {summary.rollbacks.length > 0 ? (
          <div className="ops-table">
            <div className="ops-row ops-row-head ops-row-audit">
              <span>Time</span><span>Action</span><span>From</span><span>To</span><span>Node</span><span>Owner</span><span>Reason</span>
            </div>
            {summary.rollbacks.map((receipt) => (
              <div className="ops-row ops-row-audit" key={receipt.event_id}>
                <span>{formatTime(receipt.tx_time)}</span><span>{receipt.action}</span><span>{receipt.previous_confidence.toFixed(2)}</span><span>{receipt.restored_confidence.toFixed(2)}</span><span>{short(receipt.node_id)}</span><span>{receipt.owner || '-'}</span><span>{receipt.reason || '-'}</span>
              </div>
            ))}
          </div>
        ) : <div className="ops-empty">No rollback receipts recorded</div>}
      </section>

      {(summary.queue.error || (summary.warnings && summary.warnings.length > 0)) && (
        <section className="ops-section ops-warnings">
          <h3>Warnings</h3>
          {summary.queue.error && <p>{summary.queue.error}</p>}
          {summary.warnings?.map((warning) => <p key={warning}>{warning}</p>)}
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
