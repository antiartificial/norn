import type { AccessEvent } from '../types/index.ts'

export interface AccessEventSummary {
  total: number
  clientErrors: number
  serverErrors: number
  p95DurationMs: number
  spanMs: number
  label: string
}

function spanLabel(spanMs: number): string {
  if (spanMs <= 0) return 'no span yet'
  const minutes = Math.max(1, Math.round(spanMs / 60_000))
  if (minutes < 60) return `spanning ${minutes}m`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `spanning ${hours}h`
  return `spanning ${Math.round(hours / 24)}d`
}

export function summarizeAccessEvents(events: AccessEvent[], _now: string | number | Date): AccessEventSummary {
  if (events.length === 0) {
    return { total: 0, clientErrors: 0, serverErrors: 0, p95DurationMs: 0, spanMs: 0, label: 'last 500 requests · no events yet' }
  }

  const timestamps = events.map(event => new Date(event.timestamp).getTime()).filter(Number.isFinite)
  const newest = Math.max(...timestamps)
  const oldest = Math.min(...timestamps)
  const durations = events.map(event => event.durationMs).filter(Number.isFinite).sort((a, b) => a - b)
  const p95Index = durations.length === 0 ? 0 : Math.ceil(durations.length * 0.95) - 1
  const spanMs = Math.max(0, newest - oldest)

  return {
    total: events.length,
    clientErrors: events.filter(event => event.status >= 400 && event.status < 500).length,
    serverErrors: events.filter(event => event.status >= 500).length,
    p95DurationMs: durations[p95Index] ?? 0,
    spanMs,
    label: `last 500 requests · ${spanLabel(spanMs)}`,
  }
}
