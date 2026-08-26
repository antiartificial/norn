import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch } from './api.ts'

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
})
