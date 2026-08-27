import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../../lib/api.ts'
import { deploymentStatus, relativeTime, statusTone } from '../../lib/format.ts'
import type { Deployment, DeploymentStepListResponse } from '../../types/index.ts'
import { EmptyState, Skeleton, StatusChip } from '../ui/index.ts'

export function DeploymentTimelineList({ deployments }: { deployments: Deployment[] }) {
  const [expanded, setExpanded] = useState<string | null>(null)
  if (deployments.length === 0) return <EmptyState icon="↑" title="No deployments" hint="Deploy history for this app is empty." />
  return <div className="deployment-list">{deployments.map((deploy) => <DeploymentRow key={deploy.id} deploy={deploy} expanded={expanded === deploy.id} onToggle={() => setExpanded((cur) => cur === deploy.id ? null : deploy.id)} />)}</div>
}

function DeploymentRow({ deploy, expanded, onToggle }: { deploy: Deployment; expanded: boolean; onToggle: () => void }) {
  const steps = useQuery({ queryKey: ['deployments-v1', deploy.id, 'steps'], queryFn: () => apiFetch<DeploymentStepListResponse>(`/api/v1/deployments/${deploy.id}/steps`), enabled: expanded, staleTime: 60_000 })
  return (
    <div className="deployment-row">
      <button type="button" className="deployment-summary" onClick={onToggle}>
        <StatusChip tone={statusTone(deploy.status)} label={deploymentStatus(deploy.status)} />
        <span>{deploy.app}</span>
        <code>{deploy.commitSha?.slice(0, 7)}</code>
        <small>{relativeTime(deploy.startedAt)}</small>
      </button>
      {expanded && <div className="deployment-steps">{steps.isLoading ? <Skeleton /> : (steps.data?.steps ?? []).map((step, i) => <div key={`${step.step}-${i}`}><StatusChip tone={statusTone(step.status)} label={step.status ?? 'step'} /><span>{step.step ?? step.kind}</span><small>{step.message}</small></div>)}</div>}
    </div>
  )
}
