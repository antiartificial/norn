import { describe, it, expect } from 'vitest'
import {
  defaultDraft, validateDraft, hasBlockingFindings, costLines, totalMonthlyUSD, fleetDocuments,
  type FleetDraft,
} from './fleetDraft'

const find = (d: FleetDraft, code: string) => validateDraft(d).find(f => f.code === code)

describe('fleet validation', () => {
  it('default draft has no blocking findings', () => {
    const d = defaultDraft()
    expect(hasBlockingFindings(d)).toBe(false)
    expect(find(d, 'object-storage')?.severity).toBe('info')
  })

  it('even control count fails quorum', () => {
    const d = { ...defaultDraft(), controlA: 4 }
    expect(find(d, 'control-quorum')?.severity).toBe('error')
  })

  it('region-B control quorum rules', () => {
    const base = { ...defaultDraft(), regions: 2 }
    expect(find({ ...base, controlB: 0 }, 'control-region-b')?.severity).toBe('info')
    expect(find({ ...base, controlB: 2 }, 'control-region-b')?.severity).toBe('error')
    expect(find({ ...base, controlB: 3 }, 'control-region-b')?.severity).toBe('info')
  })

  it('region without spaces is blocking', () => {
    const d = { ...defaultDraft(), region: 'tor1' }
    expect(find(d, 'object-storage')?.severity).toBe('error')
    expect(hasBlockingFindings(d)).toBe(true)
  })

  it('undersized self DB is blocking', () => {
    const d = defaultDraft()
    d.db.selfSize = 's-2vcpu-4gb'
    expect(find(d, 'database')?.severity).toBe('error')
  })

  it('multi-region adds cross-region write warning', () => {
    const d = { ...defaultDraft(), regions: 2 }
    expect(find(d, 'cross-region-write')?.severity).toBe('warning')
  })

  it('warns when both regions are identical', () => {
    const dup = { ...defaultDraft(), regions: 2, region: 'nyc3', secondRegion: 'nyc3' }
    expect(find(dup, 'region-distinct')?.severity).toBe('warning')
    const distinct = { ...dup, secondRegion: 'sfo3' }
    expect(find(distinct, 'region-distinct')).toBeUndefined()
  })
})

describe('fleet cost', () => {
  it('default total matches macOS', () => {
    // control 3×63 + app 2×48 + postgres 3×126 + LB 12 + spaces 5
    expect(totalMonthlyUSD(defaultDraft())).toBe(189 + 96 + 378 + 12 + 5)
  })

  it('managed + replica costs two managed instances', () => {
    const d = defaultDraft()
    d.db.mode = 'managed'
    d.db.replica = true
    expect(costLines(d).find(l => l.label.startsWith('Managed DB'))?.usdMonthly).toBe(120)
  })

  it('cloudflare edge is free', () => {
    const d = { ...defaultDraft(), edge: 'cloudflare' as const }
    expect(costLines(d).find(l => l.label === 'Cloudflare edge')?.usdMonthly).toBe(0)
  })
})

describe('fleet documents', () => {
  it('default draft emits one valid norn.dev/fleet/v1 Cluster', () => {
    const docs = fleetDocuments(defaultDraft())
    expect(docs).toHaveLength(1)
    expect(docs[0].filename).toBe('norn-prod-nyc3.cluster.yaml')
    const y = docs[0].yaml
    expect(y).toContain('apiVersion: norn.dev/fleet/v1')
    expect(y).toContain('kind: Cluster')
    expect(y).toContain('cluster:\n  name: norn-prod\n  provider: digitalocean\n  region: nyc3')
    expect(y).toContain('nodePools:')
    expect(y).toContain('control-nyc3:')
    expect(y).toContain('app-nyc3:')
    expect(y).toContain('db-nyc3:')
    expect(y).toContain('workload: control')
    // Control quorum is pinned; apps get one node of headroom.
    expect(y).toContain('min: 3\n    desired: 3\n    max: 3')
    expect(y).toContain('min: 2\n    desired: 2\n    max: 3')
    expect(y).toContain('strategy: blueGreen')
    expect(y).toContain('requireCapacityHeadroom: true')
    expect(y).toContain('drainTimeout: 15m')
  })

  it('managed database goes to the fleet-extras sidecar', () => {
    const d = { ...defaultDraft(), db: { ...defaultDraft().db, mode: 'managed' as const } }
    const docs = fleetDocuments(d)
    expect(docs).toHaveLength(2)
    expect(docs[0].yaml).not.toContain('db-nyc3:')
    expect(docs[1].filename).toBe('norn-prod.fleet-extras.yaml')
    expect(docs[1].yaml).toContain('apiVersion: norn.dev/fleet-extras/v1')
    expect(docs[1].yaml).toContain('managedDatabase:')
  })

  it('two regions emit one cluster document each', () => {
    const d = { ...defaultDraft(), regions: 2, secondRegion: 'sfo3' }
    const docs = fleetDocuments(d)
    expect(docs.some(x => x.filename === 'norn-prod-nyc3.cluster.yaml')).toBe(true)
    expect(docs.some(x => x.filename === 'norn-prod-sfo3.cluster.yaml')).toBe(true)
    const b = docs.find(x => x.filename.includes('sfo3'))!.yaml
    expect(b).toContain('region: sfo3')
    expect(b).toContain('app-sfo3:')
    expect(b).not.toContain('db-sfo3:')
  })
})
