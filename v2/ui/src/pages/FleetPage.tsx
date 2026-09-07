import { useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { NavLink } from 'react-router-dom'
import { apiFetch } from '../lib/api.ts'
import { clearDurableIntent, durableIntent, type DurableIntent } from '../lib/durableIntent.ts'
import { buildFleetExecutionSteps, currentFleetStep, humanize, operationForPlan, planIsComplete, type FleetExecutionStep, type FleetStepState } from '../lib/fleetExecution.ts'
import { fleetTimingView } from '../lib/fleetTiming.ts'
import { relativeTime, statusTone } from '../lib/format.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'
import type { Deployment, DeploymentListResponse, FleetGitHubStatus, FleetInventory, FleetNodePool, FleetPlansResponse, FleetReconciliationResponse, FleetRunnerAttempt, FleetRunnerAttemptResponse, Operation, OperationsResponse } from '../types/index.ts'
import { FleetPlanTopology } from '../components/FleetPlanTopology.tsx'
import { EmptyState, StatusChip, useToast, type StatusTone } from '../components/ui/index.ts'
import { AuthorityEnrollment } from '../components/AuthorityEnrollment.tsx'

export function FleetPage() {
  const runtime = useRuntimeContext()
  const requiresAuthorityPairing = runtime.authority === 'fleet-only' && !runtime.authenticated
  const inventory = useQuery({ queryKey: ['fleet', 'inventory'], queryFn: () => apiFetch<FleetInventory>('/api/v1/fleet/node-pools'), staleTime: 15_000, refetchInterval: 30_000, enabled: !requiresAuthorityPairing })
  const plans = useQuery({ queryKey: ['fleet', 'plans'], queryFn: () => apiFetch<FleetPlansResponse>('/api/v1/fleet/plans'), staleTime: 15_000, refetchInterval: 15_000, enabled: inventory.data?.configured === true })
  const github = useQuery({ queryKey: ['fleet', 'github'], queryFn: () => apiFetch<FleetGitHubStatus>('/api/v1/fleet/github'), staleTime: 15_000, refetchInterval: 30_000, enabled: inventory.data?.configured === true })
  const activeOperations = useQuery({ queryKey: ['operations', 'active'], queryFn: () => apiFetch<OperationsResponse>('/api/operations/active'), staleTime: 8_000, refetchInterval: 10_000, enabled: runtime.operationsAvailable && !requiresAuthorityPairing })
  const deployments = useQuery({ queryKey: ['deployments-v1', { limit: 20 }], queryFn: () => apiFetch<DeploymentListResponse>('/api/v1/deployments?limit=20'), staleTime: 8_000, refetchInterval: 10_000, enabled: runtime.runtimeAvailable })
  const pools = useMemo(() => Object.entries(inventory.data?.nodePools ?? {}).sort(([a], [b]) => a.localeCompare(b)), [inventory.data?.nodePools])

  if (requiresAuthorityPairing) return <div className="fleet-page"><AuthorityEnrollment onAuthenticated={runtime.refreshAuthoritySession} onSignedOut={runtime.clearAuthoritySession} /></div>
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

      {runtime.authority === 'fleet-only' && <FleetAuthorityChecklist inventory={inventory.data} github={github.data} canWrite={runtime.canFleetWrite} />}

      {runtime.runtimeAvailable ? <PlatformNow apps={runtime.apps} activeOperations={activeOperations.data?.operations ?? []} deployments={deployments.data?.deployments ?? []} observedAt={Math.max(activeOperations.dataUpdatedAt, deployments.dataUpdatedAt)} loading={activeOperations.isLoading || deployments.isLoading || runtime.loading} stale={activeOperations.isError || deployments.isError || runtime.error !== null} /> : <AuthorityOperations operations={activeOperations.data?.operations ?? []} loading={activeOperations.isLoading} />}

      <section aria-labelledby="node-pools-title">
        <div className="section-heading"><div><span className="eyebrow">Capacity</span><h2 id="node-pools-title">Node pools</h2></div><span>{pools.length} desired pools</span></div>
        <div className="fleet-pool-grid">
          {pools.map(([name, pool]) => <FleetPoolCard key={name} name={name} pool={pool} valid={report?.valid !== false} canWrite={runtime.canFleetWrite} />)}
        </div>
      </section>

      <section className="panel" aria-labelledby="fleet-plans-title">
        <h2 id="fleet-plans-title">Durable changes</h2>
        <p className="panel-intro">Every view is reconstructed from Norn. Closing this page does not lose a plan or its last proven recovery checkpoint.</p>
        <PlanRows
          plans={plans.data?.plans ?? []}
          loading={plans.isLoading}
          fallbackWorkflowURL={inventory.data.document?.metadata?.workflowUrl}
          githubConnected={github.data?.connected === true}
          capabilities={new Set(runtime.capabilities?.features ?? [])}
          inventory={inventory.data}
          apps={runtime.apps}
          manifest={runtime.serviceManifest}
        />
      </section>
    </div>
  )
}

function FleetPoolCard({ name, pool, valid, canWrite }: { name: string; pool: FleetNodePool; valid: boolean; canWrite: boolean }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <article className="fleet-pool-card">
      <div className="fleet-pool-header"><div><span className="eyebrow">{pool.labels?.workload ?? 'node pool'}</span><h3>{name}</h3></div><StatusChip tone={pool.min >= 2 ? 'success' : 'warning'} label={`${pool.desired} desired`} /></div>
      <dl className="fleet-pool-metrics"><div><dt>Size</dt><dd>{pool.size}</dd></div><div><dt>Range</dt><dd>{pool.min}–{pool.max}</dd></div><div><dt>Replacement</dt><dd>{pool.replacement?.strategy ?? 'blueGreen'}</dd></div><div><dt>Drain</dt><dd>{pool.replacement?.drainTimeout ?? 'not set'}</dd></div></dl>
      {valid && canWrite ? <button className="filter-btn fleet-plan-toggle" type="button" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>{expanded ? 'Close change' : 'Change capacity'}</button> : <p className="fleet-plan-unavailable" role="status">{valid ? 'Your current identity is read-only; capacity changes require an operator grant.' : 'Planning is unavailable until the fleet document is valid.'}</p>}
      {expanded && canWrite && <CapacityPlanForm name={name} pool={pool} />}
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
    mutationFn: ({ request, intent }: { request: typeof body; intent: DurableIntent }) => apiFetch<Operation>(`/api/v1/fleet/node-pools/${encodeURIComponent(name)}/plan`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Idempotency-Key': intent.key },
      body: JSON.stringify(request),
    }),
    onSuccess: (operation, variables) => {
      clearDurableIntent(variables.intent)
      queryClient.invalidateQueries({ queryKey: ['fleet', 'plans'] })
      setReceipt(operation)
      toast({ kind: 'success', title: 'Durable capacity plan recorded', description: operation.id })
    },
    onError: (error) => toast({ kind: 'error', title: 'Capacity plan rejected', description: error instanceof Error ? error.message : name }),
  })
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!mutation.isPending && reason.trim().length >= 4 && size.trim()) {
      mutation.mutate({ request: body, intent: durableIntent(`fleet-plan:${name}`, body) })
    }
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

function PlatformNow({ apps, activeOperations, deployments, observedAt, loading, stale }: { apps: ReturnType<typeof useRuntimeContext>['apps']; activeOperations: Operation[]; deployments: Deployment[]; observedAt: number; loading: boolean; stale: boolean }) {
  const allocations = apps.flatMap((app) => app.allocations ?? []).filter((allocation) => allocation.lifecycle === 'active')
  const healthyAllocations = allocations.filter((allocation) => allocation.status === 'running' && allocation.healthy !== false).length
  const activeDeployments = deployments.filter((deployment) => !['deployed', 'failed', 'rolled_back'].includes(deployment.status))
  const failedRegions = deployments.flatMap((deployment) => deployment.regions ?? []).filter((region) => region.status === 'failed').length
  return (
    <section className="panel fleet-platform-now" aria-labelledby="fleet-platform-now-title" aria-live="polite">
      <div className="section-heading">
        <div><span className="eyebrow">Observed runtime</span><h2 id="fleet-platform-now-title">Platform now</h2></div>
        <StatusChip tone={stale ? 'warning' : loading ? 'neutral' : 'success'} label={stale ? 'last known' : loading ? 'syncing' : `updated ${observedAt ? relativeTime(new Date(observedAt).toISOString()) : 'now'}`} />
      </div>
      <div className="fleet-runtime-metrics">
        <div><span>Applications</span><strong>{apps.filter((app) => app.healthy).length}/{apps.length}</strong><small>healthy</small></div>
        <div><span>Allocations</span><strong>{healthyAllocations}/{allocations.length}</strong><small>healthy</small></div>
        <div><span>Operations</span><strong>{activeOperations.length}</strong><small>queued or running</small></div>
        <div><span>Deployments</span><strong>{activeDeployments.length}</strong><small>{failedRegions > 0 ? `${failedRegions} failed regions` : 'active now'}</small></div>
      </div>
      {(activeOperations.length > 0 || activeDeployments.length > 0) && <div className="fleet-runtime-activity">
        {activeOperations.slice(0, 4).map((operation) => <div key={operation.id}><StatusChip tone={statusTone(operation.status)} label={operation.status ?? 'unknown'} /><span>{operation.kind}</span><code>{operation.app || operation.ref || operation.id}</code></div>)}
        {activeDeployments.slice(0, 4).map((deployment) => <div key={deployment.id}><StatusChip tone={statusTone(deployment.status)} label={deployment.status} /><span>{deployment.app}</span><code>{deployment.regions?.map((region) => `${region.region}:${region.status}`).join(' · ') || deployment.commitSha.slice(0, 8)}</code></div>)}
      </div>}
      {stale && <p className="fleet-stale-note" role="status">Some live observations could not be refreshed. Desired state and durable receipts remain available, but runtime counts may be stale.</p>}
    </section>
  )
}

function PlanRows({ plans, loading, fallbackWorkflowURL, githubConnected, capabilities, inventory, apps, manifest }: { plans: Operation[]; loading: boolean; fallbackWorkflowURL?: string; githubConnected: boolean; capabilities: Set<string>; inventory: FleetInventory; apps: ReturnType<typeof useRuntimeContext>['apps']; manifest?: ReturnType<typeof useRuntimeContext>['serviceManifest'] }) {
  if (loading) return <div className="panel-skeleton"><span /><span /></div>
  if (plans.length === 0) return <EmptyState icon="·" title="No capacity changes" hint="Choose a node pool to create a durable planning receipt." />
  return <div className="fleet-plan-list">{plans.map((plan, index) => <PlanJourney key={plan.id ?? `${plan.createdAt}-${index}`} plan={plan} fallbackWorkflowURL={fallbackWorkflowURL} githubConnected={githubConnected} capabilities={capabilities} inventory={inventory} apps={apps} manifest={manifest} />)}</div>
}

function PlanJourney({ plan, fallbackWorkflowURL, githubConnected, capabilities, inventory, apps, manifest }: { plan: Operation; fallbackWorkflowURL?: string; githubConnected: boolean; capabilities: Set<string>; inventory: FleetInventory; apps: ReturnType<typeof useRuntimeContext>['apps']; manifest?: ReturnType<typeof useRuntimeContext>['serviceManifest'] }) {
  const [expanded, setExpanded] = useState(false)
  const [pullRequestURL, setPullRequestURL] = useState<string>()
  const [applyURL, setApplyURL] = useState<string>()
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const planID = plan.id ?? ''
  const reconciliationAvailable = capabilities.has('fleet-reconciliation-v1')
  const attemptsAvailable = capabilities.has('fleet-runner-attempts-v1')
  const githubAvailable = capabilities.has('fleet-github-app-v1') && githubConnected
  const { canFleetWrite } = useRuntimeContext()
  const checkpoints = useQuery({ queryKey: ['fleet', 'plans', planID, 'reconciliations'], queryFn: () => apiFetch<FleetReconciliationResponse>(`/api/v1/fleet/plans/${encodeURIComponent(planID)}/reconciliations`), enabled: expanded && planID.length > 0 && reconciliationAvailable, refetchInterval: expanded ? 10_000 : false })
  const attempts = useQuery({ queryKey: ['fleet', 'plans', planID, 'attempts'], queryFn: () => apiFetch<FleetRunnerAttemptResponse>(`/api/v1/fleet/plans/${encodeURIComponent(planID)}/attempts`), enabled: expanded && planID.length > 0 && attemptsAvailable, refetchInterval: expanded ? 5_000 : false })
  const pullReceipts = useQuery({ queryKey: ['operations', 'fleet.github.pull-request'], queryFn: () => apiFetch<OperationsResponse>('/api/operations?kind=fleet.github.pull-request&limit=100'), enabled: expanded && githubAvailable, staleTime: 8_000, refetchInterval: expanded ? 10_000 : false })
  const dispatchReceipts = useQuery({ queryKey: ['operations', 'fleet.github.apply-dispatch'], queryFn: () => apiFetch<OperationsResponse>('/api/operations?kind=fleet.github.apply-dispatch&limit=100'), enabled: expanded && githubAvailable, staleTime: 8_000, refetchInterval: expanded ? 10_000 : false })
  const current = objectValue(plan.payload?.current)
  const proposed = objectValue(plan.payload?.proposed)
  const workflowURL = safeWorkflowURL(String(plan.payload?.workflowUrl ?? fallbackWorkflowURL ?? ''))
  const action = String(plan.payload?.action ?? 'reconcile')
  const destructive = action === 'replace' || (action === 'scale' && Number(proposed.desired) < Number(current.desired))
  const pullRequest = useMutation({
    mutationFn: () => apiFetch<Operation>(`/api/v1/fleet/plans/${encodeURIComponent(planID)}/github/pull-request`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }),
    onSuccess: (operation) => {
      const url = safeWorkflowURL(String(operation.payload?.url ?? ''))
      setPullRequestURL(url)
      queryClient.invalidateQueries({ queryKey: ['operations', 'fleet.github.pull-request'] })
      toast({ kind: 'success', title: 'Fleet review opened', description: operation.id })
    },
    onError: (error) => toast({ kind: 'error', title: 'Could not open fleet review', description: error instanceof Error ? error.message : planID }),
  })
  const dispatch = useMutation({
    mutationFn: () => apiFetch<Operation>(`/api/v1/fleet/plans/${encodeURIComponent(planID)}/github/dispatch`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ allowDestructive: destructive }) }),
    onSuccess: (operation) => {
      const url = safeWorkflowURL(String(operation.payload?.url ?? ''))
      setApplyURL(url)
      queryClient.invalidateQueries({ queryKey: ['operations', 'fleet.github.apply-dispatch'] })
      toast({ kind: 'success', title: 'Protected apply dispatched', description: operation.id })
    },
    onError: (error) => toast({ kind: 'error', title: 'Apply is not ready', description: error instanceof Error ? error.message : planID }),
  })
  const pullReceipt = operationForPlan(pullReceipts.data?.operations, planID) ?? pullRequest.data
  const dispatchReceipt = operationForPlan(dispatchReceipts.data?.operations, planID) ?? dispatch.data
  const runnerAttempt = attempts.data?.attempts[0]
  const executionSteps = buildFleetExecutionSteps({ plan, reconciliations: checkpoints.data?.reconciliations ?? [], pullRequest: pullReceipt, dispatch: dispatchReceipt, runnerAttempt })
  const visibleSteps = reconciliationAvailable ? executionSteps : executionSteps.slice(0, 3)
  const complete = planIsComplete(executionSteps)
  const currentStep = complete ? undefined : currentFleetStep(visibleSteps)
  const failed = executionSteps.some((step) => step.state === 'failed')
  const started = Boolean(pullReceipt || dispatchReceipt || runnerAttempt || (checkpoints.data?.reconciliations.length ?? 0) > 0)
  return (
    <article className="fleet-journey">
      <button type="button" className="fleet-journey-summary" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
        <span aria-hidden="true">{complete ? '✓' : expanded ? '−' : '+'}</span>
        <span><strong>{String(plan.payload?.pool ?? 'pool')}</strong><small>{numberValue(current.desired)} → {numberValue(proposed.desired)} nodes · {action}</small></span>
        <StatusChip tone={complete ? 'success' : failed ? 'danger' : started ? 'info' : 'neutral'} label={complete ? 'complete' : failed ? 'attention' : started ? 'handoff started' : 'planned'} />
      </button>
      {expanded && (
        <div className="fleet-journey-detail">
          <FleetTimingSummary attempt={runnerAttempt} steps={visibleSteps} />
          {checkpoints.isLoading || attempts.isLoading || pullReceipts.isLoading || dispatchReceipts.isLoading ? <div className="panel-skeleton" aria-label="Loading execution evidence"><span /><span /></div> : <FleetStepList steps={visibleSteps} />}
          {!reconciliationAvailable && <p className="fleet-change-warning" role="status">This server does not advertise fleet-reconciliation-v1, so infrastructure checkpoint progress is hidden.</p>}
          {reconciliationAvailable && !attemptsAvailable && <p className="fleet-change-warning" role="status">This server has checkpoint evidence but no durable runner heartbeat contract; live phase state cannot be proven.</p>}
          {(checkpoints.isRefetchError || attempts.isRefetchError || pullReceipts.isRefetchError || dispatchReceipts.isRefetchError) && <p className="fleet-change-warning" role="alert">Execution evidence could not be refreshed. The list is last-known state; the durable plan remains safe in Norn.</p>}
          {currentStep && <div className={`fleet-current-step state-${currentStep.state}`}>
            <div><span className="eyebrow">{currentStep.state === 'failed' ? 'Needs attention' : currentStep.state === 'active' ? 'Current step' : 'Next expected step'}</span><strong>{currentStep.label}</strong><p>{currentStep.message}</p></div>
            <CurrentStepActions
              complete={complete}
              githubAvailable={githubAvailable}
              pullReceipt={pullReceipt}
              dispatchReceipt={dispatchReceipt}
              workflowURL={workflowURL}
              pullRequestURL={pullRequestURL}
              applyURL={applyURL}
              destructive={destructive}
              pullRequest={pullRequest}
              dispatch={dispatch}
              runnerAttempt={runnerAttempt}
              canWrite={canFleetWrite}
            />
          </div>}
          <div className="fleet-handoff"><code>{planID}</code>{planID && <NavLink className="filter-btn" to="/operations">View operations</NavLink>}</div>
          <p className="fleet-change-note">This plan ID is the resume key. Dispatch proves handoff; runner attempts prove liveness, heartbeats bound the active phase, and append-only checkpoints remain the authority for advancing.</p>
          {reconciliationAvailable && <FleetPlanTopology plan={plan} inventory={inventory} apps={apps} manifest={manifest} steps={executionSteps} />}
        </div>
      )}
    </article>
  )
}

function FleetTimingSummary({ attempt, steps }: { attempt?: FleetRunnerAttempt; steps: FleetExecutionStep[] }) {
  const timing = fleetTimingView(attempt)
  const provisioning = steps.filter((step) => !['plan_recorded', 'review_opened', 'apply_dispatched'].includes(step.id))
  const proven = provisioning.filter((step) => step.state === 'completed').length
  const confidence = attempt?.timing?.confidence === 'low' ? 'Provisional, low confidence' : undefined
  const sampleCount = attempt?.timing?.provenance.successfulSampleCount ?? 0
  const exclusions = attempt?.timing?.provenance.exclusions ?? []
  return (
    <section className={`fleet-timing ${timing.available ? 'available' : 'unavailable'}`} aria-label="Provisioning timing estimate">
      <div className="fleet-timing-heading">
        <div><span className="eyebrow">Runner time</span><strong>{timing.headline}</strong></div>
        {confidence && <span>{confidence}</span>}
      </div>
      <p>{timing.detail}</p>
      {attempt && <div className="fleet-timing-progress">
        <progress aria-label={`${proven} of ${provisioning.length} provisioning checkpoints proven`} max={provisioning.length || 1} value={proven} />
        <small>{proven} of {provisioning.length} checkpoints proven · {humanize(attempt.currentPhase)} · attempt {attempt.attempt}</small>
      </div>}
      {timing.available && <small className="fleet-timing-provenance">Configured range · {sampleCount} comparable successful samples{exclusions.length > 0 ? ` · excludes ${exclusions.map(humanizeLower).join(', ')}` : ''}</small>}
    </section>
  )
}

function FleetStepList({ steps }: { steps: FleetExecutionStep[] }) {
  return <ol className="fleet-checkpoints" aria-label="Provisioning steps">{steps.map((step) => (
    <li className={`state-${step.state}`} key={step.id} aria-current={step.state === 'active' ? 'step' : undefined}>
      <span aria-hidden="true">{stepIcon(step.state)}</span>
      <span><strong>{step.label}</strong><small>{step.message}</small></span>
      <small>{stepStateLabel(step.state)}</small>
    </li>
  ))}</ol>
}

function CurrentStepActions({ complete, githubAvailable, pullReceipt, dispatchReceipt, workflowURL, pullRequestURL, applyURL, destructive, pullRequest, dispatch, runnerAttempt, canWrite }: {
  complete: boolean
  githubAvailable: boolean
  pullReceipt?: Operation
  dispatchReceipt?: Operation
  workflowURL?: string
  pullRequestURL?: string
  applyURL?: string
  destructive: boolean
  pullRequest: { isPending: boolean; mutate: () => void }
  dispatch: { isPending: boolean; mutate: () => void }
  runnerAttempt?: FleetRunnerAttempt
  canWrite: boolean
}) {
  const recordedPullURL = safeWorkflowURL(String(pullReceipt?.payload?.url ?? pullRequestURL ?? ''))
  const recordedApplyURL = safeWorkflowURL(String(dispatchReceipt?.payload?.url ?? applyURL ?? ''))
  const runnerURL = safeWorkflowURL(runnerAttempt?.workflowUrl ?? '')
  const runnerActive = runnerAttempt?.status === 'queued' || runnerAttempt?.status === 'running'
  const runnerNeedsRetry = runnerAttempt?.status === 'failed' || runnerAttempt?.status === 'abandoned'
  if (complete) return <span className="fleet-action-complete">No action required</span>
  if (githubAvailable && !pullReceipt) return canWrite ? <button className="filter-btn active" type="button" disabled={pullRequest.isPending} onClick={() => pullRequest.mutate()}>{pullRequest.isPending ? 'Opening…' : 'Open or recover review'}</button> : <span className="fleet-action-unavailable">Read-only identity; an operator must open review.</span>
  if (githubAvailable && pullReceipt && !dispatchReceipt) return <div className="fleet-step-actions">{recordedPullURL && <a href={recordedPullURL} target="_blank" rel="noreferrer">View pull request ↗</a>}{canWrite ? <button className="filter-btn active" type="button" disabled={dispatch.isPending} onClick={() => dispatch.mutate()}>{dispatch.isPending ? 'Dispatching…' : destructive ? 'Dispatch reviewed change' : 'Dispatch protected apply'}</button> : <span className="fleet-action-unavailable">Read-only identity; an operator must dispatch.</span>}</div>
  if (runnerNeedsRetry && runnerURL) return <a href={runnerURL} target="_blank" rel="noreferrer">Retry in protected runner ↗</a>
  if (runnerActive && runnerURL) return <a href={runnerURL} target="_blank" rel="noreferrer">View active runner ↗</a>
  if (recordedApplyURL || recordedPullURL || workflowURL) return <div className="fleet-step-actions">{recordedApplyURL && <a href={recordedApplyURL} target="_blank" rel="noreferrer">View apply run ↗</a>}{recordedPullURL && <a href={recordedPullURL} target="_blank" rel="noreferrer">View pull request ↗</a>}{!recordedApplyURL && workflowURL && <a href={workflowURL} target="_blank" rel="noreferrer">Continue in protected runner ↗</a>}</div>
  return <span className="fleet-action-unavailable">No safe Norn action is available for this step.</span>
}

function FleetAuthorityChecklist({ inventory, github, canWrite }: { inventory: FleetInventory; github?: FleetGitHubStatus; canWrite: boolean }) {
  const checks: Array<{ label: string; detail: string; tone: StatusTone }> = [
    { label: 'Desired Fleet document', detail: inventory.validation?.valid ? 'Validated by this authority.' : 'Validation evidence is missing or reports findings.', tone: inventory.validation?.valid ? 'success' : 'warning' as const },
    { label: 'GitHub review bridge', detail: github?.connected ? 'Connection reported by the configured GitHub App.' : github?.message ?? 'No connected bridge has been reported.', tone: github?.connected ? 'success' : 'warning' as const },
    { label: 'Operator permission', detail: canWrite ? 'This identity can record Fleet planning requests.' : 'This identity is read-only; no planning actions are exposed.', tone: canWrite ? 'success' : 'neutral' as const },
  ]
  return <section className="panel" aria-labelledby="fleet-authority-checklist"><div className="section-heading"><div><span className="eyebrow">Evidence checklist</span><h2 id="fleet-authority-checklist">Fleet management scope</h2></div><StatusChip tone="neutral" label="not a readiness verdict" /></div><p className="panel-intro">These are reported control-plane facts. They do not prove provider capacity, workload health, or rollout readiness.</p><div className="compact-list">{checks.map((check) => <div className="compact-row" key={check.label}><StatusChip tone={check.tone} label={check.label} /><span>{check.detail}</span></div>)}</div></section>
}

function AuthorityOperations({ operations, loading }: { operations: Operation[]; loading: boolean }) {
  return <section className="panel" aria-labelledby="authority-operations-title"><div className="section-heading"><div><span className="eyebrow">Durable management records</span><h2 id="authority-operations-title">Fleet operations</h2></div></div>{loading ? <div className="panel-skeleton"><span /><span /></div> : operations.length === 0 ? <EmptyState icon="·" title="No active Fleet operations" hint="No runtime or deployment status was requested from this authority." /> : <div className="compact-list">{operations.map((operation) => <div className="compact-row" key={operation.id}><StatusChip tone={statusTone(operation.status)} label={operation.status ?? 'unknown'} /><span>{operation.kind ?? 'operation'}</span><code>{operation.id}</code></div>)}</div>}</section>
}

function objectValue(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {} }
function numberValue(value: unknown): string { return typeof value === 'number' ? String(value) : '?' }
function safeWorkflowURL(value: string): string | undefined { try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password ? url.toString() : undefined } catch { return undefined } }
function shortDigest(value?: string) { return value ? `${value.slice(0, 18)}…` : 'unversioned' }
function stepIcon(state: FleetStepState): string { return state === 'completed' ? '✓' : state === 'failed' ? '!' : state === 'blocked' ? '×' : state === 'active' ? '↻' : '○' }
function stepStateLabel(state: FleetStepState): string { return state === 'completed' ? 'Proven' : humanize(state) }
function humanizeLower(value: string): string { return value.replaceAll('_', ' ').toLowerCase() }
