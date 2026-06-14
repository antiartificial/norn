import { useEffect, useState } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import type { DeployGroup } from '../types/index.ts'

export function DeployGroupsSection() {
  const [groups, setGroups] = useState<DeployGroup[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      try {
        const res = await fetch(apiUrl('/api/deploy-groups'), fetchOpts)
        if (!res.ok) throw new Error(await res.text())
        const data = await res.json()
        if (!cancelled) {
          setGroups(data.groups ?? [])
          setError(null)
        }
      } catch (err) {
        if (!cancelled) setError(String(err))
      }
    }
    load()
    return () => { cancelled = true }
  }, [])

  async function deployGroup(name: string) {
    setBusy(name)
    try {
      const res = await fetch(apiUrl(`/api/deploy-groups/${encodeURIComponent(name)}/deploy`), {
        ...fetchOpts,
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ref: 'HEAD' }),
      })
      if (!res.ok) throw new Error(await res.text())
    } catch (err) {
      setError(String(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <section className="ops-section">
      <h3>Deploy Groups</h3>
      {error && <div className="ops-empty" style={{ color: 'var(--color-warn)' }}>{error}</div>}
      {groups.length > 0 ? (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.75rem' }}>
          {groups.map((group) => (
            <div key={group.name} className="ops-section" style={{ minWidth: '14rem', flex: '0 0 auto', padding: '0.6rem 0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.4rem' }}>
                <strong>{group.name}</strong>
                <button
                  className="btn btn-small"
                  disabled={busy === group.name}
                  onClick={() => deployGroup(group.name)}
                >
                  {busy === group.name ? 'deploying' : 'deploy'}
                </button>
              </div>
              <ol style={{ margin: 0, paddingLeft: '1.25rem', fontSize: '0.82rem' }}>
                {group.apps.map((entry) => (
                  <li key={entry.app}>
                    {entry.app}{entry.waitReady ? ' (wait)' : ''}
                  </li>
                ))}
              </ol>
            </div>
          ))}
        </div>
      ) : (
        <div className="ops-empty">No deploy groups configured</div>
      )}
    </section>
  )
}
