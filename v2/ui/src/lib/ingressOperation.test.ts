import { describe, expect, it, vi } from 'vitest'
import { pollIngressOperation, validateIngressAcceptance } from './ingressOperation.ts'

describe('ingress operation receipts', () => {
  it('rejects an unbound queued response', () => {
    expect(() => validateIngressAcceptance({ status: 'queued' })).toThrow('invalid ingress')
  })

  it('reports terminal replay without polling', async () => {
    const read = vi.fn()
    const result = await pollIngressOperation({ id: 'op-1', kind: 'app.cloudflared-mutate', status: 'succeeded' }, read)
    expect(result.status).toBe('succeeded')
    expect(read).not.toHaveBeenCalled()
  })

  it('waits for completion and tolerates a temporary status read failure', async () => {
    const read = vi.fn()
      .mockRejectedValueOnce(new Error('temporary'))
      .mockResolvedValueOnce({ id: 'op-1', status: 'running' })
      .mockResolvedValueOnce({ id: 'op-1', status: 'succeeded' })
    const pause = vi.fn().mockResolvedValue(undefined)
    const result = await pollIngressOperation({ id: 'op-1', kind: 'app.cloudflared-mutate', status: 'queued' }, read, pause, 3)
    expect(result.status).toBe('succeeded')
    expect(read).toHaveBeenCalledTimes(3)
    expect(pause).toHaveBeenCalledTimes(3)
  })

  it('does not claim success when polling budget expires', async () => {
    const result = await pollIngressOperation({ id: 'op-1', kind: 'app.cloudflared-mutate', status: 'queued' }, async () => ({ id: 'op-1', status: 'running' }), async () => {}, 1)
    expect(result.status).toBe('running')
  })
})
