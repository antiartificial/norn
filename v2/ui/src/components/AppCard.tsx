import { useState, useEffect } from 'react'
import type { AccessPattern, AppStatus, RepoSpec, CanaryStatus, ServiceManifestEntry } from '../types/index.ts'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import { Tooltip } from './Tooltip.tsx'

function truncateURL(url: string): string {
  const stripped = url.replace(/\.git$/, '')
  const parts = stripped.split('/')
  return parts.slice(-2).join('/')
}

function repoWebURL(repo: RepoSpec): string | null {
  if (repo.repoWeb) return repo.repoWeb
  const gitea = repo.url.match(/gitea\.default\.svc\.cluster\.local:\d+\/(.+?)(?:\.git)?$/)
  if (gitea) return `https://gitea.slopistry.com/${gitea[1]}`
  const gh = repo.url.match(/github\.com[:/](.+?)(?:\.git)?$/)
  if (gh) return `https://github.com/${gh[1]}`
  return null
}

function endpointHostname(value: string): string {
  try {
    return new URL(value).hostname
  } catch {
    return value.split('/')[0]
  }
}

function endpointAuthority(value: string): string {
  try {
    const parsed = new URL(value)
    return parsed.port ? `${parsed.hostname}:${parsed.port}` : parsed.hostname
  } catch {
    return value.split('/')[0]
  }
}

function endpointLabel(value: string): string {
  return endpointAuthority(value) || endpointHostname(value)
}

function isCleanGatewayEndpoint(value: string): boolean {
  if (typeof window === 'undefined') return false
  try {
    const parsed = new URL(value)
    return parsed.protocol === 'https:' && parsed.hostname === window.location.hostname && parsed.port !== ''
  } catch {
    return false
  }
}

function gatewayURL(value: string): string | null {
  if (typeof window === 'undefined') return null
  const authority = endpointAuthority(value)
  if (!authority) return null
  return `${window.location.origin}/api/wake-gateway/${encodeURIComponent(authority)}`
}

function CopyBadge({ url, icon, label, region, className }: {
  url: string, icon: string, label: string, region?: string, className: string
}) {
  const [copied, setCopied] = useState(false)
  const handleCopy = () => {
    navigator.clipboard.writeText(url)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <Tooltip text={copied ? 'Copied!' : url}>
      <span className={`endpoint-badge ${className}`} onClick={handleCopy}>
        <i className={`fawsb ${icon}`} /> {label}
        {region && <span className="endpoint-region">{region}</span>}
      </span>
    </Tooltip>
  )
}

function nodeLabel(provider?: string, region?: string, name?: string): { icon: string; text: string } {
  switch (provider) {
    case 'local': return { icon: 'fa-laptop', text: 'local' }
    case 'do': return { icon: 'fa-cloud', text: region ? `do-${region}` : 'do' }
    case 'hz': return { icon: 'fa-cloud', text: region ? `hz-${region}` : 'hz' }
    default: return { icon: 'fa-server', text: name ?? 'remote' }
  }
}

function CanaryIndicator({ appId }: { appId: string }) {
  const [canary, setCanary] = useState<CanaryStatus | null>(null)
  const [promoting, setPromoting] = useState(false)
  const [promoteMsg, setPromoteMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  useEffect(() => {
    let cancelled = false
    fetch(apiUrl(`/api/apps/${appId}/canary`), fetchOpts)
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (!cancelled && data) setCanary(data) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [appId])

  if (!canary || !canary.isCanary) return null

  const handlePromote = async () => {
    setPromoting(true)
    setPromoteMsg(null)
    try {
      const res = await fetch(apiUrl(`/api/apps/${appId}/promote`), { ...fetchOpts, method: 'POST' })
      if (res.ok) {
        setPromoteMsg({ type: 'success', text: 'Promoted' })
        setCanary(prev => prev ? { ...prev, isCanary: false, status: 'promoted' } : prev)
      } else {
        const data = await res.json().catch(() => ({ error: 'Unknown error' }))
        setPromoteMsg({ type: 'error', text: data.error || 'Promote failed' })
      }
    } catch (e) {
      setPromoteMsg({ type: 'error', text: `Promote failed: ${e}` })
    }
    setPromoting(false)
  }

  return (
    <div className="app-card-canary">
      <Tooltip text={canary.statusDescription ?? canary.status}>
        <span className="canary-badge">
          <i className="fawsb fa-bird" /> Canary: {canary.status}
        </span>
      </Tooltip>
      {promoteMsg ? (
        <span className={`canary-msg canary-msg-${promoteMsg.type}`}>{promoteMsg.text}</span>
      ) : (
        <button className="btn btn-small" disabled={promoting} onClick={handlePromote}>
          {promoting ? <span className="btn-spinner" /> : <i className="fawsb fa-arrow-up" />}
          Promote
        </button>
      )}
    </div>
  )
}

function EndpointBadges({ spec, allocations, services, activeIngress, appId, onToggleEndpoint }: {
  spec: AppStatus['spec'],
  allocations: AppStatus['allocations'],
  services?: ServiceManifestEntry[],
  activeIngress?: Set<string>,
  appId: string,
  onToggleEndpoint?: (appId: string, hostname: string, enabled: boolean) => void,
}) {
  const serviceEndpoints = (services ?? [])
    .filter(service => service.type === 'service')
    .flatMap(service => service.endpoints ?? [])
  const external = serviceEndpoints.length > 0 ? serviceEndpoints : (spec.endpoints ?? [])
  const seen = new Set<string>()
  const internal: { host: string; url: string; provider?: string; region?: string; nodeName?: string }[] = []
  for (const service of services ?? []) {
    if (service.type !== 'service') continue
    for (const instance of service.instances ?? []) {
      if (!instance.address || !instance.port || seen.has(`${instance.address}:${instance.port}`)) continue
      const host = `${instance.address}:${instance.port}`
      seen.add(host)
      internal.push({ host, url: `http://${host}`, nodeName: instance.node })
    }
  }
  if (internal.length === 0) {
    for (const a of allocations) {
      if (a.status !== 'running' || !a.nodeAddress) continue
      const port = Object.values(spec.processes).find(p => p.port)?.port
      const url = port ? `http://${a.nodeAddress}:${port}` : `http://${a.nodeAddress}`
      const host = port ? `${a.nodeAddress}:${port}` : a.nodeAddress
      if (seen.has(host)) continue
      seen.add(host)
      internal.push({ host, url, provider: a.nodeProvider, region: a.nodeRegion, nodeName: a.nodeName })
    }
  }
  if (external.length === 0 && internal.length === 0) return null
  const hasIngress = activeIngress && activeIngress.size > 0
  const hasCleanGateway = external.some(ep => isCleanGatewayEndpoint(ep.url))
  return (
    <div className="app-card-endpoints">
      {external.map(ep => {
        const hostname = endpointHostname(ep.url)
        const label = endpointLabel(ep.url)
        const isActive = activeIngress?.has(hostname) ?? false
        const gateway = hasCleanGateway ? null : gatewayURL(ep.url)
        return (
          <span key={ep.url} className="endpoint-group">
            <CopyBadge url={ep.url} icon="fa-globe" label={label} region={ep.region} className="external" />
            {gateway && (
              <CopyBadge url={gateway} icon="fa-route" label="gateway" className="gateway" />
            )}
            {hasIngress && onToggleEndpoint && (
              <Tooltip text={isActive ? 'Disable endpoint' : 'Enable endpoint'}>
                <i
                  className={`fawsb endpoint-toggle ${isActive ? 'fa-cloud active' : 'fa-cloud-slash'}`}
                  onClick={() => onToggleEndpoint(appId, hostname, !isActive)}
                />
              </Tooltip>
            )}
          </span>
        )
      })}
      {internal.map(ep => {
        const node = nodeLabel(ep.provider, ep.region, ep.nodeName)
        return (
          <span key={ep.url} className="endpoint-group">
            <CopyBadge url={ep.url} icon="fa-link" label={ep.host} className="internal" />
            <span className="node-badge">
              <i className={`fawsb ${node.icon}`} /> {node.text}
            </span>
          </span>
        )
      })}
    </div>
  )
}

function idleCandidateTooltip(patterns: AccessPattern[]): string {
  return patterns
    .map(pattern => {
      const quiet = pattern.quietForHours !== undefined ? `, quiet ${Math.round(pattern.quietForHours)}h` : ''
      const reason = pattern.idleReason ? `: ${pattern.idleReason}` : ''
      return `${pattern.process}${quiet}${reason}`
    })
    .join('\n')
}

interface Props {
  app: AppStatus
  busy: boolean
  activeIngress?: Set<string>
  services?: ServiceManifestEntry[]
  idleCandidates?: AccessPattern[]
  onPreflight: (appId: string) => void
  onDeploy: (appId: string) => void
  onRestart: (appId: string) => void
  onScale: (appId: string) => void
  onViewLogs: (appId: string) => void
  onExec: (appId: string) => void
  onSnapshots?: (appId: string) => void
  onCron?: (appId: string) => void
  onFunction?: (appId: string) => void
  onToggleEndpoint?: (appId: string, hostname: string, enabled: boolean) => void
}

export function AppCard({ app, busy, activeIngress, services, idleCandidates = [], onPreflight, onDeploy, onRestart, onScale, onViewLogs, onExec, onSnapshots, onCron, onFunction, onToggleEndpoint }: Props) {
  const { spec, healthy, nomadStatus } = app
  const allocations = app.allocations ?? []

  const allocationSummary = app.allocationSummary ?? {
    running: allocations.filter(a => a.status === 'running').length,
    active: allocations.filter(a => a.lifecycle !== 'retained' && !['complete', 'failed', 'lost'].includes(a.status)).length,
    retained: allocations.filter(a => a.lifecycle === 'retained' || ['complete', 'failed', 'lost'].includes(a.status)).length,
    total: allocations.length,
  }
  const runningCount = allocationSummary.running
  const activeCount = allocationSummary.active
  const retainedCount = allocationSummary.retained

  // Group breakdown
  const groups: Record<string, { running: number; active: number; retained: number; total: number }> = {}
  for (const alloc of allocations) {
    if (!groups[alloc.taskGroup]) groups[alloc.taskGroup] = { running: 0, active: 0, retained: 0, total: 0 }
    groups[alloc.taskGroup].total++
    if (alloc.status === 'running') groups[alloc.taskGroup].running++
    if (alloc.lifecycle === 'retained' || ['complete', 'failed', 'lost'].includes(alloc.status)) {
      groups[alloc.taskGroup].retained++
    } else {
      groups[alloc.taskGroup].active++
    }
  }

  const processes = spec.processes ?? {}
  const hasCron = Object.values(processes).some(p => p.schedule)
  const hasFunctions = Object.values(processes).some(p => p.function)

  return (
    <div className={`app-card ${healthy ? 'healthy' : 'unhealthy'}`}>
      <div className="app-card-header">
        <div className="app-card-title">
          <Tooltip text={healthy ? 'All allocations healthy' : 'Unhealthy'}>
            <span className={`health-dot ${healthy ? 'green' : 'red'}`} />
          </Tooltip>
          <h3>{spec.name}</h3>
          <span className="nomad-status">{nomadStatus}</span>
          {idleCandidates.length > 0 && (
            <Tooltip text={idleCandidateTooltip(idleCandidates)}>
              <span className="idle-candidate-badge" aria-label="Idle candidate">
                <i className="fawsb fa-moon" />
              </span>
            </Tooltip>
          )}
        </div>
        <div className="app-card-ready-group">
          <Tooltip text={`${runningCount} running, ${activeCount} active, ${retainedCount} retained`}>
            <div className="alloc-summary">{runningCount} live</div>
          </Tooltip>
        </div>
      </div>

      {/* Processes */}
      {spec.processes && Object.keys(spec.processes).length > 0 && (
        <div className="app-card-processes">
          {Object.entries(spec.processes).map(([name, proc]) => (
            <span key={name} className="process-badge">
              {name}
              {proc.port ? `:${proc.port}` : ''}
              {proc.metrics?.enabled ? ' metrics' : ''}
            </span>
          ))}
        </div>
      )}

      {/* Allocation group breakdown */}
      {Object.keys(groups).length > 1 && (
        <div className="app-card-meta">
          {Object.entries(groups).map(([group, counts]) => (
            <span key={group} className="process-badge">
              {group}: {counts.running} live{counts.retained ? `, ${counts.retained} retained` : ''}
            </span>
          ))}
        </div>
      )}

      {/* Infrastructure badges */}
      {spec.infrastructure && (
        <div className="app-card-services">
          {spec.infrastructure.postgres && (
            <Tooltip text={`Database: ${spec.infrastructure.postgres.database}`}>
              <span className="infra-badge"><i className="fawsb fa-database" /> PG</span>
            </Tooltip>
          )}
          {spec.infrastructure.redis && (
            <Tooltip text={`Redis: ${spec.infrastructure.redis.namespace ?? 'default'}`}>
              <span className="infra-badge"><i className="fawsb fa-bolt" /> Redis</span>
            </Tooltip>
          )}
          {spec.infrastructure.kafka && (
            <Tooltip text={`Topics: ${spec.infrastructure.kafka.topics?.join(', ') ?? 'none'}`}>
              <span className="infra-badge"><i className="fawsb fa-bolt" /> Kafka</span>
            </Tooltip>
          )}
          {spec.infrastructure.nats && (
            <Tooltip text={`Streams: ${spec.infrastructure.nats.streams?.join(', ') ?? 'none'}`}>
              <span className="infra-badge"><i className="fawsb fa-bolt" /> NATS</span>
            </Tooltip>
          )}
          {spec.infrastructure.objectStorage && (
            <Tooltip text={`Object storage: ${spec.infrastructure.objectStorage.buckets?.map(b => b.name).join(', ') ?? 'no buckets'}`}>
              <span className="infra-badge"><i className="fawsb fa-hard-drive" /> S3</span>
            </Tooltip>
          )}
        </div>
      )}

      {/* Repo */}
      {spec.repo && (() => {
        const rUrl = repoWebURL(spec.repo)
        return (
          <div className="app-card-repo">
            {rUrl ? (
              <a href={rUrl} target="_blank" rel="noopener noreferrer" className="repo-link">
                <span className="repo-badge">
                  <i className="fawsb fa-code-branch" /> {truncateURL(spec.repo.url)}
                  <i className="fawsb fa-arrow-up-right-from-square repo-external-icon" />
                </span>
              </a>
            ) : (
              <Tooltip text={spec.repo.url}>
                <span className="repo-badge">
                  <i className="fawsb fa-code-branch" /> {truncateURL(spec.repo.url)}
                </span>
              </Tooltip>
            )}
            {spec.repo.branch && (
              <span className="repo-badge">{spec.repo.branch}</span>
            )}
          </div>
        )
      })()}

      {/* Endpoints */}
      <EndpointBadges spec={spec} allocations={allocations} services={services} activeIngress={activeIngress} appId={spec.name} onToggleEndpoint={onToggleEndpoint} />

      {/* Canary status */}
      <CanaryIndicator appId={spec.name} />

      <div className="app-card-actions">
        <Tooltip text="Validate, build, and test without deploying">
          <button onClick={() => onPreflight(spec.name)} disabled={busy} className="btn">
            <i className="fawsb fa-clipboard-check" /> Check
          </button>
        </Tooltip>
        <Tooltip text="Deploy latest from repo">
          <button onClick={() => onDeploy(spec.name)} disabled={busy} className="btn btn-primary">
            <i className="fawsb fa-rocket-launch" /> Deploy
          </button>
        </Tooltip>
        <Tooltip text="Rolling restart of all allocations">
          <button onClick={() => onRestart(spec.name)} disabled={busy} className="btn">
            <i className="fawsb fa-arrows-rotate" /> Restart
          </button>
        </Tooltip>
        <Tooltip text="Scale a task group">
          <button onClick={() => onScale(spec.name)} disabled={busy} className="btn">
            <i className="fawsb fa-up-right-and-down-left-from-center" /> Scale
          </button>
        </Tooltip>
        <Tooltip text="Stream live logs">
          <button onClick={() => onViewLogs(spec.name)} className="btn">
            <i className="fawsb fa-rectangle-code" /> Logs
          </button>
        </Tooltip>
        <Tooltip text="Open shell in running container">
          <button onClick={() => onExec(spec.name)} disabled={runningCount === 0} className="btn">
            <i className="fawsb fa-terminal" /> Shell
          </button>
        </Tooltip>
        {spec.infrastructure?.postgres && onSnapshots && (
          <Tooltip text="Database snapshots">
            <button onClick={() => onSnapshots(spec.name)} className="btn">
              <i className="fawsb fa-database" />
            </button>
          </Tooltip>
        )}
        {hasCron && onCron && (
          <Tooltip text="Cron jobs">
            <button onClick={() => onCron(spec.name)} className="btn">
              <i className="fawsb fa-clock" />
            </button>
          </Tooltip>
        )}
        {hasFunctions && onFunction && (
          <Tooltip text="Functions">
            <button onClick={() => onFunction(spec.name)} className="btn">
              <i className="fawsb fa-bolt" />
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )
}
