import { NavLink } from 'react-router-dom'
import { EmptyState, StatusChip } from '../components/ui/index.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'

export function ManagementOnlyPage({ area }: { area: string }) {
  const { authority, environment } = useRuntimeContext()
  return (
    <section className="panel management-only-state" aria-labelledby="management-only-title">
      <div className="section-heading">
        <div><span className="eyebrow">Management-only control plane</span><h2 id="management-only-title">{area} is not served here</h2></div>
        <StatusChip tone="neutral" label={`${authority} · ${environment.id}`} />
      </div>
      <EmptyState icon="◇" title="No workload runtime was requested" hint="This authority manages Fleet records, authenticated operations, and audit evidence. It intentionally does not expose application, release, event, or scheduler APIs." />
      <NavLink className="filter-btn active" to="/fleet">Open Fleet management</NavLink>
    </section>
  )
}
