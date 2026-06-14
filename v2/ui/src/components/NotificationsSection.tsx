import { useEffect, useState } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import type { NotificationChannel } from '../types/index.ts'

const PROVIDERS = ['discord', 'ntfy', 'pushover', 'webhook'] as const
const ALL_SEVERITIES = ['info', 'warning', 'critical']

export function NotificationsSection() {
  const [channels, setChannels] = useState<NotificationChannel[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [showForm, setShowForm] = useState(false)

  const [provider, setProvider] = useState<string>('discord')
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [userKey, setUserKey] = useState('')
  const [severities, setSeverities] = useState<string[]>([])
  const [adding, setAdding] = useState(false)

  async function load() {
    try {
      const res = await fetch(apiUrl('/api/notifications/channels'), fetchOpts)
      if (!res.ok) throw new Error(await res.text())
      const data = await res.json()
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
      const res = await fetch(apiUrl(`/api/notifications/channels/${encodeURIComponent(id)}/test`), {
        ...fetchOpts,
        method: 'POST',
      })
      if (!res.ok) throw new Error(await res.text())
    } catch (err) {
      setError(String(err))
    } finally {
      setBusy(null)
    }
  }

  async function removeChannel(id: string) {
    setBusy(id + ':remove')
    try {
      const res = await fetch(apiUrl(`/api/notifications/channels/${encodeURIComponent(id)}`), {
        ...fetchOpts,
        method: 'DELETE',
      })
      if (!res.ok) throw new Error(await res.text())
      await load()
    } catch (err) {
      setError(String(err))
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
      const res = await fetch(apiUrl('/api/notifications/channels'), {
        ...fetchOpts,
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error(await res.text())
      setProvider('discord')
      setName('')
      setUrl('')
      setToken('')
      setUserKey('')
      setSeverities([])
      setShowForm(false)
      await load()
    } catch (err) {
      setError(String(err))
    } finally {
      setAdding(false)
    }
  }

  function toggleSeverity(s: string) {
    setSeverities((prev) => prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s])
  }

  return (
    <section className="ops-section">
      <h3>Notification Channels</h3>
      {error && <div className="ops-empty" style={{ color: 'var(--color-warn)' }}>{error}</div>}
      {channels.length > 0 ? (
        <div className="ops-table">
          <div className="ops-row ops-row-head">
            <span>ID</span><span>Provider</span><span>Name</span><span>Severities</span><span>Actions</span>
          </div>
          {channels.map((ch) => (
            <div className="ops-row" key={ch.id}>
              <span title={ch.id}>{ch.id.length > 8 ? ch.id.slice(0, 8) + '...' : ch.id}</span>
              <span>{ch.provider}</span>
              <span>{ch.name}</span>
              <span>{ch.severities && ch.severities.length > 0 ? ch.severities.join(', ') : 'all'}</span>
              <span className="ops-actions">
                <button
                  className="btn btn-small"
                  disabled={busy === ch.id + ':test'}
                  onClick={() => testChannel(ch.id)}
                >
                  {busy === ch.id + ':test' ? 'sending' : 'test'}
                </button>
                <button
                  className="btn btn-small btn-danger"
                  disabled={busy === ch.id + ':remove'}
                  onClick={() => removeChannel(ch.id)}
                >
                  {busy === ch.id + ':remove' ? 'removing' : 'remove'}
                </button>
              </span>
            </div>
          ))}
        </div>
      ) : (
        <div className="ops-empty">No notification channels configured</div>
      )}
      {showForm ? (
        <form onSubmit={addChannel} style={{ marginTop: '0.75rem', display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
            <select value={provider} onChange={(e) => setProvider(e.target.value)}>
              {PROVIDERS.map((p) => <option key={p} value={p}>{p}</option>)}
            </select>
            <input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} required />
            <input placeholder="URL" value={url} onChange={(e) => setUrl(e.target.value)} required style={{ minWidth: '16rem' }} />
            <input placeholder="Token (optional)" value={token} onChange={(e) => setToken(e.target.value)} />
            <input placeholder="User key (optional)" value={userKey} onChange={(e) => setUserKey(e.target.value)} />
          </div>
          <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
            <span style={{ fontSize: '0.8rem' }}>Severities:</span>
            {ALL_SEVERITIES.map((s) => (
              <label key={s} style={{ fontSize: '0.8rem', display: 'flex', gap: '0.25rem', alignItems: 'center', cursor: 'pointer' }}>
                <input type="checkbox" checked={severities.includes(s)} onChange={() => toggleSeverity(s)} />
                {s}
              </label>
            ))}
            <span style={{ fontSize: '0.75rem', color: 'var(--color-muted)' }}>(none = all)</span>
          </div>
          <div style={{ display: 'flex', gap: '0.5rem' }}>
            <button type="submit" className="btn btn-small" disabled={adding}>{adding ? 'adding' : 'add channel'}</button>
            <button type="button" className="btn btn-small" onClick={() => setShowForm(false)}>cancel</button>
          </div>
        </form>
      ) : (
        <button className="btn btn-small" style={{ marginTop: '0.5rem' }} onClick={() => setShowForm(true)}>+ add channel</button>
      )}
    </section>
  )
}
