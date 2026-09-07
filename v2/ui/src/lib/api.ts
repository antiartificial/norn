const API_BASE = import.meta.env.VITE_API_URL || ''
let memoryAccessToken: string | undefined

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

// Enrollment tokens deliberately live only in this module. They are never
// accepted from URL parameters or persisted in browser storage.
export function setMemoryAccessToken(token: string): void {
  memoryAccessToken = token.trim() || undefined
}

export function clearMemoryAccessToken(): void {
  memoryAccessToken = undefined
}

export function hasMemoryAccessToken(): boolean {
  return Boolean(memoryAccessToken)
}

function isConfiguredControlEndpoint(path: string, url: string): boolean {
  if (!path.startsWith('/api/')) return false
  try {
    const target = new URL(url, window.location.origin)
    const configured = new URL(API_BASE || window.location.origin, window.location.origin)
    return target.origin === configured.origin && target.pathname.startsWith('/api/')
  } catch {
    return false
  }
}

export function wsUrl(): string {
  if (API_BASE) {
    const url = new URL(API_BASE)
    const protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${protocol}//${url.host}/api/v1/events`
  }
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/api/v1/events`
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
  const url = apiUrl(path)
  const headers = jsonHeaders(init.headers)
  if (memoryAccessToken && isConfiguredControlEndpoint(path, url)) {
    headers.set('Authorization', `Bearer ${memoryAccessToken}`)
  }
  const response = await fetch(url, {
    ...fetchOpts,
    ...init,
    // A control endpoint must not silently forward a bearer token to an
    // unexpected redirect destination, even when that redirect is same-origin.
    redirect: 'error',
    headers,
  })
  const body = await parseBody(response)

  if (!response.ok) {
    const detail = body && typeof body === 'object' && 'detail' in body ? (body as Record<string, unknown>)['detail'] : body
    throw new ApiError(response.status, errorMessage(response.status, body), detail)
  }

  return body as T
}
