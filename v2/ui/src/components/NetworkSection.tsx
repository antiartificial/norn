import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import type { ServiceManifest, ServiceManifestEntry } from '../types/index.ts'

interface NetworkSectionProps {
  services: {
    total: number
    public: number
    private: number
    local: number
    internal: number
    byType: Record<string, number>
    byStatus: Record<string, number>
  }
}

function joinCounts(values: Record<string, number>) {
  const parts = Object.entries(values).map(([key, value]) => `${key} ${value}`)
  return parts.length > 0 ? parts.join(', ') : '-'
}

const exposureOrder: Record<string, number> = { public: 0, private: 1, local: 2, internal: 3 }

function sortServices(services: ServiceManifestEntry[]): ServiceManifestEntry[] {
  return [...services].sort((a, b) => {
    const ao = exposureOrder[a.reachability.exposure] ?? 99
    const bo = exposureOrder[b.reachability.exposure] ?? 99
    return ao - bo
  })
}

export function NetworkSection({ services }: NetworkSectionProps) {
  const [expanded, setExpanded] = useState(false)
  const [manifest, setManifest] = useState<ServiceManifest | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!expanded || manifest !== null) return
    setLoading(true)
    fetch(apiUrl('/api/services/manifest'), fetchOpts)
      .then(r => r.json())
      .then((data: ServiceManifest) => setManifest(data))
      .catch(() => setManifest(null))
      .finally(() => setLoading(false))
  }, [expanded, manifest])

  const sorted = manifest ? sortServices(manifest.services) : []

  return (
    <section className="ops-section">
      <h3 onClick={() => setExpanded(e => !e)} style={{ cursor: 'pointer', userSelect: 'none' }}>
        {expanded ? '▾' : '▸'} Service Exposure
      </h3>
      <div className="network-toggle" onClick={() => setExpanded(e => !e)} style={{ cursor: 'pointer' }}>
        <div className="ops-kv">
          <span>public</span><strong>{services.public}</strong>
          <span>private</span><strong>{services.private}</strong>
          <span>local</span><strong>{services.local}</strong>
          <span>internal</span><strong>{services.internal}</strong>
          <span>types</span><strong>{joinCounts(services.byType)}</strong>
        </div>
      </div>
      {expanded && (
        <div className="network-services">
          {loading && <span style={{ color: 'var(--text-dim)' }}>loading…</span>}
          {!loading && manifest && (
            <div className="ops-table">
              <div className="ops-row ops-row-head">
                <span>App</span><span>Process</span><span>Type</span><span>Exposure</span><span>Endpoint</span><span>Instance</span><span>Endpoints</span>
              </div>
              {sorted.map(svc => {
                const firstUrl = svc.endpoints?.[0]?.url ?? '-'
                const endpoint = firstUrl.length > 40 ? firstUrl.slice(0, 40) + '…' : firstUrl
                return (
                  <div key={svc.name} className="ops-row">
                    <span>{svc.app}</span><span>{svc.process}</span><span>{svc.type}</span><span>{svc.reachability.exposure}</span><span>{endpoint}</span><span>{svc.reachability.instanceScope}</span><span>{svc.endpoints?.length ?? 0}</span>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}
    </section>
  )
}
