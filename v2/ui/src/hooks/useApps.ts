import { useState, useEffect, useCallback } from 'react'
import type { AppStatus } from '../types/index.ts'
import { apiUrl, fetchOpts } from '../lib/api.ts'

export function useApps() {
  const [apps, setApps] = useState<AppStatus[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchApps = useCallback(async () => {
    try {
      const res = await fetch(apiUrl('/api/apps'), fetchOpts)
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
      const data = await res.json()
      setApps(data ?? [])
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch apps')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchApps()
    // Poll faster (3s) when in error state, normal (15s) otherwise
    const interval = setInterval(fetchApps, error ? 3000 : 15000)
    return () => clearInterval(interval)
  }, [fetchApps, error])

  return { apps, loading, error, refetch: fetchApps }
}
