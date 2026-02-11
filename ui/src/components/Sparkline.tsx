import type { HealthCheck } from '../types/index.ts'

interface Props {
  checks: HealthCheck[]
  width?: number
  height?: number
  onClick?: () => void
}

export function Sparkline({ checks, width = 60, height = 20, onClick }: Props) {
  const mid = height / 2
  const spikeUp = height * 0.35
  const spikeDown = height * 0.25

  if (checks.length === 0) {
    return (
      <svg className="sparkline" width={width} height={height} onClick={onClick}>
        <line x1={0} y1={mid} x2={width} y2={mid}
              stroke="var(--text-dim)" strokeWidth={1} opacity={0.3} />
      </svg>
    )
  }

  // Build EKG path segments, colored by health status
  const spacing = checks.length === 1 ? width / 2 : width / (checks.length - 1)
  const segments: { d: string; color: string }[] = []

  for (let i = 0; i < checks.length; i++) {
    const x = checks.length === 1 ? width / 2 : i * spacing
    const prevX = i === 0 ? 0 : (checks.length === 1 ? 0 : (i - 1) * spacing)
    const check = checks[i]
    const color = check.healthy ? 'var(--green)' : 'var(--red)'

    let d = ''
    // Flat line from previous point to just before this spike
    if (i === 0) {
      d += `M 0 ${mid} L ${Math.max(0, x - 3)} ${mid} `
    } else {
      d += `M ${prevX + 3} ${mid} L ${Math.max(prevX + 3, x - 3)} ${mid} `
    }

    // The spike
    if (check.healthy) {
      // QRS-style upward blip
      d += `L ${x - 1} ${mid} L ${x} ${mid - spikeUp} L ${x + 1} ${mid + spikeDown * 0.3} L ${x + 2} ${mid} `
    } else {
      // Downward dip
      d += `L ${x - 1} ${mid} L ${x} ${mid + spikeDown} L ${x + 1} ${mid} `
    }

    // Flat line after last spike to edge
    if (i === checks.length - 1) {
      d += `L ${width} ${mid}`
    }

    segments.push({ d, color })
  }

  return (
    <svg className="sparkline" width={width} height={height} onClick={onClick}>
      {segments.map((seg, i) => (
        <path key={i} d={seg.d} fill="none" stroke={seg.color} strokeWidth={1.5} />
      ))}
    </svg>
  )
}
