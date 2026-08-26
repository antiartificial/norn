import { describe, expect, it } from 'vitest'
import { buildFleetPlanTopology } from './FleetPlanTopology.tsx'
import { buildFleetExecutionSteps } from '../lib/fleetExecution.ts'
import type { AppStatus, FleetInventory, Operation, ServiceManifest } from '../types/index.ts'

describe('fleet provisioning topology', () => {
  it('connects desired ingress and pool placement to observed allocations and dependencies', () => {
    const plan: Operation = { id: 'plan-1', status: 'succeeded', payload: { pool: 'app', proposed: { desired: 3 } } }
    const graph = buildFleetPlanTopology(plan, inventory, [app], manifest, buildFleetExecutionSteps({ plan, reconciliations: [] }))

    expect(graph.nodes.map((node) => node.id)).toEqual(expect.arrayContaining([
      'ingress-nyc3', 'region-nyc3', 'pool-app', 'workload-orders-web', 'allocation-orders-web-nyc3',
      'dependency-orders-postgres', 'dependency-orders-redis', 'dependency-orders-kafka',
    ]))
    expect(graph.edges.some((edge) => edge.source === 'ingress-nyc3' && edge.target === 'region-nyc3')).toBe(true)
    expect(graph.edges.some((edge) => edge.source === 'workload-orders-web' && edge.target === 'allocation-orders-web-nyc3' && edge.animated)).toBe(true)
    expect(graph.summary.some((line) => line.includes('1 of 1 observed allocations healthy'))).toBe(true)
  })
})

const inventory: FleetInventory = {
  schemaVersion: 'norn.fleet-inventory/v1',
  configured: true,
  document: { apiVersion: 'norn.dev/fleet/v1', kind: 'Cluster', cluster: { name: 'test', provider: 'digitalocean', region: 'nyc3' } },
  nodePools: { app: { size: 's-2vcpu-4gb', min: 2, desired: 2, max: 6 } },
}

const app: AppStatus = {
  spec: {
    name: 'orders',
    placement: { nodePool: 'app' },
    regions: { nyc3: { nomadRegion: 'global', datacenters: ['nyc3'] } },
    processes: { web: { port: 8080 } },
    infrastructure: { postgres: { database: 'orders' }, redis: { namespace: 'orders' }, kafka: { topics: ['orders.created'] } },
  },
  nomadStatus: 'running',
  healthy: true,
  allocations: [{ id: 'alloc-1', taskGroup: 'web', status: 'running', lifecycle: 'active', healthy: true, nodeName: 'node-1', nodeRegion: 'nyc3' }],
  allocationSummary: { running: 1, active: 1, retained: 0, total: 1 },
}

const manifest: ServiceManifest = {
  version: 1,
  generatedAt: '2026-08-26T00:00:00Z',
  networkMode: 'test',
  services: [{ name: 'orders-web', app: 'orders', process: 'web', type: 'service', status: 'passing', reachability: { endpointScope: 'public', instanceScope: 'private', exposure: 'public', routable: true } }],
}
