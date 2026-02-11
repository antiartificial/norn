import type { AppStatus, ForgeStatus, HealthCheck } from '../types/index.ts'
import { Tooltip } from './Tooltip.tsx'
import { Sparkline } from './Sparkline.tsx'

const roleIcons: Record<string, string> = {
  webserver: 'fa-server',
  worker: 'fa-gear',
  cron: 'fa-clock',
}

const roleDescriptions: Record<string, string> = {
  webserver: 'Serves HTTP traffic',
  worker: 'Background job processor',
  cron: 'Scheduled task runner',
}

function truncateURL(url: string): string {
  // Show last path segments: norn/hello-norn.git → norn/hello-norn
  const stripped = url.replace(/\.git$/, '')
  const parts = stripped.split('/')
  return parts.slice(-2).join('/')
}

function timeAgo(iso: string): string {
  if (!iso) return ''
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

function forgeBadge(status: ForgeStatus) {
  switch (status) {
    case 'forged':
      return (
        <Tooltip text="Infrastructure fully provisioned">
          <span className="forge-badge forged">
            <i className="fawsb fa-hammer" /> forged
          </span>
        </Tooltip>
      )
    case 'forge_failed':
      return (
        <Tooltip text="Forge partially completed — can resume or tear down">
          <span className="forge-badge partial">
            <i className="fawsb fa-circle-exclamation" /> partial
          </span>
        </Tooltip>
      )
    case 'forging':
      return (
        <Tooltip text="Forge in progress">
          <span className="forge-badge in-progress">
            <span className="badge-spinner" /> forging
          </span>
        </Tooltip>
      )
    case 'tearing_down':
      return (
        <Tooltip text="Teardown in progress">
          <span className="forge-badge in-progress">
            <span className="badge-spinner" /> tearing down
          </span>
        </Tooltip>
      )
    default:
      return null
  }
}

interface Props {
  app: AppStatus
  busy?: boolean
  activeOp?: string // 'deploying' | 'forging' | 'tearing_down' | 'restarting' | 'rolling_back'
  healthChecks?: HealthCheck[]
  onDeploy: (appId: string) => void
  onForge: (appId: string) => void
  onTeardown: (appId: string) => void
  onRestart: (appId: string) => void
  onRollback: (appId: string) => void
  onViewLogs: (appId: string) => void
  onHealthClick?: () => void
}

export function AppCard({ app, busy, activeOp, healthChecks, onDeploy, onForge, onTeardown, onRestart, onRollback, onViewLogs, onHealthClick }: Props) {
  const { spec, healthy, ready, commitSha, deployedAt } = app
  const forgeStatus: ForgeStatus = app.forgeState?.status ?? 'unforged'
  const isForged = forgeStatus === 'forged'
  const hasPods = app.pods && app.pods.length > 0

  return (
    <div className={`app-card ${healthy ? 'healthy' : 'unhealthy'}`}>
      <div className="app-card-header">
        <div className="app-card-title">
          <Tooltip text={healthy ? 'All pods healthy' : 'One or more pods unhealthy'}>
            <span className={`health-dot ${healthy ? 'green' : 'red'}`} />
          </Tooltip>
          <h3>{spec.app}</h3>
          {spec.core && (
            <Tooltip text="Core norn infrastructure component">
              <span className="core-badge">
                <i className="fawsb fa-shield" /> core
              </span>
            </Tooltip>
          )}
          <Tooltip text={roleDescriptions[spec.role] ?? spec.role}>
            <span className="role-badge">
              <i className={`fawsb ${roleIcons[spec.role] ?? 'fa-box'}`} />
              {spec.role}
            </span>
          </Tooltip>
          {forgeBadge(forgeStatus)}
          <span className={`health-label ${healthy ? 'green' : 'red'}`}>
            {healthy ? 'healthy' : 'unhealthy'}
          </span>
          {healthChecks && healthChecks.length > 0 && (
            <Sparkline checks={healthChecks} onClick={onHealthClick} />
          )}
        </div>
        <Tooltip text={`${ready} pods ready`}>
          <div className="app-card-ready">{ready}</div>
        </Tooltip>
      </div>

      <div className="app-card-meta">
        {commitSha && (
          <Tooltip text={`Full SHA: ${commitSha}`}>
            <span className="commit">
              <i className="fawsb fa-code" /> {commitSha.slice(0, 7)}
            </span>
          </Tooltip>
        )}
        {deployedAt && (
          <Tooltip text={new Date(deployedAt).toLocaleString()}>
            <span className="deployed">
              deployed {timeAgo(deployedAt)}
            </span>
          </Tooltip>
        )}
        {!commitSha && !deployedAt && (
          <span className="no-deploys">never deployed</span>
        )}
      </div>

      {spec.repo && (
        <div className="app-card-repo">
          <Tooltip text={spec.repo.url}>
            <span className="repo-badge">
              <i className="fawsb fa-code-branch" /> {truncateURL(spec.repo.url)}
            </span>
          </Tooltip>
          {spec.repo.autoDeploy && (
            <Tooltip text="Pushes to this repo auto-trigger deploys">
              <span className="auto-deploy-badge">
                <i className="fawsb fa-arrows-spin" /> auto-deploy
              </span>
            </Tooltip>
          )}
        </div>
      )}

      <div className="app-card-hosts">
        {spec.hosts?.external && (
          <Tooltip text="Publicly accessible hostname (via Cloudflare Tunnel)">
            <span className="host external">
              <i className="fawsb fa-globe" /> {spec.hosts.external}
            </span>
          </Tooltip>
        )}
        {spec.hosts?.internal && (
          <Tooltip text="Cluster-internal service DNS">
            <span className="host internal">
              <i className="fawsb fa-link" /> {spec.hosts.internal}
            </span>
          </Tooltip>
        )}
      </div>

      {spec.services && (
        <div className="app-card-services">
          {spec.services.postgres && (
            <Tooltip text={`Database: ${spec.services.postgres.database}`}>
              <span className="service-badge"><i className="fawsb fa-database" /> PG</span>
            </Tooltip>
          )}
          {spec.services.kv && (
            <Tooltip text={`Key namespace: ${spec.services.kv.namespace}`}>
              <span className="service-badge"><i className="fawsb fa-bolt" /> KV</span>
            </Tooltip>
          )}
          {spec.services.events && (
            <Tooltip text={`Topics: ${spec.services.events.topics.join(', ')}`}>
              <span className="service-badge"><i className="fawsb fa-bolt" /> Events</span>
            </Tooltip>
          )}
          {spec.secrets && spec.secrets.length > 0 && (
            <Tooltip text={`${spec.secrets.length} secret(s): ${spec.secrets.join(', ')}`}>
              <span className="service-badge secrets">
                <i className="fawsb fa-key" /> {spec.secrets.length}
              </span>
            </Tooltip>
          )}
        </div>
      )}

      <div className="app-card-actions">
        {(forgeStatus === 'unforged') && (
          <Tooltip text="Provision K8s deployment, service, and DNS">
            <button onClick={() => onForge(spec.app)} disabled={busy} className={`btn btn-forge ${activeOp === 'forging' ? 'btn-busy' : ''}`}>
              {activeOp === 'forging' ? <span className="btn-spinner" /> : <i className="fawsb fa-wand-magic-sparkles" />} Forge
            </button>
          </Tooltip>
        )}
        {forgeStatus === 'forge_failed' && (
          <Tooltip text="Resume forge from the last completed step">
            <button onClick={() => onForge(spec.app)} disabled={busy} className={`btn btn-forge ${activeOp === 'forging' ? 'btn-busy' : ''}`}>
              {activeOp === 'forging' ? <span className="btn-spinner" /> : <i className="fawsb fa-wand-magic-sparkles" />} Resume Forge
            </button>
          </Tooltip>
        )}
        {(forgeStatus === 'forged' || forgeStatus === 'forge_failed') && (
          <Tooltip text="Remove all forged infrastructure">
            <button onClick={() => onTeardown(spec.app)} disabled={busy} className={`btn btn-danger ${activeOp === 'tearing_down' ? 'btn-busy' : ''}`}>
              {activeOp === 'tearing_down' ? <span className="btn-spinner" /> : <i className="fawsb fa-trash" />} Teardown
            </button>
          </Tooltip>
        )}
        {isForged && (
          <Tooltip text={spec.repo ? "Deploy latest from repo" : "Build, test, and deploy a commit"}>
            <button onClick={() => onDeploy(spec.app)} disabled={busy} className={`btn btn-primary ${activeOp === 'deploying' ? 'btn-busy' : ''}`}>
              {activeOp === 'deploying' ? <span className="btn-spinner" /> : <i className="fawsb fa-rocket-launch" />} {spec.repo ? 'Deploy Latest' : 'Deploy'}
            </button>
          </Tooltip>
        )}
        {isForged && commitSha && (
          <>
            <Tooltip text="Rolling restart of all pods">
              <button onClick={() => onRestart(spec.app)} disabled={busy} className={`btn ${activeOp === 'restarting' ? 'btn-busy' : ''}`}>
                {activeOp === 'restarting' ? <span className="btn-spinner" /> : <i className="fawsb fa-arrows-rotate" />} Restart
              </button>
            </Tooltip>
            <Tooltip text="Revert to the previous image">
              <button onClick={() => onRollback(spec.app)} disabled={busy} className={`btn ${activeOp === 'rolling_back' ? 'btn-busy' : ''}`}>
                {activeOp === 'rolling_back' ? <span className="btn-spinner" /> : <i className="fawsb fa-arrow-rotate-left" />} Rollback
              </button>
            </Tooltip>
          </>
        )}
        {hasPods && (
          <Tooltip text="Stream live pod logs">
            <button onClick={() => onViewLogs(spec.app)} className="btn">
              <i className="fawsb fa-rectangle-code" /> Logs
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )
}
