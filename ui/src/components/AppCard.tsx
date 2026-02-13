import { useState } from 'react'
import type { AppStatus, CronState, ForgeStatus, HealthCheck, RepoSpec } from '../types/index.ts'
import { Tooltip } from './Tooltip.tsx'
import { Sparkline } from './Sparkline.tsx'

const roleIcons: Record<string, string> = {
  webserver: 'fa-server',
  worker: 'fa-gear',
  cron: 'fa-clock',
  function: 'fa-bolt',
}

const roleDescriptions: Record<string, string> = {
  webserver: 'Serves HTTP traffic',
  worker: 'Background job processor',
  cron: 'Scheduled task runner',
  function: 'HTTP-triggered function',
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

function repoWebURL(repo: RepoSpec): string | null {
  if (repo.repoWeb) return repo.repoWeb
  const gitea = repo.url.match(/gitea\.default\.svc\.cluster\.local:\d+\/(.+?)(?:\.git)?$/)
  if (gitea) return `https://gitea.slopistry.com/${gitea[1]}`
  const gh = repo.url.match(/github\.com[:/](.+?)(?:\.git)?$/)
  if (gh) return `https://github.com/${gh[1]}`
  return null
}

function commitURL(repo: RepoSpec, sha: string): string | null {
  const base = repoWebURL(repo)
  if (!base) return null
  return `${base}/commit/${sha}`
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
  onScale?: (appId: string, replicas: number) => void
  onCommands?: () => void
  onHealthClick?: () => void
  onCronPanel?: (appId: string) => void
  onCronTrigger?: (appId: string) => void
  onFuncInvoke?: (appId: string) => void
  onFuncPanel?: (appId: string) => void
}

function cronStateBadge(cronState?: CronState) {
  if (!cronState) return null
  if (cronState.paused) {
    return (
      <Tooltip text="Cron schedule is paused">
        <span className="cron-state-badge paused">
          <i className="fawsb fa-pause" /> paused
        </span>
      </Tooltip>
    )
  }
  return (
    <Tooltip text={`Schedule: ${cronState.schedule}`}>
      <span className="cron-state-badge active">
        <i className="fawsb fa-clock" /> {cronState.schedule}
      </span>
    </Tooltip>
  )
}

export function AppCard({ app, busy, activeOp, healthChecks, onDeploy, onForge, onTeardown, onRestart, onRollback, onViewLogs, onScale, onCommands, onHealthClick, onCronPanel, onCronTrigger, onFuncInvoke, onFuncPanel }: Props) {
  const { spec, healthy, ready, commitSha, deployedAt } = app
  const forgeStatus: ForgeStatus = app.forgeState?.status ?? 'unforged'
  const isForged = forgeStatus === 'forged'
  const isCron = spec.role === 'cron'
  const isFunction = spec.role === 'function'
  const hasPods = app.pods && app.pods.length > 0
  const [copiedHost, setCopiedHost] = useState<string | null>(null)

  const copyHost = (value: string) => {
    navigator.clipboard.writeText(value)
    setCopiedHost(value)
    setTimeout(() => setCopiedHost(null), 1500)
  }

  return (
    <div className={`app-card ${!deployedAt ? '' : healthy ? 'healthy' : 'unhealthy'}`}>
      <div className="app-card-header">
        <div className="app-card-title">
          {deployedAt && (
            <Tooltip text={healthy ? 'All pods healthy' : 'One or more pods unhealthy'}>
              <span className={`health-dot ${healthy ? 'green' : 'red'}`} />
            </Tooltip>
          )}
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
          {isCron && cronStateBadge(app.cronState)}
          {deployedAt && (
            <span className={`health-label ${healthy ? 'green' : 'red'}`}>
              {healthy ? 'healthy' : 'unhealthy'}
            </span>
          )}
          {healthChecks && healthChecks.length > 0 && (
            <Sparkline checks={healthChecks} onClick={onHealthClick} />
          )}
        </div>
        <div className="app-card-ready-group">
          {isCron ? (
            app.cronState?.nextRunAt ? (
              <Tooltip text={`Next run: ${new Date(app.cronState.nextRunAt).toLocaleString()}`}>
                <div className="app-card-ready">
                  <i className="fawsb fa-clock" style={{ fontSize: '10px', marginRight: '4px' }} />
                  {new Date(app.cronState.nextRunAt).toLocaleTimeString()}
                </div>
              </Tooltip>
            ) : (
              <div className="app-card-ready">no schedule</div>
            )
          ) : (
            <Tooltip text={`${ready} pods ready`}>
              <div className="app-card-ready">{ready}</div>
            </Tooltip>
          )}
          {!isCron && !isFunction && isForged && onScale && (
            <div className="scale-controls">
              <Tooltip text="Scale down">
                <button
                  className="scale-btn"
                  disabled={busy || (spec.replicas ?? 1) <= 0}
                  onClick={() => onScale(spec.app, Math.max(0, (spec.replicas ?? 1) - 1))}
                >
                  <i className="fawsb fa-minus" />
                </button>
              </Tooltip>
              <Tooltip text="Scale up">
                <button
                  className="scale-btn"
                  disabled={busy || (spec.replicas ?? 1) >= 10}
                  onClick={() => onScale(spec.app, (spec.replicas ?? 1) + 1)}
                >
                  <i className="fawsb fa-plus" />
                </button>
              </Tooltip>
            </div>
          )}
        </div>
      </div>

      <div className="app-card-meta">
        {commitSha && (() => {
          const cUrl = spec.repo ? commitURL(spec.repo, commitSha) : null
          return cUrl ? (
            <a href={cUrl} target="_blank" rel="noopener noreferrer" className="commit-link">
              <i className="fawsb fa-code" /> {commitSha.slice(0, 7)}
              <i className="fawsb fa-arrow-up-right-from-square repo-external-icon" />
            </a>
          ) : (
            <Tooltip text={`Full SHA: ${commitSha}`}>
              <span className="commit">
                <i className="fawsb fa-code" /> {commitSha.slice(0, 7)}
              </span>
            </Tooltip>
          )
        })()}
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
          {spec.repo.autoDeploy && (
            <Tooltip text="Pushes to this repo auto-trigger deploys">
              <span className="auto-deploy-badge">
                <i className="fawsb fa-arrows-spin" /> auto-deploy
              </span>
            </Tooltip>
          )}
          {app.remoteHeadSha && commitSha && app.remoteHeadSha !== commitSha && (
            <Tooltip text={`Remote HEAD: ${app.remoteHeadSha.slice(0, 7)} — deployed: ${commitSha.slice(0, 7)}`}>
              <span className="update-available-badge">
                <i className="fawsb fa-arrow-up" /> update available
              </span>
            </Tooltip>
          )}
        </div>
        )
      })()}

      <div className="app-card-hosts">
        {spec.hosts?.external && (
          <Tooltip text={copiedHost === spec.hosts.external ? 'Copied!' : 'Click to copy'}>
            <span className="host external copyable" onClick={() => copyHost(spec.hosts!.external!)}>
              <i className={`fawsb ${copiedHost === spec.hosts.external ? 'fa-check' : 'fa-globe'}`} /> {spec.hosts.external}
            </span>
          </Tooltip>
        )}
        {spec.hosts?.internal && (
          <Tooltip text={copiedHost === spec.hosts.internal ? 'Copied!' : 'Click to copy'}>
            <span className="host internal copyable" onClick={() => copyHost(spec.hosts!.internal!)}>
              <i className={`fawsb ${copiedHost === spec.hosts.internal ? 'fa-check' : 'fa-link'}`} /> {spec.hosts.internal}
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
          {spec.services.storage && (
            <Tooltip text={`Bucket: ${spec.services.storage.bucket}`}>
              <span className="service-badge"><i className="fawsb fa-box-archive" /> S3</span>
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
          <Tooltip text={isCron ? "Register cron job" : isFunction ? "Register function" : "Provision K8s deployment, service, and DNS"}>
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
        {isForged && isCron && onCronTrigger && (
          <Tooltip text="Run the cron job immediately">
            <button onClick={() => onCronTrigger(spec.app)} disabled={busy} className="btn btn-primary">
              <i className="fawsb fa-play" /> Run Now
            </button>
          </Tooltip>
        )}
        {isForged && isCron && onCronPanel && (
          <Tooltip text="View execution history and schedule">
            <button onClick={() => onCronPanel(spec.app)} className="btn">
              <i className="fawsb fa-clock-rotate-left" /> History
            </button>
          </Tooltip>
        )}
        {isForged && isFunction && onFuncInvoke && (
          <Tooltip text="Invoke the function">
            <button onClick={() => onFuncInvoke(spec.app)} disabled={busy} className="btn btn-primary">
              <i className="fawsb fa-play" /> Invoke
            </button>
          </Tooltip>
        )}
        {isForged && isFunction && onFuncPanel && (
          <Tooltip text="View invocation history">
            <button onClick={() => onFuncPanel(spec.app)} className="btn">
              <i className="fawsb fa-clock-rotate-left" /> History
            </button>
          </Tooltip>
        )}
        {isForged && !isCron && !isFunction && (
          <Tooltip text={spec.repo ? "Deploy latest from repo" : "Build, test, and deploy a commit"}>
            <button onClick={() => onDeploy(spec.app)} disabled={busy} className={`btn btn-primary ${activeOp === 'deploying' ? 'btn-busy' : ''}`}>
              {activeOp === 'deploying' ? <span className="btn-spinner" /> : <i className="fawsb fa-rocket-launch" />} {spec.repo ? 'Deploy Latest' : 'Deploy'}
            </button>
          </Tooltip>
        )}
        {isForged && isCron && (
          <Tooltip text={spec.repo ? "Deploy latest image for cron" : "Build and register cron image"}>
            <button onClick={() => onDeploy(spec.app)} disabled={busy} className={`btn ${activeOp === 'deploying' ? 'btn-busy' : ''}`}>
              {activeOp === 'deploying' ? <span className="btn-spinner" /> : <i className="fawsb fa-rocket-launch" />} Deploy
            </button>
          </Tooltip>
        )}
        {isForged && isFunction && (
          <Tooltip text={spec.repo ? "Deploy latest image for function" : "Build and register function image"}>
            <button onClick={() => onDeploy(spec.app)} disabled={busy} className={`btn ${activeOp === 'deploying' ? 'btn-busy' : ''}`}>
              {activeOp === 'deploying' ? <span className="btn-spinner" /> : <i className="fawsb fa-rocket-launch" />} Deploy
            </button>
          </Tooltip>
        )}
        {isForged && !isCron && !isFunction && commitSha && (
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
        {hasPods && onCommands && (
          <Tooltip text="Show kubectl / norn commands">
            <button onClick={onCommands} className="btn">
              <i className="fawsb fa-terminal" /> Commands
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )
}
