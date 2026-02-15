import { useState } from 'react'
import type { AppStatus, RepoSpec } from '../types/index.ts'
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

function EndpointBadges({ spec, allocations, activeIngress, appId, onToggleEndpoint }: {
  spec: AppStatus['spec'],
  allocations: AppStatus['allocations'],
  activeIngress?: Set<string>,
  appId: string,
  onToggleEndpoint?: (appId: string, hostname: string, enabled: boolean) => void,
}) {
  const external = spec.endpoints ?? []
  // Dedupe internal addresses from running allocations
  const seen = new Set<string>()
  const internal: { host: string; url: string; provider?: string; region?: string; nodeName?: string }[] = []
  for (const a of allocations) {
    if (a.status !== 'running' || !a.nodeAddress) continue
    const port = Object.values(spec.processes).find(p => p.port)?.port
    const url = port ? `http://${a.nodeAddress}:${port}` : `http://${a.nodeAddress}`
    const host = port ? `${a.nodeAddress}:${port}` : a.nodeAddress
    if (seen.has(a.nodeAddress)) continue
    seen.add(a.nodeAddress)
    internal.push({ host, url, provider: a.nodeProvider, region: a.nodeRegion, nodeName: a.nodeName })
  }
  if (external.length === 0 && internal.length === 0) return null
  const hasIngress = activeIngress && activeIngress.size > 0
  return (
    <div className="app-card-endpoints">
      {external.map(ep => {
        const hostname = new URL(ep.url).hostname
        const isActive = activeIngress?.has(hostname) ?? false
        return (
          <span key={ep.url} className="endpoint-group">
            <CopyBadge url={ep.url} icon="fa-globe" label={hostname} region={ep.region} className="external" />
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

interface Props {
  app: AppStatus
  busy: boolean
  activeIngress?: Set<string>
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

export function AppCard({ app, busy, activeIngress, onDeploy, onRestart, onScale, onViewLogs, onExec, onSnapshots, onCron, onFunction, onToggleEndpoint }: Props) {
  const { spec, healthy, nomadStatus } = app
  const allocations = app.allocations ?? []

  const runningCount = allocations.filter(a => a.status === 'running').length
  const totalCount = allocations.length

  // Group breakdown
  const groups: Record<string, { running: number; total: number }> = {}
  for (const alloc of allocations) {
    if (!groups[alloc.taskGroup]) groups[alloc.taskGroup] = { running: 0, total: 0 }
    groups[alloc.taskGroup].total++
    if (alloc.status === 'running') groups[alloc.taskGroup].running++
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
        </div>
        <div className="app-card-ready-group">
          <Tooltip text={`${runningCount} of ${totalCount} allocations running`}>
            <div className="alloc-summary">{runningCount}/{totalCount}</div>
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
            </span>
          ))}
        </div>
      )}

      {/* Allocation group breakdown */}
      {Object.keys(groups).length > 1 && (
        <div className="app-card-meta">
          {Object.entries(groups).map(([group, counts]) => (
            <span key={group} className="process-badge">
              {group}: {counts.running}/{counts.total}
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
      <EndpointBadges spec={spec} allocations={allocations} activeIngress={activeIngress} appId={spec.name} onToggleEndpoint={onToggleEndpoint} />

      <div className="app-card-actions">
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
