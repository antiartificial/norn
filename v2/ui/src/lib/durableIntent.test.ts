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
    expect(localStorage.getItem(changed.storageKey)).toBeNull()
  })

  it('continues when browser storage is unavailable', () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    const intent = durableIntent('atlas:snapshot', {})
    expect(intent.key).toMatch(/^norn-web-/)
    getItem.mockRestore()
  })
})
