import { describe, it, expect } from 'vitest'
import {
  defaultDraft, validateDraft, hasBlockingFindings, costLines, totalMonthlyUSD, toClusterYaml,
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

describe('cluster.yaml', () => {
  it('emits a norn.dev/fleet/v1 Cluster', () => {
    const yaml = toClusterYaml(defaultDraft())
    expect(yaml).toContain('apiVersion: norn.dev/fleet/v1')
    expect(yaml).toContain('kind: Cluster')
    expect(yaml).toContain('name: norn-prod')
    expect(yaml).toContain('managed: false')
  })
})
