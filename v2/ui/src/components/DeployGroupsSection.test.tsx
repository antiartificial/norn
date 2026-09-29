import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from './ui/Toast.tsx'
import { DeployGroupsSection } from './DeployGroupsSection.tsx'

function json(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

function renderSection() {
  return render(<ToastProvider><DeployGroupsSection /></ToastProvider>)
}

describe('DeployGroupsSection durable acceptance', () => {
  beforeEach(() => localStorage.clear())

  it('retries a partial group with the same key and preserves member receipts', async () => {
    const headers: string[] = []
    let attempts = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.method === 'POST') {
        headers.push(new Headers(init.headers).get('Idempotency-Key') ?? '')
        attempts += 1
        if (attempts === 1) return json({ group: 'core', operationId: 'parent-1', deploys: [
          { app: 'api', sagaId: 'saga-api', operationId: 'operation-api' },
          { app: 'worker', error: 'worker unavailable' },
        ] }, 202)
        return json({ group: 'core', operationId: 'parent-1', replayed: true, deploys: [
          { app: 'api', sagaId: 'saga-api', operationId: 'operation-api', replayed: true },
          { app: 'worker', sagaId: 'saga-worker', operationId: 'operation-worker' },
        ] }, 200)
      }
      if (url.includes('/api/deploy-groups')) return json({ groups: [{ name: 'core', apps: [{ app: 'api' }, { app: 'worker', waitReady: true }] }] })
      return json({})
    }))

    renderSection()
    fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
    const card = screen.getByText('core').closest('.deploy-group-card') as HTMLElement | null
    if (!card) throw new Error('deploy group card missing')
    expect(await within(card).findByRole('alert')).toHaveTextContent('1 member accepted; 1 failed')
    expect(within(card).getByRole('alert')).toHaveTextContent('operation-api')
    fireEvent.click(screen.getByRole('button', { name: 'Retry same request' }))
    await waitFor(() => expect(headers).toHaveLength(2))
    expect(headers[1]).toBe(headers[0])
    expect(await within(card).findByRole('status')).toHaveTextContent('Resolved the existing accepted deploy group')
    expect(within(card).getByRole('status')).toHaveTextContent('operation-worker')
  })

  it('creates a different key only through the explicit whole-group action', async () => {
    const headers: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') {
        headers.push(new Headers(init.headers).get('Idempotency-Key') ?? '')
        return json({ group: 'core', operationId: `parent-${headers.length}`, deploys: [
          { app: 'api', sagaId: 'saga-api', operationId: 'operation-api' },
          { app: 'worker', error: 'worker unavailable' },
        ] }, 202)
      }
      return json({ groups: [{ name: 'core', apps: [{ app: 'api' }, { app: 'worker' }] }] })
    }))

    renderSection()
    fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Start new whole-group deploy' }))
    await waitFor(() => expect(headers).toHaveLength(2))
    expect(headers[1]).not.toBe(headers[0])
  })
})
