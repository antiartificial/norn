import type { AppStatus } from '../types/index.ts'

const roleIcons: Record<string, string> = {
  webserver: 'fa-server',
  worker: 'fa-gears',
  cron: 'fa-clock',
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
  onRestart: (appId: string) => void
  onRollback: (appId: string) => void
  onViewLogs: (appId: string) => void
}

export function AppCard({ app, onDeploy, onRestart, onRollback, onViewLogs }: Props) {
  const { spec, healthy, ready, commitSha, deployedAt } = app

  return (
    <div className={`app-card ${healthy ? 'healthy' : 'unhealthy'}`}>
      <div className="app-card-header">
        <div className="app-card-title">
          <span className={`health-dot ${healthy ? 'green' : 'red'}`} />
          <h3>{spec.app}</h3>
          <span className="role-badge">
            <i className={`fa-solid ${roleIcons[spec.role] ?? 'fa-cube'}`} />
            {spec.role}
          </span>
          <span className={`health-label ${healthy ? 'green' : 'red'}`}>
            {healthy ? 'healthy' : 'unhealthy'}
          </span>
        </div>
        <div className="app-card-ready">{ready}</div>
      </div>

      <div className="app-card-meta">
        {commitSha && (
          <span className="commit">
            <i className="fa-solid fa-code-branch" /> {commitSha.slice(0, 7)}
          </span>
        )}
        {deployedAt && (
          <span className="deployed">
            deployed {timeAgo(deployedAt)}
          </span>
        )}
      </div>

      <div className="app-card-hosts">
        {spec.hosts?.external && (
          <span className="host external">
            <i className="fa-solid fa-globe" /> {spec.hosts.external}
          </span>
        )}
        {spec.hosts?.internal && (
          <span className="host internal">
            <i className="fa-solid fa-network-wired" /> {spec.hosts.internal}
          </span>
        )}
      </div>

      <div className="app-card-actions">
        <button onClick={() => onDeploy(spec.app)} className="btn btn-primary">
          <i className="fa-solid fa-rocket" /> Deploy
        </button>
        <button onClick={() => onRestart(spec.app)} className="btn">
          <i className="fa-solid fa-rotate" /> Restart
        </button>
        <button onClick={() => onRollback(spec.app)} className="btn">
          <i className="fa-solid fa-clock-rotate-left" /> Rollback
        </button>
        <button onClick={() => onViewLogs(spec.app)} className="btn">
          <i className="fa-solid fa-terminal" /> Logs
        </button>
      </div>
    </div>
  )
}
