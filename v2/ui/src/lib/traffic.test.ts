import { describe, expect, it } from 'vitest'
import { summarizeAccessEvents } from './traffic.ts'
import type { AccessEvent } from '../types/index.ts'

function event(status: number, durationMs: number, timestamp: string): AccessEvent {
  return { timestamp, method: 'GET', path: '/api/apps', status, durationMs }
}

describe('summarizeAccessEvents', () => {
  it('summarizes an empty buffer', () => {
    expect(summarizeAccessEvents([], '2026-01-01T01:00:00.000Z')).toEqual({
      total: 0,
      clientErrors: 0,
      serverErrors: 0,
      p95DurationMs: 0,
      spanMs: 0,
      label: 'last 500 requests · no events yet',
    })
  })

  it('counts all-2xx traffic without errors', () => {
    const summary = summarizeAccessEvents([
      event(200, 10, '2026-01-01T00:59:00.000Z'),
      event(204, 20, '2026-01-01T01:00:00.000Z'),
    ], '2026-01-01T01:00:00.000Z')

    expect(summary.total).toBe(2)
    expect(summary.clientErrors).toBe(0)
    expect(summary.serverErrors).toBe(0)
    expect(summary.spanMs).toBe(60_000)
  })

  it('computes nearest-rank p95 and error counts', () => {
    const events = Array.from({ length: 20 }, (_, index) => event(index === 18 ? 404 : index === 19 ? 503 : 200, index + 1, `2026-01-01T00:${String(40 + index).padStart(2, '0')}:00.000Z`))
    const summary = summarizeAccessEvents(events, '2026-01-01T01:00:00.000Z')

    expect(summary.p95DurationMs).toBe(19)
    expect(summary.clientErrors).toBe(1)
    expect(summary.serverErrors).toBe(1)
  })
})
