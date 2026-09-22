import { fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { ActivityLogPage } from './ActivityLogPage.tsx'
import type { BeaconEvent, MutationAuditEvent } from '../types/index.ts'

function json(data: unknown) {
  return new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } })
}

function beacon(overrides: Partial<BeaconEvent> = {}): BeaconEvent {
  return {
    id: 'b1', app: 'demo', type: 'app.restarted', severity: 'info', state: '',
    title: 'App restarted', occurredAt: '2026-01-01T00:00:00.000Z',
    metadata: { actor: 'alice' }, ...overrides,
  }
}

function audit(overrides: Partial<MutationAuditEvent> = {}): MutationAuditEvent {
  return {
    id: 'evt-1', principalSubject: 'alice', method: 'PUT', path: '/api/v1/apps/demo/scale',
    status: 200, outcome: 'succeeded', startedAt: '2026-01-01T00:00:00.000Z', durationMs: 5,
    integrity: 'verified', ...overrides,
  }
}

const events = [
  beacon({ id: 'pod', app: 'demo', title: 'App restarted' }),
  beacon({ id: 'cell', app: '', type: 'nomad.allocation.rescheduled', title: 'Alloc rescheduled' }),
]

function renderPage() {
  vi.stubGlobal('fetch', vi.fn(async (input: unknown) => {
    const url = String(input)
    if (url.includes('/api/v1/audit/mutations')) return json({ schema: 'x', count: 1, events: [audit()] })
    return json({ events }) // /api/events
  }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><ActivityLogPage /></QueryClientProvider>)
}

describe('ActivityLogPage', () => {
  it('defaults to Pods and shows only workload-scoped events, with actor', async () => {
    renderPage()
    expect(await screen.findByText('App restarted')).toBeInTheDocument()
    expect(screen.getByText('by alice')).toBeInTheDocument()
    expect(screen.queryByText('Alloc rescheduled')).not.toBeInTheDocument()
  })

  it('Cell tab shows infra events (no app) and hides pod events', async () => {
    renderPage()
    await screen.findByText('App restarted')
    fireEvent.click(screen.getByRole('tab', { name: 'Cell' }))
    expect(await screen.findByText('Alloc rescheduled')).toBeInTheDocument()
    expect(screen.queryByText('App restarted')).not.toBeInTheDocument()
  })

  it('Receipts tab shows the admin mutation-audit log', async () => {
    renderPage()
    await screen.findByText('App restarted')
    fireEvent.click(screen.getByRole('tab', { name: 'Receipts' }))
    expect(await screen.findByText('/api/v1/apps/demo/scale', { exact: false })).toBeInTheDocument()
  })
})
