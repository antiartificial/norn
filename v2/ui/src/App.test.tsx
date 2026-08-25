import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App.tsx'
import { ToastProvider } from './components/ui/Toast.tsx'

const app = {
  spec: {
    name: 'api',
    processes: { web: { port: 8800, health: { path: '/health' } } },
    endpoints: [{ url: 'https://api.example.test' }],
    repo: { url: 'git@github.com:acme/api.git' },
  },
  nomadStatus: 'running',
  healthy: true,
  allocations: [{ id: 'alloc-1', taskGroup: 'web', status: 'running', lifecycle: 'active' }],
  allocationSummary: { running: 1, active: 1, retained: 0, total: 1 },
}

const unhealthyApp = {
  ...app,
  spec: { ...app.spec, name: 'worker', endpoints: [] },
  healthy: false,
  nomadStatus: 'failed',
}

function appWith(name: string, healthy = true) {
  return {
    ...app,
    spec: { ...app.spec, name, endpoints: [] },
    healthy,
    nomadStatus: healthy ? 'running' : 'failed',
  }
}

class MockWebSocket {
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((message: MessageEvent<string>) => void) | null = null
  onerror: (() => void) | null = null
  constructor() {
    setTimeout(() => this.onopen?.(), 0)
  }
  close() {
    this.onclose?.()
  }
}

function json(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

function installFetch(overrides: Record<string, Response | (() => Response)> = {}) {
  const calls: string[] = []
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push(`${init?.method ?? 'GET'} ${url}`)
    const match = Object.entries(overrides).find(([key]) => url.includes(key))
    if (match) return typeof match[1] === 'function' ? match[1]() : match[1]
    if (url.includes('/api/apps/api/logs')) return new Response('')
    if (url.includes('/api/apps/api/canary') || url.includes('/api/apps/worker/canary')) return json(null)
    if (url.includes('/api/apps')) return json([app, unhealthyApp])
    if (url.includes('/api/services/manifest')) return json({ version: 1, generatedAt: new Date().toISOString(), networkMode: 'dev', services: [] })
    if (url.includes('/api/access/patterns')) return json({ windowHours: 24, idleAfterHours: 72, patterns: [] })
    if (url.includes('/api/v1/capabilities')) return json({ protocolVersion: 1, serverVersion: 'test', features: ['fleet-v1', 'fleet-inventory', 'durable-fleet-capacity-plans', 'fleet-reconciliation-v1', 'durable-app-recovery-v1'] })
    if (url.includes('/api/cloudflared/ingress')) return json({ hostnames: [] })
    if (url.includes('/api/version')) return json({ version: 'test' })
    if (url.includes('/api/v1/fleet/node-pools')) return json({ schemaVersion: 'norn.fleet-inventory/v1', configured: false, nodePools: {} })
    if (url.includes('/reconciliations')) return json({ schemaVersion: 'norn.fleet-reconciliations/v1', planId: 'plan-1', count: 0, reconciliations: [] })
    if (url.includes('/api/v1/fleet/plans')) return json({ count: 0, plans: [] })
    if (url.includes('/api/health')) return json({ status: 'ok', services: { postgres: 'up', nomad: 'up', consul: 'up' } })
    if (url.includes('/api/events/active')) return json({ incidents: [] })
    if (url.endsWith('/api/events')) return json({ events: [] })
    if (url.includes('/api/operations/active') || url.endsWith('/api/operations')) return json({ count: 0, operations: [] })
    if (url.includes('/api/ops/platform')) return json(platformSummary())
    if (url.includes('/api/platform/releases')) return json({ current: 'abc123', releases: [] })
    if (url.includes('/api/deploy-groups')) return json({ groups: [] })
    if (url.includes('/api/notifications/channels')) return json({ channels: [] })
    if (url.includes('/api/access/grants')) return json({ grants: [] })
    if (url.includes('/api/deployments')) return json([])
    if (url.includes('/api/saga')) return json([])
    return json({})
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, calls }
}

function platformSummary() {
  return {
    generatedAt: new Date().toISOString(),
    networkMode: 'dev',
    services: { total: 2, public: 1, private: 1, local: 0, internal: 0, byType: { web: 2 }, byStatus: { passing: 2 } },
    deployments: { recent: [], dirty: [], failed: 0, successful: 0 },
    operations: { recent: [], active: [], byKind: {}, byStatus: {} },
    secrets: { ok: 1, needsAttention: 0, migrationItems: 0, apps: [] },
    snapshots: [],
    access: { totalRecent: 1, byStatus: { '200': 1 }, byClientIp: { '127.0.0.1': 1 }, recent: [{ timestamp: new Date().toISOString(), method: 'GET', path: '/overview', status: 200, clientIp: '127.0.0.1', durationMs: 12 }] },
    observability: { enabled: true, logsEnabled: true, logFormat: 'json', serviceName: 'norn', bundleAvailable: true, retention: '30d' },
  }
}

function renderApp(path = '/overview') {
  window.history.pushState({}, '', path)
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <App />
      </ToastProvider>
    </QueryClientProvider>,
  )
}

describe('App shell routing', () => {
  beforeEach(() => {
    vi.stubGlobal('WebSocket', MockWebSocket)
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, value),
      removeItem: (key: string) => store.delete(key),
      clear: () => store.clear(),
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('persists sidebar collapse and highlights the active route', async () => {
    installFetch()
    renderApp('/apps')

    await screen.findByRole('heading', { name: 'Apps' })
    expect(screen.getByRole('link', { name: /apps/i })).toHaveClass('active')
    fireEvent.click(screen.getByRole('button', { name: 'Collapse sidebar' }))
    expect(localStorage.getItem('norn.sidebar.collapsed:v1')).toBe('true')
    expect(document.querySelector('.norn-shell')).toHaveClass('sidebar-collapsed')
  })

  it('toggles the document theme from the page header', async () => {
    document.documentElement.dataset.theme = 'dark'
    installFetch()
    renderApp('/overview')

    await screen.findByRole('heading', { name: 'Overview' })
    await screen.findAllByText('PG')
    const switchToLight = screen.getByRole('button', { name: 'Switch to light theme' })
    expect(switchToLight.querySelector('.fa-moon')).toBeInTheDocument()

    fireEvent.click(switchToLight)
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem('norn-theme:v1')).toBe('light')

    const switchToDark = screen.getByRole('button', { name: 'Switch to dark theme' })
    expect(switchToDark.querySelector('.fa-sun')).toBeInTheDocument()
  })

  it('renders desired fleet pools and creates a durable expansion receipt', async () => {
    const { calls } = installFetch({
      '/api/v1/fleet/node-pools/app/plan': () => json({ id: 'plan-1', kind: 'fleet.capacity-plan', status: 'succeeded', message: 'capacity plan recorded', payload: { pool: 'app', action: 'scale', current: { desired: 2 }, proposed: { desired: 3 } }, metadata: {} }, 201),
      '/api/v1/fleet/node-pools': json({
        schemaVersion: 'norn.fleet-inventory/v1', configured: true, digest: 'sha256:1234567890abcdef',
        document: { apiVersion: 'norn.dev/fleet/v1', kind: 'Cluster', cluster: { name: 'production-nyc3', provider: 'digitalocean', region: 'nyc3' }, metadata: { environment: 'production' } },
        validation: { schemaVersion: 'norn.validation-report/v1', documentKind: 'fleet', valid: true, findings: [] },
        nodePools: { app: { size: 's-4vcpu-8gb', min: 2, desired: 2, max: 8, labels: { workload: 'app' }, replacement: { strategy: 'blueGreen', drainTimeout: '15m' } } },
      }),
      '/api/v1/fleet/plans': () => json({ count: 0, plans: [] }),
    })
    renderApp('/fleet')
    await screen.findByRole('heading', { name: 'production-nyc3' })
    expect(screen.getByRole('heading', { name: 'app' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Change capacity' }))
    fireEvent.change(screen.getByLabelText('Desired'), { target: { value: '3' } })
    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'add headroom' } })
    fireEvent.click(screen.getByRole('button', { name: 'Record expansion' }))
    await waitFor(() => expect(calls.some((call) => call === 'POST /api/v1/fleet/node-pools/app/plan')).toBe(true))
    expect(await screen.findByText('plan-1', { selector: '.fleet-receipt code' })).toBeInTheDocument()
  })

  it('reconstructs an interrupted fleet change and its proven checkpoints', async () => {
    installFetch({
      '/api/v1/fleet/node-pools': json({
        schemaVersion: 'norn.fleet-inventory/v1', configured: true,
        document: { apiVersion: 'norn.dev/fleet/v1', kind: 'Cluster', metadata: { environment: 'production', workflowUrl: 'https://github.com/acme/norn-fleet/actions/workflows/apply.yml' }, cluster: { name: 'production-nyc3', provider: 'digitalocean', region: 'nyc3' } },
        validation: { schemaVersion: 'norn.validation-report/v1', documentKind: 'fleet', valid: true, findings: [] },
        nodePools: { app: { size: 's-4vcpu-8gb', min: 2, desired: 3, max: 8 } },
      }),
      '/api/v1/fleet/plans/plan-1/reconciliations': json({ schemaVersion: 'norn.fleet-reconciliations/v1', planId: 'plan-1', count: 2, reconciliations: [
        { id: 'checkpoint-1', status: 'succeeded', payload: { phase: 'infrastructure_applied' } },
        { id: 'checkpoint-2', status: 'succeeded', payload: { phase: 'readiness_verified' } },
      ] }),
      '/api/v1/fleet/plans': json({ count: 1, plans: [{ id: 'plan-1', status: 'succeeded', payload: { pool: 'app', action: 'scale', current: { desired: 2 }, proposed: { desired: 3 } } }] }),
    })
    renderApp('/fleet')

    expect(await screen.findByText(/Closing this page does not lose/)).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: /app.*2 → 3 nodes/i }))
    expect(await screen.findByText('Readiness Verified')).toBeInTheDocument()
    expect(screen.getAllByText('Proven')).toHaveLength(2)
    expect(screen.getByRole('link', { name: /Continue in protected runner/i })).toHaveAttribute('href', expect.stringContaining('github.com/acme/norn-fleet'))
  })

  it('creates and safely recovers GitHub fleet review and apply actions', async () => {
    const { calls } = installFetch({
      '/api/v1/fleet/plans/plan-1/github/pull-request': () => json({ id: 'review-1', kind: 'fleet.github.pull-request', status: 'succeeded', payload: { url: 'https://github.com/acme/norn-fleet/pull/7' } }, 201),
      '/api/v1/fleet/plans/plan-1/github/dispatch': () => json({ id: 'apply-1', kind: 'fleet.github.apply-dispatch', status: 'succeeded', payload: { url: 'https://github.com/acme/norn-fleet/actions/runs/9' } }, 201),
      '/api/v1/fleet/github': json({ schemaVersion: 'norn.fleet-github-status/v1', configured: true, connected: true, repository: 'acme/norn-fleet' }),
      '/api/v1/fleet/node-pools': json({
        schemaVersion: 'norn.fleet-inventory/v1', configured: true,
        document: { apiVersion: 'norn.dev/fleet/v1', kind: 'Cluster', metadata: { repository: 'acme/norn-fleet' }, cluster: { name: 'production-nyc3', provider: 'digitalocean', region: 'nyc3' } },
        validation: { schemaVersion: 'norn.validation-report/v1', documentKind: 'fleet', valid: true, findings: [] },
        nodePools: { app: { size: 's-4vcpu-8gb', min: 2, desired: 3, max: 8 } },
      }),
      '/api/v1/fleet/plans': json({ count: 1, plans: [{ id: 'plan-1', status: 'succeeded', payload: { pool: 'app', action: 'scale', current: { desired: 2 }, proposed: { desired: 3 } } }] }),
    })
    renderApp('/fleet')

    expect(await screen.findByText('GitHub connected')).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: /app.*2 → 3 nodes/i }))
    fireEvent.click(screen.getByRole('button', { name: 'Open review' }))
    await waitFor(() => expect(calls).toContain('POST /api/v1/fleet/plans/plan-1/github/pull-request'))
    expect(await screen.findByRole('link', { name: /View pull request/i })).toHaveAttribute('href', 'https://github.com/acme/norn-fleet/pull/7')
    fireEvent.click(screen.getByRole('button', { name: 'Apply after review' }))
    await waitFor(() => expect(calls).toContain('POST /api/v1/fleet/plans/plan-1/github/dispatch'))
    expect(await screen.findByRole('link', { name: /View apply run/i })).toHaveAttribute('href', 'https://github.com/acme/norn-fleet/actions/runs/9')
  })

  it('stages contraction behind readiness and drain proof', async () => {
    installFetch({
      '/api/v1/fleet/node-pools': json({
        schemaVersion: 'norn.fleet-inventory/v1', configured: true,
        document: { apiVersion: 'norn.dev/fleet/v1', kind: 'Cluster', cluster: { name: 'production-nyc3', provider: 'digitalocean', region: 'nyc3' } },
        validation: { schemaVersion: 'norn.validation-report/v1', documentKind: 'fleet', valid: true, findings: [] },
        nodePools: { app: { size: 's-4vcpu-8gb', min: 2, desired: 3, max: 8 } },
      }),
      '/api/v1/fleet/plans': () => json({ count: 0, plans: [] }),
    })
    renderApp('/fleet')

    fireEvent.click(await screen.findByRole('button', { name: 'Change capacity' }))
    fireEvent.change(screen.getByLabelText('Desired'), { target: { value: '2' } })
    expect(screen.getByRole('button', { name: 'Prepare contraction' })).toBeInTheDocument()
    expect(screen.getByText(/Old nodes are not removed until Norn has proof/)).toBeInTheDocument()
  })

  it('explains why fleet planning is unavailable for an invalid document', async () => {
    installFetch({
      '/api/v1/fleet/node-pools': json({
        schemaVersion: 'norn.fleet-inventory/v1', configured: true,
        document: { apiVersion: 'norn.dev/fleet/v1', kind: 'Cluster', cluster: { name: 'production-nyc3', provider: 'digitalocean', region: 'nyc3' } },
        validation: { schemaVersion: 'norn.validation-report/v1', documentKind: 'fleet', valid: false, findings: [{ severity: 'error', code: 'fleet.node-pools.required', field: 'nodePools', message: 'at least one node pool is required' }] },
        nodePools: { app: { size: 's-4vcpu-8gb', min: 2, desired: 2, max: 8 } },
      }),
    })
    renderApp('/fleet')

    expect(await screen.findByText('Planning is unavailable until the fleet document is valid.')).toHaveAttribute('role', 'status')
    expect(screen.queryByRole('button', { name: 'Change capacity' })).not.toBeInTheDocument()
  })

  it('opens, filters, executes, and escapes the command palette', async () => {
    const { calls } = installFetch()
    renderApp('/overview')
    await screen.findByRole('heading', { name: 'Overview' })
    await screen.findByText('healthy')

    fireEvent.keyDown(window, { key: 'k', ctrlKey: true })
    const input = screen.getByPlaceholderText('Search apps, views, actions')
    fireEvent.change(input, { target: { value: 'Deploy api' } })
    expect(screen.getByRole('option', { name: /Deploy api/i })).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole('dialog', { name: 'Command palette' }), { key: 'Enter' })
    await waitFor(() => expect(calls.some((call) => call === 'POST /api/apps/api/deploy')).toBe(true))

    fireEvent.keyDown(window, { key: 'k', metaKey: true })
    expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument())
  })

  it('opens the first-class fleet view from the command palette', async () => {
    installFetch()
    renderApp('/overview')
    await screen.findByRole('heading', { name: 'Overview' })
    await screen.findByRole('link', { name: 'Fleet' })

    fireEvent.keyDown(window, { key: 'k', metaKey: true })
    const input = screen.getByPlaceholderText('Search apps, views, actions')
    fireEvent.change(input, { target: { value: 'Fleet' } })
    fireEvent.click(screen.getByRole('option', { name: /Fleet/i }))

    await screen.findByRole('heading', { name: 'Fleet' })
    expect(await screen.findByText('Connect norn-fleet')).toBeInTheDocument()
  })

  it('hides fleet navigation when the server does not advertise fleet capabilities', async () => {
    installFetch({ '/api/v1/capabilities': json({ protocolVersion: 1, serverVersion: 'legacy', features: [] }) })
    renderApp('/fleet')

    expect(await screen.findByRole('heading', { name: 'Fleet unavailable' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Fleet' })).not.toBeInTheDocument()
  })

  it('syncs app filters to URL params', async () => {
    installFetch()
    renderApp('/apps')
    await screen.findByRole('button', { name: 'healthy' })

    fireEvent.click(screen.getByRole('button', { name: 'unhealthy' }))
    expect(window.location.search).toContain('filter=unhealthy')
    fireEvent.change(screen.getByLabelText('Filter apps'), { target: { value: 'worker' } })
    expect(window.location.search).toContain('q=worker')
    await waitFor(() => expect(screen.getByRole('button', { name: 'worker' })).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: 'api' })).not.toBeInTheDocument()
  })

  it('requires confirmation before enabling deployments for a draft', async () => {
    const { calls } = installFetch()
    renderApp('/apps')

    const enableButtons = await screen.findAllByRole('button', { name: 'Enable deploys' })
    fireEvent.click(enableButtons[0])
    expect(screen.getByRole('dialog', { name: 'Enable deployments?' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Enable deployments' }))
    await waitFor(() => expect(calls.some((call) => call === 'PUT /api/v1/apps/api/deployment')).toBe(true))
  })

  it('routes app detail tabs from the URL', async () => {
    installFetch()
    renderApp('/apps/api/logs')

    await screen.findByRole('heading', { name: 'api' })
    await waitFor(() => expect(screen.getByRole('tab', { name: 'logs' })).toHaveAttribute('aria-selected', 'true'))
    expect(await screen.findByText('Waiting for logs...')).toBeInTheDocument()
  })

  it('renders overview empty and error panel states', async () => {
    installFetch({ '/api/events/active': json({ incidents: [] }, 500) })
    renderApp('/overview')

    await screen.findByRole('heading', { name: 'Overview' })
    await waitFor(() => expect(screen.getByText('No running operations')).toBeInTheDocument())
    expect(await screen.findByRole('alert')).toHaveTextContent('Request failed with status 500')
  })

  it('renders live API wrapper shapes for overview, operations, and incidents', async () => {
    const now = new Date().toISOString()
    installFetch({
      '/api/events/active': () => json({
        incidents: [{
          correlationKey: 'api:db-down',
          app: 'api',
          latestSeverity: 'critical',
          latestType: 'health',
          latestTitle: 'Database unreachable',
          eventCount: 3,
          firstSeen: now,
          lastSeen: now,
          openCount: 3,
          latestEventId: 'evt-latest',
        }],
      }),
      '/api/events': () => json({
        events: [{
          id: 'evt-1',
          source: 'beacon',
          app: 'api',
          environment: 'prod',
          type: 'health',
          severity: 'warning',
          state: 'open',
          title: 'Latency elevated',
          body: 'p95 above threshold',
          dedupeKey: 'api:latency',
          occurredAt: now,
          metadata: {},
        }],
      }),
      '/api/operations/active': () => json({
        count: 1,
        operations: [{ id: 'op-1', sagaId: 'saga-1', kind: 'deploy', app: 'api', status: 'running', attempts: 1, maxAttempts: 3, updatedAt: now }],
      }),
      '/api/operations': () => json({
        count: 1,
        operations: [{ id: 'op-2', sagaId: 'saga-2', kind: 'preflight', app: 'worker', status: 'queued', attempt: 2, maxAttempts: 4, startedAt: now }],
      }),
    })

    renderApp('/overview')
    await screen.findByRole('heading', { name: 'Overview' })
    expect(await screen.findByText('Database unreachable')).toBeInTheDocument()
    expect(screen.getByText('3 events')).toBeInTheDocument()
    expect(screen.getByText('deploy')).toBeInTheDocument()
    expect(screen.getByText('1/3')).toBeInTheDocument()
    expect(screen.queryByText('Channel')).not.toBeInTheDocument()

    window.history.pushState({}, '', '/operations')
    fireEvent.popState(window)
    expect(await screen.findByText('preflight')).toBeInTheDocument()
    expect(screen.getByText('attempt 2/4')).toBeInTheDocument()

    window.history.pushState({}, '', '/incidents')
    fireEvent.popState(window)
    expect(await screen.findByText('Latency elevated')).toBeInTheDocument()
    expect(screen.getByText('p95 above threshold')).toBeInTheDocument()
  })

  it('renders fleet health issue rows and caps each group at 5', async () => {
    const names = ['api', 'worker', 'queue', 'cron', 'billing', 'search']
    installFetch({
      '/api/apps': () => json(names.map(name => appWith(name, false))),
      '/api/access/patterns': () => json({
        windowHours: 24,
        idleAfterHours: 72,
        patterns: names.map(name => ({
          app: name,
          process: 'web',
          type: 'http',
          status: 'idle',
          windowHours: 24,
          totalRequests: 0,
          successes: 0,
          clientErrors: 0,
          serverErrors: 0,
          activeHours: 0,
          hourlyUtc: {},
          weekdayUtc: {},
          idleCandidate: true,
          recommendedAction: 'scale down',
          confidence: 'high',
        })),
      }),
    })
    renderApp('/overview')

    await screen.findByRole('heading', { name: 'Overview' })
    await waitFor(() => expect(document.querySelectorAll('.fleet-health-row .ui-status-chip.ui-status-danger')).toHaveLength(5))
    expect(document.querySelectorAll('.fleet-health-row .ui-status-chip.ui-status-warning')).toHaveLength(5)
    expect(screen.getAllByText(/\+1 more/)).toHaveLength(2)
  })

  it('caps overview incident groups at 5 inside a scroll container', async () => {
    const now = new Date().toISOString()
    installFetch({
      '/api/events/active': () => json({
        incidents: Array.from({ length: 6 }, (_, index) => ({
          correlationKey: `api:incident-${index}`,
          app: 'api',
          latestSeverity: index === 0 ? 'critical' : 'warning',
          latestType: 'health',
          latestTitle: `Incident ${index}`,
          eventCount: 1,
          firstSeen: now,
          lastSeen: now,
          openCount: 1,
          latestEventId: `evt-${index}`,
        })),
      }),
    })
    renderApp('/overview')

    await screen.findByText('Incident 0')
    expect(document.querySelector('.incident-panel-scroll')).toBeInTheDocument()
    expect(document.querySelectorAll('.incident-panel-scroll .compact-row')).toHaveLength(5)
    expect(screen.queryByText('Incident 5')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: /All incidents/i })).toHaveAttribute('href', '/incidents')
  })

  it('opens the incident drawer from overview, renders timeline, and acknowledges the latest event', async () => {
    const now = new Date().toISOString()
    const { calls } = installFetch({
      '/api/events/active': () => json({
        incidents: [{
          correlationKey: 'api:db-down',
          app: 'api',
          latestSeverity: 'critical',
          latestType: 'health',
          latestTitle: 'Database unreachable',
          eventCount: 2,
          firstSeen: now,
          lastSeen: now,
          openCount: 2,
          latestEventId: 'evt-latest',
        }],
      }),
      '/api/events/correlated': () => json({
        correlationKey: 'api:db-down',
        events: [
          { id: 'evt-latest', app: 'api', type: 'health', severity: 'critical', state: 'open', title: 'Database unreachable', body: 'postgres timed out', occurredAt: now, metadata: { correlationKey: 'api:db-down' } },
          { id: 'evt-prior', app: 'api', type: 'health', severity: 'warning', state: 'open', title: 'Database slow', occurredAt: now, metadata: { correlationKey: 'api:db-down' } },
        ],
      }),
      '/api/events?app=': () => json({
        total: 2,
        events: [
          { id: 'evt-latest', app: 'api', type: 'health', severity: 'critical', state: 'open', title: 'Database unreachable', occurredAt: now, metadata: { correlationKey: 'api:db-down' } },
          { id: 'evt-deploy', app: 'api', type: 'deploy', severity: 'info', state: 'open', title: 'Deploy finished', occurredAt: now, metadata: {} },
        ],
      }),
      '/api/events/evt-latest/ack': () => json({ ok: true }),
    })
    renderApp('/overview')

    fireEvent.click(await screen.findByRole('button', { name: /Database unreachable/i }))
    expect(await screen.findByRole('dialog', { name: 'Incident context' })).toBeInTheDocument()
    expect(await screen.findByText('Database slow')).toBeInTheDocument()
    expect(screen.getByText('Deploy finished')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Ack' }))
    await waitFor(() => expect(calls).toContain('POST /api/events/evt-latest/ack'))
    await waitFor(() => expect(calls.filter(call => call === 'GET /api/events/active').length).toBeGreaterThan(1))
  })

  it('syncs platform sub-tabs to routes', async () => {
    installFetch({
      '/api/platform/releases': () => json({ current: 'sha-current', releases: [{ sha: 'sha-current', version: 'v1', createdAt: new Date().toISOString(), path: '/releases/v1', current: true }] }),
    })
    renderApp('/platform/releases')

    await screen.findByRole('heading', { name: 'Norn Platform' })
    expect(screen.getByRole('tab', { name: 'Releases' })).toHaveAttribute('aria-selected', 'true')
    expect(await screen.findByText('v1')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: 'Observability' }))
    expect(window.location.pathname).toBe('/platform/observability')
    expect(await screen.findByText('Metrics Configuration')).toBeInTheDocument()
  })

  it('groups incidents by severity and filters by app', async () => {
    const now = new Date().toISOString()
    installFetch({
      '/api/events': () => json({
        events: [
          { id: 'evt-critical', app: 'api', type: 'health', severity: 'critical', state: 'open', title: 'API down', occurredAt: now, metadata: {} },
          { id: 'evt-warning', app: 'worker', type: 'latency', severity: 'warning', state: 'open', title: 'Worker slow', occurredAt: now, metadata: {} },
        ],
      }),
    })
    renderApp('/incidents')

    expect(await screen.findByText('API down')).toBeInTheDocument()
    expect(screen.getByText('Worker slow')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /critical/i })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/App/i), { target: { value: 'worker' } })
    expect(screen.queryByText('API down')).not.toBeInTheDocument()
    expect(screen.getByText('Worker slow')).toBeInTheDocument()
  })

  it('filters operations and renders next attempt countdown', async () => {
    const nextAttemptAt = new Date(Date.now() + 60_000).toISOString()
    installFetch({
      '/api/operations': () => json({
        count: 2,
        operations: [
          { id: 'op-1', sagaId: 'saga-1', kind: 'deploy', app: 'api', status: 'failed', attempts: 2, maxAttempts: 4, risk: 'high', lastError: 'boom', nextAttemptAt },
          { id: 'op-2', sagaId: 'saga-2', kind: 'restart', app: 'worker', status: 'succeeded', attempts: 1, maxAttempts: 1, risk: 'low' },
        ],
      }),
    })
    renderApp('/operations')

    expect(await screen.findByText('deploy')).toBeInTheDocument()
    expect(screen.getByText('attempt 2/4')).toBeInTheDocument()
    expect(screen.getByText(/next in/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'succeeded' }))
    expect(screen.queryByText('deploy')).not.toBeInTheDocument()
    expect(screen.getByText('restart')).toBeInTheDocument()
  })

  it('renders topology with token-themed classes', async () => {
    installFetch({
      '/api/services/manifest': () => json({
        version: 1,
        generatedAt: new Date().toISOString(),
        networkMode: 'dev',
        services: [{
          name: 'api-web',
          app: 'api',
          process: 'web',
          type: 'http',
          status: 'passing',
          reachability: { endpointScope: 'public', instanceScope: 'lan', exposure: 'public', routable: true },
          endpoints: [{ url: 'https://api.example.test' }],
          instances: [{ node: 'node-1', address: '127.0.0.1', port: 8800, status: 'passing' }],
        }],
      }),
      '/api/cloudflared/ingress': () => json({ hostnames: ['api.example.test'] }),
    })
    renderApp('/topology')

    expect(await screen.findByRole('application', { name: /traffic topology/i })).toBeInTheDocument()
    expect(document.querySelector('.topology-view')).toBeInTheDocument()
    expect(document.querySelector('.topology-scope-public')).toBeInTheDocument()
  })

  it('redirects bare platform route to releases', async () => {
    installFetch()
    renderApp('/platform')
    expect(await screen.findByRole('tab', { name: 'Releases' })).toHaveAttribute('aria-selected', 'true')
    expect(window.location.pathname).toBe('/platform/releases')
  })

  it('renders platform network service exposure', async () => {
    installFetch()
    renderApp('/platform/network')
    expect(await screen.findByRole('tab', { name: 'Network' })).toHaveAttribute('aria-selected', 'true')
    expect(await screen.findByText('Service Exposure')).toBeInTheDocument()
    expect(screen.getByText('Services')).toBeInTheDocument()
  })

  it('renders platform access grants and patterns', async () => {
    installFetch({
      '/api/access/grants': () => json({ grants: [{ id: 'grant-1', ip: '10.0.0.1', note: 'office', createdBy: 'me', createdAt: new Date().toISOString(), expiresAt: new Date().toISOString() }] }),
    })
    renderApp('/platform/access')
    expect(await screen.findByText('10.0.0.1')).toBeInTheDocument()
    expect(screen.getAllByText('127.0.0.1').length).toBeGreaterThan(0)
  })

  it('renders empty notifications tab state', async () => {
    installFetch()
    renderApp('/platform/notifications')
    expect(await screen.findByText('No notification channels')).toBeInTheDocument()
  })

  it('posts observability install action', async () => {
    const { calls } = installFetch()
    renderApp('/platform/observability')
    fireEvent.click(await screen.findByRole('button', { name: 'Install services' }))
    await waitFor(() => expect(calls).toContain('POST /api/observability/services/install'))
  })

  it('filters incidents by severity', async () => {
    const now = new Date().toISOString()
    installFetch({
      '/api/events': () => json({
        events: [
          { id: 'evt-critical', app: 'api', type: 'health', severity: 'critical', state: 'open', title: 'API down', occurredAt: now, metadata: {} },
          { id: 'evt-warning', app: 'api', type: 'latency', severity: 'warning', state: 'open', title: 'API slow', occurredAt: now, metadata: {} },
        ],
      }),
    })
    renderApp('/incidents')
    expect(await screen.findByText('API down')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'warning' }))
    expect(screen.queryByText('API down')).not.toBeInTheDocument()
    expect(screen.getByText('API slow')).toBeInTheDocument()
  })

  it('snoozes incidents from the drawer with selected duration', async () => {
    const now = new Date().toISOString()
    const { calls } = installFetch({
      '/api/events': () => json({ events: [{ id: 'evt-1', app: 'api', type: 'health', severity: 'critical', state: 'open', title: 'API down', occurredAt: now, metadata: {} }] }),
      '/api/events?app=': () => json({ events: [], total: 0 }),
      '/api/events/evt-1/snooze': () => json({ ok: true }),
    })
    renderApp('/incidents')
    fireEvent.click(await screen.findByRole('button', { name: /API down/i }))
    fireEvent.change(await screen.findByLabelText(/Snooze/i), { target: { value: '8h' } })
    fireEvent.click(screen.getByRole('button', { name: 'Snooze' }))
    await waitFor(() => expect(calls).toContain('POST /api/events/evt-1/snooze'))
  })

  it('acknowledges incidents from the drawer', async () => {
    const now = new Date().toISOString()
    const { calls } = installFetch({
      '/api/events': () => json({ events: [{ id: 'evt-1', app: 'api', type: 'health', severity: 'critical', state: 'open', title: 'API down', occurredAt: now, metadata: {} }] }),
      '/api/events?app=': () => json({ events: [], total: 0 }),
      '/api/events/evt-1/ack': () => json({ ok: true }),
    })
    renderApp('/incidents')
    fireEvent.click(await screen.findByRole('button', { name: /API down/i }))
    fireEvent.click(screen.getByRole('button', { name: 'Ack' }))
    await waitFor(() => expect(calls).toContain('POST /api/events/evt-1/ack'))
  })

  it('renders operation last error details', async () => {
    installFetch({
      '/api/operations': () => json({ count: 1, operations: [{ id: 'op-1', sagaId: 'saga-1', kind: 'deploy', app: 'api', status: 'failed', attempts: 2, maxAttempts: 4, risk: 'high', lastError: 'boom' }] }),
    })
    renderApp('/operations')
    expect(await screen.findByText('last error')).toBeInTheDocument()
    expect(screen.getByText('boom')).toBeInTheDocument()
  })

  it('renders saga timeline payloads and event deltas', async () => {
    const start = new Date('2026-07-03T12:00:00Z').toISOString()
    const end = new Date('2026-07-03T12:00:03Z').toISOString()
    installFetch({
      '/api/saga/saga-1': () => json([
        { id: 'e1', timestamp: start, event: 'queued', status: 'queued', payload: { step: 1 } },
        { id: 'e2', timestamp: end, event: 'running', status: 'running', payload: { step: 2 } },
      ]),
    })
    renderApp('/operations/saga-1')
    expect((await screen.findAllByText('queued')).length).toBeGreaterThan(0)
    expect(screen.getByText((_, node) => node?.textContent === '+3s')).toBeInTheDocument()
    expect(screen.getAllByText('payload')).toHaveLength(2)
  })

  it('toggles topology scope controls', async () => {
    installFetch()
    renderApp('/topology')
    const publicScope = await screen.findByRole('button', { name: 'Public' })
    expect(publicScope).toHaveClass('active')
    fireEvent.click(publicScope)
    expect(publicScope).not.toHaveClass('active')
  })

  it('shows platform release rollback confirmation', async () => {
    installFetch({
      '/api/platform/releases': () => json({ current: 'current', releases: [
        { sha: 'current', version: 'v2', createdAt: new Date().toISOString(), path: '/releases/v2', current: true },
        { sha: 'previous', version: 'v1', createdAt: new Date().toISOString(), path: '/releases/v1', current: false },
      ] }),
    })
    renderApp('/platform/releases')
    fireEvent.click(await screen.findByRole('button', { name: 'Rollback' }))
    expect(screen.getByRole('dialog', { name: 'Rollback platform release' })).toBeInTheDocument()
  })
})
