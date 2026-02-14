import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface HealthResponse {
  status: string
  services: Record<string, string>
}

const serviceLabels: Record<string, string> = {
  postgres: 'PG',
  nomad: 'Nomad',
  consul: 'Consul',
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
      {Object.entries(health.services).map(([name, status]) => (
        <span
          key={name}
          className={`status-pill ${status}`}
          title={`${name}: ${status}`}
        >
          <span className={`status-indicator ${status}`} />
          {serviceLabels[name] ?? name}
        </span>
      ))}
    </div>
  )
}
