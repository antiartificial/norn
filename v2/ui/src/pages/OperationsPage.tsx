import { useMemo, useState } from 'react'
import { NavLink, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { relativeTime, statusTone } from '../lib/format.ts'
import type { Operation, OperationsResponse } from '../types/index.ts'
import { Panel } from '../components/panels/Panel.tsx'
import { EmptyState, StatusChip } from '../components/ui/index.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'
import { AuthorityEnrollment } from '../components/AuthorityEnrollment.tsx'

interface SagaEventish {
  id?: string
  timestamp?: string
  time?: string
  createdAt?: string
  event?: string
  type?: string
  action?: string
  step?: string
  status?: string
  message?: string
  payload?: unknown
  metadata?: unknown
}

export function OperationsPage() {
  const { sagaId } = useParams()
  const [status, setStatus] = useState('all')
  const runtime = useRuntimeContext()
  if (runtime.authority === 'fleet-only' && !runtime.authenticated) {
    return <AuthorityEnrollment onAuthenticated={runtime.refreshAuthoritySession} onSignedOut={runtime.clearAuthoritySession} />
  }
  if (sagaId && runtime.authority === 'fleet-only') {
    return <AuthorityOperationReceipt operationID={sagaId} />
  }
  if (sagaId) return <SagaTimeline sagaId={sagaId} />
  const query = useQuery({ queryKey: ['operations'], queryFn: () => apiFetch<OperationsResponse>('/api/operations'), staleTime: 15_000, enabled: runtime.capabilities !== undefined && (runtime.authority === 'full' || runtime.authenticated) })
  const operations = query.data?.operations ?? []
  const statuses = useMemo(() => ['all', ...new Set(operations.map(op => op.status).filter(Boolean) as string[])], [operations])
  const filtered = useMemo(() => status === 'all' ? operations : operations.filter(op => op.status === status), [operations, status])
  return (
    <Panel title="Operations" loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
      <div className="app-filters" aria-label="Operation status filters">
        {statuses.map(value => (
          <button key={value} type="button" className={`filter-btn ${status === value ? 'active' : ''}`} onClick={() => setStatus(value)}>{value}</button>
        ))}
      </div>
      <OperationRows items={filtered} />
    </Panel>
  )
}

function AuthorityOperationReceipt({ operationID }: { operationID: string }) {
  const query = useQuery({ queryKey: ['operation-receipt', operationID], queryFn: () => apiFetch<Operation>(`/api/v1/operations/${encodeURIComponent(operationID)}`), staleTime: 15_000 })
  const operation = query.data
  return <Panel title={`Operation receipt ${operationID}`} loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
    {!operation ? null : <div className="compact-list"><div className="compact-row"><StatusChip tone={statusTone(operation.status)} label={operation.status ?? 'unknown'} /><span>{operation.kind ?? 'operation'}</span><code>{operation.id}</code></div></div>}
  </Panel>
}

function SagaTimeline({ sagaId }: { sagaId: string }) {
  const query = useQuery({ queryKey: ['saga', sagaId], queryFn: () => apiFetch<SagaEventish[]>(`/api/saga/${sagaId}`), staleTime: 15_000 })
  return (
    <Panel title={`Saga ${sagaId}`} loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
      <SagaEventList items={query.data ?? []} />
    </Panel>
  )
}

function eventTime(event: SagaEventish): string | undefined {
  return event.timestamp ?? event.time ?? event.createdAt
}

function eventName(event: SagaEventish): string {
  return event.event ?? event.type ?? event.action ?? event.step ?? 'event'
}

function eventPayload(event: SagaEventish): unknown {
  return event.payload ?? event.metadata
}

function SagaEventList({ items }: { items: SagaEventish[] }) {
  if (items.length === 0) return <EmptyState icon="·" title="No saga events" hint="This saga has no recorded events." />
  const ordered = [...items].sort((a, b) => new Date(eventTime(a) ?? 0).getTime() - new Date(eventTime(b) ?? 0).getTime())
  return (
    <div className="compact-list saga-event-list">
      {ordered.map((event, i) => {
        const payload = eventPayload(event)
        const timestamp = eventTime(event)
        const previous = i > 0 ? eventTime(ordered[i - 1]) : undefined
        const delta = durationBetween(previous, timestamp)
        return (
          <div className="compact-row saga-event-row" key={event.id ?? `${eventName(event)}-${timestamp ?? i}`}>
            <code>{timestamp ? relativeTime(timestamp) : 'unknown'}</code>
            <span>{eventName(event)}</span>
            {event.status && <StatusChip tone={statusTone(event.status)} label={event.status} />}
            {delta && <small>+{delta}</small>}
            <small>{event.message}</small>
            {payload !== undefined && (
              <details>
                <summary>payload</summary>
                <pre>{JSON.stringify(payload, null, 2)}</pre>
              </details>
            )}
          </div>
        )
      })}
    </div>
  )
}

function OperationRows({ items }: { items: Operation[] }) {
  if (items.length === 0) return <EmptyState icon="·" title="No operations" hint="No durable operations match the selected filters." />
  return (
    <div className="compact-list operations-list">
      {items.map((op, i) => (
        <NavLink className="compact-row operation-row" key={op.id ?? op.sagaId ?? i} to={`/operations/${op.sagaId ?? op.id ?? ''}`}>
          <StatusChip tone={statusTone(op.status)} label={op.status ?? 'running'} />
          <span>{op.kind ?? 'operation'}</span>
          <small>{op.app || '-'}</small>
          <small>attempt {operationAttempts(op) ?? '-'}</small>
          <small>risk {op.risk || '-'}</small>
          {op.nextAttemptAt && <small>next {countdown(op.nextAttemptAt)}</small>}
          {operationTime(op) && <small>{relativeTime(operationTime(op))}</small>}
          {op.lastError && (
            <details className="operation-error" onClick={event => event.stopPropagation()}>
              <summary>last error</summary>
              <pre>{op.lastError}</pre>
            </details>
          )}
        </NavLink>
      ))}
    </div>
  )
}

function operationAttempts(op: Operation): string | null {
  const current = op.attempts ?? op.attempt
  if (current === undefined) return null
  return op.maxAttempts === undefined ? String(current) : `${current}/${op.maxAttempts}`
}

function operationTime(op: Operation): string | undefined {
  return op.updatedAt ?? op.startedAt ?? op.createdAt ?? op.finishedAt
}

function countdown(value: string): string {
  const diff = new Date(value).getTime() - Date.now()
  if (!Number.isFinite(diff)) return value
  if (diff <= 0) return 'due now'
  const sec = Math.ceil(diff / 1000)
  if (sec < 60) return `in ${sec}s`
  const min = Math.ceil(sec / 60)
  if (min < 60) return `in ${min}m`
  return `in ${Math.ceil(min / 60)}h`
}

function durationBetween(start?: string, end?: string): string | null {
  if (!start || !end) return null
  const diff = new Date(end).getTime() - new Date(start).getTime()
  if (!Number.isFinite(diff) || diff < 0) return null
  if (diff < 1000) return `${diff}ms`
  const sec = Math.round(diff / 1000)
  if (sec < 60) return `${sec}s`
  return `${Math.round(sec / 60)}m`
}
