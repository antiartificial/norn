import { useEffect, useState } from 'react'
import { type QueryClient, useQueryClient } from '@tanstack/react-query'
import { apiFetch, wsUrl } from '../lib/api.ts'
import type { HubEvent } from '../types/ws.ts'

type HubSubscriber = (event: HubEvent) => void

const subscribers = new Set<HubSubscriber>()
const connectionSubscribers = new Set<(connected: boolean) => void>()
let ws: WebSocket | null = null
let reconnectTimer: ReturnType<typeof setTimeout> | undefined
let active = false
let connected = false
let connectingGeneration: number | null = null
let lastEventId = 0
let lifecycleGeneration = 0
const queryClients = new Map<QueryClient, number>()

interface HubStreamInfo {
  bounds: { oldestCursor: number; latestCursor: number; prunedThroughCursor?: number }
  retention: { replayPageSize: number }
}

function emit(event: HubEvent) {
  for (const subscriber of subscribers) subscriber(event)
}

function setConnected(next: boolean) {
  connected = next
  for (const subscriber of connectionSubscribers) subscriber(next)
}

export async function reconcileHubCursor(
  cursor: number,
  info: HubStreamInfo,
  refreshAuthoritativeState: () => Promise<unknown>,
): Promise<number> {
  const prunedThroughCursor = info.bounds.prunedThroughCursor ?? 0
  const values = [cursor, info.bounds.oldestCursor, info.bounds.latestCursor, prunedThroughCursor, info.retention.replayPageSize]
  if (values.some((value) => !Number.isSafeInteger(value) || value < 0) || info.retention.replayPageSize < 1) {
    throw new Error('event stream metadata contains an invalid cursor or replay page size')
  }
  if (prunedThroughCursor > info.bounds.latestCursor) {
    throw new Error('event stream metadata has a compaction watermark beyond its stream head')
  }
  if (cursor === 0) return cursor
  const minimum = Math.max(0, info.bounds.oldestCursor - 1)
  const maximum = Math.max(0, info.bounds.latestCursor)
  const replayPageSize = Math.max(1, info.retention.replayPageSize)
  if (cursor >= minimum && cursor >= prunedThroughCursor && cursor <= maximum && maximum - cursor <= replayPageSize) return cursor
  await refreshAuthoritativeState()
  return maximum
}

async function refreshAuthoritativeState(clients: QueryClient[]) {
  if (clients.length === 0) throw new Error('no authoritative query client is active')
  await Promise.all(clients.map(async (queryClient) => {
    await queryClient.invalidateQueries({ refetchType: 'none' })
    await queryClient.refetchQueries({ type: 'active' }, { throwOnError: true })
  }))
}

async function connect() {
  const generation = lifecycleGeneration
  if (!active || ws || connectingGeneration === generation) return
  connectingGeneration = generation
  const finishConnecting = () => {
    if (connectingGeneration === generation) connectingGeneration = null
  }
  let cursor = lastEventId
  const replay = cursor > 0
  const authoritativeClients = [...queryClients.keys()]
  if (cursor > 0) {
    try {
      const info = await apiFetch<HubStreamInfo>('/api/v1/events/info')
      if (!active || generation !== lifecycleGeneration) {
        finishConnecting()
        return
      }
      cursor = await reconcileHubCursor(cursor, info, () => refreshAuthoritativeState(authoritativeClients))
    } catch {
      // Metadata failure is an ordinary transport failure. Retain the durable
      // cursor and let the socket/reconnect path retry without discarding work.
    }
  }
  if (!active || ws || generation !== lifecycleGeneration) {
    finishConnecting()
    return
  }
  lastEventId = cursor
  const url = new URL(wsUrl())
  if (replay) url.searchParams.set('after', String(cursor))
  let socket: WebSocket
  try {
    socket = new WebSocket(url.toString())
    ws = socket
  } catch {
    finishConnecting()
    if (active && generation === lifecycleGeneration) reconnectTimer = setTimeout(connect, 3000)
    return
  }
  finishConnecting()
  socket.onopen = () => {
    if (ws === socket) setConnected(true)
  }
  socket.onmessage = (message) => {
    if (ws !== socket) return
    try {
      const event = JSON.parse(message.data) as HubEvent
      if (typeof event.id === 'number' && Number.isSafeInteger(event.id) && event.id >= 0 && event.id > lastEventId) lastEventId = event.id
      emit(event)
    } catch {
      // ignore malformed messages
    }
  }
  socket.onclose = () => {
    if (ws !== socket) return
    ws = null
    setConnected(false)
    if (active) reconnectTimer = setTimeout(connect, 3000)
  }
  socket.onerror = () => socket.close()
}

function ensureSocket() {
  if (!active) {
    active = true
    lifecycleGeneration += 1
  }
  clearTimeout(reconnectTimer)
  reconnectTimer = setTimeout(connect, 0)
}

function releaseSocket() {
  if (subscribers.size > 0) return
  active = false
  lifecycleGeneration += 1
  clearTimeout(reconnectTimer)
  ws?.close()
  ws = null
  setConnected(false)
}

export function invalidateForHubEvent(event: HubEvent, invalidate: (queryKey: readonly unknown[]) => void) {
  if (event.type.startsWith('deploy.') || event.type.startsWith('preflight.')) {
    invalidate(['deployments'])
    if (event.appId) invalidate(['app', event.appId])
    if (event.type === 'deploy.completed' || event.type === 'deploy.succeeded' || event.type === 'deploy.auto_rollback' || event.type === 'deploy.failed') invalidate(['apps'])
    return
  }
  if (event.type.startsWith('snapshot.')) {
    if (event.appId) invalidate(['app', event.appId])
    invalidate(['snapshots'])
    return
  }
  if (event.type === 'function.completed') {
    if (event.appId) invalidate(['app', event.appId])
    invalidate(['functions'])
    return
  }
  if (event.type === 'beacon.event') {
    invalidate(['events'])
    return
  }
  if (event.type.startsWith('rollback.')) {
    invalidate(['deployments'])
    if (event.appId) invalidate(['app', event.appId])
    return
  }
  if (event.type === 'canary.promoted') {
    invalidate(['apps'])
    if (event.appId) invalidate(['app', event.appId])
    return
  }
  if (event.type === 'app.restarted' || event.type === 'app.scaled') {
    invalidate(['apps'])
    if (event.appId) invalidate(['app', event.appId])
  }
}

export function resetHubEventsForTesting() {
  active = false
  clearTimeout(reconnectTimer)
  const current = ws
  ws = null
  current?.close()
  connected = false
  connectingGeneration = null
  lastEventId = 0
  lifecycleGeneration += 1
  subscribers.clear()
  connectionSubscribers.clear()
  queryClients.clear()
}

export function useHubEvents(onEvent?: HubSubscriber) {
  const queryClient = useQueryClient()
  const [isConnected, setIsConnected] = useState(connected)

  useEffect(() => {
    const subscriber: HubSubscriber = (event) => {
      invalidateForHubEvent(event, (queryKey) => queryClient.invalidateQueries({ queryKey }))
      onEvent?.(event)
    }
    subscribers.add(subscriber)
    connectionSubscribers.add(setIsConnected)
    queryClients.set(queryClient, (queryClients.get(queryClient) ?? 0) + 1)
    setIsConnected(connected)
    ensureSocket()
    return () => {
      subscribers.delete(subscriber)
      connectionSubscribers.delete(setIsConnected)
      const queryClientUsers = queryClients.get(queryClient) ?? 0
      if (queryClientUsers <= 1) queryClients.delete(queryClient)
      else queryClients.set(queryClient, queryClientUsers - 1)
      releaseSocket()
    }
  }, [onEvent, queryClient])

  return { connected: isConnected }
}
