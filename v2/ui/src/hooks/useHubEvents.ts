import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsUrl } from '../lib/api.ts'
import type { HubEvent } from '../types/ws.ts'

type HubSubscriber = (event: HubEvent) => void

const subscribers = new Set<HubSubscriber>()
const connectionSubscribers = new Set<(connected: boolean) => void>()
let ws: WebSocket | null = null
let reconnectTimer: ReturnType<typeof setTimeout> | undefined
let active = false
let connected = false
let lastEventId = 0

function emit(event: HubEvent) {
  for (const subscriber of subscribers) subscriber(event)
}

function setConnected(next: boolean) {
  connected = next
  for (const subscriber of connectionSubscribers) subscriber(next)
}

function connect() {
  if (!active || ws) return
  const url = new URL(wsUrl())
  if (lastEventId > 0) url.searchParams.set('after', String(lastEventId))
  ws = new WebSocket(url.toString())
  ws.onopen = () => setConnected(true)
  ws.onmessage = (message) => {
    try {
      const event = JSON.parse(message.data) as HubEvent
      if (typeof event.id === 'number' && event.id > lastEventId) lastEventId = event.id
      emit(event)
    } catch {
      // ignore malformed messages
    }
  }
  ws.onclose = () => {
    ws = null
    setConnected(false)
    if (active) reconnectTimer = setTimeout(connect, 3000)
  }
  ws.onerror = () => ws?.close()
}

function ensureSocket() {
  active = true
  clearTimeout(reconnectTimer)
  reconnectTimer = setTimeout(connect, 0)
}

function releaseSocket() {
  if (subscribers.size > 0) return
  active = false
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
    setIsConnected(connected)
    ensureSocket()
    return () => {
      subscribers.delete(subscriber)
      connectionSubscribers.delete(setIsConnected)
      releaseSocket()
    }
  }, [onEvent, queryClient])

  return { connected: isConnected }
}
