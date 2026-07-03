export type StatusTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral'

export function StatusDot({ tone = 'neutral', label }: { tone?: StatusTone; label: string }) {
  return (
    <span className="ui-status-dot-wrap">
      <span className={`ui-status-dot ui-status-${tone}`} aria-hidden="true" />
      <span>{label}</span>
    </span>
  )
}

export function StatusChip({ tone = 'neutral', label }: { tone?: StatusTone; label: string }) {
  return (
    <span className={`ui-status-chip ui-status-${tone}`}>
      <span className={`ui-status-dot ui-status-${tone}`} aria-hidden="true" />
      {label}
    </span>
  )
}
