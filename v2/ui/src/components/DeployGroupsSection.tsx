import { useEffect, useState } from 'react'
import { ApiError, apiAuthority, apiFetch } from '../lib/api.ts'
import { clearDurableIntent, durableIntent, type DurableIntent } from '../lib/durableIntent.ts'
import type { DeployGroup } from '../types/index.ts'
import { Button, ErrorState, EmptyState, useToast } from './ui/index.ts'

interface GroupDeployResult {
  app: string
  sagaId?: string
  operationId?: string
  replayed?: boolean
  error?: string
}

interface GroupAcceptance {
  group?: string
  operationId?: string
  replayed?: boolean
  status?: string
  deploys?: GroupDeployResult[]
}

interface GroupActionState extends GroupAcceptance {
  retryMode: 'same-intent' | 'new-intent'
  message: string
  failed: boolean
  intent?: DurableIntent
  allowNewIntent?: boolean
}

function validateGroupAcceptance(value: GroupAcceptance): GroupAcceptance {
  if (!value.operationId || !Array.isArray(value.deploys) || value.deploys.some((item) => !item.app || (!item.error && (!item.operationId || !item.sagaId)))) {
    throw new Error('Server returned an invalid durable deploy-group acceptance')
  }
  return value
}

function isDefinitiveRejection(error: unknown): boolean {
  return error instanceof ApiError && [400, 401, 403, 404, 409, 422].includes(error.status)
}

export function DeployGroupsSection() {
  const [groups, setGroups] = useState<DeployGroup[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [actions, setActions] = useState<Record<string, GroupActionState>>({})
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
    const body = { ref: 'HEAD' }
    const intent = durableIntent(`${apiAuthority()}:deploy-group:${name}`, body)
    try {
      const accepted = validateGroupAcceptance(await apiFetch<GroupAcceptance>(`/api/deploy-groups/${encodeURIComponent(name)}/deploy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': intent.key },
        body: JSON.stringify(body),
      }))
      const deploys = accepted.deploys ?? []
      const failures = deploys.filter((item) => item.error)
      const terminalFailure = accepted.status === 'failed' || accepted.status === 'canceled'
      const failed = terminalFailure || failures.length > 0
      if (!failed) clearDurableIntent(intent)
      const message = failed
        ? `${deploys.length - failures.length} member${deploys.length - failures.length === 1 ? '' : 's'} accepted; ${failures.length || 1} failed. Retry with the same request to recover missing members.`
        : accepted.replayed ? 'Resolved the existing accepted deploy group.' : `${deploys.length} member${deploys.length === 1 ? '' : 's'} accepted.`
      setActions((current) => ({ ...current, [name]: { ...accepted, deploys, retryMode: failed ? 'same-intent' : 'new-intent', message, failed, intent: failed ? intent : undefined, allowNewIntent: failed } }))
      setError(null)
      toast({ kind: failed ? 'error' : 'info', title: failed ? 'Deploy group partially failed' : 'Deploy group accepted', description: message })
    } catch (err) {
      const rejected = isDefinitiveRejection(err)
      const persistenceNote = intent.persistence === 'memory' ? ' Keep this tab open because browser storage is unavailable.' : ''
      const message = rejected
        ? `Request rejected: ${err instanceof Error ? err.message : String(err)}. The same request key is retained because an earlier acceptance outcome may be unknown. Retry after resolving access or policy.${persistenceNote}`
        : `Acceptance outcome is unknown. Retry reuses the same request.${persistenceNote}`
      setActions((current) => ({ ...current, [name]: { retryMode: 'same-intent', message, failed: true, intent, allowNewIntent: true } }))
      toast({ kind: 'error', title: rejected ? 'Deploy group rejected' : 'Deploy group outcome unknown', description: message })
    } finally {
      setBusy(null)
    }
  }

  function startNewGroupIntent(name: string) {
    const current = actions[name]
    if (current?.intent) clearDurableIntent(current.intent)
    setActions((all) => {
      const next = { ...all }
      delete next[name]
      return next
    })
    void deployGroup(name)
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
                <Button size="sm" icon="fa-rocket-launch" loading={busy === group.name} onClick={() => deployGroup(group.name)}>
                  {actions[group.name]?.retryMode === 'same-intent' ? 'Retry same request' : actions[group.name] ? 'Deploy again' : 'Deploy'}
                </Button>
              </div>
              <ol className="deploy-group-apps">
                {group.apps.map((entry) => (
                  <li key={entry.app}>
                    {entry.app}{entry.waitReady ? ' (wait)' : ''}
                  </li>
                ))}
              </ol>
              {actions[group.name] && (
                <div className="deploy-group-result" role={actions[group.name].failed ? 'alert' : 'status'} aria-live="polite">
                  <strong>{actions[group.name].message}</strong>
                  {actions[group.name].operationId && <div>Operation {actions[group.name].operationId} · status {actions[group.name].status ?? 'accepted'}{actions[group.name].replayed ? ' · replayed' : ''}</div>}
                  {(actions[group.name].deploys ?? []).length > 0 && (
                    <ul>
                      {actions[group.name].deploys?.map((item) => (
                        <li key={item.app}>
                          {item.app}: {item.error ? `failed — ${item.error}` : item.replayed ? 'existing operation resolved' : 'accepted'}
                          {item.operationId ? ` (${item.operationId})` : ''}
                        </li>
                      ))}
                    </ul>
                  )}
                  {actions[group.name].allowNewIntent && (
                    <Button size="sm" variant="secondary" onClick={() => startNewGroupIntent(group.name)}>Start new whole-group deploy</Button>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      ) : (
        <EmptyState title="No deploy groups" hint="Deploy group definitions will appear here." />
      )}
    </section>
  )
}
