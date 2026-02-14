import { useEffect, useRef, useState } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import type { SagaEvent } from '../types/index.ts'

interface Props {
  appId: string
  healthPath?: string
  onClose: () => void
}

function timeAgo(ts: string): string {
  const diff = Date.now() - new Date(ts).getTime()
  const secs = Math.floor(diff / 1000)
  if (secs < 60) return `${secs}s ago`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  return `${hrs}h ago`
}

function isHealthCheckLine(line: string, healthPath?: string): boolean {
  const patterns = ['GET /health', 'GET /healthz', 'health check']
  if (healthPath) patterns.push(`GET ${healthPath}`)
  const lower = line.toLowerCase()
  return patterns.some(p => lower.includes(p.toLowerCase()))
}

export function LogViewer({ appId, healthPath, onClose }: Props) {
  const [tab, setTab] = useState<'output' | 'saga'>('output')
  const [logs, setLogs] = useState('')
  const [sagaEvents, setSagaEvents] = useState<SagaEvent[]>([])
  const [sagaLoading, setSagaLoading] = useState(false)
  const [hideHealth, setHideHealth] = useState(true)
  const logRef = useRef<HTMLPreElement>(null)

  // Stream logs
  useEffect(() => {
    if (tab !== 'output') return
    const controller = new AbortController()

    async function streamLogs() {
      try {
        const res = await fetch(apiUrl(`/api/apps/${appId}/logs?follow=true`), {
          ...fetchOpts,
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
  }, [appId, tab])

  // Auto-scroll
  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight
    }
  }, [logs])

  // Fetch saga events
  useEffect(() => {
    if (tab !== 'saga') return
    setSagaLoading(true)
    fetch(apiUrl(`/api/saga?app=${appId}`), fetchOpts)
      .then(r => r.json())
      .then((events: SagaEvent[]) => {
        setSagaEvents(events ?? [])
        setSagaLoading(false)
      })
      .catch(() => setSagaLoading(false))
  }, [appId, tab])

  const filteredLogs = hideHealth
    ? logs.split('\n').filter(line => !isHealthCheckLine(line, healthPath)).join('\n')
    : logs

  return (
    <div className="log-viewer">
      <div className="log-viewer-header">
        <h3>
          <i className="fawsb fa-rectangle-code" /> {appId}
        </h3>
        <div className="log-tabs">
          <button
            className={`log-tab ${tab === 'output' ? 'active' : ''}`}
            onClick={() => setTab('output')}
          >
            Output
          </button>
          <button
            className={`log-tab ${tab === 'saga' ? 'active' : ''}`}
            onClick={() => setTab('saga')}
          >
            Saga
          </button>
        </div>
        <div className="log-viewer-actions">
          {tab === 'output' && (
            <button
              className={`btn btn-small ${hideHealth ? 'active' : ''}`}
              onClick={() => setHideHealth(h => !h)}
              title="Filter health check lines"
            >
              <i className="fawsb fa-heart-pulse" />
            </button>
          )}
          <button onClick={onClose} className="btn btn-close">
            <i className="fawsb fa-xmark" />
          </button>
        </div>
      </div>

      {tab === 'output' && (
        <pre ref={logRef} className="log-viewer-output">
          {filteredLogs || 'Waiting for logs...'}
        </pre>
      )}

      {tab === 'saga' && (
        <div className="saga-timeline">
          {sagaLoading && <div className="saga-loading">Loading saga events...</div>}
          {!sagaLoading && sagaEvents.length === 0 && (
            <div className="saga-loading">No saga events for this app</div>
          )}
          {sagaEvents.map(evt => (
            <div key={evt.id} className="saga-event">
              <span className="saga-event-time">{timeAgo(evt.timestamp)}</span>
              <span className={`saga-event-action ${evt.category}`}>{evt.action}</span>
              <span className="saga-event-msg">{evt.message}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
