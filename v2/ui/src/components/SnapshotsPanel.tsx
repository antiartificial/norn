import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { NavLink } from 'react-router-dom'
import { apiFetch } from '../lib/api.ts'
import { clearDurableIntent, durableIntent, type DurableIntent } from '../lib/durableIntent.ts'
import type { AppSnapshot, Operation, RemoteSnapshot } from '../types/index.ts'
import { Button, ConfirmDialog, EmptyState, ErrorState, Skeleton, StatusChip, useToast } from './ui/index.ts'

interface Props { appId: string; defaultKeep?: number; onClose: () => void }
type QueuedAction = { operation: Operation; label: string }

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']; let value = bytes / 1024; let index = 0
  while (value >= 1024 && index < units.length - 1) { value /= 1024; index++ }
  return `${value.toFixed(1)} ${units[index]}`
}
function snapshotDate(snapshot: AppSnapshot): string {
  if (snapshot.createdAt) return new Date(snapshot.createdAt).toLocaleString()
  const value = snapshot.timestamp
  if (!/^\d{8}T\d{6}$/.test(value)) return value
  return `${value.slice(0, 4)}-${value.slice(4, 6)}-${value.slice(6, 8)} ${value.slice(9, 11)}:${value.slice(11, 13)}:${value.slice(13, 15)} UTC`
}
function durableHeaders(key: string): HeadersInit { return { 'Content-Type': 'application/json', 'Idempotency-Key': key } }

export function SnapshotsPanel({ appId, defaultKeep = 3, onClose }: Props) {
  const queryClient = useQueryClient(); const { toast } = useToast()
  const [tab, setTab] = useState<'local' | 'remote'>('local')
  const [keep, setKeep] = useState(Math.max(1, defaultKeep))
  const [restoreTarget, setRestoreTarget] = useState<AppSnapshot | null>(null)
  const [confirmPrune, setConfirmPrune] = useState(false)
  const [queued, setQueued] = useState<QueuedAction | null>(null)
  const snapshots = useQuery({ queryKey: ['snapshots', appId], queryFn: () => apiFetch<AppSnapshot[]>(`/api/v1/apps/${encodeURIComponent(appId)}/snapshots`), staleTime: 10_000, refetchInterval: 30_000 })
  const remote = useQuery({ queryKey: ['snapshots', appId, 'remote'], queryFn: () => apiFetch<{ snapshots: RemoteSnapshot[] }>(`/api/apps/${encodeURIComponent(appId)}/snapshots/remote`), enabled: tab === 'remote', retry: false })
  const ordered = useMemo(() => [...(snapshots.data ?? [])].sort((a, b) => b.timestamp.localeCompare(a.timestamp)), [snapshots.data])
  const pruneCandidates = ordered.slice(keep)
  const recordQueued = async (operation: Operation, label: string) => {
    setQueued({ operation, label }); await queryClient.invalidateQueries({ queryKey: ['operations'] })
    toast({ kind: 'success', title: `${label} queued`, description: operation.id ?? 'Durable receipt created' })
  }
  const createSnapshot = useMutation({ mutationFn: ({ intent }: { intent: DurableIntent }) => apiFetch<Operation>(`/api/v1/apps/${encodeURIComponent(appId)}/snapshots`, { method: 'POST', headers: durableHeaders(intent.key), body: '{}' }), retry: 2, onSuccess: (operation, variables) => { clearDurableIntent(variables.intent); void recordQueued(operation, 'Snapshot') } })
  const prune = useMutation({ mutationFn: ({ intent, count }: { intent: DurableIntent; count: number }) => apiFetch<Operation>(`/api/v1/apps/${encodeURIComponent(appId)}/snapshots/retention`, { method: 'POST', headers: durableHeaders(intent.key), body: JSON.stringify({ keep: count, confirm: true }) }), retry: 2, onSuccess: (operation, variables) => { clearDurableIntent(variables.intent); setConfirmPrune(false); void recordQueued(operation, 'Snapshot pruning') } })
  const restore = useMutation({ mutationFn: ({ intent, snapshot }: { intent: DurableIntent; snapshot: string }) => apiFetch<Operation>(`/api/v1/apps/${encodeURIComponent(appId)}/snapshots/${encodeURIComponent(snapshot)}/restore`, { method: 'POST', headers: durableHeaders(intent.key), body: JSON.stringify({ confirm: true }) }), retry: 2, onSuccess: (operation, variables) => { clearDurableIntent(variables.intent); setRestoreTarget(null); void recordQueued(operation, 'Restore') } })
  const exportLatest = useMutation({ mutationFn: () => apiFetch(`/api/apps/${encodeURIComponent(appId)}/snapshots/export`, { method: 'POST' }), onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['snapshots', appId, 'remote'] }); toast({ kind: 'success', title: 'Latest snapshot exported' }) } })
  const importRemote = useMutation({ mutationFn: (key: string) => apiFetch(`/api/apps/${encodeURIComponent(appId)}/snapshots/import`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ key }) }), onSuccess: async () => { await snapshots.refetch(); toast({ kind: 'success', title: 'Remote snapshot imported' }) } })
  const mutationError = createSnapshot.error ?? prune.error ?? restore.error ?? exportLatest.error ?? importRemote.error

  return <section className="panel snapshot-workspace" aria-labelledby="snapshot-title">
    <div className="panel-header"><div><h3 id="snapshot-title"><i className="fawsb fa-database" aria-hidden /> Data recovery</h3><p>Every local change is queued, serialized per app, and survives client or API restarts.</p></div><Button variant="ghost" icon="fa-xmark" aria-label="Close data recovery" onClick={onClose} /></div>
    <div className="panel-tabs" role="tablist" aria-label="Snapshot location"><button className={`panel-tab-btn${tab === 'local' ? ' active' : ''}`} role="tab" aria-selected={tab === 'local'} onClick={() => setTab('local')}>Local</button><button className={`panel-tab-btn${tab === 'remote' ? ' active' : ''}`} role="tab" aria-selected={tab === 'remote'} onClick={() => setTab('remote')}>Remote</button></div>
    {queued && <div className="durable-receipt-banner" role="status"><StatusChip tone="info" label="queued" /><span>{queued.label} has a durable receipt. It is safe to leave this page.</span>{queued.operation.id && <NavLink to={`/operations/${queued.operation.id}`}>View operation</NavLink>}</div>}
    {mutationError instanceof Error && <ErrorState message={mutationError.message} />}
    {tab === 'local' ? <>
      <div className="snapshot-toolbar"><Button variant="primary" icon="fa-camera" loading={createSnapshot.isPending} onClick={() => createSnapshot.mutate({ intent: durableIntent(`${appId}:snapshot`, {}) })}>Create snapshot</Button><label className="snapshot-keep-control">Keep newest <input type="number" min={1} max={1000} value={keep} onChange={(event) => setKeep(Math.max(1, Number(event.target.value) || 1))} /></label><Button variant="danger" icon="fa-trash" disabled={pruneCandidates.length === 0} onClick={() => setConfirmPrune(true)}>Review prune ({pruneCandidates.length})</Button><Button variant="secondary" icon="fa-cloud-arrow-up" loading={exportLatest.isPending} disabled={ordered.length === 0} onClick={() => exportLatest.mutate()}>Export latest</Button></div>
      {snapshots.isLoading ? <div className="panel-skeleton"><Skeleton /><Skeleton /></div> : snapshots.error ? <ErrorState message={snapshots.error instanceof Error ? snapshots.error.message : 'Could not load snapshots'} onRetry={() => snapshots.refetch()} /> : ordered.length === 0 ? <EmptyState icon="◇" title="No snapshots" hint="Create a baseline before migrations or risky application changes." /> : <div className="panel-list snapshot-list"><div className="panel-list-header"><span>Created</span><span>Database</span><span>Size</span><span>Retention</span><span>Action</span></div>{ordered.map((snapshot, index) => { const willPrune = index >= keep; return <div key={snapshot.filename} className={`panel-list-row${willPrune ? ' snapshot-prune-candidate' : ''}`}><span>{snapshotDate(snapshot)}</span><span>{snapshot.database}</span><span>{formatBytes(snapshot.size)}</span><span><StatusChip tone={willPrune ? 'warning' : 'success'} label={willPrune ? 'will prune' : 'retained'} /></span><span><Button size="sm" variant="danger" icon="fa-arrow-rotate-left" onClick={() => setRestoreTarget(snapshot)}>Restore…</Button></span></div> })}</div>}
    </> : remote.isLoading ? <div className="panel-skeleton"><Skeleton /><Skeleton /></div> : remote.error ? <EmptyState icon="◇" title="Remote snapshots unavailable" hint="Configure the InfraSpec export bucket to keep off-host recovery copies." /> : (remote.data?.snapshots.length ?? 0) === 0 ? <EmptyState icon="◇" title="No remote snapshots" hint="Export a local snapshot to create an off-host copy." /> : <div className="panel-list snapshot-list"><div className="panel-list-header"><span>Object</span><span>Modified</span><span>Size</span><span /><span>Action</span></div>{remote.data?.snapshots.map((snapshot) => <div key={snapshot.key} className="panel-list-row"><span>{snapshot.key}</span><span>{new Date(snapshot.lastModified).toLocaleString()}</span><span>{formatBytes(snapshot.size)}</span><span /><span><Button size="sm" loading={importRemote.isPending && importRemote.variables === snapshot.key} onClick={() => importRemote.mutate(snapshot.key)}>Import</Button></span></div>)}</div>}
    <ConfirmDialog open={restoreTarget !== null} title="Restore database snapshot?" message={`Restore ${restoreTarget ? snapshotDate(restoreTarget) : ''}?`} consequence="Norn first creates a new safety snapshot, then restores the selected database. The operation is durable but application writes during restore may fail." confirmLabel="Queue restore" onClose={() => setRestoreTarget(null)} onConfirm={() => restoreTarget && restore.mutate({ intent: durableIntent(`${appId}:restore`, { snapshot: restoreTarget.filename }), snapshot: restoreTarget.filename })} />
    <ConfirmDialog open={confirmPrune} title="Prune old snapshots?" message={`Keep the newest ${keep} and prune ${pruneCandidates.length} local snapshot${pruneCandidates.length === 1 ? '' : 's'}?`} consequence={pruneCandidates.map((item) => item.filename).join(', ')} confirmLabel="Queue prune" onClose={() => setConfirmPrune(false)} onConfirm={() => prune.mutate({ intent: durableIntent(`${appId}:prune`, { keep }), count: keep })} />
  </section>
}
