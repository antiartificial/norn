export type SparklineTone = 'neutral' | 'ok' | 'warn' | 'danger'

export function Sparkline({
  series,
  markers = [],
  width = 180,
  height = 42,
  tone = 'neutral',
  'aria-label': ariaLabel,
}: {
  series: number[]
  markers?: number[]
  width?: number
  height?: number
  tone?: SparklineTone
  'aria-label': string
}) {
  const safeSeries = series.length > 0 ? series : [0]
  const max = Math.max(...safeSeries, 0)
  const innerWidth = Math.max(1, width - 2)
  const innerHeight = Math.max(1, height - 2)
  const points = safeSeries.map((value, index) => {
    const x = 1 + (safeSeries.length === 1 ? innerWidth / 2 : (index / (safeSeries.length - 1)) * innerWidth)
    const y = 1 + innerHeight - (max > 0 ? (value / max) * innerHeight : 0)
    return { x, y }
  })
  const linePath = points.map((point, index) => `${index === 0 ? 'M' : 'L'} ${point.x.toFixed(2)} ${point.y.toFixed(2)}`).join(' ')
  const areaPath = `${linePath} L ${points.at(-1)?.x.toFixed(2) ?? 1} ${height - 1} L ${points[0]?.x.toFixed(2) ?? 1} ${height - 1} Z`

  return (
    <svg className={`sparkline sparkline-${tone}`} width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={ariaLabel} preserveAspectRatio="none">
      <path className="sparkline-area" d={areaPath} />
      {markers.filter(marker => marker >= 0 && marker <= 1).map((marker, index) => {
        const x = 1 + marker * innerWidth
        return <line className="sparkline-marker" key={`${marker}-${index}`} x1={x} x2={x} y1="1" y2={height - 1} />
      })}
      <path className="sparkline-line" d={linePath} />
    </svg>
  )
}
