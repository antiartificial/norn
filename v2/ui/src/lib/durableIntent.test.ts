import { beforeEach, describe, expect, it, vi } from 'vitest'
import { clearDurableIntent, durableIntent } from './durableIntent.ts'

describe('durableIntent', () => {
  beforeEach(() => localStorage.clear())

  it('reuses the key for the same interrupted request', () => {
    const first = durableIntent('atlas:restore', { timestamp: '20260825T120000' })
    const retry = durableIntent('atlas:restore', { timestamp: '20260825T120000' })
    expect(retry.key).toBe(first.key)
  })

  it('replaces changed requests and clears completed intents', () => {
    const first = durableIntent('atlas:retention', { keep: 3 })
    const changed = durableIntent('atlas:retention', { keep: 5 })
    expect(changed.key).not.toBe(first.key)
    clearDurableIntent(changed)
    expect(durableIntent('atlas:retention', { keep: 3 }).key).toBe(first.key)
    clearDurableIntent(first)
    expect(localStorage.getItem(changed.storageKey)).toBeNull()
  })

  it('preserves uncertain keys independently across changed payloads', () => {
    const firstA = durableIntent('atlas:multi-payload', { ref: 'A' })
    const firstB = durableIntent('atlas:multi-payload', { ref: 'B' })
    const retryA = durableIntent('atlas:multi-payload', { ref: 'A' })
    expect(firstB.key).not.toBe(firstA.key)
    expect(retryA.key).toBe(firstA.key)
  })

  it('continues when browser storage is unavailable', () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    const intent = durableIntent('atlas:snapshot-memory', {})
    const retry = durableIntent('atlas:snapshot-memory', {})
    expect(intent.key).toMatch(/^norn-web-/)
    expect(retry.key).toBe(intent.key)
    expect(retry.persistence).toBe('memory')
    getItem.mockRestore()
    setItem.mockRestore()
  })

  it('reuses the tab-local key when reads work but writes are blocked', () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
    const first = durableIntent('atlas:snapshot-write-blocked', {})
    const retry = durableIntent('atlas:snapshot-write-blocked', {})
    expect(retry.key).toBe(first.key)
    expect(retry.persistence).toBe('memory')
    setItem.mockRestore()
  })

  it('migrates a pending v1 entry without changing its key', () => {
    const scope = 'atlas:legacy-pending'
    const request = JSON.stringify({ ref: 'A' })
    localStorage.setItem(`norn:durable-intent:v1:${encodeURIComponent(scope)}`, JSON.stringify({ request, key: 'norn-web-legacy-key' }))
    expect(durableIntent(scope, { ref: 'A' }).key).toBe('norn-web-legacy-key')
  })

  it('does not evict older unresolved payload intents', () => {
    const scope = 'atlas:many-pending'
    const first = durableIntent(scope, { ref: 0 })
    for (let ref = 1; ref <= 21; ref += 1) durableIntent(scope, { ref })
    expect(durableIntent(scope, { ref: 0 }).key).toBe(first.key)
  })

  it('ignores malformed envelope entries while preserving valid intents', () => {
    const scope = 'atlas:malformed-envelope'
    const storageKey = `norn:durable-intent:v1:${encodeURIComponent(scope)}`
    localStorage.setItem(storageKey, JSON.stringify({ version: 2, intents: [null, {}, { request: JSON.stringify({ ref: 'A' }), key: 'norn-web-valid' }] }))
    const intent = durableIntent(scope, { ref: 'A' })
    expect(intent.key).toBe('norn-web-valid')
    expect(() => clearDurableIntent(intent)).not.toThrow()
  })

  it('does not clear a newer intent stored under the same scope', () => {
    const older = durableIntent('atlas:retention-safe-clear', { keep: 3 })
    const newer = durableIntent('atlas:retention-safe-clear', { keep: 5 })
    clearDurableIntent(older)
    expect(durableIntent('atlas:retention-safe-clear', { keep: 5 }).key).toBe(newer.key)
  })

})
