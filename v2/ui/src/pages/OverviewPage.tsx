import { useState } from 'react'
import { NavLink } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'
import type { ActiveIncidentsResponse, AppStatus, Deployment, OperationsResponse, VersionResponse } from '../types/index.ts'
import { DeployList } from '../components/panels/DeployList.tsx'
import { ActiveIncidentList } from '../components/panels/IncidentList.tsx'
import { Metric, Panel } from '../components/panels/Panel.tsx'
import { OperationList } from '../components/panels/OperationList.tsx'
import { StatusBar } from '../components/StatusBar.tsx'
import { EmptyState, StatusChip } from '../components/ui/index.ts'
import { IncidentDrawer, type IncidentSelection } from '../components/incidents/IncidentDrawer.tsx'

const healthRowLimit = 5
const incidentLimit = 5

export function OverviewPage() {
  const ctx = useRuntimeContext()
  const [selection, setSelection] = useState<IncidentSelection | null>(null)
  const incidents = useQuery({ queryKey: ['events', 'active'], queryFn: () => apiFetch<ActiveIncidentsResponse>('/api/events/active'), staleTime: 15_000 })
  const operations = useQuery({ queryKey: ['operations', 'active'], queryFn: () => apiFetch<OperationsResponse>('/api/operations/active'), staleTime: 15_000 })
  const deploys = useQuery({ queryKey: ['deployments', { limit: 5 }], queryFn: () => apiFetch<Deployment[]>('/api/deployments?limit=5'), staleTime: 15_000 })
  const version = useQuery({ queryKey: ['version'], queryFn: () => apiFetch<VersionResponse>('/api/version'), staleTime: 60_000 })
  const healthy = ctx.apps.filter((app) => app.healthy).length
  const unhealthy = ctx.apps.filter((app) => !app.healthy)
  const idle = ctx.accessPatterns.filter((pattern) => pattern.idleCandidate)
  const idleApps = idle.map(pattern => pattern.app)

  return (
    <div className="overview-grid">
      <Panel title="Fleet health" loading={ctx.loading} error={ctx.error} onRetry={ctx.refetch}>
        <div className="metric-row">
          <Metric label="healthy" value={healthy} tone="success" />
          <Metric label="unhealthy" value={unhealthy.length} tone={unhealthy.length ? 'danger' : 'neutral'} />
          <Metric label="idle" value={idle.length} tone={idle.length ? 'warning' : 'neutral'} />
        </div>
        <FleetHealthRows apps={unhealthy} names={[]} tone="danger" label="unhealthy" filter="unhealthy" />
        <FleetHealthRows apps={[]} names={idleApps} tone="warning" label="idle" filter="idle" />
      </Panel>
      <Panel title="Active incidents" loading={incidents.isLoading} error={incidents.error instanceof Error ? incidents.error.message : null} onRetry={() => incidents.refetch()}>
        <div className="incident-panel-scroll">
          <ActiveIncidentList items={incidents.data?.incidents ?? []} limit={incidentLimit} onSelect={(incident) => setSelection({ kind: 'correlated', incident })} />
        </div>
        {(incidents.data?.incidents.length ?? 0) > 0 && <NavLink className="panel-footer-link" to="/incidents">All incidents <i className="fawsb fa-arrow-right" aria-hidden="true" /></NavLink>}
      </Panel>
      <Panel title="Running operations" loading={operations.isLoading} error={operations.error instanceof Error ? operations.error.message : null} onRetry={() => operations.refetch()}>
        <OperationList items={operations.data?.operations ?? []} />
      </Panel>
      <Panel title="Recent deploys" loading={deploys.isLoading} error={deploys.error instanceof Error ? deploys.error.message : null} onRetry={() => deploys.refetch()}>
        <DeployList items={(deploys.data ?? []).slice(0, 5)} />
      </Panel>
      <Panel title="Platform" loading={version.isLoading} error={version.error instanceof Error ? version.error.message : null} onRetry={() => version.refetch()}>
        <div className="platform-summary">
          <div><span>Version</span><strong>{version.data?.version ?? 'dev'}</strong></div>
          <StatusBar />
        </div>
      </Panel>
      <section className="panel activity-panel" aria-live="polite">
        <h2>Activity</h2>
        {ctx.activity.length === 0 ? <EmptyState icon="•" title="No activity yet" hint="Hub events will appear here as they arrive." /> : (
          <div className="activity-list">
            {ctx.activity.map((entry) => <div key={entry.id}><span>{entry.event.type}</span><small>{entry.event.appId ?? ''}</small></div>)}
          </div>
        )}
      </section>
      <IncidentDrawer selection={selection} onClose={() => setSelection(null)} />
    </div>
  )
}

function FleetHealthRows({ apps, names, tone, label, filter }: { apps: AppStatus[]; names: string[]; tone: 'danger' | 'warning'; label: string; filter: string }) {
  const appNames = apps.length > 0 ? apps.map(app => app.spec.name) : names
  if (appNames.length === 0) return null
  const visible = appNames.slice(0, healthRowLimit)
  const more = appNames.length - visible.length
  return (
    <div className="fleet-health-group">
      <div className="compact-list">
        {visible.map(name => (
          <NavLink className="compact-row fleet-health-row" key={`${label}-${name}`} to={`/apps/${name}/overview`}>
            <StatusChip tone={tone} label={label} />
            <span className="compact-row-title">{name}</span>
            <i className="fawsb fa-angle-right compact-row-chevron" aria-hidden="true" />
          </NavLink>
        ))}
      </div>
      {more > 0 && <NavLink className="panel-footer-link" to={`/apps?filter=${filter}`}>+{more} more <i className="fawsb fa-arrow-right" aria-hidden="true" /> Apps</NavLink>}
    </div>
  )
}
