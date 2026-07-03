import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { relativeTime, statusTone } from '../lib/format.ts'
import type { BeaconEvent, EventsResponse, EventSeverity } from '../types/index.ts'
import { Panel } from '../components/panels/Panel.tsx'
import { Button, EmptyState, StatusChip, useToast } from '../components/ui/index.ts'

type StateFilter = 'open' | 'acknowledged' | 'snoozed' | 'all'
type SeverityFilter = EventSeverity | 'all'
type EventAction = 'ack' | 'snooze' | 'open'

const severities: EventSeverity[] = ['critical', 'warning', 'info']
const snoozeDurations = ['1h', '8h', '24h']

export function IncidentsPage() {
  const [state, setState] = useState<StateFilter>('open')
  const [severity, setSeverity] = useState<SeverityFilter>('all')
  const [appFilter, setAppFilter] = useState('all')
  const [snoozeDuration, setSnoozeDuration] = useState('1h')
  const query = useQuery({ queryKey: ['events'], queryFn: () => apiFetch<EventsResponse>('/api/events'), staleTime: 15_000 })
  const events = query.data?.events ?? []
  const appOptions = useMemo(() => [...new Set(events.map(event => event.app).filter(Boolean))].sort(), [events])
  const filtered = useMemo(() => events.filter((event) => {
    const eventState = event.state || 'open'
    const stateMatches = state === 'all' || eventState === state
    const severityMatches = severity === 'all' || event.severity === severity
    const appMatches = appFilter === 'all' || event.app === appFilter
    return stateMatches && severityMatches && appMatches
  }), [appFilter, events, severity, state])
  const grouped = useMemo(() => severities.map(level => ({
    severity: level,
    events: filtered.filter(event => event.severity === level),
  })).filter(group => severity === 'all' || group.events.length > 0), [filtered, severity])

  return (
    <Panel title="Incidents" loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
      <div className="incident-toolbar">
        <div className="app-filters" aria-label="Incident state filters">
          {(['open', 'acknowledged', 'snoozed', 'all'] as const).map((value) => (
            <button key={value} type="button" className={`filter-btn ${state === value ? 'active' : ''}`} onClick={() => setState(value)}>{value}</button>
          ))}
        </div>
        <div className="app-filters" aria-label="Incident severity filters">
          {(['all', 'critical', 'warning', 'info'] as const).map((value) => (
            <button key={value} type="button" className={`filter-btn ${severity === value ? 'active' : ''}`} onClick={() => setSeverity(value)}>{value}</button>
          ))}
        </div>
        <label className="incident-filter-field">
          <span>App</span>
          <select value={appFilter} onChange={event => setAppFilter(event.target.value)}>
            <option value="all">All apps</option>
            {appOptions.map(app => <option key={app} value={app}>{app}</option>)}
          </select>
        </label>
        <label className="incident-filter-field">
          <span>Snooze</span>
          <select value={snoozeDuration} onChange={event => setSnoozeDuration(event.target.value)}>
            {snoozeDurations.map(duration => <option key={duration} value={duration}>{duration}</option>)}
          </select>
        </label>
        <Link className="btn btn-small" to="/platform/notifications">Notification sinks</Link>
      </div>
      {filtered.length === 0 ? (
        <EmptyState icon="!" title="No incidents" hint="No events match the selected filters." />
      ) : (
        <div className="incident-severity-groups">
          {grouped.map(group => (
            <section className="incident-severity-group" key={group.severity} aria-labelledby={`incidents-${group.severity}`}>
              <h2 id={`incidents-${group.severity}`}>
                <StatusChip tone={statusTone(group.severity)} label={group.severity} />
                <span>{group.events.length}</span>
              </h2>
              <IncidentRows items={group.events} snoozeDuration={snoozeDuration} />
            </section>
          ))}
        </div>
      )}
    </Panel>
  )
}

function IncidentRows({ items, snoozeDuration }: { items: BeaconEvent[]; snoozeDuration: string }) {
  const action = useIncidentActions(snoozeDuration)
  return (
    <div className="compact-list">
      {[...items].sort((a, b) => new Date(b.occurredAt).getTime() - new Date(a.occurredAt).getTime()).map((event) => {
        const eventState = event.state || 'open'
        return (
          <article className="compact-row incident-row" key={event.id}>
            <StatusChip tone={statusTone(eventState)} label={eventState} />
            <div className="incident-main">
              <strong>{event.title}</strong>
              {event.body && <small>{event.body}</small>}
            </div>
            <small>{event.app || '-'}</small>
            <small>{relativeTime(event.occurredAt)}</small>
            <small>{new Date(event.occurredAt).toLocaleString()}</small>
            <div className="ops-actions">
              {eventState === 'acknowledged'
                ? <Button size="sm" variant="ghost" loading={action.isPending && action.variables?.id === event.id} onClick={() => action.mutate({ id: event.id, name: 'open' })}>Open</Button>
                : <Button size="sm" variant="ghost" loading={action.isPending && action.variables?.id === event.id && action.variables?.name === 'ack'} onClick={() => action.mutate({ id: event.id, name: 'ack' })}>Ack</Button>}
              {eventState !== 'snoozed' && (
                <Button size="sm" variant="ghost" loading={action.isPending && action.variables?.id === event.id && action.variables?.name === 'snooze'} onClick={() => action.mutate({ id: event.id, name: 'snooze' })}>Snooze</Button>
              )}
            </div>
          </article>
        )
      })}
    </div>
  )
}

function useIncidentActions(duration: string) {
  const queryClient = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: EventAction }) => apiFetch(`/api/events/${id}/${name}`, {
      method: 'POST',
      headers: name === 'snooze' ? { 'Content-Type': 'application/json' } : undefined,
      body: name === 'snooze' ? JSON.stringify({ duration, note: 'snoozed from incidents page' }) : undefined,
    }),
    onSuccess: (_, variables) => {
      toast({ kind: 'success', title: `Incident ${variables.name === 'ack' ? 'acknowledged' : variables.name === 'snooze' ? 'snoozed' : 'opened'}` })
      queryClient.invalidateQueries({ queryKey: ['events'] })
    },
    onError: (error, variables) => toast({ kind: 'error', title: `Incident ${variables.name} failed`, description: error instanceof Error ? error.message : String(error) }),
  })
}
