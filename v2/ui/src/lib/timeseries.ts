export interface TimeBucketOptions<T> {
  getTime: (item: T) => string | number | Date
  windowMs: number
  bucketCount: number
  now: string | number | Date
}

export interface MarkerOptions {
  windowMs: number
  now: string | number | Date
}

function toMs(value: string | number | Date): number {
  return value instanceof Date ? value.getTime() : new Date(value).getTime()
}

export function bucketByTime<T>(items: T[], { getTime, windowMs, bucketCount, now }: TimeBucketOptions<T>): number[] {
  const buckets = Array.from({ length: bucketCount }, () => 0)
  if (bucketCount <= 0 || windowMs <= 0) return []

  const end = toMs(now)
  const start = end - windowMs
  const bucketMs = windowMs / bucketCount

  for (const item of items) {
    const time = toMs(getTime(item))
    if (!Number.isFinite(time) || time < start || time > end) continue
    const index = Math.min(bucketCount - 1, Math.floor((time - start) / bucketMs))
    buckets[index] += 1
  }

  return buckets
}

export function markerPositions(times: Array<string | number | Date>, { windowMs, now }: MarkerOptions): number[] {
  if (windowMs <= 0) return []

  const end = toMs(now)
  const start = end - windowMs
  return times
    .map(toMs)
    .filter(time => Number.isFinite(time) && time >= start && time <= end)
    .map(time => (time - start) / windowMs)
}
