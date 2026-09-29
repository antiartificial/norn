import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { invalidateForHubEvent, reconcileHubCursor, resetHubEventsForTesting, useHubEvents } from './useHubEvents.ts'
import type { HubEvent } from '../types/ws.ts'

class MockWebSocket {
  static instances: MockWebSocket[] = []
  onmessage: ((message: MessageEvent<string>) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  url: string

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
  }

  close() {
    this.onclose?.()
  }

  sendEvent(event: HubEvent) {
    this.onmessage?.({ data: JSON.stringify(event) } as MessageEvent<string>)
  }
}

function Probe({ onEvent }: { onEvent: (event: HubEvent) => void }) {
  useHubEvents(onEvent)
  return <div>probe</div>
}

describe('useHubEvents', () => {
  afterEach(() => {
    resetHubEventsForTesting()
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    MockWebSocket.instances = []
  })

  it('keeps an in-range cursor when its replay fits one bounded page', async () => {
    const refresh = vi.fn()
    await expect(reconcileHubCursor(12, {
      bounds: { oldestCursor: 10, latestCursor: 20 },
      retention: { replayPageSize: 500 },
    }, refresh)).resolves.toBe(12)
    expect(refresh).not.toHaveBeenCalled()
  })

  it('refreshes authoritative state before advancing an oversized replay cursor', async () => {
    const refresh = vi.fn().mockResolvedValue(undefined)
    await expect(reconcileHubCursor(1, {
      bounds: { oldestCursor: 1, latestCursor: 700 },
      retention: { replayPageSize: 500 },
    }, refresh)).resolves.toBe(700)
    expect(refresh).toHaveBeenCalledOnce()
  })

  it('refreshes authoritative state when the durable compaction watermark expires the cursor', async () => {
    const refresh = vi.fn().mockResolvedValue(undefined)
    await expect(reconcileHubCursor(5, {
      bounds: { oldestCursor: 0, latestCursor: 10, prunedThroughCursor: 10 },
      retention: { replayPageSize: 500 },
    }, refresh)).resolves.toBe(10)
    expect(refresh).toHaveBeenCalledOnce()
  })

  it('rejects a compaction watermark beyond the advertised stream head', async () => {
    await expect(reconcileHubCursor(4, {
      bounds: { oldestCursor: 0, latestCursor: 10, prunedThroughCursor: 11 },
      retention: { replayPageSize: 500 },
    }, vi.fn())).rejects.toThrow('compaction watermark')
  })

  it('retains the durable cursor when authoritative refresh fails', async () => {
    const refresh = vi.fn().mockRejectedValue(new Error('refresh failed'))
    await expect(reconcileHubCursor(1, {
      bounds: { oldestCursor: 1, latestCursor: 700 },
      retention: { replayPageSize: 500 },
    }, refresh)).rejects.toThrow('refresh failed')
  })

  it('rejects unsafe event metadata instead of resetting a cursor', async () => {
    await expect(reconcileHubCursor(4, {
      bounds: { oldestCursor: 1, latestCursor: Number.NaN },
      retention: { replayPageSize: 500 },
    }, vi.fn())).rejects.toThrow('invalid cursor')
  })

  it('maps hub event types to query invalidations', () => {
    const invalidated: unknown[][] = []
    const invalidate = (queryKey: readonly unknown[]) => invalidated.push([...queryKey])

    invalidateForHubEvent({ type: 'deploy.completed', appId: 'api', payload: {} }, invalidate)
    invalidateForHubEvent({ type: 'beacon.event', payload: { severity: 'critical' } }, invalidate)
    invalidateForHubEvent({ type: 'snapshot.restored', appId: 'web', payload: {} }, invalidate)
    invalidateForHubEvent({ type: 'canary.promoted', appId: 'web', payload: {} }, invalidate)

    expect(invalidated).toContainEqual(['deployments'])
    expect(invalidated).toContainEqual(['app', 'api'])
    expect(invalidated).toContainEqual(['apps'])
    expect(invalidated).toContainEqual(['events'])
    expect(invalidated).toContainEqual(['snapshots'])
    expect(invalidated).toContainEqual(['app', 'web'])
  })

  it('subscribes through one mocked websocket and invalidates react-query', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('WebSocket', MockWebSocket)
    const queryClient = new QueryClient()
    const invalidateSpy = vi.spyOn(queryClient, 'invalidateQueries')
    const onEvent = vi.fn()

    render(
      <QueryClientProvider client={queryClient}>
        <Probe onEvent={onEvent} />
      </QueryClientProvider>,
    )

    act(() => vi.advanceTimersByTime(0))
    expect(MockWebSocket.instances).toHaveLength(1)
    expect(MockWebSocket.instances[0].url).toContain('/api/v1/events')

    act(() => {
      MockWebSocket.instances[0].sendEvent({ type: 'deploy.step', appId: 'api', payload: { step: 'build', status: 'running' } })
    })

    expect(onEvent).toHaveBeenCalledWith(expect.objectContaining({ type: 'deploy.step', appId: 'api' }))
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ['deployments'] })
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ['app', 'api'] })
  })

  it('retains the prior cursor when reconnect resync cannot refresh authoritative queries', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('WebSocket', MockWebSocket)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      bounds: { oldestCursor: 1, latestCursor: 700 },
      retention: { replayPageSize: 500 },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const queryClient = new QueryClient()
    vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue(undefined)
    vi.spyOn(queryClient, 'refetchQueries').mockRejectedValue(new Error('authoritative refresh failed'))

    render(
      <QueryClientProvider client={queryClient}>
        <Probe onEvent={() => undefined} />
      </QueryClientProvider>,
    )
    act(() => vi.advanceTimersByTime(0))
    act(() => MockWebSocket.instances[0].sendEvent({ id: 1, type: 'deploy.step', payload: { step: 'build', status: 'running' } }))
    act(() => MockWebSocket.instances[0].close())
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })

    expect(MockWebSocket.instances).toHaveLength(2)
    expect(new URL(MockWebSocket.instances[1].url).searchParams.get('after')).toBe('1')
  })

  it('keeps a shared query client registered until its final subscriber unmounts', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('WebSocket', MockWebSocket)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      bounds: { oldestCursor: 1, latestCursor: 700 },
      retention: { replayPageSize: 500 },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue(undefined)
    const refetch = vi.spyOn(queryClient, 'refetchQueries').mockResolvedValue(undefined)
    const first = render(
      <QueryClientProvider client={queryClient}>
        <Probe onEvent={() => undefined} />
      </QueryClientProvider>,
    )
    render(
      <QueryClientProvider client={queryClient}>
        <Probe onEvent={() => undefined} />
      </QueryClientProvider>,
    )
    act(() => vi.advanceTimersByTime(0))
    first.unmount()
    act(() => MockWebSocket.instances[0].sendEvent({ id: 1, type: 'deploy.step', payload: { step: 'build', status: 'running' } }))
    act(() => MockWebSocket.instances[0].close())
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })

    expect(invalidate).toHaveBeenCalledWith({ refetchType: 'none' })
    expect(refetch).toHaveBeenCalledWith({ type: 'active' }, { throwOnError: true })
    expect(new URL(MockWebSocket.instances[1].url).searchParams.get('after')).toBe('700')
  })

  it('allows a new lifecycle to connect while an older metadata request is pending', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('WebSocket', MockWebSocket)
    let resolveOldFetch: ((response: Response) => void) | undefined
    const streamInfo = () => new Response(JSON.stringify({
      bounds: { oldestCursor: 1, latestCursor: 1 },
      retention: { replayPageSize: 500 },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { resolveOldFetch = resolve }))
      .mockResolvedValueOnce(streamInfo())
    vi.stubGlobal('fetch', fetchMock)
    const oldQueryClient = new QueryClient()
    const oldView = render(
      <QueryClientProvider client={oldQueryClient}>
        <Probe onEvent={() => undefined} />
      </QueryClientProvider>,
    )
    act(() => vi.advanceTimersByTime(0))
    act(() => MockWebSocket.instances[0].sendEvent({ id: 1, type: 'deploy.step', payload: { step: 'build', status: 'running' } }))
    act(() => MockWebSocket.instances[0].close())
    act(() => vi.advanceTimersByTime(3000))
    expect(fetchMock).toHaveBeenCalledTimes(1)

    oldView.unmount()
    const newQueryClient = new QueryClient()
    render(
      <QueryClientProvider client={newQueryClient}>
        <Probe onEvent={() => undefined} />
      </QueryClientProvider>,
    )
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(MockWebSocket.instances).toHaveLength(2)

    await act(async () => { resolveOldFetch?.(streamInfo()); await Promise.resolve() })
    expect(MockWebSocket.instances).toHaveLength(2)
  })

  it('replays explicitly from zero after refreshing a cursor ahead of the stream', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('WebSocket', MockWebSocket)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      bounds: { oldestCursor: 0, latestCursor: 0 },
      retention: { replayPageSize: 500 },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const queryClient = new QueryClient()
    vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue(undefined)
    vi.spyOn(queryClient, 'refetchQueries').mockResolvedValue(undefined)
    render(
      <QueryClientProvider client={queryClient}>
        <Probe onEvent={() => undefined} />
      </QueryClientProvider>,
    )
    act(() => vi.advanceTimersByTime(0))
    act(() => MockWebSocket.instances[0].sendEvent({ id: 5, type: 'deploy.step', payload: { step: 'build', status: 'running' } }))
    act(() => MockWebSocket.instances[0].close())
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })

    expect(new URL(MockWebSocket.instances[1].url).searchParams.get('after')).toBe('0')
  })
})
