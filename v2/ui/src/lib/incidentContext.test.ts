import { describe, expect, it } from 'vitest'
import { correlateDeploysToIncident } from './incidentContext.ts'
import type { Deployment } from '../types/index.ts'

function deploy(id: string, app: string, finishedAt: string): Deployment {
  return { id, app, commitSha: id.padEnd(8, '0'), imageTag: id, sagaId: `saga-${id}`, status: 'deployed', startedAt: finishedAt, finishedAt }
}

describe('correlateDeploysToIncident', () => {
  it('finds nearest deploy before and first deploy during an incident', () => {
    const result = correlateDeploysToIncident([
      deploy('older', 'api', '2026-01-01T00:10:00.000Z'),
      deploy('before', 'api', '2026-01-01T00:50:00.000Z'),
      deploy('during', 'api', '2026-01-01T01:05:00.000Z'),
    ], 'api', '2026-01-01T01:00:00.000Z')

    expect(result.before?.id).toBe('before')
    expect(result.during?.id).toBe('during')
  })

  it('filters unrelated apps', () => {
    expect(correlateDeploysToIncident([
      deploy('other-before', 'worker', '2026-01-01T00:50:00.000Z'),
      deploy('api-before', 'api', '2026-01-01T00:40:00.000Z'),
    ], 'api', '2026-01-01T01:00:00.000Z').before?.id).toBe('api-before')
  })

  it('returns no context when no app deployments exist', () => {
    expect(correlateDeploysToIncident([deploy('worker', 'worker', '2026-01-01T00:50:00.000Z')], 'api', '2026-01-01T01:00:00.000Z')).toEqual({})
  })
})
