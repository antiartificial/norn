import { describe, expect, it } from 'vitest'
import { collapseActivity, formatActivity } from './activity.ts'
import type { ActivityEntry } from '../runtime/AppRuntime.tsx'

function entry(id: number, type: string, appId?: string, payload: Record<string, unknown> = {}): ActivityEntry {
  return {
    id,
    event: { type, appId, payload },
    capturedAt: '2026-01-01T00:00:00.000Z',
  }
}

describe('activity formatting', () => {
  it('formats operational events into readable sentences and links', () => {
    expect(formatActivity(entry(1, 'deploy.step', 'api', { step: 'build', status: 'running' }))).toMatchObject({
      sentence: 'Deploy build running - api',
      href: '/deploys',
      tone: 'info',
    })
    expect(formatActivity(entry(2, 'beacon.event', undefined, { severity: 'critical', title: 'Database down' }))).toMatchObject({
      sentence: 'critical: Database down',
      href: '/incidents',
      tone: 'danger',
    })
    expect(formatActivity(entry(3, 'app.scaled', 'worker'))).toMatchObject({
      sentence: 'App scaled - worker',
      href: '/apps/worker/overview',
    })
  })

  it('collapses consecutive identical sentences', () => {
    const rows = collapseActivity([
      entry(1, 'deploy.succeeded', 'api'),
      entry(2, 'deploy.succeeded', 'api'),
      entry(3, 'deploy.succeeded', 'worker'),
    ])

    expect(rows.map(row => [row.sentence, row.count])).toEqual([
      ['Deploy succeeded - api', 2],
      ['Deploy succeeded - worker', 1],
    ])
  })
})
