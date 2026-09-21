import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { relativeTime } from '../lib/format.ts'
import type { BeaconEvent, EventsResponse, MutationAuditEvent, MutationAuditResponse } from '../types/index.ts'
import type { StatusTone } from '../components/ui/index.ts'
import { Panel } from '../components/panels/Panel.tsx'
import { CopyButton, EmptyState, StatusChip, Tab, TabPanel, Tabs, TabsList } from '../components/ui/index.ts'

const AUDIT_LIMIT = 200

// The Activity Log is scope-graded: Pods (operator, per-workload actions) and
// Cell (operator, cluster/infra events) both ride the api:read beacon feed;
// Receipts is the admin-only signed control-plane mutation-audit.
export function ActivityLogPage() {
  return (
    <div className="activity-log">
      <Tabs defaultValue="pods">
        <TabsList aria-label="Activity log views">
          <Tab value="pods">Pods</Tab>
          <Tab value="cell">Cell</Tab>
          <Tab value="receipts">Receipts</Tab>
        </TabsList>
        <TabPanel value="pods"><BeaconView scope="pods" /></TabPanel>
        <TabPanel value="cell"><BeaconView scope="cell" /></TabPanel>
        <TabPanel value="receipts"><ReceiptsView /></TabPanel>
      </Tabs>
    </div>
  )
}

// ---- Pods / Cell: the operator-facing beacon activity ledger -----------------

function BeaconView({ scope }: { scope: 'pods' | 'cell' }) {
  const [type, setType] = useState('all')
  const query = useQuery({ queryKey: ['events'], queryFn: () => apiFetch<EventsResponse>('/api/events'), staleTime: 15_000 })
  const all = query.data?.events ?? []
  // Pods = workload-scoped events (app set); Cell = infra/cluster events (no app).
  const scoped = useMemo(() => all.filter(e => scope === 'pods' ? Boolean(e.app) : !e.app), [all, scope])
  const types = useMemo(() => ['all', ...new Set(scoped.map(e => e.type).filter(Boolean))], [scoped])
  const filtered = useMemo(() => type === 'all' ? scoped : scoped.filter(e => e.type === type), [scoped, type])
  const title = scope === 'pods' ? 'Pod activity' : 'Cell activity'
  return (
    <Panel title={title} loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
      <div className="app-filters" aria-label={`${title} type filters`}>
        {types.map(value => (
          <button key={value} type="button" className={`filter-btn ${type === value ? 'active' : ''}`} onClick={() => setType(value)}>{value}</button>
        ))}
      </div>
      <BeaconRows items={filtered} scope={scope} />
    </Panel>
  )
}

function BeaconRows({ items, scope }: { items: BeaconEvent[]; scope: 'pods' | 'cell' }) {
  if (items.length === 0) {
    return <EmptyState icon="·" title="No activity" hint={scope === 'pods' ? 'No recorded actions on workloads yet.' : 'No cell-level events yet.'} />
  }
  return (
    <div className="compact-list activity-log-list">
      {items.map((event, i) => (
        <div className="compact-row activity-row" key={event.id ?? i}>
          <StatusChip tone={severityTone(event.severity)} label={event.type} />
          {scope === 'pods' && event.app && <small className="activity-app">{event.app}</small>}
          <span className="activity-title">{event.title}</span>
          {actorOf(event) && <small>by {actorOf(event)}</small>}
          <small>{relativeTime(event.occurredAt)}</small>
          {event.body && <small className="activity-body">{event.body}</small>}
        </div>
      ))}
    </div>
  )
}

function severityTone(severity: string): StatusTone {
  switch (severity) {
    case 'critical': return 'danger'
    case 'warning': return 'warning'
    default: return 'info'
  }
}

function actorOf(event: BeaconEvent): string | null {
  const actor = event.metadata?.actor
  return typeof actor === 'string' ? actor : null
}

// ---- Receipts: the admin signed control-plane mutation-audit ------------------

function ReceiptsView() {
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
    <Panel title="Receipts" loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
      <div className="app-filters" aria-label="Receipt outcome filters">
        {outcomes.map(value => (
          <button key={value} type="button" className={`filter-btn ${outcome === value ? 'active' : ''}`} onClick={() => setOutcome(value)}>{value}</button>
        ))}
      </div>
      <ReceiptRows items={filtered} />
    </Panel>
  )
}

function ReceiptRows({ items }: { items: MutationAuditEvent[] }) {
  if (items.length === 0) return <EmptyState icon="·" title="No receipts" hint="No signed mutation receipts match the selected filter." />
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
