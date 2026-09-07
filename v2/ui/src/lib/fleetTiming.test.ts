import { describe, expect, it } from 'vitest'
import { fleetTimingView, formatDuration, formatRange } from './fleetTiming.ts'
import type { FleetRunnerAttempt, FleetRunnerTiming } from '../types/index.ts'

describe('fleet timing presentation', () => {
  it('waits for reviewed provider-plan classification instead of timing human approval', () => {
    expect(fleetTimingView()).toEqual({
      headline: 'Estimate pending',
      detail: 'Timing starts when the protected runner classifies and begins the reviewed provider plan.',
      accessibleLabel: 'Timing starts when the protected runner classifies and begins the reviewed provider plan.',
      available: false,
    })
  })

  it('shows a rough range, elapsed time, and clamped remaining range', () => {
    const view = fleetTimingView(attempt('running', timing({ lowMs: 600_000, highMs: 1_500_000 })))
    expect(view.headline).toBe('Cold start: roughly 15m–30m')
    expect(view.detail).toBe('14m elapsed · about 10m–25m remaining')
    expect(view.accessibleLabel).toContain('15m to 30m')
  })

  it('does not promise remaining time for a failed retry', () => {
    const value = attempt('failed', timing({ lowMs: 0, highMs: 0 }))
    value.attempt = 2
    const view = fleetTimingView(value)
    expect(view.headline).toBe('Timing stopped after 14m')
    expect(view.detail).toContain('no remaining estimate')
    expect(view.accessibleLabel).toContain('Attempt 2')
  })

  it('keeps elapsed evidence when an estimate is unavailable', () => {
    const value = attempt('running')
    value.timing = { ...timing(), availability: 'unavailable', confidence: 'none', estimatedTotal: undefined, estimatedRemaining: undefined }
    expect(fleetTimingView(value).headline).toBe('14m elapsed')
    expect(fleetTimingView(value).available).toBe(false)
  })

  it('formats stable operator durations and ranges', () => {
    expect(formatDuration(90_000)).toBe('1m 30s')
    expect(formatDuration(-1)).toBe('0s')
    expect(formatRange({ lowMs: 900_000, highMs: 1_800_000 })).toBe('15m–30m')
  })
})

function timing(remaining: { lowMs: number; highMs: number } = { lowMs: 600_000, highMs: 1_500_000 }): FleetRunnerTiming {
  return {
    schemaVersion: 'norn.fleet-timing/v1', scope: 'runner_attempt', asOf: '2026-09-06T18:14:00Z', availability: 'available',
    operationClass: 'cold_start', elapsedMs: 840_000, estimatedRemaining: remaining, estimatedTotal: { lowMs: 900_000, highMs: 1_800_000 },
    estimatedCompletion: { earliestAt: '2026-09-06T18:24:00Z', latestAt: '2026-09-06T18:39:00Z' }, confidence: 'low',
    provenance: { method: 'configured_range', configuredRange: { lowMs: 900_000, highMs: 1_800_000 }, sampleCount: 0, successfulSampleCount: 0, exclusions: ['review_approval', 'github_queue'] },
    phases: [{ name: 'nodes_configured', state: 'active', elapsedMs: 60_000, estimatedRemaining: remaining }],
  }
}

function attempt(status: FleetRunnerAttempt['status'], value?: FleetRunnerTiming): FleetRunnerAttempt {
  return {
    schemaVersion: 'norn.fleet-runner-attempt/v1', id: 'attempt-1', planId: 'plan-1', attempt: 1, rootAttemptId: 'attempt-1', sourceDispatchRunId: 91, pilotRunId: '',
    status, currentPhase: 'nodes_configured', commitSha: 'a'.repeat(40), planSha256: 'b'.repeat(64), heartbeatSequence: 2,
    heartbeatTimeoutSeconds: 120, revision: 3, startedAt: '2026-09-06T18:00:00Z', heartbeatAt: '2026-09-06T18:14:00Z', heartbeatExpiresAt: '2026-09-06T18:16:00Z', updatedAt: '2026-09-06T18:14:00Z', timing: value,
  }
}
