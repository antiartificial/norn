import { Handle, Position, type NodeProps } from '@xyflow/react'
import type { FleetFlowNode, FleetNodeData, FleetNodeKind, FleetRegionNode, FleetRegionData } from '../../lib/fleetLayout.ts'

const ICON: Record<FleetNodeKind, string> = {
  control: 'fa-microchip',
  app: 'fa-cubes',
  dbPrimary: 'fa-database',
  dbReplica: 'fa-database',
  dbExtra: 'fa-flask',
  cache: 'fa-bolt',
  queue: 'fa-bars-staggered',
  lb: 'fa-code-branch',
  edgeCloudflare: 'fa-cloud',
  edgeDNS: 'fa-globe',
  spaces: 'fa-box-archive',
}

/** Custom React Flow node for the Fleet Builder. Styling via `.fleet-node*` classes on design tokens. */
export function FleetNode({ data, selected }: NodeProps<FleetFlowNode>) {
  const d = data as FleetNodeData
  return (
    <div className={`fleet-node fleet-node-${d.kind}${selected ? ' selected' : ''}${d.invalid ? ' invalid' : ''}`}>
      <Handle type="target" position={Position.Top} className="fleet-handle" />
      <div className="fleet-node-head">
        <span className="fleet-node-icon"><i className={`fawsb ${ICON[d.kind]}`} aria-hidden="true" /></span>
        <span className="fleet-node-title">
          {d.title}
          {d.badge ? <span className="fleet-node-badge">{d.badge}</span> : null}
        </span>
      </div>
      {d.meta ? <div className="fleet-node-meta">{d.meta}</div> : null}
      <Handle type="source" position={Position.Bottom} className="fleet-handle" />
    </div>
  )
}

/** Region container frame — encapsulates its nodes and grows to fit them. Rendered behind nodes. */
export function FleetRegionFrame({ data }: NodeProps<FleetRegionNode>) {
  const d = data as FleetRegionData
  return (
    <div className={`fleet-region fleet-region-${d.region}`}>
      <span className="fleet-region-label"><i className="fawsb fa-vector-square" aria-hidden="true" /> {d.label}</span>
    </div>
  )
}

export const fleetNodeTypes = { fleet: FleetNode, region: FleetRegionFrame }
