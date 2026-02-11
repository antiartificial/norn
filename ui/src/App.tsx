import { useState, useCallback } from 'react'
import { useApps } from './hooks/useApps.ts'
import { useWebSocket } from './hooks/useWebSocket.ts'
import { AppCard } from './components/AppCard.tsx'
import { LogViewer } from './components/LogViewer.tsx'
import { DeployPanel } from './components/DeployPanel.tsx'
import { Welcome } from './components/Welcome.tsx'
import { StatusBar } from './components/StatusBar.tsx'
import { Tooltip } from './components/Tooltip.tsx'
import type { WSEvent, StepLog } from './types/index.ts'

function upsertStep(steps: StepLog[], incoming: StepLog): StepLog[] {
  const idx = steps.findIndex((s) => s.step === incoming.step)
  if (idx >= 0) {
    const updated = [...steps]
    updated[idx] = { ...updated[idx], ...incoming }
    return updated
  }
  return [...steps, incoming]
}

const TOUR_KEY = 'norn:tour-complete'

export function App() {
  const { apps, loading, error, refetch } = useApps()
  const [logApp, setLogApp] = useState<string | null>(null)
  const [showTour, setShowTour] = useState(() => !localStorage.getItem(TOUR_KEY))
  const [deployState, setDeployState] = useState<{
    appId: string
    steps: StepLog[]
    status: string
    error?: string
  } | null>(null)
  const [forgeState, setForgeState] = useState<{
    appId: string
    steps: StepLog[]
    status: string
    error?: string
  } | null>(null)
  const [teardownState, setTeardownState] = useState<{
    appId: string
    steps: StepLog[]
    status: string
    error?: string
  } | null>(null)

  const [webhookToast, setWebhookToast] = useState<string | null>(null)
  const [restartingApp, setRestartingApp] = useState<string | null>(null)
  const [rollingBackApp, setRollingBackApp] = useState<string | null>(null)

  // Derive which apps are busy and with what operation
  const getAppBusy = (appId: string): { busy: boolean; activeOp?: string } => {
    if (deployState?.appId === appId && deployState.status !== 'deployed' && deployState.status !== 'failed') {
      return { busy: true, activeOp: 'deploying' }
    }
    if (forgeState?.appId === appId && forgeState.status !== 'completed' && forgeState.status !== 'failed') {
      return { busy: true, activeOp: 'forging' }
    }
    if (teardownState?.appId === appId && teardownState.status !== 'completed' && teardownState.status !== 'failed') {
      return { busy: true, activeOp: 'tearing_down' }
    }
    if (restartingApp === appId) return { busy: true, activeOp: 'restarting' }
    if (rollingBackApp === appId) return { busy: true, activeOp: 'rolling_back' }
    return { busy: false }
  }

  const handleWsEvent = useCallback((event: WSEvent) => {
    if (event.type === 'deploy.webhook') {
      const payload = event.payload as Record<string, string>
      const msg = `Auto-deployed ${event.appId} from push ${payload['commitSha']?.slice(0, 8)}`
      setWebhookToast(msg)
      setTimeout(() => setWebhookToast(null), 5000)
    }
    if (event.type === 'deploy.step' || event.type === 'deploy.completed' || event.type === 'deploy.failed') {
      const payload = event.payload as Record<string, unknown>
      if (event.type === 'deploy.step') {
        setDeployState((prev) => ({
          appId: event.appId,
          steps: upsertStep(prev?.steps ?? [], { step: payload['step'] as string, status: payload['status'] as string }),
          status: payload['status'] as string,
        }))
      } else if (event.type === 'deploy.completed') {
        setDeployState((prev) => prev ? { ...prev, status: 'deployed' } : null)
        refetch()
      } else if (event.type === 'deploy.failed') {
        setDeployState((prev) => prev ? { ...prev, status: 'failed', error: (payload as Record<string, string>)['error'] } : null)
        refetch()
      }
    }
    if (event.type === 'forge.step' || event.type === 'forge.completed' || event.type === 'forge.failed') {
      const payload = event.payload as Record<string, unknown>
      if (event.type === 'forge.step') {
        setForgeState((prev) => ({
          appId: event.appId,
          steps: upsertStep(prev?.steps ?? [], { step: payload['step'] as string, status: payload['status'] as string }),
          status: payload['status'] as string,
        }))
      } else if (event.type === 'forge.completed') {
        setForgeState((prev) => prev ? { ...prev, status: 'completed' } : null)
        refetch()
      } else if (event.type === 'forge.failed') {
        setForgeState((prev) => prev ? { ...prev, status: 'failed', error: (payload as Record<string, string>)['error'] } : null)
        refetch()
      }
    }
    if (event.type === 'teardown.step' || event.type === 'teardown.completed' || event.type === 'teardown.failed') {
      const payload = event.payload as Record<string, unknown>
      if (event.type === 'teardown.step') {
        setTeardownState((prev) => ({
          appId: event.appId,
          steps: upsertStep(prev?.steps ?? [], { step: payload['step'] as string, status: payload['status'] as string }),
          status: payload['status'] as string,
        }))
      } else if (event.type === 'teardown.completed') {
        setTeardownState((prev) => prev ? { ...prev, status: 'completed' } : null)
        refetch()
      } else if (event.type === 'teardown.failed') {
        setTeardownState((prev) => prev ? { ...prev, status: 'failed', error: (payload as Record<string, string>)['error'] } : null)
        refetch()
      }
    }
    if (event.type === 'app.restarted') {
      setRestartingApp(null)
      refetch()
    }
    if (event.type === 'deploy.rollback') {
      setRollingBackApp(null)
      refetch()
    }
  }, [refetch])

  const { connected } = useWebSocket(handleWsEvent)

  const dismissTour = () => {
    localStorage.setItem(TOUR_KEY, '1')
    setShowTour(false)
  }

  const handleForge = async (appId: string) => {
    setForgeState({ appId, steps: [], status: 'queued' })
    await fetch(`/api/apps/${appId}/forge`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
  }

  const handleTeardown = async (appId: string) => {
    if (!window.confirm(`Tear down all infrastructure for ${appId}?`)) return
    setTeardownState({ appId, steps: [], status: 'queued' })
    await fetch(`/api/apps/${appId}/teardown`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
  }

  const handleDeploy = async (appId: string) => {
    const app = apps.find((a) => a.spec.app === appId)
    let sha: string | null
    if (app?.spec.repo) {
      // Warn if last deploy was very recent (< 2 min)
      if (app.deployedAt) {
        const ago = Date.now() - new Date(app.deployedAt).getTime()
        if (ago < 120_000) {
          const mins = Math.floor(ago / 60_000)
          const secs = Math.floor((ago % 60_000) / 1000)
          const timeStr = mins > 0 ? `${mins}m ${secs}s` : `${secs}s`
          if (!window.confirm(`Last deploy was ${timeStr} ago. Deploy latest anyway?`)) return
        }
      }
      sha = 'HEAD'
    } else {
      sha = prompt('Commit SHA to deploy:')
      if (!sha) return
    }
    setDeployState({ appId, steps: [], status: 'queued' })
    await fetch(`/api/apps/${appId}/deploy`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ commitSha: sha }),
    })
  }

  const handleRestart = async (appId: string) => {
    setRestartingApp(appId)
    await fetch(`/api/apps/${appId}/restart`, { method: 'POST' })
    // Clear after a timeout in case WS event doesn't arrive
    setTimeout(() => setRestartingApp((cur) => cur === appId ? null : cur), 10_000)
  }

  const handleRollback = async (appId: string) => {
    setRollingBackApp(appId)
    await fetch(`/api/apps/${appId}/rollback`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
    setTimeout(() => setRollingBackApp((cur) => cur === appId ? null : cur), 10_000)
  }

  return (
    <div className="norn">
      {showTour && <Welcome onDismiss={dismissTour} />}

      <header className="norn-header">
        <div className="header-left">
          <h1>NORN</h1>
          <span className="header-tagline">control plane</span>
        </div>
        <div className="header-right">
          <StatusBar />
          <span className={`ws-status ${connected ? 'connected' : 'disconnected'}`}>
            <span className={`ws-dot ${connected ? 'green' : 'red'}`} />
            {connected ? 'live' : 'reconnecting'}
          </span>
          <Tooltip text="Show welcome tour">
            <button className="btn btn-icon" onClick={() => setShowTour(true)}>
              ?
            </button>
          </Tooltip>
        </div>
      </header>

      {webhookToast && (
        <div className="webhook-toast">{webhookToast}</div>
      )}

      <main className="norn-main">
        {error && (
          <div className="error-banner">
            <strong>Connection error</strong> &mdash; {error}
            <span className="error-hint">Is the API running? <code>make dev</code></span>
          </div>
        )}

        {loading && (
          <div className="loading">
            <div className="loading-spinner" />
            <p>Discovering apps...</p>
          </div>
        )}

        <div className="app-grid">
          {apps.map((app) => {
            const { busy, activeOp } = getAppBusy(app.spec.app)
            return (
              <AppCard
                key={app.spec.app}
                app={app}
                busy={busy}
                activeOp={activeOp}
                onDeploy={handleDeploy}
                onForge={handleForge}
                onTeardown={handleTeardown}
                onRestart={handleRestart}
                onRollback={handleRollback}
                onViewLogs={setLogApp}
              />
            )
          })}
        </div>

        {apps.length === 0 && !loading && !error && (
          <div className="empty-state">
            <div className="empty-state-icon">&#9733;</div>
            <h2>No apps discovered yet</h2>
            <p>
              Norn scans your projects directory for <code>infraspec.yaml</code> files.
              Each file declares an app and its infrastructure needs.
            </p>
            <div className="empty-state-example">
              <div className="example-header">
                <span className="example-filename">~/projects/my-app/infraspec.yaml</span>
              </div>
              <pre>{`app: my-app
role: webserver
port: 3000
healthcheck: /health
hosts:
  external: app.example.com
  internal: my-app-service
build:
  dockerfile: Dockerfile
  test: npm test
services:
  postgres:
    database: myapp_db
  kv:
    namespace: my-app
secrets:
  - DATABASE_URL
  - API_KEY
artifacts:
  retain: 5`}</pre>
            </div>
            <div className="empty-state-steps">
              <div className="step">
                <span className="step-number">1</span>
                <span>Create <code>infraspec.yaml</code> in your project</span>
              </div>
              <div className="step">
                <span className="step-number">2</span>
                <span>Norn discovers it automatically</span>
              </div>
              <div className="step">
                <span className="step-number">3</span>
                <span>Deploy, monitor, and manage from here</span>
              </div>
            </div>
          </div>
        )}

        {deployState && (
          <DeployPanel
            appId={deployState.appId}
            steps={deployState.steps}
            status={deployState.status}
            error={deployState.error}
            onClose={() => setDeployState(null)}
          />
        )}

        {forgeState && (
          <DeployPanel
            appId={forgeState.appId}
            steps={forgeState.steps}
            status={forgeState.status}
            error={forgeState.error}
            title="Forging"
            onClose={() => setForgeState(null)}
          />
        )}

        {teardownState && (
          <DeployPanel
            appId={teardownState.appId}
            steps={teardownState.steps}
            status={teardownState.status}
            error={teardownState.error}
            title="Tearing Down"
            onClose={() => setTeardownState(null)}
          />
        )}

        {logApp && <LogViewer appId={logApp} onClose={() => setLogApp(null)} />}
      </main>

      <footer className="norn-footer">
        <span>norn v0.1.0</span>
        <span className="footer-sep">&middot;</span>
        <span>{apps.length} app{apps.length !== 1 ? 's' : ''}</span>
      </footer>
    </div>
  )
}
