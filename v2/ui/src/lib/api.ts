const API_BASE = import.meta.env.VITE_API_URL || ''

export class ApiError extends Error {
  status: number
  detail?: unknown

  constructor(status: number, message: string, detail?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
  }
}

export function apiUrl(path: string): string {
  return `${API_BASE}${path}`
}

export function wsUrl(): string {
  if (API_BASE) {
    const url = new URL(API_BASE)
    const protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${protocol}//${url.host}/ws`
  }
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws`
}

// Include credentials for cross-origin requests (CF Access cookies).
export const fetchOpts: RequestInit = API_BASE ? { credentials: 'include' } : {}

function jsonHeaders(headers: HeadersInit | undefined): Headers {
  const next = new Headers(headers)
  if (!next.has('Accept')) next.set('Accept', 'application/json')
  return next
}

async function parseBody(response: Response): Promise<unknown> {
  const text = await response.text()
  if (!text) return undefined
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

function errorMessage(status: number, body: unknown): string {
  if (body && typeof body === 'object') {
    const record = body as Record<string, unknown>
    const message = record['message'] ?? record['error']
    if (typeof message === 'string' && message.trim()) return message
  }
  if (typeof body === 'string' && body.trim()) return body
  return `Request failed with status ${status}`
}

export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(apiUrl(path), {
    ...fetchOpts,
    ...init,
    headers: jsonHeaders(init.headers),
  })
  const body = await parseBody(response)

  if (!response.ok) {
    const detail = body && typeof body === 'object' && 'detail' in body ? (body as Record<string, unknown>)['detail'] : body
    throw new ApiError(response.status, errorMessage(response.status, body), detail)
  }

  return body as T
}
