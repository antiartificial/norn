import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { relativeTime } from '../lib/format.ts'
import { bucketByTime } from '../lib/timeseries.ts'
import { summarizeAccessEvents } from '../lib/traffic.ts'
import type { AccessEvent } from '../types/index.ts'
import { DataTable, Sparkline, StatusChip, type DataTableColumn, type StatusTone } from './ui/index.ts'

export function PlatformTrafficView() {
  const [pathFilter, setPathFilter] = useState('')
  const [errorsOnly, setErrorsOnly] = useState(false)
  const [slowOnly, setSlowOnly] = useState(false)
  const accessEvents = useQuery({
    queryKey: ['access', 'events', { limit: 500 }],
    queryFn: () => apiFetch<AccessEvent[]>('/api/access/events?limit=500'),
    staleTime: 10_000,
    refetchInterval: 10_000,
  })
  const rows = accessEvents.data ?? []
  const now = Date.now()
  const summary = useMemo(() => summarizeAccessEvents(rows, now), [rows, now])
  const filteredRows = useMemo(() => {
    const query = pathFilter.trim().toLowerCase()
    return rows.filter(row => {
      if (query && !row.path.toLowerCase().includes(query)) return false
      if (errorsOnly && row.status < 400) return false
      if (slowOnly && row.durationMs < 500) return false
      return true
    })
  }, [errorsOnly, pathFilter, rows, slowOnly])
  const windowMs = Math.max(summary.spanMs, 60_000)
  const series = bucketByTime(rows, { getTime: event => event.timestamp, windowMs, bucketCount: 40, now })
  const hasServerErrors = rows.some(row => row.status >= 500)
  const hasClientErrors = rows.some(row => row.status >= 400 && row.status < 500)
  const columns: DataTableColumn<AccessEvent>[] = [
    { key: 'time', header: 'Time', cell: row => <span title={new Date(row.timestamp).toLocaleString()}>{relativeTime(row.timestamp)}</span> },
    { key: 'method', header: 'Method', cell: row => <code>{row.method}</code> },
    { key: 'path', header: 'Path', cell: row => <code className="traffic-path" title={row.path}>{row.path}</code> },
    { key: 'status', header: 'Status', cell: row => <StatusChip tone={statusTone(row.status)} label={String(row.status)} /> },
    { key: 'duration', header: 'Duration', cell: row => row.durationMs, numeric: true },
    { key: 'client', header: 'Client', cell: row => row.cfAccessEmail || row.cfConnectingIp || row.clientIp || row.forwarded || '-' },
  ]

  return (
    <div className="traffic-view">
      <section className="ops-section">
        <div className="traffic-summary">
          <TrafficMetric label="requests" value={summary.total} />
          <TrafficMetric label="5xx" value={summary.serverErrors} tone={summary.serverErrors ? 'danger' : 'neutral'} />
          <TrafficMetric label="4xx" value={summary.clientErrors} tone={summary.clientErrors ? 'warning' : 'neutral'} />
          <TrafficMetric label="p95" value={`${summary.p95DurationMs} ms`} />
          <TrafficMetric label="window" value={summary.label} wide />
        </div>
        <div className="traffic-chart-row">
          <Sparkline series={series} tone={hasServerErrors ? 'danger' : hasClientErrors ? 'warn' : 'neutral'} width={520} height={54} aria-label="Control-plane requests over the access event buffer" />
          <p>Control-plane access log only, last 500 requests. This is not full app traffic.</p>
        </div>
      </section>

      <section className="ops-section">
        <div className="traffic-filter-row">
          <label className="incident-filter-field">
            <span>Path</span>
            <input className="filter-input" value={pathFilter} onChange={event => setPathFilter(event.target.value)} placeholder="/api/apps" />
          </label>
          <label className="traffic-toggle">
            <input type="checkbox" checked={errorsOnly} onChange={event => setErrorsOnly(event.target.checked)} />
            errors only
          </label>
          <label className="traffic-toggle">
            <input type="checkbox" checked={slowOnly} onChange={event => setSlowOnly(event.target.checked)} />
            slow only
          </label>
        </div>
        <DataTable
          columns={columns}
          rows={filteredRows}
          getRowKey={(row) => `${row.timestamp}:${row.method}:${row.path}:${row.durationMs}`}
          loading={accessEvents.isLoading}
          error={accessEvents.error instanceof Error ? accessEvents.error.message : null}
          emptyTitle="No matching requests"
          emptyHint="Adjust filters or wait for control-plane requests to arrive."
          onRetry={() => accessEvents.refetch()}
        />
      </section>
    </div>
  )
}

function statusTone(status: number): StatusTone {
  if (status >= 500) return 'danger'
  if (status >= 400) return 'warning'
  if (status >= 300) return 'neutral'
  return 'success'
}

function TrafficMetric({ label, value, tone = 'neutral', wide = false }: { label: string; value: string | number; tone?: StatusTone; wide?: boolean }) {
  return (
    <div className={`traffic-metric traffic-metric-${tone} ${wide ? 'traffic-metric-wide' : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}
