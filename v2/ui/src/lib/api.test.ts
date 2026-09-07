import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch, clearMemoryAccessToken, setMemoryAccessToken } from './api.ts'

describe('apiFetch', () => {
  afterEach(() => {
    clearMemoryAccessToken()
    vi.restoreAllMocks()
  })

  it('parses JSON and sends credentials-compatible options', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ ok: true }), { status: 200 })))
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

  it('keeps a paired token in memory and sends it only to configured API paths', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ ok: true }), { status: 200 })))
    vi.stubGlobal('fetch', fetchMock)
    setMemoryAccessToken('paired-device-token')

    await apiFetch('/api/v1/fleet/node-pools')
    expect(fetchMock.mock.calls[0][1].headers.get('Authorization')).toBe('Bearer paired-device-token')
    expect(fetchMock.mock.calls[0][1].redirect).toBe('error')
    expect(localStorage.getItem('norn-access-token')).toBeNull()
    expect(sessionStorage.getItem('norn-access-token')).toBeNull()

    await apiFetch('https://untrusted.example/api/operations')
    expect(fetchMock.mock.calls[1][1].headers.get('Authorization')).toBeNull()
  })
})
