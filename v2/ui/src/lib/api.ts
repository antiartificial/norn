const API_BASE = import.meta.env.VITE_API_URL || ''

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
