import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch } from './api.ts'
import type { AccessEvent, Deployment, EventsResponse } from '../types/index.ts'

describe('apiFetch', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('parses JSON and sends credentials-compatible options', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(apiFetch<{ ok: boolean }>('/api/health')).resolves.toEqual({ ok: true })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/health')
    expect(fetchMock.mock.calls[0][1].headers.get('Accept')).toBe('application/json')
  })

  it('normalizes structured error payloads', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'nope', detail: { reason: 'bad' } }), { status: 503 })))

    await expect(apiFetch('/api/apps')).rejects.toMatchObject({
      name: 'ApiError',
      status: 503,
      message: 'nope',
      detail: { reason: 'bad' },
    })
  })

  it('normalizes plain-text errors', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('broken', { status: 500 })))

    try {
      await apiFetch('/api/broken')
      throw new Error('expected apiFetch to throw')
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError)
      expect((error as ApiError).status).toBe(500)
      expect((error as ApiError).message).toBe('broken')
      expect((error as ApiError).detail).toBe('broken')
    }
  })

  it('shape-locks deployment, events, and access event response types', () => {
    const deployment = {
      id: 'dep-1',
      app: 'api',
      commitSha: 'a1b2c3d4',
      imageTag: 'api:a1b2c3d4',
      sagaId: 'saga-1',
      status: 'deployed',
      sourceKind: 'git',
      sourceRef: 'main',
      startedAt: '2026-01-01T00:00:00.000Z',
      finishedAt: '2026-01-01T00:01:00.000Z',
    } satisfies Deployment
    const events = {
      events: [{ id: 'evt-1', app: 'api', type: 'health', severity: 'warning', state: 'open', title: 'Slow', occurredAt: '2026-01-01T00:00:00.000Z' }],
      total: 1,
    } satisfies EventsResponse
    const accessEvent = {
      timestamp: '2026-01-01T00:00:00.000Z',
      method: 'GET',
      path: '/api/apps',
      status: 200,
      durationMs: 12,
      clientIp: '127.0.0.1',
      cfAccessEmail: 'me@example.test',
    } satisfies AccessEvent

    expect(deployment.sourceKind).toBe('git')
    expect(events.events[0].severity).toBe('warning')
    expect(accessEvent.durationMs).toBe(12)
  })
})
