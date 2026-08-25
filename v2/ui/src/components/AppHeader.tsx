import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { relativeTime, repoWebURL, statusTone } from '../lib/format.ts'
import { bucketByTime, markerPositions } from '../lib/timeseries.ts'
import type { AppAction } from '../runtime/AppRuntime.tsx'
import type { AppStatus, Deployment, EventsResponse } from '../types/index.ts'
import { Button, CopyButton, Sparkline, StatusChip, type SparklineTone } from './ui/index.ts'

const dayMs = 24 * 60 * 60 * 1000

export function AppHeader({
  app,
  onAction,
  onScale,
  deployRunning = false,
}: {
  app: AppStatus
  onAction: (appId: string, action: AppAction) => void
  onScale: (app: AppStatus) => void
  deployRunning?: boolean
}) {
  const appId = app.spec.name
  const deployments = useQuery({ queryKey: ['deployments', { limit: 50 }], queryFn: () => apiFetch<Deployment[]>('/api/deployments?limit=50'), staleTime: 15_000 })
  const events = useQuery({ queryKey: ['events', { app: appId, limit: 200 }], queryFn: () => apiFetch<EventsResponse>(`/api/events?app=${encodeURIComponent(appId)}&limit=200`), staleTime: 15_000 })
  const repo = repoWebURL(app.spec.repo?.url, app.spec.repo?.repoWeb)
  const appDeployments = useMemo(() => (deployments.data ?? []).filter(deployment => deployment.app === appId), [appId, deployments.data])
  const latestDeployment = appDeployments[0]
  const now = Date.now()
  const spark = useMemo(() => {
    const appEvents = (events.data?.events ?? []).filter(event => new Date(event.occurredAt).getTime() >= now - dayMs && new Date(event.occurredAt).getTime() <= now)
    const deployTimes = appDeployments.map(deployment => deployment.finishedAt).filter((value): value is string => Boolean(value))
    const critical = appEvents.some(event => event.severity === 'critical')
    const warning = appEvents.some(event => event.severity === 'warning')
    const tone: SparklineTone = critical ? 'danger' : warning ? 'warn' : 'neutral'
    return {
      series: bucketByTime(appEvents, { getTime: event => event.occurredAt, windowMs: dayMs, bucketCount: 48, now }),
      markers: markerPositions(deployTimes, { windowMs: dayMs, now }),
      tone,
    }
  }, [appDeployments, events.data?.events, now])

  const resourceSpecs = Object.entries(app.spec.processes ?? {}).map(([name, process]) => {
    const cpu = process.resources?.cpu ?? 'default'
    const memory = process.resources?.memory ?? 'default'
    return `${name} · cpu ${cpu} · mem ${memory}${typeof memory === 'number' ? ' MB' : ''}`
  })
  const runningAllocations = (app.allocations ?? []).filter(allocation => allocation.status === 'running')
  const nodeNames = Array.from(new Set(runningAllocations.map(allocation => allocation.nodeName).filter(Boolean)))
  const allocationText = `${app.allocations.length} ${app.allocations.length === 1 ? 'allocation' : 'allocations'}${nodeNames.length ? ` · running on ${nodeNames.join(', ')}` : ''}`

  return (
    <div className="app-command-header">
      <div className="app-command-main">
        <div className="app-detail-title">
          <h2>{appId}</h2>
          <StatusChip tone={app.healthy ? 'success' : 'danger'} label={app.healthy ? 'healthy' : 'unhealthy'} />
          <StatusChip tone={statusTone(app.nomadStatus)} label={app.nomadStatus} />
        </div>
        <div className="app-command-version">
          {latestDeployment ? (
            <>
              <span>deployed <code>{latestDeployment.commitSha.slice(0, 7)}</code></span>
              <CopyButton value={latestDeployment.commitSha} label="Copy deployment SHA" />
              <span title={latestDeployment.finishedAt ? new Date(latestDeployment.finishedAt).toLocaleString() : undefined}>{relativeTime(latestDeployment.finishedAt ?? latestDeployment.startedAt)}</span>
            </>
          ) : (
            <span>no deployments yet</span>
          )}
        </div>
        <div className="endpoint-line">
          {(app.spec.endpoints ?? []).map((endpoint) => (
            <span key={endpoint.url}>
              <a href={endpoint.url} target="_blank" rel="noreferrer">{endpoint.url} <i className="fawsb fa-arrow-up-right-from-square" aria-hidden="true" /></a>
              <CopyButton value={endpoint.url} label="Copy endpoint" />
            </span>
          ))}
          {repo && <a href={repo} target="_blank" rel="noreferrer"><i className="fawsb fa-code" aria-hidden="true" /> repo</a>}
        </div>
        <div className="app-command-meta">
          {resourceSpecs.map(spec => <span key={spec}>{spec}</span>)}
          <span>{allocationText}</span>
        </div>
      </div>
      <div className="app-command-side">
        <Sparkline series={spark.series} markers={spark.markers} tone={spark.tone} width={190} height={42} aria-label={`${appId} events over the last 24 hours`} />
        <small>Events · 24h</small>
        <div className="app-detail-actions">
          <Button variant="secondary" icon="fa-clipboard-check" onClick={() => onAction(appId, 'preflight')}>Preflight</Button>
          <Button variant="primary" icon="fa-rocket-launch" onClick={() => onAction(appId, 'deploy')} loading={deployRunning}>Deploy</Button>
          <Button variant="secondary" icon="fa-arrows-rotate" onClick={() => onAction(appId, 'restart')}>Restart</Button>
          <Button variant="secondary" icon="fa-arrow-up-arrow-down" onClick={() => onScale(app)}>Scale</Button>
        </div>
        <Link className="app-command-deploy-link" to={`/apps/${appId}/deploys`}>deploy history</Link>
      </div>
    </div>
  )
}
