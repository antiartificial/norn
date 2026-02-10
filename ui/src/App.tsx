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

  const handleWsEvent = useCallback((event: WSEvent) => {
    if (event.type === 'deploy.step' || event.type === 'deploy.completed' || event.type === 'deploy.failed') {
      const payload = event.payload as Record<string, unknown>
      if (event.type === 'deploy.step') {
        setDeployState((prev) => ({
          appId: event.appId,
          steps: [...(prev?.steps ?? []), { step: payload['step'] as string, status: payload['status'] as string }],
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
          steps: [...(prev?.steps ?? []), { step: payload['step'] as string, status: payload['status'] as string }],
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
    if (event.type === 'app.restarted') {
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

  const handleDeploy = async (appId: string) => {
    const sha = prompt('Commit SHA to deploy:')
    if (!sha) return
    setDeployState({ appId, steps: [], status: 'queued' })
    await fetch(`/api/apps/${appId}/deploy`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ commitSha: sha }),
    })
  }

  const handleRestart = async (appId: string) => {
    await fetch(`/api/apps/${appId}/restart`, { method: 'POST' })
  }

  const handleRollback = async (appId: string) => {
    await fetch(`/api/apps/${appId}/rollback`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
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
          {apps.map((app) => (
            <AppCard
              key={app.spec.app}
              app={app}
              onDeploy={handleDeploy}
              onForge={handleForge}
              onRestart={handleRestart}
              onRollback={handleRollback}
              onViewLogs={setLogApp}
            />
          ))}
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
          />
        )}

        {forgeState && (
          <DeployPanel
            appId={forgeState.appId}
            steps={forgeState.steps}
            status={forgeState.status}
            error={forgeState.error}
            title="Forging"
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
