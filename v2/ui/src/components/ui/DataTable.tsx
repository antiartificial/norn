import type { ReactNode } from 'react'
import { EmptyState, ErrorState, Skeleton } from './States.tsx'

export interface DataTableColumn<T> {
  key: string
  header: ReactNode
  cell: (row: T) => ReactNode
  numeric?: boolean
}

export function DataTable<T>({
  columns,
  rows,
  getRowKey,
  loading = false,
  error,
  emptyTitle = 'No rows',
  emptyHint,
  onRetry,
}: {
  columns: DataTableColumn<T>[]
  rows: T[]
  getRowKey: (row: T) => string
  loading?: boolean
  error?: string | null
  emptyTitle?: string
  emptyHint?: string
  onRetry?: () => void
}) {
  if (loading) return <Skeleton height={96} label="Loading table" />
  if (error) return <ErrorState message={error} onRetry={onRetry} />
  if (rows.length === 0) return <EmptyState title={emptyTitle} hint={emptyHint} />

  return (
    <div className="ui-table-wrap">
      <table className="ui-table">
        <thead>
          <tr>{columns.map((column) => <th key={column.key}>{column.header}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={getRowKey(row)}>
              {columns.map((column) => (
                <td key={column.key} className={column.numeric ? 'ui-table-num' : undefined}>{column.cell(row)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
