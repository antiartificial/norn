import { useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import type { FleetInventory, FleetNodePool, FleetPlansResponse, Operation } from '../types/index.ts'
import { EmptyState, StatusChip, useToast } from '../components/ui/index.ts'

export function FleetPage() {
  const inventory = useQuery({ queryKey: ['fleet', 'inventory'], queryFn: () => apiFetch<FleetInventory>('/api/v1/fleet/node-pools'), staleTime: 15_000, refetchInterval: 30_000 })
  const plans = useQuery({ queryKey: ['fleet', 'plans'], queryFn: () => apiFetch<FleetPlansResponse>('/api/v1/fleet/plans'), staleTime: 15_000, enabled: inventory.data?.configured === true })
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
        <StatusChip tone={report?.valid ? 'success' : 'danger'} label={report?.valid ? 'schema valid' : 'attention required'} />
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
        <h2 id="fleet-plans-title">Recent capacity plans</h2>
        <p className="panel-intro">Plans are durable review artifacts. Norn does not apply Terraform or hold provider credentials.</p>
        <PlanRows plans={plans.data?.plans ?? []} loading={plans.isLoading} />
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
      {valid ? (
        <button className="filter-btn fleet-plan-toggle" type="button" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>{expanded ? 'Close plan' : 'Plan capacity'}</button>
      ) : (
        <p className="fleet-plan-unavailable" role="status">Planning is unavailable until the fleet document is valid.</p>
      )}
      {expanded && <CapacityPlanForm name={name} pool={pool} onDone={() => setExpanded(false)} />}
    </article>
  )
}

function CapacityPlanForm({ name, pool, onDone }: { name: string; pool: FleetNodePool; onDone: () => void }) {
  const [desired, setDesired] = useState(pool.desired)
  const [size, setSize] = useState(pool.size)
  const [reason, setReason] = useState('')
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const mutation = useMutation({
    mutationFn: () => apiFetch<Operation>(`/api/v1/fleet/node-pools/${encodeURIComponent(name)}/plan`, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({ desired, size, strategy:size !== pool.size ? 'blueGreen' : undefined, reason }) }),
    onSuccess: (operation) => { queryClient.invalidateQueries({ queryKey:['fleet','plans'] }); toast({ kind:'success', title:'Capacity plan recorded', description:operation.id }); onDone() },
    onError: (error) => toast({ kind:'error', title:'Capacity plan rejected', description:error instanceof Error ? error.message : name }),
  })
  const submit = (event: FormEvent) => { event.preventDefault(); mutation.mutate() }
  return (
    <form className="fleet-plan-form" onSubmit={submit}>
      <label>Desired<input type="number" min={pool.min} max={pool.max} value={desired} onChange={(event) => setDesired(Number(event.target.value))} /></label>
      <label>VM size<input value={size} onChange={(event) => setSize(event.target.value)} /></label>
      <label className="fleet-reason">Reason<input value={reason} maxLength={1000} placeholder="Why this capacity change?" onChange={(event) => setReason(event.target.value)} /></label>
      <button className="filter-btn active" type="submit" disabled={mutation.isPending}>{mutation.isPending ? 'Recording…' : 'Record plan'}</button>
    </form>
  )
}

function PlanRows({ plans, loading }: { plans: Operation[]; loading: boolean }) {
  if (loading) return <div className="panel-skeleton"><span /><span /></div>
  if (plans.length === 0) return <EmptyState icon="·" title="No capacity plans" hint="Choose a node pool to create a planning-only receipt." />
  return <div className="compact-list">{plans.map((plan) => <div className="compact-row" key={plan.id}><StatusChip tone="success" label={plan.status ?? 'recorded'} /><strong>{String(plan.payload?.pool ?? 'pool')}</strong><span>{String(plan.payload?.action ?? 'reconcile')}</span><small>{plan.message}</small><code>{shortDigest(String(plan.metadata?.planDigest ?? ''))}</code></div>)}</div>
}

function shortDigest(value?: string) { return value ? `${value.slice(0, 18)}…` : 'unversioned' }
