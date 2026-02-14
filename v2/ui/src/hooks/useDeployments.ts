import { useState, useEffect, useCallback } from 'react'
import type { Deployment } from '../types/index.ts'
import { apiUrl, fetchOpts } from '../lib/api.ts'

export interface DeploymentFilters {
  app: string
  status: string
  limit: number
  offset: number
}

export function useDeployments() {
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [loading, setLoading] = useState(true)
  const [filters, setFilters] = useState<DeploymentFilters>({
    app: '',
    status: '',
    limit: 50,
    offset: 0,
  })

  const fetchDeployments = useCallback(async (f: DeploymentFilters) => {
    setLoading(true)
    try {
      const params = new URLSearchParams()
      if (f.app) params.set('app', f.app)
      if (f.status) params.set('status', f.status)
      params.set('limit', String(f.limit))
      params.set('offset', String(f.offset))
      const res = await fetch(apiUrl(`/api/deployments?${params}`), fetchOpts)
      if (!res.ok) throw new Error(`${res.status}`)
      const data = await res.json()
      setDeployments(data ?? [])
    } catch {
      setDeployments([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchDeployments(filters)
  }, [filters, fetchDeployments])

  const total = deployments.length
  const setApp = (app: string) => setFilters(f => ({ ...f, app, offset: 0 }))
  const setStatus = (status: string) => setFilters(f => ({ ...f, status, offset: 0 }))
  const nextPage = () => setFilters(f => ({ ...f, offset: f.offset + f.limit }))
  const prevPage = () => setFilters(f => ({ ...f, offset: Math.max(0, f.offset - f.limit) }))
  const refetch = () => fetchDeployments(filters)

  return { deployments, total, loading, filters, setApp, setStatus, nextPage, prevPage, refetch }
}
