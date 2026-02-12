import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface ServiceHealth {
  name: string
  status: string
  details?: string
}

interface HealthResponse {
  status: string
  services: ServiceHealth[]
}

const serviceLabels: Record<string, string> = {
  postgres: 'PG',
  kubernetes: 'K8s',
  valkey: 'KV',
  redpanda: 'RP',
  sops: 'SOPS',
}

export function StatusBar() {
  const [health, setHealth] = useState<HealthResponse | null>(null)

  useEffect(() => {
    async function check() {
      try {
        const res = await fetch(apiUrl('/api/health'), fetchOpts)
        if (res.ok) setHealth(await res.json())
      } catch {
        setHealth(null)
      }
    }
    check()
    const interval = setInterval(check, 30000)
    return () => clearInterval(interval)
  }, [])

  if (!health) return null

  return (
    <div className="status-bar">
      {health.services.map((svc) => (
        <span
          key={svc.name}
          className={`status-pill ${svc.status}`}
          title={svc.details || `${svc.name}: ${svc.status}`}
        >
          <span className={`status-indicator ${svc.status}`} />
          {serviceLabels[svc.name] ?? svc.name}
        </span>
      ))}
    </div>
  )
}
