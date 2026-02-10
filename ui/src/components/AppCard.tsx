import type { AppStatus } from '../types/index.ts'
import { Tooltip } from './Tooltip.tsx'

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

function timeAgo(iso: string): string {
  if (!iso) return ''
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

interface Props {
  app: AppStatus
  onDeploy: (appId: string) => void
  onForge: (appId: string) => void
  onRestart: (appId: string) => void
  onRollback: (appId: string) => void
  onViewLogs: (appId: string) => void
}

export function AppCard({ app, onDeploy, onForge, onRestart, onRollback, onViewLogs }: Props) {
  const { spec, healthy, ready, commitSha, deployedAt } = app

  return (
    <div className={`app-card ${healthy ? 'healthy' : 'unhealthy'}`}>
      <div className="app-card-header">
        <div className="app-card-title">
          <Tooltip text={healthy ? 'All pods healthy' : 'One or more pods unhealthy'}>
            <span className={`health-dot ${healthy ? 'green' : 'red'}`} />
          </Tooltip>
          <h3>{spec.app}</h3>
          <Tooltip text={roleDescriptions[spec.role] ?? spec.role}>
            <span className="role-badge">
              <i className={`fawsb ${roleIcons[spec.role] ?? 'fa-box'}`} />
              {spec.role}
            </span>
          </Tooltip>
          <span className={`health-label ${healthy ? 'green' : 'red'}`}>
            {healthy ? 'healthy' : 'unhealthy'}
          </span>
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
        {ready === '0/0' && !commitSha && !deployedAt && (
          <Tooltip text="Provision K8s deployment, service, and DNS">
            <button onClick={() => onForge(spec.app)} className="btn btn-forge">
              <i className="fawsb fa-wand-magic-sparkles" /> Forge
            </button>
          </Tooltip>
        )}
        <Tooltip text="Build, test, and deploy a commit">
          <button onClick={() => onDeploy(spec.app)} className="btn btn-primary">
            <i className="fawsb fa-rocket-launch" /> Deploy
          </button>
        </Tooltip>
        <Tooltip text="Rolling restart of all pods">
          <button onClick={() => onRestart(spec.app)} className="btn">
            <i className="fawsb fa-arrows-rotate" /> Restart
          </button>
        </Tooltip>
        <Tooltip text="Revert to the previous image">
          <button onClick={() => onRollback(spec.app)} className="btn">
            <i className="fawsb fa-arrow-rotate-left" /> Rollback
          </button>
        </Tooltip>
        <Tooltip text="Stream live pod logs">
          <button onClick={() => onViewLogs(spec.app)} className="btn">
            <i className="fawsb fa-rectangle-code" /> Logs
          </button>
        </Tooltip>
      </div>
    </div>
  )
}
