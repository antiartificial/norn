import { useEffect, useState } from 'react'
import { apiFetch } from '../lib/api.ts'
import type { DeployGroup } from '../types/index.ts'
import { Button, ErrorState, EmptyState, useToast } from './ui/index.ts'

export function DeployGroupsSection() {
  const [groups, setGroups] = useState<DeployGroup[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const { toast } = useToast()

  useEffect(() => {
    let cancelled = false
    async function load() {
      try {
        const data = await apiFetch<{ groups?: DeployGroup[] }>('/api/deploy-groups')
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
      await apiFetch(`/api/deploy-groups/${encodeURIComponent(name)}/deploy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ref: 'HEAD' }),
      })
      toast({ kind: 'success', title: 'Deploy group requested', description: name })
    } catch (err) {
      setError(String(err))
      toast({ kind: 'error', title: 'Deploy group failed', description: String(err) })
    } finally {
      setBusy(null)
    }
  }

  return (
    <section className="ops-section">
      <h3>Deploy Groups</h3>
      {error && <ErrorState title="Deploy group error" message={error} />}
      {groups.length > 0 ? (
        <div className="deploy-group-grid">
          {groups.map((group) => (
            <div key={group.name} className="deploy-group-card">
              <div className="deploy-group-head">
                <strong>{group.name}</strong>
                <Button size="sm" icon="fa-rocket-launch" loading={busy === group.name} onClick={() => deployGroup(group.name)}>Deploy</Button>
              </div>
              <ol className="deploy-group-apps">
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
        <EmptyState title="No deploy groups" hint="Deploy group definitions will appear here." />
      )}
    </section>
  )
}
