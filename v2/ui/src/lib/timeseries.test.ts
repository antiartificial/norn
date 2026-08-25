import { describe, expect, it } from 'vitest'
import { bucketByTime, markerPositions } from './timeseries.ts'

const now = '2026-01-01T01:00:00.000Z'
const minute = 60_000

describe('bucketByTime', () => {
  it('returns empty buckets for empty input', () => {
    expect(bucketByTime([], { getTime: item => item, windowMs: 4 * minute, bucketCount: 4, now })).toEqual([0, 0, 0, 0])
  })

  it('ignores items outside the window', () => {
    const items = [
      '2026-01-01T00:55:59.999Z',
      '2026-01-01T00:56:00.000Z',
      '2026-01-01T01:00:00.001Z',
    ]
    expect(bucketByTime(items, { getTime: item => item, windowMs: 4 * minute, bucketCount: 4, now })).toEqual([1, 0, 0, 0])
  })

  it('places boundary timestamps in deterministic buckets', () => {
    const items = [
      '2026-01-01T00:56:00.000Z',
      '2026-01-01T00:57:00.000Z',
      '2026-01-01T00:59:59.999Z',
      '2026-01-01T01:00:00.000Z',
    ]
    expect(bucketByTime(items, { getTime: item => item, windowMs: 4 * minute, bucketCount: 4, now })).toEqual([1, 1, 0, 2])
  })

  it('returns no buckets for invalid bucket options', () => {
    expect(bucketByTime(['2026-01-01T00:56:00.000Z'], { getTime: item => item, windowMs: 0, bucketCount: 4, now })).toEqual([])
    expect(bucketByTime(['2026-01-01T00:56:00.000Z'], { getTime: item => item, windowMs: 4 * minute, bucketCount: 0, now })).toEqual([])
  })
})

describe('markerPositions', () => {
  it('returns fractional x positions for times inside the window', () => {
    expect(markerPositions([
      '2026-01-01T00:56:00.000Z',
      '2026-01-01T00:58:00.000Z',
      '2026-01-01T01:00:00.000Z',
    ], { windowMs: 4 * minute, now })).toEqual([0, 0.5, 1])
  })

  it('filters markers outside the window', () => {
    expect(markerPositions([
      '2026-01-01T00:55:59.999Z',
      '2026-01-01T00:57:00.000Z',
      '2026-01-01T01:00:00.001Z',
    ], { windowMs: 4 * minute, now })).toEqual([0.25])
  })
})
