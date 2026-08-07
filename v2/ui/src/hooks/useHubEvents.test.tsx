import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { invalidateForHubEvent, useHubEvents } from './useHubEvents.ts'
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
    vi.useRealTimers()
    vi.restoreAllMocks()
    MockWebSocket.instances = []
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
})
