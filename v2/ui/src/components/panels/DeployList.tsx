import { NavLink } from 'react-router-dom'
import { deploymentStatus, relativeTime, statusTone } from '../../lib/format.ts'
import type { Deployment } from '../../types/index.ts'
import { EmptyState, StatusChip } from '../ui/index.ts'

export function DeployList({ items }: { items: Deployment[] }) {
  if (items.length === 0) return <EmptyState icon="↑" title="No deploys yet" hint="Recent deployments will appear here." />
  return (
    <div className="compact-list">
      {items.map((deploy) => (
        <NavLink className="compact-row" key={deploy.id} to="/deploys">
          <StatusChip tone={statusTone(deploy.status)} label={deploymentStatus(deploy.status)} />
          <span>{deploy.app}</span>
          <code>{deploy.commitSha?.slice(0, 7)}</code>
          <small>{relativeTime(deploy.startedAt)}</small>
        </NavLink>
      ))}
    </div>
  )
}
