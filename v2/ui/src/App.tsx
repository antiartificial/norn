import { lazy, Suspense } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useParams } from 'react-router-dom'
import { AppRuntimeProvider } from './runtime/AppRuntime.tsx'
import { Shell } from './shell/Shell.tsx'
import { EmptyState } from './components/ui/index.ts'

const OverviewPage = lazy(() => import('./pages/OverviewPage.tsx').then((module) => ({ default: module.OverviewPage })))
const AppsPage = lazy(() => import('./pages/AppsPage.tsx').then((module) => ({ default: module.AppsPage })))
const AppDetailPage = lazy(() => import('./pages/AppDetailPage.tsx').then((module) => ({ default: module.AppDetailPage })))
const DeploysPage = lazy(() => import('./pages/DeploysPage.tsx').then((module) => ({ default: module.DeploysPage })))
const IncidentsPage = lazy(() => import('./pages/IncidentsPage.tsx').then((module) => ({ default: module.IncidentsPage })))
const OperationsPage = lazy(() => import('./pages/OperationsPage.tsx').then((module) => ({ default: module.OperationsPage })))
const FleetPage = lazy(() => import('./pages/FleetPage.tsx').then((module) => ({ default: module.FleetPage })))
const TopologyView = lazy(() => import('./components/TopologyView.tsx').then((module) => ({ default: module.TopologyView })))
const PlatformPanel = lazy(() => import('./components/PlatformPanel.tsx').then((module) => ({ default: module.PlatformPanel })))

function NavigateToAppOverview() {
  const { id } = useParams()
  return <Navigate to={`/apps/${id}/overview`} replace />
}

function RouteLoading() {
  return <div className="route-loading" role="status" aria-live="polite">Loading view…</div>
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
            fleetAvailable={runtime.fleetAvailable}
          >
            <Suspense fallback={<RouteLoading />}>
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
              <Route path="/fleet" element={runtime.fleetAvailable ? <FleetPage /> : <EmptyState icon="!" title="Fleet unavailable" hint="This server does not advertise the complete fleet-v1 capability set." />} />
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
            </Suspense>
          </Shell>
        )}
      </AppRuntimeProvider>
    </BrowserRouter>
  )
}
