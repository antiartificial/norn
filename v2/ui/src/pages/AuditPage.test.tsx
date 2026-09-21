import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { AuditPage } from './AuditPage.tsx'
import type { MutationAuditEvent, MutationAuditResponse } from '../types/index.ts'

function json(data: unknown) {
  return new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } })
}

function event(overrides: Partial<MutationAuditEvent> = {}): MutationAuditEvent {
  return {
    id: 'evt-1',
    principalSubject: 'alice',
    method: 'PUT',
    path: '/api/v1/apps/{name}/scale',
    status: 200,
    outcome: 'succeeded',
    startedAt: '2026-01-01T00:00:00.000Z',
    finishedAt: '2026-01-01T00:00:01.000Z',
    durationMs: 1000,
    integrity: 'verified',
    ...overrides,
  }
}

function renderPage(response: MutationAuditResponse) {
  vi.stubGlobal('fetch', vi.fn(async () => json(response)))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <AuditPage />
    </QueryClientProvider>,
  )
}

describe('AuditPage', () => {
  it('renders mutation receipts with outcome and integrity', async () => {
    renderPage({ schema: 'norn.mutation-audit/v1', count: 1, events: [event()] })
    expect(await screen.findByText('/api/v1/apps/{name}/scale', { exact: false })).toBeInTheDocument()
    expect(screen.getByText('alice')).toBeInTheDocument()
    // "succeeded" appears both as the row status chip and a derived filter chip
    expect(screen.getAllByText('succeeded').length).toBeGreaterThan(0)
    expect(screen.getByText('verified')).toBeInTheDocument()
  })

  it('filters by outcome', async () => {
    renderPage({
      schema: 'norn.mutation-audit/v1',
      count: 2,
      events: [
        event({ id: 'ok', outcome: 'succeeded', path: '/api/v1/apps/a/scale' }),
        event({ id: 'bad', outcome: 'failed', status: 500, integrity: 'invalid', path: '/api/v1/apps/b/scale' }),
      ],
    })
    // both visible under "all"
    expect(await screen.findByText('/api/v1/apps/a/scale', { exact: false })).toBeInTheDocument()
    expect(screen.getByText('/api/v1/apps/b/scale', { exact: false })).toBeInTheDocument()
    // an outcome filter chip is derived from the data
    expect(screen.getByRole('button', { name: 'failed' })).toBeInTheDocument()
  })

  it('shows an empty state when there are no events', async () => {
    renderPage({ schema: 'norn.mutation-audit/v1', count: 0, events: [] })
    expect(await screen.findByText('No audit events')).toBeInTheDocument()
  })
})
