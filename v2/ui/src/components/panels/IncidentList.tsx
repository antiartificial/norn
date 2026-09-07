import { relativeTime, statusTone } from '../../lib/format.ts'
import type { BeaconEvent, CorrelatedIncident } from '../../types/index.ts'
import { EmptyState, StatusChip } from '../ui/index.ts'

function severityRank(severity: string): number {
  if (severity === 'critical') return 0
  if (severity === 'warning') return 1
  return 2
}

export function sortIncidentsBySeverity<T extends { latestSeverity?: string; severity?: string; lastSeen?: string; occurredAt?: string }>(items: T[]): T[] {
  return [...items].sort((a, b) => {
    const severity = severityRank(a.latestSeverity ?? a.severity ?? '') - severityRank(b.latestSeverity ?? b.severity ?? '')
    if (severity !== 0) return severity
    return new Date(b.lastSeen ?? b.occurredAt ?? 0).getTime() - new Date(a.lastSeen ?? a.occurredAt ?? 0).getTime()
  })
}

export function ActiveIncidentList({ items, limit, onSelect }: { items: CorrelatedIncident[]; limit?: number; onSelect?: (incident: CorrelatedIncident) => void }) {
  if (items.length === 0) return <EmptyState icon="!" title="No active incidents" hint="Beacon events are quiet." />
  const visible = sortIncidentsBySeverity(items).slice(0, limit ?? items.length)
  return (
    <div className="compact-list">
      {visible.map((incident) => (
        <button className="compact-row compact-row-button" type="button" key={incident.latestEventId} onClick={() => onSelect?.(incident)}>
          <StatusChip tone={statusTone(incident.latestSeverity)} label={incident.latestSeverity} />
          <span className="compact-row-title">{incident.latestTitle}</span>
          <small>{incident.app}</small>
          <small>{incident.source}{incident.environment ? ` · ${incident.environment}` : ''}</small>
          {incident.eventCount > 1 && <StatusChip tone="info" label={`${incident.eventCount} events`} />}
          <small>{relativeTime(incident.lastSeen)}</small>
          <i className="fawsb fa-angle-right compact-row-chevron" aria-hidden="true" />
        </button>
      ))}
    </div>
  )
}

export function IncidentList({ items, onSelect }: { items: BeaconEvent[]; onSelect?: (event: BeaconEvent) => void }) {
  if (items.length === 0) return <EmptyState icon="!" title="No incidents" hint="No events match the selected filters." />
  return (
    <div className="compact-list">
      {[...items].sort((a, b) => severityRank(a.severity) - severityRank(b.severity)).map((event) => (
        <button className="compact-row compact-row-button" type="button" key={event.id} onClick={() => onSelect?.(event)}>
          <StatusChip tone={statusTone(event.severity)} label={event.severity} />
          <StatusChip tone={statusTone(event.state)} label={event.state} />
          <span className="compact-row-title">{event.title}</span>
          <small>{event.app}</small>
          <small>{relativeTime(event.occurredAt)}</small>
          {event.body && <small>{event.body}</small>}
          <i className="fawsb fa-angle-right compact-row-chevron" aria-hidden="true" />
        </button>
      ))}
    </div>
  )
}
