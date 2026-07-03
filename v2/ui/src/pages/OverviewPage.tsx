import { NavLink } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'
import type { ActiveIncidentsResponse, Deployment, OperationsResponse, VersionResponse } from '../types/index.ts'
import { DeployList } from '../components/panels/DeployList.tsx'
import { ActiveIncidentList } from '../components/panels/IncidentList.tsx'
import { Metric, Panel } from '../components/panels/Panel.tsx'
import { OperationList } from '../components/panels/OperationList.tsx'
import { StatusBar } from '../components/StatusBar.tsx'
import { EmptyState } from '../components/ui/index.ts'

export function OverviewPage() {
  const ctx = useRuntimeContext()
  const incidents = useQuery({ queryKey: ['events', 'active'], queryFn: () => apiFetch<ActiveIncidentsResponse>('/api/events/active'), staleTime: 15_000 })
  const operations = useQuery({ queryKey: ['operations', 'active'], queryFn: () => apiFetch<OperationsResponse>('/api/operations/active'), staleTime: 15_000 })
  const deploys = useQuery({ queryKey: ['deployments', { limit: 5 }], queryFn: () => apiFetch<Deployment[]>('/api/deployments?limit=5'), staleTime: 15_000 })
  const version = useQuery({ queryKey: ['version'], queryFn: () => apiFetch<VersionResponse>('/api/version'), staleTime: 60_000 })
  const healthy = ctx.apps.filter((app) => app.healthy).length
  const unhealthy = ctx.apps.filter((app) => !app.healthy)
  const idle = ctx.accessPatterns.filter((pattern) => pattern.idleCandidate)

  return (
    <div className="overview-grid">
      <Panel title="Fleet health" loading={ctx.loading} error={ctx.error} onRetry={ctx.refetch}>
        <div className="metric-row">
          <Metric label="healthy" value={healthy} tone="success" />
          <Metric label="unhealthy" value={unhealthy.length} tone={unhealthy.length ? 'danger' : 'neutral'} />
          <Metric label="idle" value={idle.length} tone={idle.length ? 'warning' : 'neutral'} />
        </div>
        {unhealthy.length > 0 && <div className="compact-list">{unhealthy.map((app) => <NavLink key={app.spec.name} to={`/apps/${app.spec.name}/overview`}>{app.spec.name}</NavLink>)}</div>}
      </Panel>
      <Panel title="Active incidents" loading={incidents.isLoading} error={incidents.error instanceof Error ? incidents.error.message : null} onRetry={() => incidents.refetch()}>
        <ActiveIncidentList items={incidents.data?.incidents ?? []} />
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
    </div>
  )
}
