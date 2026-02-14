import { useEffect, useRef, useCallback, useState } from 'react'
import type { WSEvent } from '../types/index.ts'
import { wsUrl } from '../lib/api.ts'

export function useWebSocket(onEvent: (event: WSEvent) => void) {
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const activeRef = useRef(false)

  const connect = useCallback(() => {
    if (!activeRef.current) return
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
      if (activeRef.current) {
        timerRef.current = setTimeout(connect, 3000)
      }
    }

    ws.onerror = () => ws.close()

    wsRef.current = ws
  }, [onEvent])

  useEffect(() => {
    activeRef.current = true
    // Defer so StrictMode's immediate unmount can cancel before socket opens
    timerRef.current = setTimeout(connect, 0)
    return () => {
      activeRef.current = false
      clearTimeout(timerRef.current)
      wsRef.current?.close()
    }
  }, [connect])

  return { connected }
}
