import type { HealthCheck } from '../types/index.ts'

const SLOTS = 12

interface Props {
  checks: HealthCheck[]
  onClick?: () => void
}

export function Sparkline({ checks, onClick }: Props) {
  // Bucket all checks into SLOTS segments for a cleaner summary
  const buckets: Array<'empty' | 'up' | 'down' | 'mixed'> = []

  if (checks.length === 0) {
    for (let i = 0; i < SLOTS; i++) buckets.push('empty')
  } else {
    const bucketSize = Math.max(1, Math.ceil(checks.length / SLOTS))
    for (let i = 0; i < SLOTS; i++) {
      const start = i * bucketSize
      const slice = checks.slice(start, start + bucketSize)
      if (slice.length === 0) {
        buckets.push('empty')
      } else {
        const healthy = slice.filter(c => c.healthy).length
        if (healthy === slice.length) buckets.push('up')
        else if (healthy === 0) buckets.push('down')
        else buckets.push('mixed')
      }
    }
  }

  return (
    <div className="sparkline-strip" onClick={onClick}>
      {buckets.map((status, i) => (
        <span key={i} className={`sparkline-seg ${status}`} />
      ))}
    </div>
  )
}
