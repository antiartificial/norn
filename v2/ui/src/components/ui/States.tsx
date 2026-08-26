import type { ReactNode } from 'react'
import { Button } from './Button.tsx'

export function Skeleton({ width = '100%', height = 16, label = 'Loading' }: { width?: number | string; height?: number | string; label?: string }) {
  return <div className="ui-skeleton" style={{ width, height }} role="status" aria-label={label} />
}

export function EmptyState({ icon, title, hint, action }: { icon?: ReactNode; title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="ui-empty">
      {icon && <div className="ui-empty-icon" aria-hidden="true">{icon}</div>}
      <h2 className="ui-empty-title">{title}</h2>
      {hint && <p className="ui-empty-hint">{hint}</p>}
      {action}
    </div>
  )
}

export function ErrorState({ title = 'Something went wrong', message, onRetry }: { title?: string; message: string; onRetry?: () => void }) {
  return (
    <div className="ui-error" role="alert">
      <h2 className="ui-error-title">{title}</h2>
      <p className="ui-error-message">{message}</p>
      {onRetry && <Button variant="secondary" icon="fa-arrow-rotate-right" onClick={onRetry}>Retry</Button>}
    </div>
  )
}
