import { describe, expect, it } from 'vitest'
import { buildFleetExecutionSteps, currentFleetStep, destructiveReconciliationPhases, operationForPlan, reconciliationPhases } from './fleetExecution.ts'
import type { FleetRunnerAttempt, Operation } from '../types/index.ts'

const plan: Operation = { id: 'plan-1', kind: 'fleet.capacity-plan', status: 'succeeded', message: 'plan recorded' }

describe('fleet execution evidence', () => {
  it('keeps a dispatched but unproven phase pending', () => {
    const steps = buildFleetExecutionSteps({
      plan,
      reconciliations: [],
      pullRequest: operation('review', 'fleet.github.pull-request', 'succeeded'),
      dispatch: operation('dispatch', 'fleet.github.apply-dispatch', 'succeeded'),
    })

    expect(steps.find((step) => step.id === 'infrastructure_applied')?.state).toBe('pending')
    expect(steps.some((step) => step.state === 'active')).toBe(false)
    expect(currentFleetStep(steps)?.id).toBe('infrastructure_applied')
  })

  it('starts destructive plans with prechange proof before provider application', () => {
    const destructivePlan = operation('replace-plan', 'fleet.capacity-plan', 'succeeded', {
      action: 'replace', current: { desired: 2 }, proposed: { desired: 2 },
    })
    const steps = buildFleetExecutionSteps({
      plan: destructivePlan,
      reconciliations: [],
      pullRequest: operation('review', 'fleet.github.pull-request', 'succeeded'),
      dispatch: operation('dispatch', 'fleet.github.apply-dispatch', 'succeeded'),
    })

    expect(currentFleetStep(steps)?.id).toBe('prechange_verified')
    expect(destructiveReconciliationPhases).toEqual([
      'prechange_verified', 'provider_applying', 'infrastructure_applied', 'inventory_generated',
      'nodes_configured', 'nodes_enrolled', 'readiness_verified', 'old_nodes_drained', 'complete',
    ])
  })

  it('keeps drain out of non-destructive phase progression', () => {
    expect(reconciliationPhases).toEqual([
      'infrastructure_applied', 'inventory_generated', 'nodes_configured', 'nodes_enrolled', 'readiness_verified', 'complete',
    ])
    expect(reconciliationPhases).not.toContain('old_nodes_drained')
  })

  it('shows the latest failure and blocks downstream phases', () => {
    const steps = buildFleetExecutionSteps({
      plan,
      pullRequest: operation('review', 'fleet.github.pull-request', 'succeeded'),
      dispatch: operation('dispatch', 'fleet.github.apply-dispatch', 'succeeded'),
      reconciliations: [
        checkpoint('infra', 'infrastructure_applied', 'succeeded'),
        checkpoint('inventory', 'inventory_generated', 'succeeded'),
        checkpoint('configure', 'nodes_configured', 'failed'),
      ],
    })

    expect(currentFleetStep(steps)?.id).toBe('nodes_configured')
    expect(steps.find((step) => step.id === 'nodes_enrolled')?.state).toBe('blocked')
    expect(steps.find((step) => step.id === 'complete')?.state).toBe('blocked')
  })

  it('matches GitHub receipts to their plan without relying on list order', () => {
    const match = operation('match', 'fleet.github.pull-request', 'succeeded', { planId: 'plan-1' })
    expect(operationForPlan([operation('other', 'fleet.github.pull-request', 'succeeded', { planId: 'plan-2' }), match], 'plan-1')).toBe(match)
  })

  it('marks only a durably heartbeating runner phase active', () => {
    const steps = buildFleetExecutionSteps({
      plan,
      reconciliations: [],
      runnerAttempt: attempt('running', 'nodes_configured'),
    })

    expect(steps.find((step) => step.id === 'nodes_configured')?.state).toBe('active')
    expect(steps.filter((step) => step.state === 'active')).toHaveLength(1)
  })

  it('surfaces abandoned runner state and blocks later phases', () => {
    const steps = buildFleetExecutionSteps({
      plan,
      reconciliations: [checkpoint('infra', 'infrastructure_applied', 'succeeded')],
      runnerAttempt: attempt('abandoned', 'inventory_generated'),
    })

    expect(currentFleetStep(steps)?.id).toBe('inventory_generated')
    expect(steps.find((step) => step.id === 'inventory_generated')?.message).toContain('stopped heartbeating')
    expect(steps.find((step) => step.id === 'nodes_configured')?.state).toBe('blocked')
  })

  it('keeps a proven phase active until the runner advances it', () => {
    const steps = buildFleetExecutionSteps({
      plan,
      reconciliations: [checkpoint('configured', 'nodes_configured', 'succeeded')],
      runnerAttempt: attempt('running', 'nodes_configured'),
    })

    expect(steps.find((step) => step.id === 'nodes_configured')?.state).toBe('active')
    expect(steps.find((step) => step.id === 'nodes_configured')?.message).toContain('ready for an evidence-gated advance')
  })
})

function operation(id: string, kind: string, status: string, payload: Record<string, unknown> = {}): Operation {
  return { id, kind, status, payload }
}

function checkpoint(id: string, phase: string, status: string): Operation {
  return { id, kind: 'fleet.reconciliation', status, updatedAt: `2026-08-26T00:00:0${id.length}Z`, payload: { phase } }
}

function attempt(status: FleetRunnerAttempt['status'], currentPhase: string): FleetRunnerAttempt {
  return {
    schemaVersion: 'norn.fleet-runner-attempt/v1', id: 'attempt-1', planId: 'plan-1', attempt: 1, rootAttemptId: 'attempt-1', sourceDispatchRunId: 91, pilotRunId: '',
    status, currentPhase, commitSha: 'a'.repeat(40), planSha256: 'b'.repeat(64), heartbeatSequence: 2,
    heartbeatTimeoutSeconds: 120, revision: 3, startedAt: '2026-08-26T00:00:00Z',
    heartbeatAt: '2026-08-26T00:01:00Z', heartbeatExpiresAt: '2026-08-26T00:03:00Z', updatedAt: '2026-08-26T00:01:00Z',
  }
}
