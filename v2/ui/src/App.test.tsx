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
    if (url.includes('/api/cloudflared/ingress')) return json({ hostnames: [] })
    if (url.includes('/api/version')) return json({ version: 'test' })
    if (url.includes('/api/health')) return json({ status: 'ok', services: { postgres: 'up', nomad: 'up', consul: 'up' } })
    if (url.includes('/api/events/active')) return json({ incidents: [] })
    if (url.endsWith('/api/events')) return json({ events: [] })
    if (url.includes('/api/operations/active') || url.endsWith('/api/operations')) return json({ count: 0, operations: [] })
    if (url.includes('/api/deployments')) return json([])
    if (url.includes('/api/saga')) return json([])
    return json({})
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, calls }
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
    expect(localStorage.getItem('norn.sidebar.collapsed')).toBe('true')
    expect(document.querySelector('.norn-shell')).toHaveClass('sidebar-collapsed')
  })

  it('opens, filters, executes, and escapes the command palette', async () => {
    const { calls } = installFetch()
    renderApp('/overview')
    await screen.findByRole('heading', { name: 'Overview' })

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
    expect(screen.getByText('2/4')).toBeInTheDocument()

    window.history.pushState({}, '', '/incidents')
    fireEvent.popState(window)
    expect(await screen.findByText('Latency elevated')).toBeInTheDocument()
    expect(screen.getByText('p95 above threshold')).toBeInTheDocument()
  })
})
