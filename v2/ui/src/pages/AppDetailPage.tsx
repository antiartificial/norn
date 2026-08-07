import { Navigate, useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { repoWebURL, statusTone } from '../lib/format.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'
import type { AccessPattern, AppStatus, Deployment, ServiceManifest } from '../types/index.ts'
import { CronPanel } from '../components/CronPanel.tsx'
import { ExecTerminal } from '../components/ExecTerminal.tsx'
import { FunctionPanel } from '../components/FunctionPanel.tsx'
import { LogViewer } from '../components/LogViewer.tsx'
import { SnapshotsPanel } from '../components/SnapshotsPanel.tsx'
import { DeploymentTimelineList } from '../components/panels/DeploymentTimelineList.tsx'
import { Metric, Panel } from '../components/panels/Panel.tsx'
import { Button, CopyButton, EmptyState, ErrorState, Skeleton, StatusChip, Tab, TabPanel, Tabs, TabsList } from '../components/ui/index.ts'

export function AppDetailPage() {
  const ctx = useRuntimeContext()
  const { id, tab = 'overview' } = useParams()
  const navigate = useNavigate()
  const app = ctx.apps.find((item) => item.spec.name === id)
  const validTabs = ['overview', 'logs', 'deploys', 'snapshots', 'cron', 'functions', 'shell']
  if (!validTabs.includes(tab)) return <Navigate to={`/apps/${id}/overview`} replace />
  if (ctx.loading) return <div className="panel-skeleton"><Skeleton /><Skeleton /><Skeleton /></div>
  if (!app) return <ErrorState message={`App ${id} was not found`} />
  const appId = app.spec.name
  const firstProcess = Object.values(app.spec.processes ?? {})[0]
  const functionProcesses = Object.entries(app.spec.processes ?? {}).filter(([, process]) => process.function).map(([name]) => name)
  const endpoints = app.spec.endpoints ?? []
  const repo = repoWebURL(app.spec.repo?.url, app.spec.repo?.repoWeb)

  return (
    <div className="app-detail">
      <div className="app-detail-header">
        <div>
          <div className="app-detail-title">
            <h2>{appId}</h2>
            <StatusChip tone={app.healthy ? 'success' : 'danger'} label={app.healthy ? 'healthy' : 'unhealthy'} />
            <StatusChip tone={statusTone(app.nomadStatus)} label={app.nomadStatus} />
          </div>
          <div className="endpoint-line">
            {endpoints.map((endpoint) => <span key={endpoint.url}><a href={endpoint.url} target="_blank" rel="noreferrer">{endpoint.url}</a><CopyButton value={endpoint.url} label="Copy endpoint" /></span>)}
            {repo && <a href={repo} target="_blank" rel="noreferrer"><i className="fawsb fa-code" aria-hidden /> repo</a>}
          </div>
        </div>
        <div className="app-detail-actions">
          <Button variant="secondary" icon="fa-clipboard-check" onClick={() => ctx.mutations.run(appId, 'preflight')}>Preflight</Button>
          <Button variant="primary" icon="fa-rocket-launch" onClick={() => ctx.mutations.run(appId, 'deploy')}>Deploy</Button>
          <Button variant="secondary" icon="fa-arrows-rotate" onClick={() => ctx.mutations.run(appId, 'restart')}>Restart</Button>
          <Button variant="secondary" icon="fa-arrow-up-arrow-down" onClick={() => ctx.mutations.scale(app)}>Scale</Button>
        </div>
      </div>
      {ctx.deployState?.appId === appId && <div className="deploy-inline-note" aria-live="polite">Live {ctx.deployState.operation} is running for this app.</div>}
      <Tabs value={tab} onValueChange={(next) => navigate(`/apps/${appId}/${next}`)}>
        <TabsList aria-label="App detail tabs">
          {validTabs.map((item) => <Tab key={item} value={item}>{item}</Tab>)}
        </TabsList>
        <TabPanel value="overview"><AppOverviewTab app={app} services={ctx.serviceManifest?.services.filter((service) => service.app === appId) ?? []} idleCandidates={ctx.accessPatterns.filter((pattern) => pattern.app === appId && pattern.idleCandidate)} /></TabPanel>
        <TabPanel value="logs"><div className="embedded-panel"><LogViewer appId={appId} healthPath={firstProcess?.health?.path} onClose={() => navigate(`/apps/${appId}/overview`)} /></div></TabPanel>
        <TabPanel value="deploys"><AppDeploysTab appId={appId} /></TabPanel>
        <TabPanel value="snapshots"><div className="embedded-panel"><SnapshotsPanel appId={appId} onClose={() => navigate(`/apps/${appId}/overview`)} /></div></TabPanel>
        <TabPanel value="cron"><div className="embedded-panel"><CronPanel appId={appId} onClose={() => navigate(`/apps/${appId}/overview`)} /></div></TabPanel>
        <TabPanel value="functions"><div className="embedded-panel"><FunctionPanel appId={appId} processes={functionProcesses} onClose={() => navigate(`/apps/${appId}/overview`)} /></div></TabPanel>
        <TabPanel value="shell"><div className="embedded-panel"><ExecTerminal appId={appId} onClose={() => navigate(`/apps/${appId}/overview`)} /></div></TabPanel>
      </Tabs>
    </div>
  )
}

function AppOverviewTab({ app, services, idleCandidates }: { app: AppStatus; services: ServiceManifest['services']; idleCandidates: AccessPattern[] }) {
  const processes = Object.entries(app.spec.processes ?? {})
  return (
    <div className="detail-grid">
      <Panel title="Processes"><div className="compact-list">{processes.map(([name, process]) => <div className="compact-row" key={name}><span>{name}</span><small>{process.port ? `:${process.port}` : process.command ?? process.schedule ?? 'worker'}</small></div>)}</div></Panel>
      <Panel title="Allocations"><div className="metric-row"><Metric label="running" value={app.allocationSummary?.running ?? 0} tone="success" /><Metric label="active" value={app.allocationSummary?.active ?? 0} tone="info" /><Metric label="retained" value={app.allocationSummary?.retained ?? 0} tone="neutral" /></div></Panel>
      <Panel title="Infrastructure"><div className="compact-list">{Object.keys(app.spec.infrastructure ?? {}).length === 0 ? <EmptyState icon="·" title="No backing services" hint="This app declares no infrastructure." /> : Object.keys(app.spec.infrastructure ?? {}).map((key) => <span key={key} className="process-badge">{key}</span>)}</div></Panel>
      <Panel title="Services"><div className="compact-list">{services.length === 0 ? <EmptyState icon="·" title="No services" hint="No service manifest entries yet." /> : services.map((service) => <div className="compact-row" key={service.name}><StatusChip tone={statusTone(service.status)} label={service.status} /><span>{service.name}</span><small>{service.reachability?.exposure}</small></div>)}</div></Panel>
      <Panel title="Secrets"><div className="compact-list">{(app.spec.secrets ?? []).length === 0 ? <EmptyState icon="·" title="No secrets" hint="No secret names declared." /> : app.spec.secrets?.map((secret) => <span key={secret} className="process-badge">{secret}</span>)}</div></Panel>
      <Panel title="Idle analysis">{idleCandidates.length === 0 ? <EmptyState icon="·" title="No idle signals" hint="Access patterns are active or unknown." /> : <div className="compact-list">{idleCandidates.map((pattern) => <div className="compact-row" key={pattern.process}><span>{pattern.process}</span><small>{pattern.recommendedAction}</small></div>)}</div>}</Panel>
    </div>
  )
}

function AppDeploysTab({ appId }: { appId: string }) {
  const deployments = useQuery({ queryKey: ['deployments', { app: appId }], queryFn: () => apiFetch<Deployment[]>(`/api/deployments?app=${encodeURIComponent(appId)}&limit=50`), staleTime: 15_000 })
  if (deployments.isLoading) return <div className="panel-skeleton"><Skeleton /><Skeleton /></div>
  if (deployments.error) return <ErrorState message={deployments.error instanceof Error ? deployments.error.message : 'Failed to load deployments'} onRetry={() => deployments.refetch()} />
  return <DeploymentTimelineList deployments={deployments.data ?? []} />
}
