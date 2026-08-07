import { useEffect, useState } from 'react'
import { apiFetch } from '../lib/api.ts'
import type { NotificationChannel } from '../types/index.ts'
import { Button, ConfirmDialog, DataTable, ErrorState, useToast, type DataTableColumn } from './ui/index.ts'

const PROVIDERS = ['discord', 'ntfy', 'pushover', 'webhook'] as const
const ALL_SEVERITIES = ['info', 'warning', 'critical']

export function NotificationsSection() {
  const [channels, setChannels] = useState<NotificationChannel[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [showForm, setShowForm] = useState(false)
  const [removeTarget, setRemoveTarget] = useState<NotificationChannel | null>(null)
  const { toast } = useToast()

  const [provider, setProvider] = useState<string>('discord')
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [userKey, setUserKey] = useState('')
  const [severities, setSeverities] = useState<string[]>([])
  const [adding, setAdding] = useState(false)

  async function load() {
    try {
      const data = await apiFetch<{ channels?: NotificationChannel[] }>('/api/notifications/channels')
      setChannels(data.channels ?? [])
      setError(null)
    } catch (err) {
      setError(String(err))
    }
  }

  useEffect(() => {
    load()
  }, [])

  async function testChannel(id: string) {
    setBusy(id + ':test')
    try {
      await apiFetch(`/api/notifications/channels/${encodeURIComponent(id)}/test`, { method: 'POST' })
      toast({ kind: 'success', title: 'Test notification sent' })
    } catch (err) {
      setError(String(err))
      toast({ kind: 'error', title: 'Notification test failed', description: String(err) })
    } finally {
      setBusy(null)
    }
  }

  async function removeChannel(id: string) {
    setBusy(id + ':remove')
    try {
      await apiFetch(`/api/notifications/channels/${encodeURIComponent(id)}`, { method: 'DELETE' })
      toast({ kind: 'success', title: 'Notification channel removed' })
      setRemoveTarget(null)
      await load()
    } catch (err) {
      setError(String(err))
      toast({ kind: 'error', title: 'Remove channel failed', description: String(err) })
    } finally {
      setBusy(null)
    }
  }

  async function addChannel(e: React.FormEvent) {
    e.preventDefault()
    setAdding(true)
    try {
      const body: Record<string, unknown> = { provider, name, url }
      if (token) body.token = token
      if (userKey) body.userKey = userKey
      if (severities.length > 0) body.severities = severities
      await apiFetch('/api/notifications/channels', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      setProvider('discord')
      setName('')
      setUrl('')
      setToken('')
      setUserKey('')
      setSeverities([])
      setShowForm(false)
      toast({ kind: 'success', title: 'Notification channel added', description: name })
      await load()
    } catch (err) {
      setError(String(err))
      toast({ kind: 'error', title: 'Add channel failed', description: String(err) })
    } finally {
      setAdding(false)
    }
  }

  function toggleSeverity(s: string) {
    setSeverities((prev) => prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s])
  }

  const columns: DataTableColumn<NotificationChannel>[] = [
    { key: 'id', header: 'ID', cell: ch => <span title={ch.id}>{ch.id.length > 8 ? `${ch.id.slice(0, 8)}...` : ch.id}</span> },
    { key: 'provider', header: 'Provider', cell: ch => ch.provider },
    { key: 'name', header: 'Name', cell: ch => ch.name },
    { key: 'severities', header: 'Severities', cell: ch => ch.severities && ch.severities.length > 0 ? ch.severities.join(', ') : 'all' },
    {
      key: 'actions',
      header: 'Actions',
      cell: ch => (
        <span className="ops-actions">
          <Button size="sm" icon="fa-paper-plane" loading={busy === `${ch.id}:test`} onClick={() => testChannel(ch.id)}>Test</Button>
          <Button size="sm" variant="danger" icon="fa-trash" loading={busy === `${ch.id}:remove`} onClick={() => setRemoveTarget(ch)}>Remove</Button>
        </span>
      ),
    },
  ]

  return (
    <section className="ops-section">
      <h3>Notification Channels</h3>
      {error && <ErrorState title="Notification error" message={error} onRetry={load} />}
      <DataTable columns={columns} rows={channels} getRowKey={ch => ch.id} emptyTitle="No notification channels" emptyHint="Configure a channel to receive Beacon events." />
      {showForm ? (
        <form onSubmit={addChannel} className="platform-form-stack">
          <div className="platform-inline-form">
            <select value={provider} onChange={(e) => setProvider(e.target.value)}>
              {PROVIDERS.map((p) => <option key={p} value={p}>{p}</option>)}
            </select>
            <input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} required />
            <input className="platform-input-lg" placeholder="URL" value={url} onChange={(e) => setUrl(e.target.value)} required />
            <input placeholder="Token (optional)" value={token} onChange={(e) => setToken(e.target.value)} />
            <input placeholder="User key (optional)" value={userKey} onChange={(e) => setUserKey(e.target.value)} />
          </div>
          <div className="platform-checkbox-row">
            <span>Severities:</span>
            {ALL_SEVERITIES.map((s) => (
              <label key={s}>
                <input type="checkbox" checked={severities.includes(s)} onChange={() => toggleSeverity(s)} />
                {s}
              </label>
            ))}
            <span>(none = all)</span>
          </div>
          <div className="platform-action-row">
            <Button type="submit" size="sm" icon="fa-plus" loading={adding}>Add channel</Button>
            <Button type="button" size="sm" variant="ghost" icon="fa-xmark" onClick={() => setShowForm(false)}>Cancel</Button>
          </div>
        </form>
      ) : (
        <Button size="sm" variant="secondary" icon="fa-plus" className="platform-spaced-button" onClick={() => setShowForm(true)}>Add channel</Button>
      )}
      <ConfirmDialog
        open={!!removeTarget}
        title="Remove notification channel"
        message={`Remove ${removeTarget?.name ?? 'this channel'}?`}
        consequence="Beacon events will no longer be sent to this destination."
        confirmLabel="Remove"
        danger
        onClose={() => setRemoveTarget(null)}
        onConfirm={() => removeTarget && removeChannel(removeTarget.id)}
      />
    </section>
  )
}
