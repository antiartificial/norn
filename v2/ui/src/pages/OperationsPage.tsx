import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { relativeTime, statusTone } from '../lib/format.ts'
import type { OperationsResponse } from '../types/index.ts'
import { OperationList } from '../components/panels/OperationList.tsx'
import { Panel } from '../components/panels/Panel.tsx'
import { EmptyState, StatusChip } from '../components/ui/index.ts'

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
  if (sagaId) return <SagaTimeline sagaId={sagaId} />
  const query = useQuery({ queryKey: ['operations'], queryFn: () => apiFetch<OperationsResponse>('/api/operations'), staleTime: 15_000 })
  return <Panel title="Operations" loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}><OperationList items={query.data?.operations ?? []} /></Panel>
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
        return (
          <div className="compact-row saga-event-row" key={event.id ?? `${eventName(event)}-${timestamp ?? i}`}>
            <code>{timestamp ? relativeTime(timestamp) : 'unknown'}</code>
            <span>{eventName(event)}</span>
            {event.status && <StatusChip tone={statusTone(event.status)} label={event.status} />}
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
