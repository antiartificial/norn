import { useState, useCallback } from 'react'
import { useApps } from './hooks/useApps.ts'
import { useWebSocket } from './hooks/useWebSocket.ts'
import { AppCard } from './components/AppCard.tsx'
import { LogViewer } from './components/LogViewer.tsx'
import { DeployPanel } from './components/DeployPanel.tsx'
import type { WSEvent, StepLog } from './types/index.ts'

export function App() {
  const { apps, loading, error, refetch } = useApps()
  const [logApp, setLogApp] = useState<string | null>(null)
  const [deployState, setDeployState] = useState<{
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
    if (event.type === 'app.restarted') {
      refetch()
    }
  }, [refetch])

  const { connected } = useWebSocket(handleWsEvent)

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
      <header className="norn-header">
        <h1>
          <i className="fa-solid fa-shield-halved" /> NORN
        </h1>
        <div className="header-status">
          <span className={`ws-status ${connected ? 'connected' : 'disconnected'}`}>
            <i className={`fa-solid ${connected ? 'fa-circle-check' : 'fa-circle-xmark'}`} />
            {connected ? 'live' : 'reconnecting'}
          </span>
        </div>
      </header>

      <main className="norn-main">
        {loading && <div className="loading">Loading apps...</div>}
        {error && <div className="error-banner"><i className="fa-solid fa-triangle-exclamation" /> {error}</div>}

        <div className="app-grid">
          {apps.map((app) => (
            <AppCard
              key={app.spec.app}
              app={app}
              onDeploy={handleDeploy}
              onRestart={handleRestart}
              onRollback={handleRollback}
              onViewLogs={setLogApp}
            />
          ))}
        </div>

        {apps.length === 0 && !loading && (
          <div className="empty-state">
            <i className="fa-solid fa-folder-open" />
            <p>No apps found. Add an <code>infraspec.yaml</code> to a project directory.</p>
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

        {logApp && <LogViewer appId={logApp} onClose={() => setLogApp(null)} />}
      </main>
    </div>
  )
}
