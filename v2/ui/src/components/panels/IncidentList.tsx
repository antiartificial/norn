import { useMutation, useQueryClient } from '@tanstack/react-query'
import { relativeTime, statusTone } from '../../lib/format.ts'
import type { BeaconEvent, CorrelatedIncident } from '../../types/index.ts'
import { apiFetch } from '../../lib/api.ts'
import { Button, EmptyState, StatusChip } from '../ui/index.ts'

function severityRank(severity: string): number {
  if (severity === 'critical') return 0
  if (severity === 'warning') return 1
  return 2
}

function useIncidentActions() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: 'ack' | 'snooze' }) => apiFetch(`/api/events/${id}/${name}`, { method: 'POST' }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['events'] }),
  })
}

export function ActiveIncidentList({ items }: { items: CorrelatedIncident[] }) {
  const action = useIncidentActions()
  if (items.length === 0) return <EmptyState icon="!" title="No active incidents" hint="Beacon events are quiet." />
  return (
    <div className="compact-list">
      {[...items].sort((a, b) => severityRank(a.latestSeverity) - severityRank(b.latestSeverity)).map((incident) => (
        <div className="compact-row" key={incident.correlationKey}>
          <StatusChip tone={statusTone(incident.latestSeverity)} label={incident.latestSeverity} />
          <span>{incident.latestTitle}</span>
          <small>{incident.app}</small>
          {incident.eventCount > 1 && <StatusChip tone="info" label={`${incident.eventCount} events`} />}
          <small>{relativeTime(incident.lastSeen)}</small>
          <Button size="sm" variant="ghost" onClick={() => action.mutate({ id: incident.latestEventId, name: 'ack' })}>Ack</Button>
          <Button size="sm" variant="ghost" onClick={() => action.mutate({ id: incident.latestEventId, name: 'snooze' })}>Snooze</Button>
        </div>
      ))}
    </div>
  )
}

export function IncidentList({ items }: { items: BeaconEvent[] }) {
  const action = useIncidentActions()
  if (items.length === 0) return <EmptyState icon="!" title="No incidents" hint="No events match the selected filters." />
  return (
    <div className="compact-list">
      {[...items].sort((a, b) => severityRank(a.severity) - severityRank(b.severity)).map((event) => (
        <div className="compact-row" key={event.id}>
          <StatusChip tone={statusTone(event.severity)} label={event.severity} />
          <StatusChip tone={statusTone(event.state)} label={event.state} />
          <span>{event.title}</span>
          <small>{event.app}</small>
          <small>{relativeTime(event.occurredAt)}</small>
          {event.body && <small>{event.body}</small>}
          <Button size="sm" variant="ghost" onClick={() => action.mutate({ id: event.id, name: 'ack' })}>Ack</Button>
          <Button size="sm" variant="ghost" onClick={() => action.mutate({ id: event.id, name: 'snooze' })}>Snooze</Button>
        </div>
      ))}
    </div>
  )
}
