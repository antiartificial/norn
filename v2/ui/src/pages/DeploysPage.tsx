import { DeployHistory } from '../components/DeployHistory.tsx'
import { StatusChip } from '../components/ui/index.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'

export function DeploysPage() {
  const { apps, deployState } = useRuntimeContext()
  return (
    <div className="deploys-page">
      {deployState && <div className="live-deploy-row"><StatusChip tone="info" label="live" />{deployState.operation} {deployState.appId}</div>}
      <DeployHistory apps={apps.map((app) => app.spec.name)} onClose={() => undefined} />
    </div>
  )
}
