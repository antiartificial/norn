import { useState, useCallback, useEffect } from 'react'
import { apiUrl, fetchOpts } from './lib/api.ts'
import { useApps } from './hooks/useApps.ts'
import { useWebSocket } from './hooks/useWebSocket.ts'
import { AppCard } from './components/AppCard.tsx'
import { LogViewer } from './components/LogViewer.tsx'
import { DeployPanel } from './components/DeployPanel.tsx'
import { DeployHistory } from './components/DeployHistory.tsx'
import { StatusBar } from './components/StatusBar.tsx'
import { StatsPanel } from './components/StatsPanel.tsx'
import { ScaleModal } from './components/ScaleModal.tsx'
import { ExecTerminal } from './components/ExecTerminal.tsx'
import { SnapshotsPanel } from './components/SnapshotsPanel.tsx'
import { CronPanel } from './components/CronPanel.tsx'
import { FunctionPanel } from './components/FunctionPanel.tsx'
import type { AppStatus, WSEvent } from './types/index.ts'

export interface StepEvent {
  message: string
  timestamp: number
  allocId?: string
  node?: string
  allocStatus?: string
}

interface DeployStep {
  step: string
  status: string
  events?: StepEvent[]
}

function upsertStep(steps: DeployStep[], incoming: DeployStep): DeployStep[] {
  const idx = steps.findIndex((s) => s.step === incoming.step)
  if (idx >= 0) {
    const updated = [...steps]
    updated[idx] = { ...updated[idx], ...incoming, events: updated[idx].events }
    return updated
  }
  return [...steps, incoming]
}

function appendStepEvent(steps: DeployStep[], stepName: string, event: StepEvent): DeployStep[] {
  const idx = steps.findIndex((s) => s.step === stepName)
  if (idx < 0) return steps
  const updated = [...steps]
  updated[idx] = { ...updated[idx], events: [...(updated[idx].events ?? []), event] }
  return updated
}

export function App() {
  const { apps, loading, error, refetch } = useApps()
  const [logApp, setLogApp] = useState<string | null>(null)
  const [deployState, setDeployState] = useState<{
    appId: string
    steps: DeployStep[]
    status: string
    sagaId?: string
    error?: string
  } | null>(null)
  const [restartingApp, setRestartingApp] = useState<string | null>(null)
  const [scaleState, setScaleState] = useState<{ appId: string; groups: { name: string; current: number }[] } | null>(null)
  const [execApp, setExecApp] = useState<string | null>(null)
  const [snapshotsApp, setSnapshotsApp] = useState<string | null>(null)
  const [cronApp, setCronApp] = useState<string | null>(null)
  const [functionApp, setFunctionApp] = useState<string | null>(null)
  const [filter, setFilter] = useState<'all' | 'healthy' | 'unhealthy'>('all')
  const [view, setView] = useState<'apps' | 'history' | 'stats'>('apps')
  const [apiVersion, setApiVersion] = useState<string | null>(null)

  const handleWsEvent = useCallback((event: WSEvent) => {
    if (event.type === 'deploy.step' || event.type === 'deploy.completed' || event.type === 'deploy.failed') {
      const payload = event.payload as Record<string, unknown>
      if (event.type === 'deploy.step') {
        setDeployState((prev) => ({
          appId: event.appId,
          steps: upsertStep(prev?.steps ?? [], {
            step: payload['step'] as string,
            status: payload['status'] as string,
          }),
          status: payload['status'] as string,
          sagaId: (payload['sagaId'] as string) || prev?.sagaId,
        }))
      } else if (event.type === 'deploy.completed') {
        setDeployState((prev) => prev ? { ...prev, status: 'deployed' } : null)
        refetch()
      } else if (event.type === 'deploy.failed') {
        setDeployState((prev) => prev ? { ...prev, status: 'failed', error: (payload as Record<string, string>)['error'] } : null)
        refetch()
      }
    }
    if (event.type === 'deploy.progress') {
      const payload = event.payload as Record<string, string>
      setDeployState((prev) => {
        if (!prev) return null
        return {
          ...prev,
          steps: appendStepEvent(prev.steps, payload['step'], {
            message: payload['message'],
            timestamp: Date.now(),
            allocId: payload['allocId'],
            node: payload['node'],
            allocStatus: payload['allocStatus'],
          }),
        }
      })
    }
    if (event.type === 'app.restarted') {
      setRestartingApp(null)
      refetch()
    }
    if (event.type === 'app.scaled') {
      refetch()
    }
  }, [refetch])

  const { connected } = useWebSocket(handleWsEvent)

  useEffect(() => {
    fetch(apiUrl('/api/version'), fetchOpts)
      .then(r => r.json())
      .then(data => setApiVersion(data.version))
      .catch(() => {})
  }, [])

  const handleDeploy = async (appId: string) => {
    setDeployState({ appId, steps: [], status: 'queued' })
    await fetch(apiUrl(`/api/apps/${appId}/deploy`), {
      ...fetchOpts,
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ref: 'HEAD' }),
    })
  }

  const handleRestart = async (appId: string) => {
    setRestartingApp(appId)
    await fetch(apiUrl(`/api/apps/${appId}/restart`), { ...fetchOpts, method: 'POST' })
    setTimeout(() => setRestartingApp((cur) => cur === appId ? null : cur), 10_000)
  }

  const handleScale = (appId: string) => {
    const app = apps.find((a: AppStatus) => a.spec.name === appId)
    if (!app) return
    const allocations = app.allocations ?? []
    const groupMap: Record<string, number> = {}
    for (const a of allocations) {
      if (a.status === 'running') {
        groupMap[a.taskGroup] = (groupMap[a.taskGroup] ?? 0) + 1
      }
    }
    const groups = Object.entries(groupMap).map(([name, current]) => ({ name, current }))
    if (groups.length === 0) {
      // Fallback: derive groups from process spec
      for (const name of Object.keys(app.spec.processes)) {
        groups.push({ name, current: 0 })
      }
    }
    setScaleState({ appId, groups })
  }

  const isBusy = (appId: string): boolean => {
    if (deployState?.appId === appId && deployState.status !== 'deployed' && deployState.status !== 'failed') return true
    if (restartingApp === appId) return true
    return false
  }

  return (
    <div className="norn">
      <header className="norn-header">
        <div className="header-left">
          <h1>NORN</h1>
          <span className="header-tagline">control plane</span>
        </div>
        <div className="header-right">
          {(['apps', 'history', 'stats'] as const).map(v => (
            <button
              key={v}
              className={`filter-btn ${view === v ? 'active' : ''}`}
              onClick={() => setView(v)}
            >
              {v === 'apps' ? 'Apps' : v === 'history' ? 'History' : 'Stats'}
            </button>
          ))}
          <StatusBar />
          <span className={`ws-status ${connected ? 'connected' : 'disconnected'}`}>
            <span className={`ws-dot ${connected ? 'green' : 'red'}`} />
            {connected ? 'live' : 'reconnecting'}
          </span>
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

        {view === 'stats' ? (
          <StatsPanel />
        ) : view === 'history' ? (
          <DeployHistory
            apps={apps.map(a => a.spec.name)}
            onClose={() => setView('apps')}
          />
        ) : (
        <>
        {apps.length > 0 && (
          <div className="app-filters">
            {(['all', 'healthy', 'unhealthy'] as const).map(f => (
              <button key={f} className={`filter-btn ${filter === f ? 'active' : ''}`} onClick={() => setFilter(f)}>
                {f === 'all' ? 'All' : f === 'healthy' ? 'Healthy' : 'Unhealthy'}
                {f === 'all' && <span className="filter-count">{apps.length}</span>}
                {f === 'healthy' && <span className="filter-count">{apps.filter(a => a.healthy).length}</span>}
                {f === 'unhealthy' && <span className="filter-count">{apps.filter(a => !a.healthy).length}</span>}
              </button>
            ))}
          </div>
        )}

        <div className="app-grid">
          {apps.filter(app => {
            if (filter === 'healthy') return app.healthy
            if (filter === 'unhealthy') return !app.healthy
            return true
          }).map((app, i) => {
            const appId = app.spec.name || `unknown-${i}`
            return (
              <AppCard
                key={appId}
                app={app}
                busy={isBusy(appId)}
                onDeploy={handleDeploy}
                onRestart={handleRestart}
                onScale={handleScale}
                onViewLogs={setLogApp}
                onExec={setExecApp}
                onSnapshots={setSnapshotsApp}
                onCron={setCronApp}
                onFunction={setFunctionApp}
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
              <pre>{`name: my-app
processes:
  web:
    port: 3000
    command: npm start
  worker:
    command: npm run worker
infrastructure:
  postgres:
    database: myapp_db
repo:
  url: git@github.com:you/my-app.git
  branch: main
secrets:
  - DATABASE_URL
  - API_KEY`}</pre>
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
        </>
        )}

        {deployState && (
          <DeployPanel
            appId={deployState.appId}
            steps={deployState.steps}
            status={deployState.status}
            error={deployState.error}
            sagaId={deployState.sagaId}
            onClose={() => setDeployState(null)}
            onRetry={() => { setDeployState(null); handleDeploy(deployState.appId) }}
          />
        )}

        {logApp && (() => {
          const app = apps.find((a: AppStatus) => a.spec.name === logApp)
          const firstProcess = app ? Object.values(app.spec.processes)[0] : undefined
          const healthPath = firstProcess?.health?.path
          return <LogViewer appId={logApp} healthPath={healthPath} onClose={() => setLogApp(null)} />
        })()}

        {scaleState && (
          <ScaleModal
            appId={scaleState.appId}
            groups={scaleState.groups}
            onClose={() => setScaleState(null)}
            onScaled={() => { setScaleState(null); refetch() }}
          />
        )}

        {execApp && <ExecTerminal appId={execApp} onClose={() => setExecApp(null)} />}

        {snapshotsApp && (
          <SnapshotsPanel appId={snapshotsApp} onClose={() => setSnapshotsApp(null)} />
        )}

        {cronApp && (
          <CronPanel appId={cronApp} onClose={() => setCronApp(null)} />
        )}

        {functionApp && (() => {
          const app = apps.find((a: AppStatus) => a.spec.name === functionApp)
          const funcProcesses = app
            ? Object.entries(app.spec.processes)
                .filter(([, p]) => p.function)
                .map(([name]) => name)
            : []
          return (
            <FunctionPanel
              appId={functionApp}
              processes={funcProcesses}
              onClose={() => setFunctionApp(null)}
            />
          )
        })()}
      </main>

      <footer className="norn-footer">
        <span>norn {apiVersion ?? 'dev'}</span>
        <span className="footer-sep">&middot;</span>
        <span>{apps.length} app{apps.length !== 1 ? 's' : ''}</span>
      </footer>
    </div>
  )
}
