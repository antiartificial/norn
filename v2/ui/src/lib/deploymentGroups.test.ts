import { describe, expect, it } from 'vitest'
import { groupConsecutiveDeployments } from './deploymentGroups.ts'
import type { Deployment } from '../types/index.ts'

function deploy(id: string, app: string): Deployment {
  return {
    id,
    app,
    commitSha: `${id}abcdef`,
    imageTag: id,
    sagaId: `saga-${id}`,
    status: 'deployed',
    startedAt: '2026-01-01T00:00:00.000Z',
    finishedAt: '2026-01-01T00:00:10.000Z',
  }
}

describe('groupConsecutiveDeployments', () => {
  it('collapses only successive deployments for the same app', () => {
    const groups = groupConsecutiveDeployments([
      deploy('a1', 'api'),
      deploy('a2', 'api'),
      deploy('w1', 'worker'),
      deploy('a3', 'api'),
      deploy('a4', 'api'),
      deploy('w2', 'worker'),
    ])

    expect(groups).toHaveLength(4)
    expect(groups.map(group => group.latest.id)).toEqual(['a1', 'w1', 'a3', 'w2'])
    expect(groups.map(group => group.earlier.map(deployment => deployment.id))).toEqual([['a2'], [], ['a4'], []])
  })
})
