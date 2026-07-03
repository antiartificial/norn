import { BrowserRouter, Navigate, Route, Routes, useParams } from 'react-router-dom'
import { AppRuntimeProvider } from './runtime/AppRuntime.tsx'
import { Shell } from './shell/Shell.tsx'
import { OverviewPage } from './pages/OverviewPage.tsx'
import { AppsPage } from './pages/AppsPage.tsx'
import { AppDetailPage } from './pages/AppDetailPage.tsx'
import { DeploysPage } from './pages/DeploysPage.tsx'
import { IncidentsPage } from './pages/IncidentsPage.tsx'
import { OperationsPage } from './pages/OperationsPage.tsx'
import { TopologyView } from './components/TopologyView.tsx'
import { PlatformPanel } from './components/PlatformPanel.tsx'

function NavigateToAppOverview() {
  const { id } = useParams()
  return <Navigate to={`/apps/${id}/overview`} replace />
}

export function App() {
  return (
    <BrowserRouter>
      <AppRuntimeProvider>
        {(runtime) => (
          <Shell
            connected={runtime.connected}
            version={runtime.version}
            apps={runtime.apps}
            activity={runtime.activity}
            runAction={runtime.mutations.run}
          >
            <Routes>
              <Route path="/" element={<Navigate to="/overview" replace />} />
              <Route path="/overview" element={<OverviewPage />} />
              <Route path="/apps" element={<AppsPage />} />
              <Route path="/apps/:id" element={<NavigateToAppOverview />} />
              <Route path="/apps/:id/:tab" element={<AppDetailPage />} />
              <Route path="/deploys" element={<DeploysPage />} />
              <Route path="/incidents" element={<IncidentsPage />} />
              <Route path="/operations" element={<OperationsPage />} />
              <Route path="/operations/:sagaId" element={<OperationsPage />} />
              <Route
                path="/topology"
                element={(
                  <TopologyView
                    apps={runtime.apps}
                    serviceManifest={runtime.serviceManifest ?? null}
                    accessPatterns={runtime.accessPatterns}
                    activeIngress={runtime.activeIngress}
                  />
                )}
              />
              <Route path="/platform" element={<Navigate to="/platform/releases" replace />} />
              <Route path="/platform/*" element={<PlatformPanel />} />
            </Routes>
          </Shell>
        )}
      </AppRuntimeProvider>
    </BrowserRouter>
  )
}
