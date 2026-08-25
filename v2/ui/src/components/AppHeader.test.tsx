import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppHeader } from './AppHeader.tsx'
import type { AppStatus, Deployment, EventsResponse } from '../types/index.ts'

const app: AppStatus = {
  spec: {
    name: 'api',
    processes: { web: { port: 8800, resources: { cpu: 50, memory: 2048 } } },
    endpoints: [{ url: 'https://api.example.test' }],
  },
  nomadStatus: 'running',
  healthy: true,
  allocations: [{ id: 'alloc-1', taskGroup: 'web', status: 'running', lifecycle: 'active', healthy: true, nodeName: 'Aarons-Mac-mini' }],
  allocationSummary: { running: 1, active: 1, retained: 0, total: 1 },
}

function deployment(appName = 'api'): Deployment {
  return {
    id: `${appName}-deploy`,
    app: appName,
    commitSha: 'a1b2c3d4e5f6',
    imageTag: 'img',
    sagaId: 'saga-1',
    status: 'deployed',
    startedAt: '2026-01-01T10:55:00.000Z',
    finishedAt: '2026-01-01T11:00:00.000Z',
  }
}

function json(data: unknown) {
  return new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } })
}

function renderHeader(deployments: Deployment[]) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes('/api/deployments')) return json(deployments)
    const events: EventsResponse = { events: [], total: 0 }
    if (url.includes('/api/events')) return json(events)
    return json({})
  }))
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <AppHeader app={app} onAction={() => undefined} onScale={() => undefined} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('AppHeader', () => {
  beforeEach(() => {
    vi.setSystemTime(new Date('2026-01-01T13:00:00.000Z'))
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('renders deploy version, relative time, endpoint link, resources, and allocations', async () => {
    renderHeader([deployment('worker'), deployment('api')])

    expect(await screen.findByText('a1b2c3d')).toBeInTheDocument()
    expect(screen.getByText('2h ago')).toHaveAttribute('title', expect.stringContaining('2026'))
    expect(screen.getByRole('link', { name: /https:\/\/api.example.test/i })).toHaveAttribute('href', 'https://api.example.test')
    expect(screen.getByText('web · cpu 50 · mem 2048 MB')).toBeInTheDocument()
    expect(screen.getByText('1 allocation · running on Aarons-Mac-mini')).toBeInTheDocument()
  })

  it('renders no deployments fallback', async () => {
    renderHeader([])

    expect(await screen.findByText('no deployments yet')).toBeInTheDocument()
  })
})
