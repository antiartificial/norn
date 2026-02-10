import { useState, useEffect, useCallback } from 'react'
import type { AppStatus } from '../types/index.ts'

export function useApps() {
  const [apps, setApps] = useState<AppStatus[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchApps = useCallback(async () => {
    try {
      const res = await fetch('/api/apps')
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
    const interval = setInterval(fetchApps, 15000)
    return () => clearInterval(interval)
  }, [fetchApps])

  return { apps, loading, error, refetch: fetchApps }
}
