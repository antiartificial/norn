import { useEffect, useRef, useState } from 'react'

interface Props {
  appId: string
  onClose: () => void
}

export function LogViewer({ appId, onClose }: Props) {
  const [logs, setLogs] = useState('')
  const logRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    const controller = new AbortController()

    async function streamLogs() {
      try {
        const res = await fetch(`/api/apps/${appId}/logs?follow=true`, {
          signal: controller.signal,
        })
        const reader = res.body?.getReader()
        const decoder = new TextDecoder()

        if (!reader) return

        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          setLogs((prev) => prev + decoder.decode(value))
        }
      } catch {
        // aborted or network error
      }
    }

    streamLogs()
    return () => controller.abort()
  }, [appId])

  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight
    }
  }, [logs])

  return (
    <div className="log-viewer">
      <div className="log-viewer-header">
        <h3>
          <i className="fawsb fa-rectangle-code" /> {appId}
        </h3>
        <button onClick={onClose} className="btn btn-close">
          <i className="fawsb fa-xmark" />
        </button>
      </div>
      <pre ref={logRef} className="log-viewer-output">{logs || 'Waiting for logs...'}</pre>
    </div>
  )
}
