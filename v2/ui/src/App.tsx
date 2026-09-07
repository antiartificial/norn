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
const ReleasesPage = lazy(() => import('./pages/ReleasesPage.tsx').then((module) => ({ default: module.ReleasesPage })))
const FleetPage = lazy(() => import('./pages/FleetPage.tsx').then((module) => ({ default: module.FleetPage })))
const TopologyView = lazy(() => import('./components/TopologyView.tsx').then((module) => ({ default: module.TopologyView })))
const PlatformPanel = lazy(() => import('./components/PlatformPanel.tsx').then((module) => ({ default: module.PlatformPanel })))
const ManagementOnlyPage = lazy(() => import('./pages/ManagementOnlyPage.tsx').then((module) => ({ default: module.ManagementOnlyPage })))

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
            authority={runtime.authority}
            environment={runtime.environment}
            hasRevocableAuthoritySession={runtime.hasRevocableAuthoritySession}
            revokeAuthoritySession={runtime.revokeAuthoritySession}
          >
            <Suspense fallback={<RouteLoading />}>
              <Routes>
              <Route path="/" element={<Navigate to="/overview" replace />} />
              <Route path="/overview" element={runtime.runtimeAvailable ? <OverviewPage /> : <ManagementOnlyPage area="Runtime overview" />} />
              <Route path="/apps" element={runtime.runtimeAvailable ? <AppsPage /> : <ManagementOnlyPage area="Applications" />} />
              <Route path="/apps/:id" element={runtime.runtimeAvailable ? <NavigateToAppOverview /> : <ManagementOnlyPage area="Applications" />} />
              <Route path="/apps/:id/:tab" element={runtime.runtimeAvailable ? <AppDetailPage /> : <ManagementOnlyPage area="Applications" />} />
              <Route path="/deploys" element={runtime.runtimeAvailable ? <DeploysPage /> : <ManagementOnlyPage area="Deployments" />} />
              <Route path="/incidents" element={runtime.runtimeAvailable ? <IncidentsPage /> : <ManagementOnlyPage area="Incidents" />} />
              <Route path="/operations" element={<OperationsPage />} />
              <Route path="/operations/:sagaId" element={<OperationsPage />} />
              <Route path="/releases" element={runtime.runtimeAvailable ? <ReleasesPage /> : <ManagementOnlyPage area="Releases" />} />
              <Route path="/fleet" element={runtime.fleetAvailable ? <FleetPage /> : <EmptyState icon="!" title="Fleet unavailable" hint="This server does not advertise the complete fleet-v1 capability set." />} />
              <Route
                path="/topology"
                element={(
                  runtime.runtimeAvailable ? <TopologyView
                    apps={runtime.apps}
                    serviceManifest={runtime.serviceManifest ?? null}
                    accessPatterns={runtime.accessPatterns}
                    activeIngress={runtime.activeIngress}
                  /> : <ManagementOnlyPage area="Topology" />
                )}
              />
              <Route path="/platform" element={<Navigate to="/platform/releases" replace />} />
              <Route path="/platform/*" element={runtime.runtimeAvailable ? <PlatformPanel /> : <ManagementOnlyPage area="Platform administration" />} />
              </Routes>
            </Suspense>
          </Shell>
        )}
      </AppRuntimeProvider>
    </BrowserRouter>
  )
}
