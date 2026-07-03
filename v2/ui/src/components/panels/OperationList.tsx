import { NavLink } from 'react-router-dom'
import { relativeTime, statusTone } from '../../lib/format.ts'
import type { Operation } from '../../types/index.ts'
import { EmptyState, StatusChip } from '../ui/index.ts'

function operationAttempts(op: Operation): string | null {
  const current = op.attempts ?? op.attempt
  if (current === undefined) return null
  return op.maxAttempts === undefined ? String(current) : `${current}/${op.maxAttempts}`
}

function operationTime(op: Operation): string | undefined {
  return op.updatedAt ?? op.startedAt ?? op.createdAt ?? op.finishedAt
}

export function OperationList({ items }: { items: Operation[] }) {
  if (items.length === 0) return <EmptyState icon="·" title="No running operations" hint="Durable operations are idle." />
  return (
    <div className="compact-list">
      {items.map((op, i) => {
        const attempts = operationAttempts(op)
        const time = operationTime(op)
        return (
          <NavLink className="compact-row" key={op.id ?? op.sagaId ?? i} to={`/operations/${op.sagaId ?? op.id ?? ''}`}>
            <StatusChip tone={statusTone(op.status)} label={op.status ?? 'running'} />
            <span>{op.kind ?? 'operation'}</span>
            <small>{op.app}</small>
            {attempts && <small>{attempts}</small>}
            {time && <small>{relativeTime(time)}</small>}
          </NavLink>
        )
      })}
    </div>
  )
}
