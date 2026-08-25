import { useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import type { FleetGitHubStatus, FleetInventory, FleetNodePool, FleetPlansResponse, FleetReconciliationResponse, Operation } from '../types/index.ts'
import { EmptyState, StatusChip, useToast } from '../components/ui/index.ts'

const reconciliationPhases = [
  'infrastructure_applied', 'inventory_generated', 'nodes_configured',
  'nodes_enrolled', 'readiness_verified', 'old_nodes_drained', 'complete',
]

export function FleetPage() {
  const inventory = useQuery({ queryKey: ['fleet', 'inventory'], queryFn: () => apiFetch<FleetInventory>('/api/v1/fleet/node-pools'), staleTime: 15_000, refetchInterval: 30_000 })
  const plans = useQuery({ queryKey: ['fleet', 'plans'], queryFn: () => apiFetch<FleetPlansResponse>('/api/v1/fleet/plans'), staleTime: 15_000, refetchInterval: 15_000, enabled: inventory.data?.configured === true })
  const github = useQuery({ queryKey: ['fleet', 'github'], queryFn: () => apiFetch<FleetGitHubStatus>('/api/v1/fleet/github'), staleTime: 15_000, refetchInterval: 30_000, enabled: inventory.data?.configured === true })
  const pools = useMemo(() => Object.entries(inventory.data?.nodePools ?? {}).sort(([a], [b]) => a.localeCompare(b)), [inventory.data?.nodePools])

  if (inventory.isLoading) return <div className="panel panel-skeleton" aria-label="Loading fleet"><span /><span /><span /></div>
  if (inventory.error) return <EmptyState icon="!" title="Fleet unavailable" hint={inventory.error instanceof Error ? inventory.error.message : 'Could not load fleet inventory.'} />
  if (!inventory.data?.configured) return <EmptyState icon="◇" title="Connect norn-fleet" hint="Set NORN_FLEET_CONFIG to a read-only checkout of a norn.dev/fleet/v1 Cluster document. Provider credentials remain in the protected Terraform runner." />

  const report = inventory.data.validation
  return (
    <div className="fleet-page">
      <section className="fleet-hero panel">
        <div>
          <span className="eyebrow">Desired infrastructure</span>
          <h2>{inventory.data.document?.cluster.name ?? report?.name ?? 'Fleet'}</h2>
          <p>{inventory.data.document?.cluster.provider} · {inventory.data.document?.cluster.region} · {inventory.data.document?.metadata?.environment ?? 'environment not labeled'}</p>
        </div>
        <div className="fleet-hero-status">
          <StatusChip tone={report?.valid ? 'success' : 'danger'} label={report?.valid ? 'schema valid' : 'attention required'} />
          <StatusChip tone={github.data?.connected ? 'success' : github.data?.configured ? 'warning' : 'neutral'} label={github.data?.connected ? 'GitHub connected' : github.data?.configured ? 'GitHub needs attention' : 'GitHub not configured'} />
        </div>
        <code title={inventory.data.digest}>{shortDigest(inventory.data.digest)}</code>
      </section>

      {report && report.findings.length > 0 && (
        <section className="panel" aria-labelledby="fleet-findings-title">
          <h2 id="fleet-findings-title">Sanity findings</h2>
          <div className="compact-list">
            {report.findings.map((finding) => (
              <div className="compact-row" key={`${finding.code}-${finding.field}`}>
                <StatusChip tone={finding.severity === 'error' ? 'danger' : finding.severity === 'warning' ? 'warning' : 'neutral'} label={finding.severity} />
                <code>{finding.code}</code><span>{finding.message}</span>{finding.remediation && <small>{finding.remediation}</small>}
              </div>
            ))}
          </div>
        </section>
      )}

      <section aria-labelledby="node-pools-title">
        <div className="section-heading"><div><span className="eyebrow">Capacity</span><h2 id="node-pools-title">Node pools</h2></div><span>{pools.length} desired pools</span></div>
        <div className="fleet-pool-grid">
          {pools.map(([name, pool]) => <FleetPoolCard key={name} name={name} pool={pool} valid={report?.valid !== false} />)}
        </div>
      </section>

      <section className="panel" aria-labelledby="fleet-plans-title">
        <h2 id="fleet-plans-title">Durable changes</h2>
        <p className="panel-intro">Every view is reconstructed from Norn. Closing this page does not lose a plan or its last proven recovery checkpoint.</p>
        <PlanRows plans={plans.data?.plans ?? []} loading={plans.isLoading} fallbackWorkflowURL={inventory.data.document?.metadata?.workflowUrl} githubConnected={github.data?.connected === true} />
      </section>
    </div>
  )
}

function FleetPoolCard({ name, pool, valid }: { name: string; pool: FleetNodePool; valid: boolean }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <article className="fleet-pool-card">
      <div className="fleet-pool-header"><div><span className="eyebrow">{pool.labels?.workload ?? 'node pool'}</span><h3>{name}</h3></div><StatusChip tone={pool.min >= 2 ? 'success' : 'warning'} label={`${pool.desired} desired`} /></div>
      <dl className="fleet-pool-metrics"><div><dt>Size</dt><dd>{pool.size}</dd></div><div><dt>Range</dt><dd>{pool.min}–{pool.max}</dd></div><div><dt>Replacement</dt><dd>{pool.replacement?.strategy ?? 'blueGreen'}</dd></div><div><dt>Drain</dt><dd>{pool.replacement?.drainTimeout ?? 'not set'}</dd></div></dl>
      {valid ? <button className="filter-btn fleet-plan-toggle" type="button" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>{expanded ? 'Close change' : 'Change capacity'}</button> : <p className="fleet-plan-unavailable" role="status">Planning is unavailable until the fleet document is valid.</p>}
      {expanded && <CapacityPlanForm name={name} pool={pool} />}
    </article>
  )
}

function CapacityPlanForm({ name, pool }: { name: string; pool: FleetNodePool }) {
  const [desired, setDesired] = useState(pool.desired)
  const [size, setSize] = useState(pool.size)
  const [reason, setReason] = useState('')
  const [receipt, setReceipt] = useState<Operation>()
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const body = { desired, size, strategy: size !== pool.size ? 'blueGreen' : undefined, reason: reason.trim() }
  const isContraction = desired < pool.desired
  const actionLabel = isContraction ? 'Prepare contraction' : size !== pool.size ? 'Prepare replacement' : desired > pool.desired ? 'Record expansion' : 'Record reconciliation'
  const mutation = useMutation({
    mutationFn: () => {
      const serialized = JSON.stringify(body)
      const storageKey = `norn:fleet-plan:${name}`
      let retry: { request: string; key: string } | undefined
      try { retry = JSON.parse(localStorage.getItem(storageKey) ?? '') as typeof retry } catch { retry = undefined }
      if (!retry || retry.request !== serialized) {
        retry = { request: serialized, key: crypto.randomUUID() }
        localStorage.setItem(storageKey, JSON.stringify(retry))
      }
      return apiFetch<Operation>(`/api/v1/fleet/node-pools/${encodeURIComponent(name)}/plan`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': retry.key }, body: serialized })
    },
    onSuccess: (operation) => {
      localStorage.removeItem(`norn:fleet-plan:${name}`)
      queryClient.invalidateQueries({ queryKey: ['fleet', 'plans'] })
      setReceipt(operation)
      toast({ kind: 'success', title: 'Durable capacity plan recorded', description: operation.id })
    },
    onError: (error) => toast({ kind: 'error', title: 'Capacity plan rejected', description: error instanceof Error ? error.message : name }),
  })
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!mutation.isPending && reason.trim().length >= 4 && size.trim()) mutation.mutate()
  }
  return (
    <form className="fleet-plan-form" onSubmit={submit} aria-busy={mutation.isPending}>
      <div className="fleet-capacity-change" aria-label={`Capacity changes from ${pool.desired} to ${desired} nodes`}>
        <span><small>Current</small><strong>{pool.desired}</strong></span><b aria-hidden="true">→</b><span><small>Proposed</small><strong>{desired}</strong></span>
      </div>
      <label>Desired nodes<input aria-label="Desired" type="number" min={pool.min} max={pool.max} value={desired} onChange={(event) => setDesired(Number(event.target.value))} /></label>
      <label>VM size<input value={size} required onChange={(event) => setSize(event.target.value)} /></label>
      <label className="fleet-reason">Reason<input value={reason} required minLength={4} maxLength={1000} placeholder="Why is this capacity change needed?" onChange={(event) => setReason(event.target.value)} /></label>
      {isContraction ? <p className="fleet-change-warning" role="note"><strong>Safe contraction:</strong> this prepares the change. Old nodes are not removed until Norn has proof of capacity headroom, readiness, and drain completion.</p> : <p className="fleet-change-note">The protected runner resumes expansion from its last matching provider and Norn checkpoint if interrupted.</p>}
      <button className="filter-btn active" type="submit">{mutation.isPending ? 'Recording…' : actionLabel}</button>
      {receipt?.id && <p className="fleet-receipt" role="status"><strong>Recorded.</strong> Receipt <code>{receipt.id}</code> will appear below and survive reloads.</p>}
    </form>
  )
}

function PlanRows({ plans, loading, fallbackWorkflowURL, githubConnected }: { plans: Operation[]; loading: boolean; fallbackWorkflowURL?: string; githubConnected: boolean }) {
  if (loading) return <div className="panel-skeleton"><span /><span /></div>
  if (plans.length === 0) return <EmptyState icon="·" title="No capacity changes" hint="Choose a node pool to create a durable planning receipt." />
  return <div className="fleet-plan-list">{plans.map((plan, index) => <PlanJourney key={plan.id ?? `${plan.createdAt}-${index}`} plan={plan} fallbackWorkflowURL={fallbackWorkflowURL} githubConnected={githubConnected} />)}</div>
}

function PlanJourney({ plan, fallbackWorkflowURL, githubConnected }: { plan: Operation; fallbackWorkflowURL?: string; githubConnected: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const [pullRequestURL, setPullRequestURL] = useState<string>()
  const [applyURL, setApplyURL] = useState<string>()
  const { toast } = useToast()
  const planID = plan.id ?? ''
  const checkpoints = useQuery({ queryKey: ['fleet', 'plans', planID, 'reconciliations'], queryFn: () => apiFetch<FleetReconciliationResponse>(`/api/v1/fleet/plans/${encodeURIComponent(planID)}/reconciliations`), enabled: expanded && planID.length > 0, refetchInterval: expanded ? 10_000 : false })
  const current = objectValue(plan.payload?.current)
  const proposed = objectValue(plan.payload?.proposed)
  const completed = new Set((checkpoints.data?.reconciliations ?? []).filter((item) => item.status === 'succeeded').map((item) => String(item.payload?.phase ?? '')))
  const complete = completed.has('complete')
  const workflowURL = safeWorkflowURL(String(plan.payload?.workflowUrl ?? fallbackWorkflowURL ?? ''))
  const action = String(plan.payload?.action ?? 'reconcile')
  const destructive = action === 'replace' || (action === 'scale' && Number(proposed.desired) < Number(current.desired))
  const pullRequest = useMutation({
    mutationFn: () => apiFetch<Operation>(`/api/v1/fleet/plans/${encodeURIComponent(planID)}/github/pull-request`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }),
    onSuccess: (operation) => {
      const url = safeWorkflowURL(String(operation.payload?.url ?? ''))
      setPullRequestURL(url)
      toast({ kind: 'success', title: 'Fleet review opened', description: operation.id })
    },
    onError: (error) => toast({ kind: 'error', title: 'Could not open fleet review', description: error instanceof Error ? error.message : planID }),
  })
  const dispatch = useMutation({
    mutationFn: () => apiFetch<Operation>(`/api/v1/fleet/plans/${encodeURIComponent(planID)}/github/dispatch`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ allowDestructive: destructive }) }),
    onSuccess: (operation) => {
      const url = safeWorkflowURL(String(operation.payload?.url ?? ''))
      setApplyURL(url)
      toast({ kind: 'success', title: 'Protected apply dispatched', description: operation.id })
    },
    onError: (error) => toast({ kind: 'error', title: 'Apply is not ready', description: error instanceof Error ? error.message : planID }),
  })
  return (
    <article className="fleet-journey">
      <button type="button" className="fleet-journey-summary" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
        <span aria-hidden="true">{complete ? '✓' : expanded ? '−' : '+'}</span>
        <span><strong>{String(plan.payload?.pool ?? 'pool')}</strong><small>{numberValue(current.desired)} → {numberValue(proposed.desired)} nodes · {action}</small></span>
        <StatusChip tone={complete ? 'success' : completed.size > 0 ? 'warning' : 'neutral'} label={complete ? 'complete' : completed.size > 0 ? 'in progress' : 'planned'} />
      </button>
      {expanded && (
        <div className="fleet-journey-detail">
          {checkpoints.isLoading ? <div className="panel-skeleton" aria-label="Loading recovery checkpoints"><span /><span /></div> : <ol className="fleet-checkpoints">{reconciliationPhases.map((phase) => <li className={completed.has(phase) ? 'complete' : ''} key={phase}><span aria-hidden="true">{completed.has(phase) ? '✓' : '○'}</span><span>{humanize(phase)}</span><small>{completed.has(phase) ? 'Proven' : 'Pending'}</small></li>)}</ol>}
          {checkpoints.error && <p className="fleet-change-warning" role="alert">Recovery checkpoints could not be refreshed. The durable plan remains safe in Norn.</p>}
          <div className="fleet-handoff"><code>{planID}</code>{githubConnected ? <><button className="filter-btn" type="button" disabled={pullRequest.isPending} onClick={() => pullRequest.mutate()}>{pullRequest.isPending ? 'Opening…' : 'Open review'}</button><button className="filter-btn active" type="button" disabled={dispatch.isPending} onClick={() => dispatch.mutate()}>{dispatch.isPending ? 'Dispatching…' : destructive ? 'Apply reviewed change' : 'Apply after review'}</button></> : workflowURL && <a className="filter-btn" href={workflowURL} target="_blank" rel="noreferrer">Continue in protected runner ↗</a>}</div>
          {(pullRequestURL || applyURL) && <div className="fleet-github-links">{pullRequestURL && <a href={pullRequestURL} target="_blank" rel="noreferrer">View pull request ↗</a>}{applyURL && <a href={applyURL} target="_blank" rel="noreferrer">View apply run ↗</a>}</div>}
          <p className="fleet-change-note">This plan ID is the resume key. Review and merge the pull request, wait for the protected plan to pass, then apply. Repeating either action safely recovers the existing GitHub work.</p>
        </div>
      )}
    </article>
  )
}

function objectValue(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {} }
function numberValue(value: unknown): string { return typeof value === 'number' ? String(value) : '?' }
function humanize(value: string): string { return value.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase()) }
function safeWorkflowURL(value: string): string | undefined { try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password ? url.toString() : undefined } catch { return undefined } }
function shortDigest(value?: string) { return value ? `${value.slice(0, 18)}…` : 'unversioned' }
