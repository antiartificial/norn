import { useState, useEffect, useRef } from 'react'
import type { HealthCheck, AlertConfig } from '../types/index.ts'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface Props {
  appId: string
  checks: HealthCheck[]
  alerts?: AlertConfig
  onClose: () => void
}

const ranges = ['15m', '1h', '6h', '24h'] as const

function parseDuration(s: string): number {
  const match = s.match(/^(\d+)(m|h|s)$/)
  if (!match) return 5 * 60 * 1000
  const n = parseInt(match[1])
  switch (match[2]) {
    case 's': return n * 1000
    case 'm': return n * 60 * 1000
    case 'h': return n * 60 * 60 * 1000
    default: return n * 60 * 1000
  }
}

function computeAlertWindows(
  checks: HealthCheck[],
  windowMs: number,
  threshold: number
): { start: number; end: number }[] {
  if (checks.length === 0) return []

  const alertPoints: number[] = []
  for (let i = 0; i < checks.length; i++) {
    const t = new Date(checks[i].checkedAt).getTime()
    let failures = 0
    for (let j = i; j >= 0; j--) {
      const tj = new Date(checks[j].checkedAt).getTime()
      if (t - tj > windowMs) break
      if (!checks[j].healthy) failures++
    }
    if (failures >= threshold) {
      alertPoints.push(i)
    }
  }

  // Merge adjacent points into windows
  const windows: { start: number; end: number }[] = []
  for (const idx of alertPoints) {
    if (windows.length > 0 && idx - windows[windows.length - 1].end <= 1) {
      windows[windows.length - 1].end = idx
    } else {
      windows.push({ start: idx, end: idx })
    }
  }
  return windows
}

export function HealthPanel({ appId, checks: initialChecks, alerts, onClose }: Props) {
  const [range_, setRange] = useState<string>('15m')
  const scrollRef = useRef<HTMLDivElement>(null)
  const [checks, setChecks] = useState<HealthCheck[]>(initialChecks)
  const [loading, setLoading] = useState(false)
  const [containerWidth, setContainerWidth] = useState(0)

  useEffect(() => {
    setLoading(true)
    fetch(apiUrl(`/api/apps/${appId}/health-checks?range=${range_}`), fetchOpts)
      .then(r => r.json())
      .then(data => {
        setChecks(data.checks ?? [])
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }, [appId, range_])

  // Measure container width
  useEffect(() => {
    if (!scrollRef.current) return
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setContainerWidth(entry.contentRect.width)
      }
    })
    observer.observe(scrollRef.current)
    return () => observer.disconnect()
  }, [])

  // Auto-scroll to latest checks
  useEffect(() => {
    if (scrollRef.current && checks.length > 0) {
      scrollRef.current.scrollLeft = scrollRef.current.scrollWidth
    }
  }, [checks])

  // Stats
  const healthyCount = checks.filter(c => c.healthy).length
  const uptime = checks.length > 0 ? ((healthyCount / checks.length) * 100).toFixed(1) : '--'
  const avgResponse = checks.length > 0
    ? Math.round(checks.reduce((s, c) => s + c.responseMs, 0) / checks.length)
    : 0
  const lastCheck = checks.length > 0
    ? new Date(checks[checks.length - 1].checkedAt).toLocaleTimeString()
    : '--'

  // Alert computation
  const windowMs = alerts ? parseDuration(alerts.window) : 5 * 60 * 1000
  const threshold = alerts?.threshold ?? 3
  const alertWindows = computeAlertWindows(checks, windowMs, threshold)

  // Timeline dimensions — fill container, scroll only if data needs more space
  const svgWidth = Math.max(containerWidth || 700, checks.length * 8)
  const svgHeight = 120
  const plotTop = 10
  const plotBottom = svgHeight - 20
  const plotHeight = plotBottom - plotTop

  const maxResponse = checks.length > 0
    ? Math.max(...checks.map(c => c.responseMs), 1)
    : 1

  return (
    <div className="health-panel">
      <div className="health-panel-header">
        <h4>
          <i className="fawsb fa-heart-pulse" /> {appId}
        </h4>
        <div className="range-selector">
          {ranges.map(r => (
            <button key={r}
                    className={`range-btn ${range_ === r ? 'active' : ''}`}
                    onClick={() => setRange(r)}>
              {r}
            </button>
          ))}
        </div>
        <button className="btn-close" onClick={onClose}>&times;</button>
      </div>

      <div className="health-panel-stats">
        <div className="health-stat">
          <span className="stat-value">{uptime}%</span>
          <span className="stat-label">uptime</span>
        </div>
        <div className="health-stat">
          <span className="stat-value">{avgResponse}ms</span>
          <span className="stat-label">avg response</span>
        </div>
        <div className="health-stat">
          <span className="stat-value">{lastCheck}</span>
          <span className="stat-label">last check</span>
        </div>
        <div className="health-stat">
          <span className="stat-value">{checks.length}</span>
          <span className="stat-label">checks</span>
        </div>
      </div>

      {loading ? (
        <div className="health-timeline-loading">Loading...</div>
      ) : checks.length === 0 ? (
        <div className="health-timeline-empty">No health checks in this range</div>
      ) : (
        <div className="health-timeline-scroll" ref={scrollRef}>
        <svg className="health-timeline" width={svgWidth} height={svgHeight}
             viewBox={`0 0 ${svgWidth} ${svgHeight}`} preserveAspectRatio="none">
          {/* Alert windows */}
          {alertWindows.map((w, i) => {
            const x1 = (w.start / Math.max(checks.length - 1, 1)) * svgWidth
            const x2 = (w.end / Math.max(checks.length - 1, 1)) * svgWidth
            return (
              <rect key={`alert-${i}`}
                    x={x1} y={plotTop}
                    width={Math.max(x2 - x1, 4)} height={plotHeight}
                    fill="var(--red)" opacity={0.12} />
            )
          })}

          {/* Health bars */}
          {checks.map((check, i) => {
            const x = (i / Math.max(checks.length - 1, 1)) * svgWidth
            return (
              <line key={`bar-${i}`}
                    x1={x} y1={plotTop} x2={x} y2={plotBottom}
                    stroke={check.healthy ? 'var(--green)' : 'var(--red)'}
                    strokeWidth={Math.max(1, svgWidth / checks.length * 0.6)}
                    opacity={0.3} />
            )
          })}

          {/* Response time line */}
          {checks.length > 1 && (
            <polyline
              fill="none"
              stroke="var(--blue)"
              strokeWidth={1.5}
              points={checks.map((c, i) => {
                const x = (i / (checks.length - 1)) * svgWidth
                const y = plotBottom - (c.responseMs / maxResponse) * plotHeight
                return `${x},${y}`
              }).join(' ')}
            />
          )}

          {/* Time axis labels */}
          {checks.length > 0 && [0, Math.floor(checks.length / 2), checks.length - 1].map((idx) => {
            if (idx >= checks.length) return null
            const x = (idx / Math.max(checks.length - 1, 1)) * svgWidth
            const time = new Date(checks[idx].checkedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
            return (
              <text key={`time-${idx}`}
                    x={x} y={svgHeight - 2}
                    fill="var(--text-dim)" fontSize={9}
                    textAnchor={idx === 0 ? 'start' : idx === checks.length - 1 ? 'end' : 'middle'}>
                {time}
              </text>
            )
          })}
        </svg>
        </div>
      )}
    </div>
  )
}
