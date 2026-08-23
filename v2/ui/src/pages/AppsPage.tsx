import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'
import { AppCard } from '../components/AppCard.tsx'
import { ConfirmDialog, EmptyState, ErrorState, Skeleton } from '../components/ui/index.ts'
import { Button, useToast } from '../components/ui/index.ts'
import { CreateAppModal, type CreateAppInput } from '../components/CreateAppModal.tsx'
import { apiFetch } from '../lib/api.ts'

type AppFilter = 'all' | 'healthy' | 'unhealthy' | 'idle'

export function AppsPage() {
  const ctx = useRuntimeContext()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
	const queryClient = useQueryClient()
	const { toast } = useToast()
	const [creating, setCreating] = useState(false)
	const [enableCandidate, setEnableCandidate] = useState<string | null>(null)
	const createApp = useMutation({
		mutationFn: (input: CreateAppInput) => apiFetch<{ app: string }>('/api/v1/apps', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }),
		onSuccess: async (receipt) => { await queryClient.invalidateQueries({ queryKey: ['apps'] }); setCreating(false); toast({ kind: 'success', title: 'Draft app created', description: receipt.app }) },
	})
  const filter = (params.get('filter') as AppFilter) || 'all'
  const q = params.get('q') ?? ''
  const idleApps = useMemo(() => new Set(ctx.accessPatterns.filter((pattern) => pattern.idleCandidate).map((pattern) => pattern.app)), [ctx.accessPatterns])
  const filtered = ctx.apps.filter((app) => {
    if (filter === 'healthy' && !app.healthy) return false
    if (filter === 'unhealthy' && app.healthy) return false
    if (filter === 'idle' && !idleApps.has(app.spec.name)) return false
    return !q || app.spec.name.toLowerCase().includes(q.toLowerCase())
  })
  const setFilter = (next: AppFilter) => {
    const copy = new URLSearchParams(params)
    if (next === 'all') copy.delete('filter')
    else copy.set('filter', next)
    setParams(copy)
  }
  const setQuery = (next: string) => {
    const copy = new URLSearchParams(params)
    if (next) copy.set('q', next)
    else copy.delete('q')
    setParams(copy)
  }
  if (ctx.error) return <ErrorState message={ctx.error} onRetry={ctx.refetch} />
  return (
    <div>
		<div className="apps-toolbar">
      <div className="app-filter-bar">
        {(['all', 'healthy', 'unhealthy', 'idle'] as const).map((item) => <button key={item} className={`filter-btn ${filter === item ? 'active' : ''}`} type="button" onClick={() => setFilter(item)}>{item}</button>)}
        <input className="filter-input" value={q} onChange={(event) => setQuery(event.target.value)} placeholder="Filter apps" aria-label="Filter apps" />
      </div>
			<Button variant="primary" icon="fa-plus" onClick={() => setCreating(true)}>Create app</Button>
		</div>
      {ctx.loading ? <div className="panel-skeleton"><Skeleton /><Skeleton /><Skeleton /></div> : filtered.length === 0 ? (
        ctx.apps.length === 0 ? <LegacyEmptyState /> : <EmptyState icon="⌕" title="No apps match" hint="Adjust the health or text filters." />
      ) : (
        <div className="app-grid">
          {filtered.map((app) => {
            const appId = app.spec.name
            return (
              <AppCard
                key={appId}
                app={app}
                busy={ctx.mutations.busy === appId}
                services={ctx.serviceManifest?.services.filter((service) => service.app === appId) ?? []}
                idleCandidates={ctx.accessPatterns.filter((pattern) => pattern.app === appId && pattern.idleCandidate)}
                activeIngress={ctx.activeIngress}
                onOpen={() => navigate(`/apps/${appId}/overview`)}
                onPreflight={(id) => ctx.mutations.run(id, 'preflight')}
                onDeploy={(id) => ctx.mutations.run(id, 'deploy')}
                onRestart={(id) => ctx.mutations.run(id, 'restart')}
                onScale={() => ctx.mutations.scale(app)}
                onViewLogs={(id) => navigate(`/apps/${id}/logs`)}
                onExec={(id) => navigate(`/apps/${id}/shell`)}
                onSnapshots={(id) => navigate(`/apps/${id}/snapshots`)}
                onCron={(id) => navigate(`/apps/${id}/cron`)}
                onFunction={(id) => navigate(`/apps/${id}/functions`)}
                onToggleEndpoint={ctx.toggleEndpoint}
				onToggleDeployment={async (id, enabled) => {
					if (enabled) setEnableCandidate(id)
					else await ctx.toggleDeployment(id, false)
				}}
              />
            )
          })}
        </div>
      )}
		<CreateAppModal open={creating} busy={createApp.isPending} error={createApp.error instanceof Error ? createApp.error.message : null} onClose={() => !createApp.isPending && setCreating(false)} onCreate={(input) => createApp.mutate(input)} />
		<ConfirmDialog
			open={enableCandidate !== null}
			title="Enable deployments?"
			message={`Norn will allow deployment operations for ${enableCandidate ?? 'this app'}.`}
			consequence="This only removes the safety gate; review the InfraSpec and run Check before the first deploy."
			confirmLabel="Enable deployments"
			confirmIcon="fa-shield-check"
			onClose={() => setEnableCandidate(null)}
			onConfirm={() => {
				const appId = enableCandidate
				setEnableCandidate(null)
				if (appId) void ctx.toggleDeployment(appId, true)
			}}
		/>
    </div>
  )
}

function LegacyEmptyState() {
  return (
    <div className="empty-state">
      <div className="empty-state-icon">*</div>
      <h2>No apps discovered yet</h2>
      <p>Create a disabled draft here, or add an <code>infraspec.yaml</code> to the projects directory.</p>
      <div className="empty-state-example"><pre>{`name: my-app
deploy: false
processes:
  web:
    port: 3000
repo:
  url: git@github.com:you/my-app.git`}</pre></div>
    </div>
  )
}
