// Fleet Builder draft model + pure validation / cost / cluster.yaml logic.
//
// 1:1 port of the macOS client (NornUI/Features/FleetBuilder/FleetDraft.swift). Field names,
// finding codes/severities, cost math, and cluster.yaml output must stay identical across both
// so the two clients (and the future server /api/v1/fleet/plan) agree.

import {
  NODE_SIZES, MANAGED_SIZES, SPACES_REGIONS, SECOND_REGION_DEFAULT,
  LB_MONTHLY, SPACES_MONTHLY, DB_SELF_MIN, nodeSize, managedSize, hasSpaces,
} from './fleetCatalog'

export type FleetDBMode = 'self' | 'managed'
export type FleetDBEngine = 'pg' | 'mysql'
export type FleetEdgeMode = 'none' | 'cloudflare'
export type FleetReplicaRegion = 'same' | 'b'
export type FleetServiceKind = 'cache' | 'queue'
export type FleetSeverity = 'error' | 'warning' | 'info'

export interface FleetFinding {
  severity: FleetSeverity
  code: string
  field: string
  message: string
  remediation?: string
}

export interface FleetServicePool {
  id: string
  kind: FleetServiceKind
  engine: string
  size: string
  count: number
  x: number
  y: number
}

export interface FleetTestDB {
  id: string
  mode: FleetDBMode
  engine: FleetDBEngine
  size: string
  x: number
  y: number
}

export interface FleetDraft {
  name: string
  region: string
  secondRegion: string
  regions: number // 1 | 2
  edge: FleetEdgeMode
  controlA: number
  controlB: number
  appA: number
  appB: number
  db: {
    mode: FleetDBMode
    engine: FleetDBEngine
    replica: boolean
    replicaRegion: FleetReplicaRegion
    managedSize: string
    selfSize: string
  }
  services: FleetServicePool[]
  extras: FleetTestDB[]
  sizes: { control: string; app: string }
  positions: Record<string, { x: number; y: number }>
}

export function defaultDraft(): FleetDraft {
  return {
    name: 'norn-prod',
    region: 'nyc3',
    secondRegion: SECOND_REGION_DEFAULT,
    regions: 1,
    edge: 'none',
    controlA: 3,
    controlB: 0,
    appA: 2,
    appB: 2,
    db: { mode: 'self', engine: 'pg', replica: false, replicaRegion: 'same', managedSize: 'db-s-2vcpu-4gb', selfSize: 'g-4vcpu-16gb' },
    services: [],
    extras: [],
    sizes: { control: 'g-2vcpu-8gb', app: 's-4vcpu-8gb' },
    positions: {},
  }
}

export const totalApps = (d: FleetDraft): number => d.appA + (d.regions === 2 ? d.appB : 0)
export const replicaResidesInRegionB = (d: FleetDraft): boolean => d.regions === 2 && d.db.replicaRegion === 'b'
export const edgePresent = (d: FleetDraft): boolean => d.edge === 'cloudflare' || d.regions === 2
export const engineTitle = (e: FleetDBEngine): string => (e === 'mysql' ? 'MySQL' : 'PostgreSQL')

export function validateDraft(d: FleetDraft): FleetFinding[] {
  const out: FleetFinding[] = []

  const quorumA = d.controlA % 2 === 1 && d.controlA >= 3
  out.push({
    severity: quorumA ? 'info' : 'error',
    code: 'control-quorum',
    field: `control.${d.region}`,
    message: `Control quorum ${d.region}: ${d.controlA} node${d.controlA === 1 ? '' : 's'} — ${quorumA ? 'odd, ≥3' : 'must be odd and ≥3'}`,
    remediation: quorumA ? undefined : 'Use an odd number of at least 3 control nodes.',
  })

  if (d.regions === 2) {
    if (d.controlB === 0) {
      out.push({ severity: 'info', code: 'control-region-b', field: `control.${d.secondRegion}`,
        message: `Region B has no control plane — depends on ${d.region} for scheduling (no independent quorum)` })
    } else if (d.controlB % 2 === 1 && d.controlB >= 3) {
      out.push({ severity: 'info', code: 'control-region-b', field: `control.${d.secondRegion}`,
        message: `Region B: ${d.controlB}-node federated quorum — survives isolation from ${d.region}` })
    } else {
      out.push({ severity: 'error', code: 'control-region-b', field: `control.${d.secondRegion}`,
        message: `Region B control (${d.controlB}) can't hold quorum`,
        remediation: `Use 0 (dependent on ${d.region}) or an odd number ≥3 (federated).` })
    }
  }

  out.push(hasSpaces(d.region)
    ? { severity: 'info', code: 'object-storage', field: 'region', message: `${d.region} has DO Spaces for state + WAL backup` }
    : { severity: 'error', code: 'object-storage', field: 'region', message: `${d.region} has no DO Spaces — state + WAL backup need it`,
        remediation: `Choose a Spaces region: ${[...SPACES_REGIONS].sort().join(', ')}.` })

  if (d.db.mode === 'managed') {
    out.push({ severity: 'info', code: 'database', field: 'db.mode',
      message: `Managed ${engineTitle(d.db.engine)} — provider-run HA${d.db.replica ? ' + read replica' : ''}` })
  } else {
    const size = nodeSize(d.db.selfSize)
    const ok = (size?.vcpu ?? 0) >= DB_SELF_MIN.vcpu && (size?.memGB ?? 0) >= DB_SELF_MIN.memGB
    out.push({ severity: ok ? 'info' : 'error', code: 'database', field: 'db.size',
      message: `Self-managed Postgres (Patroni ×3) in ${d.region} · ${d.db.selfSize}${ok ? '' : ' below minimum'}`,
      remediation: ok ? undefined : `Use at least ${DB_SELF_MIN.vcpu}vcpu / ${DB_SELF_MIN.memGB}gb.` })
  }

  if (d.regions === 2) {
    out.push({ severity: 'warning', code: 'cross-region-write', field: 'db',
      message: `Region B apps write to the ${d.region} primary (added latency)`,
      remediation: replicaResidesInRegionB(d) ? undefined : 'Add a read replica in region B for local reads.' })

    if (d.region === d.secondRegion) {
      out.push({ severity: 'warning', code: 'region-distinct', field: 'regions',
        message: `Region A and Region B are both ${d.region} — a second region should be distinct for HA`,
        remediation: 'Pick a different region for Region B, or set regions to 1.' })
    }
  }

  return out
}

export const hasBlockingFindings = (d: FleetDraft): boolean => validateDraft(d).some(f => f.severity === 'error')

export interface CostLine { label: string; usdMonthly: number }

export function costLines(d: FleetDraft): CostLine[] {
  const lines: CostLine[] = []
  const control = nodeSize(d.sizes.control)?.usdMonthly ?? 0
  lines.push({ label: `Control A · ${d.controlA}×${d.sizes.control}`, usdMonthly: control * d.controlA })
  if (d.regions === 2 && d.controlB > 0) {
    lines.push({ label: `Control B · ${d.controlB}×${d.sizes.control}`, usdMonthly: control * d.controlB })
  }
  const app = nodeSize(d.sizes.app)?.usdMonthly ?? 0
  lines.push({ label: `App · ${totalApps(d)}×${d.sizes.app}`, usdMonthly: app * totalApps(d) })

  if (d.db.mode === 'managed') {
    const count = 1 + (d.db.replica ? 1 : 0)
    const unit = managedSize(d.db.managedSize)?.usdMonthly ?? 0
    lines.push({ label: `Managed DB · ${count}×${d.db.managedSize}`, usdMonthly: unit * count })
  } else {
    const unit = nodeSize(d.db.selfSize)?.usdMonthly ?? 0
    lines.push({ label: `Postgres · 3×${d.db.selfSize}`, usdMonthly: unit * 3 })
  }

  d.extras.forEach((ex, i) => {
    const unit = (ex.mode === 'managed' ? managedSize(ex.size) : nodeSize(ex.size))?.usdMonthly ?? 0
    lines.push({ label: `Test DB ${i + 1} · ${ex.mode}`, usdMonthly: unit })
  })
  d.services.forEach(sv => {
    const unit = nodeSize(sv.size)?.usdMonthly ?? 0
    lines.push({ label: `${sv.kind === 'cache' ? 'Cache' : 'Queue'} · ${sv.count}×${sv.size}`, usdMonthly: unit * sv.count })
  })

  if (d.edge === 'cloudflare') lines.push({ label: 'Cloudflare edge', usdMonthly: 0 })
  lines.push({ label: `Regional LB · ${d.regions}×`, usdMonthly: LB_MONTHLY * d.regions })
  lines.push({ label: 'Spaces (est.)', usdMonthly: SPACES_MONTHLY })
  return lines
}

export const totalMonthlyUSD = (d: FleetDraft): number => costLines(d).reduce((sum, l) => sum + l.usdMonthly, 0)

export function toClusterYaml(d: FleetDraft): string {
  const pool = (name: string, size: string, count: number) =>
    `    - name: ${name}\n      size: ${size}\n      count: ${count}\n`
  let out = ''
  out += 'apiVersion: norn.dev/fleet/v1\n'
  out += 'kind: Cluster\n'
  out += `metadata:\n  name: ${d.name}\n`
  out += 'spec:\n'
  out += '  provider: digitalocean\n'
  out += '  regions:\n'
  out += `    - name: ${d.region}\n      role: primary\n`
  if (d.regions === 2) out += `    - name: ${d.secondRegion}\n      role: secondary\n`
  out += '  nodePools:\n'
  out += pool(`control-${d.region}`, d.sizes.control, d.controlA)
  if (d.regions === 2 && d.controlB > 0) out += pool(`control-${d.secondRegion}`, d.sizes.control, d.controlB)
  out += pool(`app-${d.region}`, d.sizes.app, d.appA)
  if (d.regions === 2) out += pool(`app-${d.secondRegion}`, d.sizes.app, d.appB)
  out += '  database:\n'
  if (d.db.mode === 'managed') {
    out += `    managed: true\n    engine: ${d.db.engine}\n    size: ${d.db.managedSize}\n    region: ${d.region}\n`
    if (d.db.replica) out += `    readReplica:\n      region: ${replicaResidesInRegionB(d) ? d.secondRegion : d.region}\n`
  } else {
    out += `    managed: false\n    engine: pg\n    patroni: 3\n    size: ${d.db.selfSize}\n    region: ${d.region}\n`
  }
  if (d.services.length > 0) {
    out += '  services:\n'
    for (const sv of d.services) {
      out += `    - kind: ${sv.kind}\n      engine: ${sv.engine.toLowerCase()}\n      size: ${sv.size}\n      count: ${sv.count}\n`
    }
  }
  out += '  ingress:\n    loadBalancer: do-regional\n'
  if (d.edge === 'cloudflare') out += '    edge: cloudflare\n'
  out += `  objectStorage:\n    spaces: ${hasSpaces(d.region)}\n`
  return out
}

// Void reference so the unused import lint doesn't trip if MANAGED_SIZES/NODE_SIZES aren't
// referenced directly (they are, via helpers) — kept intentionally minimal.
export const CATALOG_SIZES = { node: NODE_SIZES, managed: MANAGED_SIZES }
