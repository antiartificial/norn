import { useEffect, useRef, useCallback, useState } from 'react'
import type { WSEvent } from '../types/index.ts'
import { wsUrl } from '../lib/api.ts'

export function useWebSocket(onEvent: (event: WSEvent) => void) {
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(undefined)

  const connect = useCallback(() => {
    const ws = new WebSocket(wsUrl())

    ws.onopen = () => setConnected(true)

    ws.onmessage = (e) => {
      try {
        const event: WSEvent = JSON.parse(e.data)
        onEvent(event)
      } catch {
        // ignore malformed messages
      }
    }

    ws.onclose = () => {
      setConnected(false)
      reconnectTimer.current = setTimeout(connect, 3000)
    }

    ws.onerror = () => ws.close()

    wsRef.current = ws
  }, [onEvent])

  useEffect(() => {
    connect()
    return () => {
      clearTimeout(reconnectTimer.current)
      wsRef.current?.close()
    }
  }, [connect])

  return { connected }
}
