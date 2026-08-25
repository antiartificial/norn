import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { apiFetch } from '../lib/api.ts'
import { NotificationsSection } from './NotificationsSection.tsx'
import { DeployGroupsSection } from './DeployGroupsSection.tsx'
import { NetworkSection } from './NetworkSection.tsx'
import { OpsPanel } from './OpsPanel.tsx'
import { PlatformTrafficView } from './PlatformTrafficView.tsx'
import type { AccessEvent, AccessGrant } from '../types/index.ts'
import { Button, ConfirmDialog, DataTable, EmptyState, ErrorState, Skeleton, StatusChip, Tabs, TabsList, Tab, useToast, type DataTableColumn } from './ui/index.ts'

type PlatformTab = 'releases' | 'network' | 'access' | 'traffic' | 'notifications' | 'observability' | 'contextdb'

interface PlatformSummary {
  generatedAt: string
  networkMode?: string
  services: {
    total: number
    public: number
    private: number
    local: number
    internal: number
    byType: Record<string, number>
    byStatus: Record<string, number>
  }
  deployments: {
    recent: Array<{ app: string; commitSha: string; imageTag: string; status: string; sourceKind?: string; sourceDirty?: boolean; sourceChanges?: string[]; startedAt: string }>
    dirty: Array<{ app: string; commitSha: string; imageTag: string; sourceChanges?: string[] }>
    failed: number
    successful: number
  }
  access: {
    totalRecent: number
    byStatus: Record<string, number>
    byClientIp: Record<string, number>
    recent: Array<{ timestamp: string; method: string; path: string; status: number; clientIp?: string; cfAccessEmail?: string; durationMs: number }>
  }
  observability: {
    enabled: boolean
    logsEnabled: boolean
    logFormat: string
    serviceName?: string
    otlpEndpoint?: string
    bundleAvailable?: boolean
    retention?: string
  }
  warnings?: string[]
}

interface PlatformRelease {
  sha: string
  version: string
  createdAt: string
  path: string
  current: boolean
}

interface PlatformReleaseList {
  current?: string
  releases: PlatformRelease[]
}

const tabs: Array<{ value: PlatformTab; label: string }> = [
  { value: 'releases', label: 'Releases' },
  { value: 'network', label: 'Network' },
  { value: 'access', label: 'Access' },
  { value: 'traffic', label: 'Traffic' },
  { value: 'notifications', label: 'Notifications' },
  { value: 'observability', label: 'Observability' },
  { value: 'contextdb', label: 'ContextDB' },
]

function tabFromPath(pathname: string): PlatformTab | null {
  const value = pathname.split('/').filter(Boolean).at(-1)
  return tabs.some(tab => tab.value === value) ? value as PlatformTab : null
}

export function PlatformPanel() {
  const navigate = useNavigate()
  const location = useLocation()
  const activeTab = tabFromPath(location.pathname)
  const summary = useQuery({ queryKey: ['platform', 'summary'], queryFn: () => apiFetch<PlatformSummary>('/api/ops/platform'), staleTime: 15_000, refetchInterval: 15_000 })

  if (!activeTab) return <Navigate to="/platform/releases" replace />

  return (
    <div className="ops-panel platform-panel">
      <div className="ops-header">
        <div>
          <h2>Norn Platform</h2>
          <p>{summary.data?.networkMode || 'network unknown'} · generated {formatTime(summary.data?.generatedAt)}</p>
        </div>
        {summary.data && (
          <StatusChip tone={summary.data.observability.enabled ? 'success' : 'warning'} label={summary.data.observability.enabled ? 'otel enabled' : 'otel disabled'} />
        )}
      </div>

      <Tabs value={activeTab} onValueChange={(value) => navigate(`/platform/${value}`)}>
        <TabsList aria-label="Platform sections">
          {tabs.map(tab => <Tab key={tab.value} value={tab.value}>{tab.label}</Tab>)}
        </TabsList>
      </Tabs>

      <div className="platform-tab-body">
        {activeTab === 'releases' && <ReleasesTab />}
        {activeTab === 'network' && <NetworkTab summary={summary.data} loading={summary.isLoading} error={errorMessage(summary.error)} onRetry={() => summary.refetch()} />}
        {activeTab === 'access' && <AccessTab summary={summary.data} loading={summary.isLoading} error={errorMessage(summary.error)} onRetry={() => summary.refetch()} />}
        {activeTab === 'traffic' && <PlatformTrafficView />}
        {activeTab === 'notifications' && <NotificationsSection />}
        {activeTab === 'observability' && <ObservabilityTab summary={summary.data} loading={summary.isLoading} error={errorMessage(summary.error)} onRetry={() => summary.refetch()} />}
        {activeTab === 'contextdb' && <OpsPanel />}
      </div>
    </div>
  )
}

function ReleasesTab() {
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const releases = useQuery({ queryKey: ['platform', 'releases'], queryFn: () => apiFetch<PlatformReleaseList>('/api/platform/releases'), staleTime: 15_000 })
  const [rollbackTarget, setRollbackTarget] = useState<PlatformRelease | null>(null)
  const rollback = useMutation({
    mutationFn: (sha: string) => apiFetch(`/api/platform/releases/${encodeURIComponent(sha)}/rollback`, { method: 'POST' }),
    onSuccess: () => {
      toast({ kind: 'success', title: 'Platform rollback started', description: rollbackTarget?.version ?? rollbackTarget?.sha })
      queryClient.invalidateQueries({ queryKey: ['platform', 'releases'] })
      setRollbackTarget(null)
    },
    onError: (error) => toast({ kind: 'error', title: 'Rollback failed', description: errorMessage(error) }),
  })
  const rows = releases.data?.releases ?? []
  const columns: DataTableColumn<PlatformRelease>[] = [
    { key: 'created', header: 'Created', cell: row => formatTime(row.createdAt) },
    { key: 'version', header: 'Version', cell: row => row.version },
    { key: 'sha', header: 'SHA', cell: row => <code>{short(row.sha)}</code> },
    { key: 'status', header: 'Status', cell: row => row.current ? <StatusChip tone="success" label="current" /> : '-' },
    { key: 'path', header: 'Path', cell: row => <code>{row.path}</code> },
    { key: 'action', header: 'Action', cell: row => row.current ? '-' : <Button size="sm" variant="danger" icon="fa-arrow-rotate-left" loading={rollback.isPending && rollback.variables === row.sha} onClick={() => setRollbackTarget(row)}>Rollback</Button> },
  ]

  return (
    <>
      <section className="ops-section">
        <h3>Platform Releases</h3>
        <DataTable columns={columns} rows={rows} getRowKey={row => row.sha} loading={releases.isLoading} error={errorMessage(releases.error)} emptyTitle="No platform releases" emptyHint="Release metadata has not been recorded yet." onRetry={() => releases.refetch()} />
      </section>
      <DeployGroupsSection />
      <ConfirmDialog
        open={!!rollbackTarget}
        title="Rollback platform release"
        message={`Rollback to ${rollbackTarget?.version ?? short(rollbackTarget?.sha)}?`}
        consequence="This is a platform-level change and may affect all Norn services."
        confirmLabel="Rollback"
        confirmIcon="fa-arrow-rotate-left"
        danger
        onClose={() => setRollbackTarget(null)}
        onConfirm={() => rollbackTarget && rollback.mutate(rollbackTarget.sha)}
      />
    </>
  )
}

function NetworkTab({ summary, loading, error, onRetry }: { summary?: PlatformSummary; loading: boolean; error: string | null; onRetry: () => void }) {
  if (loading) return <Skeleton height={160} label="Loading platform network" />
  if (error) return <ErrorState message={error} onRetry={onRetry} />
  if (!summary) return <EmptyState title="No network summary" hint="Platform network data is unavailable." />
  return (
    <>
      <div className="ops-metrics">
        <Metric label="Services" value={String(summary.services.total)} />
        <Metric label="Public" value={String(summary.services.public)} tone={summary.services.public > 0 ? 'warn' : 'ok'} />
        <Metric label="Private" value={String(summary.services.private)} />
        <Metric label="Local" value={String(summary.services.local)} />
        <Metric label="Internal" value={String(summary.services.internal)} />
      </div>
      <NetworkSection services={summary.services} />
    </>
  )
}

function AccessTab({ summary, loading, error, onRetry }: { summary?: PlatformSummary; loading: boolean; error: string | null; onRetry: () => void }) {
  const accessEvents = summary?.access.recent ?? []
  const eventColumns: DataTableColumn<AccessEvent>[] = [
    { key: 'time', header: 'Time', cell: row => formatTime(row.timestamp) },
    { key: 'status', header: 'Status', cell: row => <StatusChip tone={row.status >= 500 ? 'danger' : row.status >= 400 ? 'warning' : 'success'} label={String(row.status)} /> },
    { key: 'method', header: 'Method', cell: row => row.method },
    { key: 'path', header: 'Path', cell: row => <code>{row.path}</code> },
    { key: 'client', header: 'Client', cell: row => row.clientIp || '-' },
    { key: 'user', header: 'User', cell: row => row.cfAccessEmail || '-' },
    { key: 'ms', header: 'MS', cell: row => row.durationMs, numeric: true },
  ]
  if (loading) return <Skeleton height={160} label="Loading access controls" />
  if (error) return <ErrorState message={error} onRetry={onRetry} />
  if (!summary) return <EmptyState title="No access summary" hint="Access data is unavailable." />
  return (
    <>
      <AccessControls />
      <section className="ops-section">
        <h3>Access Events</h3>
        <DataTable columns={eventColumns} rows={accessEvents.slice(0, 12)} getRowKey={(row, i?: number) => `${row.timestamp}:${row.path}:${i ?? row.durationMs}`} emptyTitle="No access events" emptyHint="No dashboard access requests have been recorded." />
      </section>
      <section className="ops-section">
        <h3>Access Patterns</h3>
        {Object.keys(summary.access.byClientIp ?? {}).length > 0 ? (
          <div className="platform-kv-list">
            {Object.entries(summary.access.byClientIp).map(([client, count]) => <div key={client}><span>{client}</span><strong>{count}</strong></div>)}
          </div>
        ) : <EmptyState title="No access patterns" hint="Pattern summaries will appear after access observations are recorded." />}
      </section>
    </>
  )
}

function AccessControls() {
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const grants = useQuery({ queryKey: ['access', 'grants'], queryFn: () => apiFetch<{ grants?: AccessGrant[] }>('/api/access/grants'), staleTime: 20_000 })
  const [showGrantForm, setShowGrantForm] = useState(false)
  const [grantIp, setGrantIp] = useState('')
  const [grantTtl, setGrantTtl] = useState('24h')
  const [grantNote, setGrantNote] = useState('')
  const [tokenTTL, setTokenTTL] = useState('2h')
  const [tokenNote, setTokenNote] = useState('')
  const [createdToken, setCreatedToken] = useState<string | null>(null)
  const [tokenExpiry, setTokenExpiry] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<AccessGrant | null>(null)
  const grantRows = grants.data?.grants ?? []

  const createGrant = useMutation({
    mutationFn: () => apiFetch('/api/access/grants', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ip: grantIp, ttl: grantTtl, note: grantNote }),
    }),
    onSuccess: () => {
      toast({ kind: 'success', title: 'Access grant created', description: grantIp })
      setGrantIp('')
      setGrantTtl('24h')
      setGrantNote('')
      setShowGrantForm(false)
      queryClient.invalidateQueries({ queryKey: ['access', 'grants'] })
    },
    onError: (error) => toast({ kind: 'error', title: 'Grant failed', description: errorMessage(error) }),
  })
  const revokeGrant = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/access/grants/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    onSuccess: () => {
      toast({ kind: 'success', title: 'Access grant revoked', description: revokeTarget?.ip })
      setRevokeTarget(null)
      queryClient.invalidateQueries({ queryKey: ['access', 'grants'] })
    },
    onError: (error) => toast({ kind: 'error', title: 'Revoke failed', description: errorMessage(error) }),
  })
  const createToken = useMutation({
    mutationFn: () => apiFetch<{ token: string; expiresAt?: string }>('/api/access/tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ttl: tokenTTL, note: tokenNote }),
    }),
    onSuccess: (data) => {
      setCreatedToken(data.token)
      setTokenExpiry(data.expiresAt ?? null)
      setTokenNote('')
      toast({ kind: 'success', title: 'Access token created', description: data.expiresAt ? `Expires ${formatTime(data.expiresAt)}` : undefined })
    },
    onError: (error) => toast({ kind: 'error', title: 'Token failed', description: errorMessage(error) }),
  })

  const columns: DataTableColumn<AccessGrant>[] = [
    { key: 'ip', header: 'IP', cell: row => row.ip },
    { key: 'note', header: 'Note', cell: row => row.note || '-' },
    { key: 'created', header: 'Created', cell: row => formatTime(row.createdAt) },
    { key: 'by', header: 'By', cell: row => row.createdBy || '-' },
    { key: 'expires', header: 'Expires', cell: row => formatTime(row.expiresAt) },
    { key: 'action', header: 'Action', cell: row => <Button size="sm" variant="danger" icon="fa-trash" onClick={() => setRevokeTarget(row)}>Revoke</Button> },
  ]

  return (
    <section className="ops-section">
      <h3>Grants & Tokens</h3>
      <DataTable columns={columns} rows={grantRows} getRowKey={row => row.id} loading={grants.isLoading} error={errorMessage(grants.error)} emptyTitle="No active grants" emptyHint="Temporary IP grants will appear here." onRetry={() => grants.refetch()} />
      {showGrantForm ? (
        <form className="platform-inline-form" onSubmit={(event: FormEvent) => { event.preventDefault(); createGrant.mutate() }}>
          <input className="platform-input-sm" placeholder="IP address" value={grantIp} onChange={event => setGrantIp(event.target.value)} required />
          <input className="platform-input-xs" placeholder="TTL" value={grantTtl} onChange={event => setGrantTtl(event.target.value)} required />
          <input className="platform-input-md" placeholder="Note (optional)" value={grantNote} onChange={event => setGrantNote(event.target.value)} />
          <Button type="submit" size="sm" icon="fa-user-plus" loading={createGrant.isPending}>Grant</Button>
          <Button type="button" size="sm" variant="ghost" icon="fa-xmark" onClick={() => setShowGrantForm(false)}>Cancel</Button>
        </form>
      ) : (
        <Button size="sm" variant="secondary" icon="fa-user-plus" className="platform-spaced-button" onClick={() => setShowGrantForm(true)}>Grant IP access</Button>
      )}

      <h4 className="platform-subhead">Access Tokens</h4>
      <div className="platform-inline-form">
        <input className="platform-input-xs" placeholder="TTL" value={tokenTTL} onChange={event => setTokenTTL(event.target.value)} />
        <input className="platform-input-md" placeholder="Note (optional)" value={tokenNote} onChange={event => setTokenNote(event.target.value)} />
        <Button size="sm" icon="fa-key" loading={createToken.isPending} onClick={() => createToken.mutate()}>Create token</Button>
      </div>
      {createdToken && (
        <div className="platform-token-result">
          <textarea readOnly value={createdToken} rows={3} onClick={event => event.currentTarget.select()} />
          {tokenExpiry && <p>Expires: {formatTime(tokenExpiry)}</p>}
          <p>Append ?token=&lt;value&gt; to share dashboard URLs.</p>
          <Button size="sm" variant="ghost" icon="fa-xmark" onClick={() => { setCreatedToken(null); setTokenExpiry(null) }}>Clear</Button>
        </div>
      )}
      <ConfirmDialog
        open={!!revokeTarget}
        title="Revoke access grant"
        message={`Revoke access for ${revokeTarget?.ip}?`}
        consequence="Existing sessions using this temporary grant may lose access."
        confirmLabel="Revoke"
        danger
        onClose={() => setRevokeTarget(null)}
        onConfirm={() => revokeTarget && revokeGrant.mutate(revokeTarget.id)}
      />
    </section>
  )
}

function ObservabilityTab({ summary, loading, error, onRetry }: { summary?: PlatformSummary; loading: boolean; error: string | null; onRetry: () => void }) {
  const { toast } = useToast()
  const install = useMutation({
    mutationFn: () => apiFetch('/api/observability/services/install', { method: 'POST' }),
    onSuccess: () => toast({ kind: 'success', title: 'Observability install requested' }),
    onError: (err) => toast({ kind: 'error', title: 'Install failed', description: errorMessage(err) }),
  })
  if (loading) return <Skeleton height={160} label="Loading observability" />
  if (error) return <ErrorState message={error} onRetry={onRetry} />
  if (!summary) return <EmptyState title="No observability summary" hint="Metrics configuration is unavailable." />
  return (
    <>
      <div className="ops-metrics">
        <Metric label="Enabled" value={String(summary.observability.enabled)} tone={summary.observability.enabled ? 'ok' : 'warn'} />
        <Metric label="Logs" value={String(summary.observability.logsEnabled)} tone={summary.observability.logsEnabled ? 'ok' : 'warn'} />
        <Metric label="Format" value={summary.observability.logFormat || '-'} />
        <Metric label="Retention" value={summary.observability.retention || '-'} />
      </div>
      <section className="ops-section">
        <h3>Metrics Configuration</h3>
        <div className="ops-kv">
          <span>service</span><strong>{summary.observability.serviceName || '-'}</strong>
          <span>otlp</span><strong>{summary.observability.otlpEndpoint || '-'}</strong>
          <span>bundle</span><strong>{summary.observability.bundleAvailable ? 'available' : '-'}</strong>
        </div>
        <div className="platform-action-row">
          <a className="btn btn-small" href="/api/observability/prometheus.yml">Prometheus config</a>
          <a className="btn btn-small" href="/api/observability/alerts.yml">Alert rules</a>
          <Button size="sm" icon="fa-gear" loading={install.isPending} onClick={() => install.mutate()}>Install services</Button>
        </div>
      </section>
      {(summary.warnings && summary.warnings.length > 0) && (
        <section className="ops-section ops-warnings">
          <h3>Warnings</h3>
          {summary.warnings.map(warning => <p key={warning}>{warning}</p>)}
        </section>
      )}
    </>
  )
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: 'ok' | 'warn' | 'bad' }) {
  return (
    <div className={`ops-metric ${tone || ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : error ? String(error) : ''
}

function formatTime(value?: string) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

function short(value?: string) {
  if (!value) return '-'
  return value.length > 10 ? value.slice(0, 10) : value
}
