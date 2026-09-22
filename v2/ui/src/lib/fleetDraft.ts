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

/** A deliberately narrow managed-cache description. It is exported as a Fleet sidecar, not a node pool. */
export interface FleetManagedValkey {
  id: string
  application: string
  name: string
  size: string
  region: string
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
  managedValkey: FleetManagedValkey[]
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
    managedValkey: [],
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

  const assignedApps = new Set<string>()
  for (const cache of d.managedValkey) {
    const application = cache.application.trim()
    const name = cache.name.trim()
    if (!application || !name) {
      out.push({ severity: 'error', code: 'managed-valkey-identity', field: 'managedValkey',
        message: 'Managed Valkey needs both an application and a cluster name.',
        remediation: 'Name the owning application and its separate managed Valkey cluster.' })
    } else if (assignedApps.has(application)) {
      out.push({ severity: 'error', code: 'managed-valkey-separate-cluster', field: 'managedValkey',
        message: `Managed Valkey application ${application} is assigned more than one cluster.`,
        remediation: 'Keep one separate managed Valkey cluster per application.' })
    } else {
      assignedApps.add(application)
    }
    if (cache.region !== d.region && !(d.regions === 2 && cache.region === d.secondRegion)) {
      out.push({ severity: 'error', code: 'managed-valkey-region', field: 'managedValkey.region',
        message: `Managed Valkey ${name || 'cluster'} is outside this Fleet's regions.`,
        remediation: 'Select Region A or Region B.' })
    }
    out.push({ severity: 'info', code: `managed-valkey-${cache.id}`, field: 'managedValkey',
      message: `Managed Valkey ${name || 'cluster'}: single node, VPC-only, TLS required, cache-only.`,
      remediation: 'Applications must use cache-aside/read-through behavior with a durable source of truth.' })
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
  d.managedValkey.forEach(cache => {
    const unit = managedSize(cache.size)?.usdMonthly ?? 0
    lines.push({ label: `Managed Valkey · ${cache.application || 'unassigned'} · 1×${cache.size}`, usdMonthly: unit })
  })

  if (d.edge === 'cloudflare') lines.push({ label: 'Cloudflare edge', usdMonthly: 0 })
  lines.push({ label: `Regional LB · ${d.regions}×`, usdMonthly: LB_MONTHLY * d.regions })
  lines.push({ label: 'Spaces (est.)', usdMonthly: SPACES_MONTHLY })
  return lines
}

export const totalMonthlyUSD = (d: FleetDraft): number => costLines(d).reduce((sum, l) => sum + l.usdMonthly, 0)

export interface FleetDocument { filename: string; yaml: string }

// Auto-derive capacity bounds from the single builder count. Stateful/quorum pools stay pinned;
// stateless pools get one node of blue/green headroom. Mirror of FleetDraft.swift.
function nodePoolYaml(name: string, size: string, min: number, desired: number, max: number, workload: string): string {
  let out = `  ${name}:\n`
  out += `    size: ${size}\n`
  out += `    min: ${min}\n    desired: ${desired}\n    max: ${max}\n`
  out += `    labels:\n      workload: ${workload}\n`
  out += '    replacement:\n'
  out += '      strategy: blueGreen\n'
  out += '      requireCapacityHeadroom: true\n'
  out += '      requireReadiness: true\n'
  out += '      drainTimeout: 15m\n'
  return out
}

function clusterDoc(d: FleetDraft, r: number, regionName: string): string {
  let out = ''
  out += 'apiVersion: norn.dev/fleet/v1\n'
  out += 'kind: Cluster\n'
  out += `cluster:\n  name: ${d.name}\n  provider: digitalocean\n  region: ${regionName}\n`
  out += `# objectStorage: DO Spaces (terraform state + WAL) in ${d.region}${hasSpaces(d.region) ? '' : ' — none available'}\n`
  if (d.edge === 'cloudflare') out += '# edge: cloudflare in front of the regional load balancer\n'
  if (r === 0 && d.db.mode === 'managed') {
    const engine = d.db.engine === 'pg' ? 'postgresql' : 'mysql'
    out += 'managedDatabases:\n'
    out += `  - name: ${d.name}-db\n    engine: ${engine}\n    size: ${d.db.managedSize}\n    region: ${d.region}\n`
    out += '    network:\n      exposure: vpc-only\n      tls: required\n'
    if (d.db.replica) out += `    readReplica:\n      name: ${d.name}-db-replica\n      region: ${replicaResidesInRegionB(d) ? d.secondRegion : d.region}\n`
  }
  out += 'nodePools:\n'

  const controlCount = r === 0 ? d.controlA : d.controlB
  if (controlCount > 0) out += nodePoolYaml(`control-${regionName}`, d.sizes.control, controlCount, controlCount, controlCount, 'control')
  const appCount = r === 0 ? d.appA : d.appB
  out += nodePoolYaml(`app-${regionName}`, d.sizes.app, appCount, appCount, appCount + 1, 'app')

  // DB, cache/queue and self-managed test DBs live in the primary region.
  if (r === 0) {
    if (d.db.mode === 'self') out += nodePoolYaml(`db-${regionName}`, d.db.selfSize, 3, 3, 3, 'database')
    let cacheN = 0, queueN = 0
    for (const svc of d.services) {
      const kind = svc.kind === 'cache' ? 'cache' : 'queue'
      const n = svc.kind === 'cache' ? cacheN++ : queueN++
      const poolName = n === 0 ? `${kind}-${regionName}` : `${kind}-${regionName}-${n + 1}`
      out += nodePoolYaml(poolName, svc.size, svc.count, svc.count, svc.count + 1, kind)
    }
    d.extras.forEach((ex, i) => {
      if (ex.mode === 'self') out += nodePoolYaml(`db-test-${i + 1}-${regionName}`, ex.size, 1, 1, 1, 'database')
    })
  }
  return out
}

function extrasDoc(d: FleetDraft): string | null {
  const managedExtras = d.extras.filter(e => e.mode === 'managed')
  if (d.edge !== 'cloudflare' && managedExtras.length === 0 && d.managedValkey.length === 0) return null

  let out = '# Managed services not modelled by norn.dev/fleet/v1 — provisioned separately.\n'
  out += 'apiVersion: norn.dev/fleet-extras/v1\n'
  out += `cluster: ${d.name}\n`
  if (managedExtras.length > 0) {
    out += 'testDatabases:\n'
    for (const ex of managedExtras) out += `  - engine: ${ex.engine}\n    size: ${ex.size}\n    region: ${d.region}\n`
  }
  if (d.managedValkey.length > 0) {
    out += 'managedValkey:\n'
    for (const cache of d.managedValkey) {
      out += `  - application: ${cache.application}\n    name: ${cache.name}\n    size: ${cache.size}\n    region: ${cache.region}\n`
      out += '    topology:\n      nodes: 1\n    network:\n      exposure: vpc-only\n      tls: required\n'
      out += '    usage:\n      purpose: cache-only\n      consistency: best-effort\n      applicationPattern: cache-aside-or-read-through\n      durableSourceOfTruth: required\n'
    }
  }
  if (d.edge === 'cloudflare') out += 'edge:\n  provider: cloudflare\n'
  out += `objectStorage:\n  spaces: ${hasSpaces(d.region)}\n  region: ${d.region}\n`
  return out
}

/**
 * The emitted documents: one valid single-region `norn.dev/fleet/v1` Cluster per region, plus a
 * `fleet-extras.yaml` sidecar for services the contract does not model. Managed databases are
 * emitted in the validated norn.dev/fleet/v1 document; managed Valkey remains export-only.
 */
export function fleetDocuments(d: FleetDraft): FleetDocument[] {
  const docs: FleetDocument[] = []
  for (let r = 0; r < d.regions; r++) {
    const regionName = r === 0 ? d.region : d.secondRegion
    docs.push({ filename: `${d.name}-${regionName}.cluster.yaml`, yaml: clusterDoc(d, r, regionName) })
  }
  const extras = extrasDoc(d)
  if (extras) docs.push({ filename: `${d.name}.fleet-extras.yaml`, yaml: extras })
  return docs
}

/** Combined, human-readable view of every emitted document (for the YAML pane + clipboard). */
export function clusterYaml(d: FleetDraft): string {
  return fleetDocuments(d).map(doc => `# ===== ${doc.filename} =====\n${doc.yaml}`).join('\n')
}

// Void reference so the unused import lint doesn't trip if MANAGED_SIZES/NODE_SIZES aren't
// referenced directly (they are, via helpers) — kept intentionally minimal.
export const CATALOG_SIZES = { node: NODE_SIZES, managed: MANAGED_SIZES }
