import type { ReactNode } from 'react'
import { ErrorState, Skeleton } from '../ui/index.ts'

export function Panel({ title, loading, error, onRetry, children }: { title: string; loading?: boolean; error?: string | null; onRetry?: () => void; children: ReactNode }) {
  return (
    <section className="panel">
      <h2>{title}</h2>
      {loading ? <div className="panel-skeleton"><Skeleton /><Skeleton /><Skeleton /></div> : error ? <ErrorState message={error} onRetry={onRetry} /> : children}
    </section>
  )
}

export function Metric({ label, value, tone }: { label: string; value: number; tone: string }) {
  return <div className={`metric metric-${tone}`}><strong>{value}</strong><span>{label}</span></div>
}
