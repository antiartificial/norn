import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import type { EventsResponse, EventSeverity } from '../types/index.ts'
import { IncidentList } from '../components/panels/IncidentList.tsx'
import { Panel } from '../components/panels/Panel.tsx'

type StateFilter = 'open' | 'acked' | 'all'
type SeverityFilter = EventSeverity | 'all'

export function IncidentsPage() {
  const [state, setState] = useState<StateFilter>('open')
  const [severity, setSeverity] = useState<SeverityFilter>('all')
  const query = useQuery({ queryKey: ['events'], queryFn: () => apiFetch<EventsResponse>('/api/events'), staleTime: 15_000 })
  const events = query.data?.events ?? []
  const filtered = useMemo(() => events.filter((event) => {
    const stateMatches = state === 'all' || event.state === state
    const severityMatches = severity === 'all' || event.severity === severity
    return stateMatches && severityMatches
  }), [events, severity, state])

  return (
    <Panel title="Incidents" loading={query.isLoading} error={query.error instanceof Error ? query.error.message : null} onRetry={() => query.refetch()}>
      <div className="app-filters" aria-label="Incident state filters">
        {(['open', 'acked', 'all'] as const).map((value) => (
          <button key={value} type="button" className={`filter-btn ${state === value ? 'active' : ''}`} onClick={() => setState(value)}>{value}</button>
        ))}
      </div>
      <div className="app-filters" aria-label="Incident severity filters">
        {(['all', 'critical', 'warning', 'info'] as const).map((value) => (
          <button key={value} type="button" className={`filter-btn ${severity === value ? 'active' : ''}`} onClick={() => setSeverity(value)}>{value}</button>
        ))}
      </div>
      <IncidentList items={filtered} />
    </Panel>
  )
}
