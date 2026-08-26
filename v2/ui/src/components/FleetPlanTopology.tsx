import { useMemo, useState, type CSSProperties } from 'react'
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import type { FleetInventory, Operation, AppStatus, ServiceManifest } from '../types/index.ts'
import type { FleetExecutionStep } from '../lib/fleetExecution.ts'
import { currentFleetStep, reconciliationPhases, type ReconciliationPhase } from '../lib/fleetExecution.ts'

type FleetTopologyKind = 'client' | 'ingress' | 'region' | 'pool' | 'workload' | 'allocation' | 'dependency'
type FleetTopologyState = 'healthy' | 'active' | 'planned' | 'failed' | 'unknown'

interface FleetTopologyData extends Record<string, unknown> {
  label: string
  kind: FleetTopologyKind
  state: FleetTopologyState
  detail: string
  meta: string[]
}

export type FleetTopologyNode = Node<FleetTopologyData, 'fleetTopology'>

export interface FleetTopologyGraph {
  nodes: FleetTopologyNode[]
  edges: Edge[]
  summary: string[]
}

interface FleetPlanTopologyProps {
  plan: Operation
  inventory: FleetInventory
  apps: AppStatus[]
  manifest?: ServiceManifest
  steps: FleetExecutionStep[]
}

const kindColor: Record<FleetTopologyKind, string> = {
  client: 'var(--text-3)',
  ingress: 'var(--warn)',
  region: 'var(--info)',
  pool: 'var(--accent)',
  workload: 'var(--ok)',
  allocation: 'var(--info)',
  dependency: 'var(--text-2)',
}

function FleetTopologyCard({ data, selected }: NodeProps<FleetTopologyNode>) {
  return (
    <div className={`fleet-topology-node state-${data.state} ${selected ? 'selected' : ''}`} style={{ '--fleet-node-color': kindColor[data.kind] } as CSSProperties}>
      <Handle type="target" position={Position.Left} className="topology-handle" />
      <div className="fleet-topology-node-heading"><span>{data.kind}</span><i aria-hidden="true" /></div>
      <strong>{data.label}</strong>
      <small>{data.detail}</small>
      {data.meta.length > 0 && <div>{data.meta.slice(0, 2).map((item) => <code key={item}>{item}</code>)}</div>}
      <Handle type="source" position={Position.Right} className="topology-handle" />
    </div>
  )
}

const nodeTypes = { fleetTopology: FleetTopologyCard }

export function FleetPlanTopology({ plan, inventory, apps, manifest, steps }: FleetPlanTopologyProps) {
  const [selectedID, setSelectedID] = useState<string | null>(null)
  const graph = useMemo(() => buildFleetPlanTopology(plan, inventory, apps, manifest, steps), [apps, inventory, manifest, plan, steps])
  const selected = graph.nodes.find((node) => node.id === selectedID)
  return (
    <section className="fleet-topology" aria-labelledby={`fleet-topology-${plan.id}`}>
      <div className="fleet-topology-title">
        <div><span className="eyebrow">Desired + observed</span><h4 id={`fleet-topology-${plan.id}`}>Provisioning topology</h4></div>
        <p>Animated paths identify observed healthy route readiness or a durably running operation.</p>
      </div>
      <div className="fleet-topology-shell">
        <div className="fleet-topology-canvas" aria-label="Interactive fleet provisioning topology">
          <ReactFlow
            nodes={graph.nodes}
            edges={graph.edges}
            nodeTypes={nodeTypes}
            fitView
            fitViewOptions={{ padding: 0.2 }}
            minZoom={0.35}
            maxZoom={1.5}
            nodesDraggable={false}
            nodesFocusable
            edgesFocusable
            onNodeClick={(_, node) => setSelectedID(node.id)}
            onPaneClick={() => setSelectedID(null)}
            proOptions={{ hideAttribution: true }}
          >
            <Background color="var(--topology-grid)" gap={24} />
            <Controls position="bottom-left" showInteractive={false} />
            <MiniMap position="bottom-right" nodeColor={(node) => kindColor[(node.data.kind as FleetTopologyKind) ?? 'dependency']} pannable zoomable />
          </ReactFlow>
        </div>
        <aside className="fleet-topology-inspector" aria-live="polite">
          {selected ? <><span className="eyebrow">{selected.data.kind}</span><h4>{selected.data.label}</h4><p>{selected.data.detail}</p>{selected.data.meta.map((item) => <code key={item}>{item}</code>)}</> : <><span className="eyebrow">Graph key</span><h4>Evidence boundaries</h4><p>Pool and region nodes come from norn-fleet. Workload, allocation, and health details come from Norn runtime observations.</p><p>Cloud VMs are not shown as enrolled nodes because the current fleet contract does not expose provider or enrollment inventory.</p></>}
        </aside>
      </div>
      <ol className="sr-only" aria-label="Fleet topology as a list">{graph.summary.map((item) => <li key={item}>{item}</li>)}</ol>
    </section>
  )
}

export function buildFleetPlanTopology(plan: Operation, inventory: FleetInventory, apps: AppStatus[], manifest: ServiceManifest | undefined, steps: FleetExecutionStep[]): FleetTopologyGraph {
  const nodes: FleetTopologyNode[] = []
  const edges: Edge[] = []
  const summary: string[] = []
  const poolName = String(plan.payload?.pool ?? '')
  const pool = inventory.nodePools[poolName]
  const proposed = recordValue(plan.payload?.proposed)
  const desired = numberValue(proposed.desired) ?? pool?.desired
  const current = currentFleetStep(steps)
  const planActive = current?.state === 'active' && reconciliationPhases.includes(current.id as ReconciliationPhase)
  const placedApps = apps.filter((app) => app.spec.placement?.nodePool === poolName)
  const regions = collectRegions(inventory, placedApps)

  addNode(nodes, 'clients', 'client', 'Callers', 'unknown', 'Public, tailnet, and internal request sources', [])
  summary.push('Callers can enter through declared regional ingress.')
  regions.forEach((region, index) => {
    const y = index * 190
    const regionReady = regionHasPassingService(region, placedApps, manifest, regions)
    addNode(nodes, `ingress-${region}`, 'ingress', `${region} ingress`, regionReady ? 'healthy' : 'planned', 'Regional routing boundary', [], 230, y)
    addNode(nodes, `region-${region}`, 'region', region, planActive ? 'active' : 'planned', 'Norn deployment region', [], 475, y)
    edges.push(graphEdge(`clients-ingress-${region}`, 'clients', `ingress-${region}`, 'var(--warn)', regionReady, 'traffic'))
    edges.push(graphEdge(`ingress-region-${region}`, `ingress-${region}`, `region-${region}`, 'var(--info)', regionReady, 'route'))
    summary.push(regionReady
      ? `${region} has a passing service with an endpoint observed in that region.`
      : `${region} ingress is declared, but region-specific route readiness is not proven by the current observation.`)
  })

  const poolY = Math.max(160, regions.length * 190)
  addNode(nodes, `pool-${poolName}`, 'pool', poolName || 'unresolved pool', planActive ? 'active' : 'planned', `${pool?.size ?? 'unknown size'} · ${desired ?? '?'} desired`, [`range ${pool?.min ?? '?'}–${pool?.max ?? '?'}`], 475, poolY)
  summary.push(`${poolName || 'The unresolved pool'} targets ${desired ?? 'unknown'} nodes.`)

  let workloadIndex = 0
  for (const app of placedApps) {
    for (const processName of Object.keys(app.spec.processes ?? {}).sort()) {
      const processID = `workload-${app.spec.name}-${processName}`
      const y = workloadIndex++ * 150
      const service = manifest?.services.find((entry) => entry.app === app.spec.name && entry.process === processName)
      const passing = service?.status === 'passing' || (app.healthy && service?.status !== 'critical')
      const processRegions = processRegionList(app, processName, regions)
      addNode(nodes, processID, 'workload', `${app.spec.name} / ${processName}`, passing ? 'healthy' : service ? 'failed' : 'unknown', `${service?.type ?? processType(app, processName)} · ${service?.status ?? app.nomadStatus ?? 'not observed'}`, processRegions, 760, y)
      edges.push(graphEdge(`pool-${poolName}-${processID}`, `pool-${poolName}`, processID, 'var(--accent)', planActive, 'placement'))
      for (const region of processRegions) {
        if (regions.includes(region)) {
          const readyInRegion = processReadyInRegion(app, processName, region, service)
          edges.push(graphEdge(`region-${region}-${processID}`, `region-${region}`, processID, 'var(--ok)', readyInRegion, 'serves'))
        }
      }

      const allocations = (app.allocations ?? []).filter((allocation) => allocation.taskGroup === processName && allocation.lifecycle !== 'retained')
      const byRegion = new Map<string, typeof allocations>()
      for (const allocation of allocations) {
        const region = allocation.nodeRegion || 'region unknown'
        byRegion.set(region, [...(byRegion.get(region) ?? []), allocation])
      }
      let allocationOffset = 0
      for (const [region, regionAllocations] of byRegion) {
        const allocationID = `allocation-${app.spec.name}-${processName}-${region}`
        const healthy = regionAllocations.filter((allocation) => allocation.healthy !== false && allocation.status === 'running').length
        addNode(nodes, allocationID, 'allocation', `${healthy}/${regionAllocations.length} allocations`, healthy === regionAllocations.length ? 'healthy' : 'failed', region, regionAllocations.map((allocation) => allocation.nodeName || allocation.id).slice(0, 2), 1045, y + allocationOffset)
        edges.push(graphEdge(`runtime-${processID}-${allocationID}`, processID, allocationID, 'var(--info)', healthy > 0, 'runs'))
        summary.push(`${app.spec.name} ${processName} has ${healthy} of ${regionAllocations.length} observed allocations healthy in ${region}.`)
        allocationOffset += 74
      }

      const dependencies = appDependencies(app)
      dependencies.forEach((dependency, dependencyIndex) => {
        const dependencyID = `dependency-${app.spec.name}-${dependency.id}`
        if (!nodes.some((node) => node.id === dependencyID)) addNode(nodes, dependencyID, 'dependency', dependency.label, 'unknown', dependency.detail, [], 1320, y + dependencyIndex * 76)
        edges.push(graphEdge(`dependency-${processID}-${dependency.id}`, processID, dependencyID, 'var(--text-2)', false, 'depends'))
      })
    }
  }

  if (placedApps.length === 0) summary.push(`No discovered application currently declares placement.nodePool: ${poolName}.`)
  return { nodes, edges, summary }
}

function addNode(nodes: FleetTopologyNode[], id: string, kind: FleetTopologyKind, label: string, state: FleetTopologyState, detail: string, meta: string[], x = 0, y = 80) {
  nodes.push({ id, type: 'fleetTopology', position: { x, y }, data: { label, kind, state, detail, meta }, ariaLabel: `${kind}: ${label}. ${detail}. State ${state}` })
}

function graphEdge(id: string, source: string, target: string, color: string, animated: boolean, label: string): Edge {
  return { id, source, target, label, type: 'smoothstep', animated, markerEnd: { type: MarkerType.ArrowClosed, color }, style: { stroke: color, strokeWidth: animated ? 2.5 : 1.5, strokeDasharray: animated ? '7 5' : undefined }, labelStyle: { fill: 'var(--topology-edge-label)', fontSize: 10 }, labelBgStyle: { fill: 'var(--surface-1)', fillOpacity: 0.9 } }
}

function collectRegions(inventory: FleetInventory, apps: AppStatus[]): string[] {
  const values = new Set<string>()
  if (inventory.document?.cluster.region) values.add(inventory.document.cluster.region)
  for (const app of apps) {
    Object.keys(app.spec.regions ?? {}).forEach((region) => values.add(region))
    app.spec.endpoints?.forEach((endpoint) => endpoint.region && values.add(endpoint.region))
    app.allocations?.forEach((allocation) => allocation.nodeRegion && values.add(allocation.nodeRegion))
  }
  return [...values].sort()
}

function processRegionList(app: AppStatus, processName: string, fallback: string[]): string[] {
  const declared = app.spec.processes?.[processName]?.regions
  if (declared && declared.length > 0) return declared
  const regions = Object.keys(app.spec.regions ?? {})
  return regions.length > 0 ? regions : fallback
}

function regionHasPassingService(region: string, apps: AppStatus[], manifest: ServiceManifest | undefined, fallback: string[]): boolean {
  return apps.some((app) => Object.keys(app.spec.processes ?? {}).some((processName) => {
    if (!processRegionList(app, processName, fallback).includes(region)) return false
    const service = manifest?.services.find((entry) => entry.app === app.spec.name && entry.process === processName)
    return service?.status === 'passing' && service.endpoints?.some((endpoint) => endpoint.region === region) === true
  }))
}

function processReadyInRegion(app: AppStatus, processName: string, region: string, service: ServiceManifest['services'][number] | undefined): boolean {
  const healthyAllocation = app.allocations?.some((allocation) =>
    allocation.taskGroup === processName &&
    allocation.nodeRegion === region &&
    allocation.lifecycle === 'active' &&
    allocation.status === 'running' &&
    allocation.healthy !== false)
  const passingRegionalEndpoint = service?.status === 'passing' && service.endpoints?.some((endpoint) => endpoint.region === region) === true
  return healthyAllocation || passingRegionalEndpoint
}

function appDependencies(app: AppStatus): Array<{ id: string; label: string; detail: string }> {
  const result: Array<{ id: string; label: string; detail: string }> = []
  const infra = app.spec.infrastructure
  if (infra?.postgres) result.push({ id: 'postgres', label: 'PostgreSQL', detail: infra.postgres.database })
  if (infra?.redis) result.push({ id: 'redis', label: 'Redis / Valkey', detail: infra.redis.namespace ?? 'declared cache' })
  if (infra?.kafka) result.push({ id: 'kafka', label: 'Kafka / Redpanda', detail: `${infra.kafka.topics?.length ?? 0} topics` })
  if (infra?.nats) result.push({ id: 'nats', label: 'NATS', detail: `${infra.nats.streams?.length ?? 0} streams` })
  if (infra?.objectStorage) result.push({ id: 'object-storage', label: 'Object storage', detail: `${infra.objectStorage.buckets?.length ?? 0} buckets` })
  for (const service of app.spec.services ?? []) result.push({ id: `service-${service}`, label: service, detail: 'declared service dependency' })
  return result
}

function processType(app: AppStatus, processName: string): string {
  const process = app.spec.processes?.[processName]
  if (process?.schedule) return 'cron'
  if (process?.function) return 'function'
  return process?.port ? 'service' : 'worker'
}

function recordValue(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {} }
function numberValue(value: unknown): number | undefined { return typeof value === 'number' && Number.isFinite(value) ? value : undefined }
