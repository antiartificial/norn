import type { FleetRunnerAttempt, Operation } from '../types/index.ts'

export const reconciliationPhases = [
  'infrastructure_applied',
  'inventory_generated',
  'nodes_configured',
  'nodes_enrolled',
  'readiness_verified',
  'complete',
] as const

export const destructiveReconciliationPhases = [
  'prechange_verified',
  'provider_applying',
  'infrastructure_applied',
  'inventory_generated',
  'nodes_configured',
  'nodes_enrolled',
  'readiness_verified',
  'old_nodes_drained',
  'complete',
] as const

export type ReconciliationPhase = typeof destructiveReconciliationPhases[number]
export type FleetStepState = 'completed' | 'pending' | 'active' | 'failed' | 'blocked'

export interface FleetExecutionStep {
  id: 'plan_recorded' | 'review_opened' | 'apply_dispatched' | ReconciliationPhase
  label: string
  state: FleetStepState
  message: string
  operation?: Operation
  runnerAttempt?: FleetRunnerAttempt
}

interface FleetExecutionEvidence {
  plan: Operation
  reconciliations: Operation[]
  pullRequest?: Operation
  dispatch?: Operation
  runnerAttempt?: FleetRunnerAttempt
}

export function operationForPlan(operations: Operation[] | undefined, planID: string): Operation | undefined {
  return (operations ?? []).find((operation) => operation.ref === planID || String(operation.payload?.planId ?? '') === planID)
}

export function buildFleetExecutionSteps({ plan, reconciliations, pullRequest, dispatch, runnerAttempt }: FleetExecutionEvidence): FleetExecutionStep[] {
  const steps: FleetExecutionStep[] = [
    {
      id: 'plan_recorded',
      label: 'Capacity plan recorded',
      state: plan.status === 'failed' ? 'failed' : 'completed',
      message: plan.message || 'Norn retained the requested capacity intent.',
      operation: plan,
    },
    {
      id: 'review_opened',
      label: 'Infrastructure review opened',
      state: pullRequest ? operationState(pullRequest) : 'pending',
      message: pullRequest?.message || 'Create or recover the source-bound norn-fleet pull request.',
      operation: pullRequest,
    },
    {
      id: 'apply_dispatched',
      label: 'Protected apply dispatched',
      state: dispatch ? operationState(dispatch) : 'pending',
      message: dispatch?.message || 'Dispatch remains gated on the reviewed, merged plan artifact.',
      operation: dispatch,
    },
  ]

  const phases = fleetPlanRequiresDestructivePhases(plan) ? destructiveReconciliationPhases : reconciliationPhases
  const latest = latestCheckpointByPhase(reconciliations, phases)
  const runnerFailed = runnerAttempt?.status === 'failed' || runnerAttempt?.status === 'abandoned' || runnerAttempt?.status === 'canceled'
  const runnerActive = runnerAttempt?.status === 'queued' || runnerAttempt?.status === 'running'
  const failedIndex = phases.findIndex((phase) => latest.get(phase)?.status === 'failed' || (runnerFailed && runnerAttempt?.currentPhase === phase))
  for (const [index, phase] of phases.entries()) {
    const operation = latest.get(phase)
    let state: FleetStepState
    if (runnerActive && runnerAttempt?.currentPhase === phase) state = 'active'
    else if (runnerFailed && runnerAttempt?.currentPhase === phase) state = 'failed'
    else if (operation?.status === 'succeeded') state = 'completed'
    else if (operation?.status === 'failed') state = 'failed'
    else if (failedIndex >= 0 && index > failedIndex) state = 'blocked'
    else state = 'pending'

    steps.push({
      id: phase,
      label: humanize(phase),
      state,
      message: runnerAttempt?.currentPhase === phase ? runnerAttemptMessage(runnerAttempt, operation) : operation?.message || (state === 'pending' && dispatch?.status === 'succeeded'
        ? 'The protected apply was dispatched, but Norn has not received proof for this phase.'
        : stepMessage(state)),
      operation,
      runnerAttempt: runnerAttempt?.currentPhase === phase ? runnerAttempt : undefined,
    })
  }

  return steps
}

function runnerAttemptMessage(attempt: FleetRunnerAttempt, checkpoint?: Operation): string {
  if (attempt.status === 'abandoned') return `Attempt ${attempt.attempt} stopped heartbeating and was durably marked abandoned.`
  if (attempt.status === 'failed') return attempt.lastError || `Attempt ${attempt.attempt} failed in this phase.`
  if (attempt.status === 'canceled') return attempt.lastError || `Attempt ${attempt.attempt} was canceled.`
  if (checkpoint?.status === 'succeeded') return `Attempt ${attempt.attempt} recorded proof and is ready for an evidence-gated advance.`
  return `Attempt ${attempt.attempt} is live; heartbeat ${new Date(attempt.heartbeatAt).toLocaleString()}.`
}

export function currentFleetStep(steps: FleetExecutionStep[]): FleetExecutionStep | undefined {
  return steps.find((step) => step.state === 'failed') ?? steps.find((step) => step.state === 'active') ?? steps.find((step) => step.state === 'pending')
}

export function planIsComplete(steps: FleetExecutionStep[]): boolean {
  return steps.some((step) => step.id === 'complete' && step.state === 'completed')
}

function latestCheckpointByPhase(operations: Operation[], phases: readonly ReconciliationPhase[]): Map<ReconciliationPhase, Operation> {
  const sorted = [...operations].sort((a, b) => timestamp(b) - timestamp(a))
  const result = new Map<ReconciliationPhase, Operation>()
  for (const operation of sorted) {
    const phase = String(operation.payload?.phase ?? '') as ReconciliationPhase
    if (phases.includes(phase) && !result.has(phase)) result.set(phase, operation)
  }
  return result
}

function fleetPlanRequiresDestructivePhases(plan: Operation): boolean {
  if (plan.payload?.action === 'replace') return true
  if (plan.payload?.action !== 'scale') return false
  const current = Number((plan.payload.current as Record<string, unknown> | undefined)?.desired)
  const proposed = Number((plan.payload.proposed as Record<string, unknown> | undefined)?.desired)
  return Number.isFinite(current) && Number.isFinite(proposed) && proposed < current
}

function timestamp(operation: Operation): number {
  return new Date(operation.updatedAt ?? operation.finishedAt ?? operation.startedAt ?? operation.createdAt ?? 0).getTime()
}

function operationState(operation: Operation): FleetStepState {
  if (operation.status === 'failed' || operation.status === 'canceled') return 'failed'
  if (operation.status === 'queued' || operation.status === 'running') return 'active'
  return 'completed'
}

function stepMessage(state: FleetStepState): string {
  if (state === 'active') return 'This operation is durably queued or running.'
  if (state === 'blocked') return 'Blocked until the failed checkpoint is resolved and new evidence is recorded.'
  return 'No durable evidence has been recorded for this step yet.'
}

export function humanize(value: string): string {
  return value.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
}
