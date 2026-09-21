import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { relativeTime } from '../lib/format.ts'
import type { MutationAuditEvent, MutationAuditResponse } from '../types/index.ts'
import type { StatusTone } from '../components/ui/index.ts'
import { Panel } from '../components/panels/Panel.tsx'
import { CopyButton, EmptyState, StatusChip } from '../components/ui/index.ts'

const AUDIT_LIMIT = 200

export function AuditPage() {
  const [outcome, setOutcome] = useState('all')
  const query = useQuery({
    queryKey: ['audit-mutations', AUDIT_LIMIT],
    queryFn: () => apiFetch<MutationAuditResponse>(`/api/v1/audit/mutations?limit=${AUDIT_LIMIT}`),
    staleTime: 15_000,
  })
  const events = query.data?.events ?? []
  const outcomes = useMemo(() => ['all', ...new Set(events.map(e => e.outcome).filter(Boolean))], [events])
  const filtered = useMemo(() => outcome === 'all' ? events : events.filter(e => e.outcome === outcome), [events, outcome])
  return (
    <Panel title="Audit" loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
      <div className="app-filters" aria-label="Audit outcome filters">
        {outcomes.map(value => (
          <button key={value} type="button" className={`filter-btn ${outcome === value ? 'active' : ''}`} onClick={() => setOutcome(value)}>{value}</button>
        ))}
      </div>
      <AuditRows items={filtered} />
    </Panel>
  )
}

function AuditRows({ items }: { items: MutationAuditEvent[] }) {
  if (items.length === 0) return <EmptyState icon="·" title="No audit events" hint="No signed mutation receipts match the selected filter." />
  return (
    <div className="compact-list audit-list">
      {items.map((event, i) => (
        <div className="compact-row audit-row" key={event.id ?? i}>
          <StatusChip tone={outcomeTone(event.outcome)} label={event.outcome} />
          <code className="audit-route">{event.method} {event.path}</code>
          <small className="audit-principal">{event.principalSubject}</small>
          {event.status > 0 && <small>{event.status}</small>}
          {event.durationMs > 0 && <small>{event.durationMs}ms</small>}
          <small>{relativeTime(event.startedAt)}</small>
          {event.integrity && <StatusChip tone={integrityTone(event.integrity)} label={event.integrity} />}
          <details className="audit-detail">
            <summary>details</summary>
            <dl className="audit-meta">
              <div><dt>id</dt><dd><code>{event.id}</code> <CopyButton value={event.id} label="Copy id" /></dd></div>
              {event.requestId && <div><dt>request</dt><dd><code>{event.requestId}</code></dd></div>}
              {event.scopes && event.scopes.length > 0 && <div><dt>scopes</dt><dd>{event.scopes.join(', ')}</dd></div>}
              {event.clientIp && <div><dt>client</dt><dd>{event.clientIp}</dd></div>}
              {event.tokenId && <div><dt>token</dt><dd><code>{event.tokenId}</code></dd></div>}
              {event.deviceId && <div><dt>device</dt><dd><code>{event.deviceId}</code></dd></div>}
              {event.finishedAt && <div><dt>finished</dt><dd>{relativeTime(event.finishedAt)}</dd></div>}
              {event.keyId && <div><dt>key</dt><dd><code>{event.keyId}</code></dd></div>}
              {event.incident && (
                <div><dt>incident</dt><dd>{event.incident.reasonCode}: {event.incident.explanation} (ack {event.incident.acknowledgedBy})</dd></div>
              )}
            </dl>
          </details>
        </div>
      ))}
    </div>
  )
}

function outcomeTone(outcome: string): StatusTone {
  switch (outcome) {
    case 'succeeded': return 'success'
    case 'started': return 'info'
    case 'rejected': return 'warning'
    case 'failed':
    case 'crashed': return 'danger'
    default: return 'neutral'
  }
}

function integrityTone(integrity: string): StatusTone {
  switch (integrity) {
    case 'verified': return 'success'
    case 'pending': return 'info'
    case 'invalid': return 'danger'
    case 'acknowledged-invalid': return 'warning'
    default: return 'neutral'
  }
}
