import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../../lib/api.ts'
import { relativeTime, statusTone } from '../../lib/format.ts'
import type { BeaconEvent, CorrelatedEventsResponse, CorrelatedIncident, EventsResponse } from '../../types/index.ts'
import { Button, Drawer, EmptyState, ErrorState, Skeleton, StatusChip, useToast } from '../ui/index.ts'

type EventAction = 'ack' | 'snooze' | 'open'
const snoozeDurations = ['1h', '8h', '24h']

export type IncidentSelection =
  | { kind: 'correlated'; incident: CorrelatedIncident }
  | { kind: 'event'; event: BeaconEvent }

interface IncidentDrawerProps {
  selection: IncidentSelection | null
  onClose: () => void
}

function metadataCorrelationKey(event: BeaconEvent): string | null {
  const value = event.metadata?.correlationKey
  return typeof value === 'string' && value.trim() ? value : null
}

function latestEventId(selection: IncidentSelection): string {
  return selection.kind === 'correlated' ? selection.incident.latestEventId : selection.event.id
}

function incidentApp(selection: IncidentSelection): string {
  return selection.kind === 'correlated' ? selection.incident.app : selection.event.app
}

function incidentTitle(selection: IncidentSelection): string {
  return selection.kind === 'correlated' ? selection.incident.latestTitle : selection.event.title
}

function incidentSeverity(selection: IncidentSelection): string {
  return selection.kind === 'correlated' ? selection.incident.latestSeverity : selection.event.severity
}

function incidentState(selection: IncidentSelection): string {
  return selection.kind === 'correlated' ? 'open' : selection.event.state || 'open'
}

function incidentType(selection: IncidentSelection): string {
  return selection.kind === 'correlated' ? selection.incident.latestType : selection.event.type
}

function correlationKey(selection: IncidentSelection): string | null {
  return selection.kind === 'correlated' ? selection.incident.correlationKey : metadataCorrelationKey(selection.event)
}

export function useIncidentActions(duration: string) {
  const queryClient = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: EventAction }) => apiFetch(`/api/events/${id}/${name}`, {
      method: 'POST',
      headers: name === 'snooze' ? { 'Content-Type': 'application/json' } : undefined,
      body: name === 'snooze' ? JSON.stringify({ duration, note: 'snoozed from incident drawer' }) : undefined,
    }),
    onSuccess: (_, variables) => {
      toast({ kind: 'success', title: `Incident ${variables.name === 'ack' ? 'acknowledged' : variables.name === 'snooze' ? 'snoozed' : 'opened'}` })
      queryClient.invalidateQueries({ queryKey: ['events'] })
    },
    onError: (error, variables) => toast({ kind: 'error', title: `Incident ${variables.name} failed`, description: error instanceof Error ? error.message : String(error) }),
  })
}

export function IncidentDrawer({ selection, onClose }: IncidentDrawerProps) {
  const [duration, setDuration] = useState('1h')
  const action = useIncidentActions(duration)
  const key = selection ? correlationKey(selection) : null
  const app = selection ? incidentApp(selection) : ''
  const timeline = useQuery({
    queryKey: ['events', 'correlated', key],
    queryFn: () => apiFetch<CorrelatedEventsResponse>(`/api/events/correlated?key=${encodeURIComponent(key ?? '')}&limit=50`),
    enabled: Boolean(selection && key),
    staleTime: 15_000,
  })
  const activity = useQuery({
    queryKey: ['events', 'app', app, { limit: 20 }],
    queryFn: () => apiFetch<EventsResponse>(`/api/events?app=${encodeURIComponent(app)}&limit=20`),
    enabled: Boolean(selection && app),
    staleTime: 15_000,
  })
  const timelineEvents = useMemo(() => [...(timeline.data?.events ?? [])].sort((a, b) => new Date(b.occurredAt).getTime() - new Date(a.occurredAt).getTime()), [timeline.data?.events])
  const timelineIds = useMemo(() => {
    const ids = new Set(timelineEvents.map(event => event.id))
    if (selection?.kind === 'event') ids.add(selection.event.id)
    return ids
  }, [selection, timelineEvents])
  const activityEvents = useMemo(() => (activity.data?.events ?? [])
    .filter(event => !timelineIds.has(event.id))
    .sort((a, b) => new Date(b.occurredAt).getTime() - new Date(a.occurredAt).getTime()), [activity.data?.events, timelineIds])

  if (!selection) return null

  const id = latestEventId(selection)
  const title = incidentTitle(selection)

  return (
    <Drawer
      open
      title="Incident context"
      onClose={onClose}
      footer={(
        <>
          <Link className="btn btn-small" to={`/apps/${app}/overview`} onClick={onClose}>Open app</Link>
          <Link className="btn btn-small" to={`/apps/${app}/logs`} onClick={onClose}>Live logs</Link>
        </>
      )}
    >
      <div className="incident-drawer">
        <header className="incident-drawer-head">
          <div>
            <strong>{title}</strong>
            <small>{app} / {incidentType(selection)}</small>
          </div>
          <div className="incident-drawer-status">
            <StatusChip tone={statusTone(incidentSeverity(selection))} label={incidentSeverity(selection)} />
            <StatusChip tone={statusTone(incidentState(selection))} label={incidentState(selection)} />
          </div>
        </header>

        <div className="incident-drawer-actions" aria-label="Incident actions">
          <Button size="sm" variant="secondary" icon="fa-check" loading={action.isPending && action.variables?.id === id && action.variables?.name === 'ack'} onClick={() => action.mutate({ id, name: 'ack' })}>Ack</Button>
          <label className="incident-filter-field">
            <span>Snooze</span>
            <select value={duration} onChange={event => setDuration(event.target.value)}>
              {snoozeDurations.map(value => <option key={value} value={value}>{value}</option>)}
            </select>
          </label>
          <Button size="sm" variant="secondary" icon="fa-clock" loading={action.isPending && action.variables?.id === id && action.variables?.name === 'snooze'} onClick={() => action.mutate({ id, name: 'snooze' })}>Snooze</Button>
          <Button size="sm" variant="ghost" icon="fa-arrow-rotate-left" loading={action.isPending && action.variables?.id === id && action.variables?.name === 'open'} onClick={() => action.mutate({ id, name: 'open' })}>Re-open</Button>
        </div>

        {key ? (
          <IncidentDrawerSection title="Incident timeline">
            {timeline.isLoading ? <DrawerSkeleton /> : timeline.error instanceof Error ? (
              <ErrorState message={timeline.error.message} onRetry={() => timeline.refetch()} />
            ) : timelineEvents.length === 0 ? (
              <EmptyState icon="!" title="No timeline events" hint="No correlated events were returned." />
            ) : (
              <EventTimeline events={timelineEvents} />
            )}
          </IncidentDrawerSection>
        ) : selection.kind === 'event' ? (
          <IncidentDrawerSection title="Event detail">
            <EventTimeline events={[selection.event]} />
          </IncidentDrawerSection>
        ) : null}

        <IncidentDrawerSection title="Recent app activity">
          {activity.isLoading ? <DrawerSkeleton /> : activity.error instanceof Error ? (
            <ErrorState message={activity.error.message} onRetry={() => activity.refetch()} />
          ) : activityEvents.length === 0 ? (
            <EmptyState icon="." title="No recent activity" hint="No other app events were returned." />
          ) : (
            <EventTimeline events={activityEvents} />
          )}
        </IncidentDrawerSection>
      </div>
    </Drawer>
  )
}

function IncidentDrawerSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="incident-drawer-section">
      <h3>{title}</h3>
      {children}
    </section>
  )
}

function DrawerSkeleton() {
  return <div className="panel-skeleton"><Skeleton /><Skeleton /><Skeleton /></div>
}

function EventTimeline({ events }: { events: BeaconEvent[] }) {
  return (
    <div className="incident-timeline">
      {events.map(event => (
        <article className="incident-timeline-row" key={event.id}>
          <div className="incident-timeline-marker" aria-hidden="true" />
          <div className="incident-timeline-content">
            <div className="incident-timeline-meta">
              <StatusChip tone={statusTone(event.severity)} label={event.severity} />
              <code>{event.type}</code>
              <small>{relativeTime(event.occurredAt)}</small>
            </div>
            <strong>{event.title}</strong>
            {event.body && <p>{event.body}</p>}
            {event.metadata && Object.keys(event.metadata).length > 0 && (
              <details>
                <summary>metadata</summary>
                <pre>{JSON.stringify(event.metadata, null, 2)}</pre>
              </details>
            )}
          </div>
        </article>
      ))}
    </div>
  )
}
