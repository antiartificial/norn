// Draft → React Flow nodes/edges for the Fleet Builder canvas.
// Mirrors the macOS layout (NornUI/Features/FleetBuilder/FleetGraph.swift). React Flow draws
// the connectors between node handles, so we only compute positions + typed edges here.

import { MarkerType, type Edge, type Node } from '@xyflow/react'
import { nodeSize } from './fleetCatalog'
import {
  type FleetDraft, engineTitle, edgePresent, replicaResidesInRegionB, validateDraft,
} from './fleetDraft'

export type FleetNodeKind =
  | 'control' | 'app' | 'dbPrimary' | 'dbReplica' | 'dbExtra'
  | 'cache' | 'queue' | 'lb' | 'edgeCloudflare' | 'edgeDNS' | 'spaces'

export interface FleetNodeData extends Record<string, unknown> {
  kind: FleetNodeKind
  title: string
  badge?: string
  meta?: string
  region: number
  invalid: boolean
  serviceId?: string
  extraId?: string
}

/** Data for a region container frame that encapsulates its nodes (rendered behind them). */
export interface FleetRegionData extends Record<string, unknown> {
  kind: 'region'
  region: number
  label: string
}

export type FleetFlowNode = Node<FleetNodeData>
export type FleetRegionNode = Node<FleetRegionData>

export type EdgeKind = 'traffic' | 'write' | 'xwrite' | 'read' | 'xread' | 'repl' | 'raft' | 'fed' | 'orch' | 'cache' | 'queue'

// Marching ants: dashed connectors animate to show flow direction; solid connectors stay static.
export const EDGE_STYLE: Record<EdgeKind, { stroke: string; width: number; dash?: string; animated?: boolean; arrow?: boolean }> = {
  traffic: { stroke: 'var(--accent)', width: 2, arrow: true },
  write: { stroke: 'var(--info)', width: 2, arrow: true },
  xwrite: { stroke: 'var(--info)', width: 1.8, dash: '7 5', animated: true, arrow: true },
  read: { stroke: 'var(--info)', width: 1.6, dash: '5 4', animated: true, arrow: true },
  xread: { stroke: 'var(--info)', width: 1.4, dash: '3 5', animated: true, arrow: true },
  repl: { stroke: 'var(--ok)', width: 1.8, dash: '5 4', animated: true },
  raft: { stroke: 'var(--text-3)', width: 1.4, dash: '4 4', animated: true },
  fed: { stroke: 'var(--text-3)', width: 1.6, dash: '6 4', animated: true },
  orch: { stroke: 'var(--text-3)', width: 1, dash: '1 5', animated: true },
  cache: { stroke: 'var(--ok)', width: 1.5, dash: '2 4', animated: true, arrow: true },
  queue: { stroke: 'var(--warn)', width: 1.5, dash: '2 4', animated: true, arrow: true },
}

export const EDGE_ORDER: EdgeKind[] =
  ['traffic', 'write', 'xwrite', 'read', 'xread', 'repl', 'raft', 'fed', 'orch', 'cache', 'queue']

export const EDGE_LABEL: Record<EdgeKind, string> = {
  traffic: 'Ingress traffic', write: 'DB write', xwrite: 'Cross-region write',
  read: 'DB read', xread: 'Cross-region read', repl: 'Replication / backup',
  raft: 'Raft quorum', fed: 'Region federation', orch: 'Scheduling', cache: 'Cache', queue: 'Queue',
}

function edge(id: string, source: string, target: string, kind: EdgeKind): Edge {
  const s = EDGE_STYLE[kind]
  return {
    id, source, target, type: 'smoothstep', animated: s.animated ?? false, data: { kind },
    markerEnd: s.arrow ? { type: MarkerType.ArrowClosed, color: s.stroke } : undefined,
    style: { stroke: s.stroke, strokeWidth: s.width, strokeDasharray: s.dash },
  }
}

const NW = 168
const NODE_H = 64

/**
 * Bounding-box container nodes (one per region) computed from live node positions, so each frame
 * grows as its nodes are dragged or added. Rendered with `type: 'region'` behind the fleet nodes.
 */
export function regionFrameNodes(nodes: FleetFlowNode[], d: FleetDraft): FleetRegionNode[] {
  const pad = 26, labelGutter = 30
  const frames: FleetRegionNode[] = []
  const w = (n: FleetFlowNode) => n.measured?.width ?? n.width ?? NW
  const h = (n: FleetFlowNode) => n.measured?.height ?? n.height ?? NODE_H
  for (let r = 0; r < d.regions; r++) {
    const rn = nodes.filter(n => n.data?.region === r)
    if (rn.length === 0) continue
    const minX = Math.min(...rn.map(n => n.position.x)) - pad
    const minY = Math.min(...rn.map(n => n.position.y)) - pad - labelGutter
    const maxX = Math.max(...rn.map(n => n.position.x + w(n))) + pad
    const maxY = Math.max(...rn.map(n => n.position.y + h(n))) + pad
    frames.push({
      id: `region-${r}`, type: 'region',
      position: { x: minX, y: minY },
      data: { kind: 'region', region: r, label: `Region ${r === 0 ? 'A' : 'B'} · ${r === 0 ? d.region : d.secondRegion}` },
      width: maxX - minX, height: maxY - minY,
      style: { width: maxX - minX, height: maxY - minY },
      draggable: false, selectable: false, focusable: false, deletable: false, zIndex: -1,
    })
  }
  return frames
}

export function layout(d: FleetDraft): { nodes: FleetFlowNode[]; edges: Edge[] } {
  const twoR = d.regions === 2
  const blocking = new Set(validateDraft(d).filter(f => f.severity === 'error').map(f => f.code))
  const nodes: FleetFlowNode[] = []
  const edges: Edge[] = []

  const bandTop = (r: number) => (twoR ? (r === 0 ? 96 : 430) : edgePresent(d) ? 96 : 44)

  const at = (id: string, fallback: { x: number; y: number }) => d.positions[id] ?? fallback
  const push = (id: string, data: FleetNodeData, x: number, y: number) =>
    nodes.push({ id, type: 'fleet', position: at(id, { x, y }), data })

  if (edgePresent(d)) {
    const cf = d.edge === 'cloudflare'
    push('edge', { kind: cf ? 'edgeCloudflare' : 'edgeDNS', title: cf ? 'Cloudflare' : 'Global DNS', meta: cf ? 'edge / WAF' : 'anycast', region: -1, invalid: false }, 400, 8)
  }

  for (let r = 0; r < d.regions; r++) {
    const top = bandTop(r)
    push(`lb${r}`, { kind: 'lb', title: 'Regional LB', meta: 'Traefik :18080', region: r, invalid: false }, 400, top)
    const count = r === 0 ? d.appA : d.appB
    const gap = Math.min(210, 680 / Math.max(1, count - 1))
    const startX = 360 - (gap * (count - 1)) / 2 - NW / 2
    for (let i = 0; i < count; i++) {
      push(`a${r}_${i}`, { kind: 'app', title: `App ${i + 1}`, meta: d.sizes.app, region: r, invalid: false }, Math.max(16, startX + i * gap), top + 100)
    }
    const cc = r === 0 ? d.controlA : d.controlB
    if (cc > 0) {
      const cx = 820, cy = top + 78, R = 74
      const ctlMeta = nodeSize(d.sizes.control)
      for (let i = 0; i < cc; i++) {
        const a = -Math.PI / 2 + i * ((2 * Math.PI) / cc)
        push(`c${r}_${i}`, {
          kind: 'control', title: `Control ${i + 1}`, meta: ctlMeta ? `${ctlMeta.vcpu}v·${ctlMeta.memGB}g` : undefined,
          region: r, invalid: blocking.has(r === 0 ? 'control-quorum' : 'control-region-b'),
        }, cx + R * Math.cos(a) - NW / 2, cy + R * Math.sin(a) - 30)
      }
    }
  }

  const managed = d.db.mode === 'managed'
  const eng = engineTitle(d.db.engine)
  const dbY = bandTop(0) + 200
  push('db0', { kind: 'dbPrimary', title: eng, badge: managed ? 'Managed' : 'Patroni', meta: managed ? d.db.managedSize : `3× ${d.db.selfSize}`, region: 0, invalid: blocking.has('database') }, 320, dbY)
  push('sp', { kind: 'spaces', title: 'DO Spaces', meta: managed ? 'state' : 'state + WAL', region: 0, invalid: blocking.has('object-storage') }, 60, dbY)

  if (managed && d.db.replica) {
    const inB = replicaResidesInRegionB(d)
    push('dbR', { kind: 'dbReplica', title: eng, badge: 'Read replica', meta: d.db.managedSize, region: inB ? 1 : 0, invalid: false }, inB ? 320 : 560, inB ? bandTop(1) + 200 : dbY)
  }

  d.extras.forEach((ex, i) => {
    push(`ex${i}`, { kind: 'dbExtra', title: engineTitle(ex.engine), badge: 'Test', meta: `${ex.mode} · ${ex.size}`, region: 0, invalid: false, extraId: ex.id }, ex.x || 470 + i * 28, ex.y || dbY + 84)
  })
  d.services.forEach((sv, i) => {
    push(`sv${i}`, { kind: sv.kind === 'cache' ? 'cache' : 'queue', title: sv.kind === 'cache' ? 'Cache' : 'Queue', badge: sv.engine, meta: `${sv.count}× ${sv.size}`, region: 0, invalid: false, serviceId: sv.id }, sv.x || 210 + i * 32, sv.y || (twoR ? 360 : 320))
  })

  // Edges
  const apps = nodes.filter(n => n.data.kind === 'app')
  for (let r = 0; r < d.regions; r++) {
    if (edgePresent(d)) edges.push(edge(`e-edge-lb${r}`, 'edge', `lb${r}`, 'traffic'))
    apps.filter(a => a.data.region === r).forEach(a => edges.push(edge(`e-lb${r}-${a.id}`, `lb${r}`, a.id, 'traffic')))
  }
  const replica = nodes.find(n => n.data.kind === 'dbReplica')
  for (const a of apps) {
    edges.push(edge(`e-${a.id}-db0`, a.id, 'db0', a.data.region === 0 ? 'write' : 'xwrite'))
    if (replica) edges.push(edge(`e-${a.id}-${replica.id}`, a.id, replica.id, replica.data.region === a.data.region ? 'read' : 'xread'))
  }
  if (replica) edges.push(edge(`e-db0-${replica.id}`, 'db0', replica.id, 'repl'))
  if (d.db.mode === 'self') edges.push(edge('e-db0-sp', 'db0', 'sp', 'repl'))

  for (let r = 0; r < d.regions; r++) {
    const ctl = nodes.filter(n => n.data.kind === 'control' && n.data.region === r)
    for (let i = 0; i < ctl.length; i++) {
      for (let j = i + 1; j < ctl.length; j++) edges.push(edge(`e-raft-${ctl[i].id}-${ctl[j].id}`, ctl[i].id, ctl[j].id, 'raft'))
    }
    const head = ctl[0]
    if (head) apps.filter(a => a.data.region === r).forEach(a => edges.push(edge(`e-orch-${head.id}-${a.id}`, head.id, a.id, 'orch')))
  }
  const ctlA = nodes.find(n => n.data.kind === 'control' && n.data.region === 0)
  const ctlB = nodes.find(n => n.data.kind === 'control' && n.data.region === 1)
  if (ctlA && ctlB) edges.push(edge('e-fed', ctlA.id, ctlB.id, 'fed'))

  for (const sv of nodes.filter(n => n.data.kind === 'cache' || n.data.kind === 'queue')) {
    for (const a of apps) edges.push(edge(`e-${a.id}-${sv.id}`, a.id, sv.id, sv.data.kind === 'cache' ? 'cache' : 'queue'))
  }

  return { nodes, edges }
}
