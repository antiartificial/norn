import { describe, expect, it } from 'vitest'
import { buildFleetExecutionSteps, currentFleetStep, operationForPlan } from './fleetExecution.ts'
import type { Operation } from '../types/index.ts'

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
})

function operation(id: string, kind: string, status: string, payload: Record<string, unknown> = {}): Operation {
  return { id, kind, status, payload }
}

function checkpoint(id: string, phase: string, status: string): Operation {
  return { id, kind: 'fleet.reconciliation', status, updatedAt: `2026-08-26T00:00:0${id.length}Z`, payload: { phase } }
}
