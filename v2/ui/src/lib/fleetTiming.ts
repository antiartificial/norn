import type { FleetRunnerAttempt, FleetTimingRange } from '../types/index.ts'

export interface FleetTimingView {
  headline: string
  detail: string
  accessibleLabel: string
  available: boolean
}

export function fleetTimingView(attempt?: FleetRunnerAttempt): FleetTimingView {
  if (!attempt) {
    const message = 'Timing starts when the protected runner classifies and begins the reviewed provider plan.'
    return { headline: 'Estimate pending', detail: message, accessibleLabel: message, available: false }
  }

  const timing = attempt.timing
  const elapsedMs = timing?.elapsedMs ?? elapsedFromAttempt(attempt)
  const elapsed = formatDuration(elapsedMs)
  const attemptLabel = `Attempt ${attempt.attempt}`
  const terminalFailure = ['failed', 'canceled', 'abandoned'].includes(attempt.status)

  if (!timing || timing.availability !== 'available' || !timing.estimatedTotal) {
    const status = terminalFailure ? `Timing stopped after ${elapsed}` : `${elapsed} elapsed`
    const detail = 'No comparable cold-start estimate is available for this change.'
    return {
      headline: status,
      detail,
      accessibleLabel: `${attemptLabel}. ${status}. ${detail}`,
      available: false,
    }
  }

  const total = formatRange(timing.estimatedTotal)
  if (attempt.status === 'succeeded') {
    const headline = `Completed in ${elapsed}`
    const detail = `Provisional cold-start range was ${total}.`
    return { headline, detail, accessibleLabel: `${attemptLabel}. ${headline}. ${detail}`, available: true }
  }
  if (terminalFailure) {
    const headline = `Timing stopped after ${elapsed}`
    const detail = `Provisional cold-start range ${total}; no remaining estimate while the attempt is ${attempt.status}.`
    return { headline, detail, accessibleLabel: `${attemptLabel}. ${headline}. ${detail}`, available: true }
  }

  const remaining = timing.estimatedRemaining ? formatRange(timing.estimatedRemaining) : undefined
  const headline = `Cold start: roughly ${total}`
  const detail = `${elapsed} elapsed${remaining ? ` · about ${remaining} remaining` : ''}`
  return {
    headline,
    detail,
    accessibleLabel: `${attemptLabel}. Cold start estimate ${spokenRange(timing.estimatedTotal)}. ${elapsed} elapsed${remaining ? `. About ${spokenRange(timing.estimatedRemaining!)} remaining` : ''}.`,
    available: true,
  }
}

export function formatRange(range: FleetTimingRange): string {
  return range.lowMs === range.highMs
    ? formatDuration(range.lowMs)
    : `${formatDuration(range.lowMs)}–${formatDuration(range.highMs)}`
}

export function formatDuration(ms: number): string {
  const safe = Math.max(0, Math.round(ms))
  if (safe < 60_000) return `${Math.round(safe / 1_000)}s`
  const minutes = Math.floor(safe / 60_000)
  const seconds = Math.floor((safe % 60_000) / 1_000)
  if (minutes < 60) return seconds === 0 ? `${minutes}m` : `${minutes}m ${seconds}s`
  const hours = Math.floor(minutes / 60)
  const remainder = minutes % 60
  return remainder === 0 ? `${hours}h` : `${hours}h ${remainder}m`
}

function elapsedFromAttempt(attempt: FleetRunnerAttempt): number {
  const start = new Date(attempt.startedAt).getTime()
  const end = new Date(attempt.finishedAt ?? attempt.updatedAt).getTime()
  return Number.isFinite(start) && Number.isFinite(end) ? Math.max(0, end - start) : 0
}

function spokenRange(range: FleetTimingRange): string {
  return range.lowMs === range.highMs
    ? formatDuration(range.lowMs)
    : `${formatDuration(range.lowMs)} to ${formatDuration(range.highMs)}`
}
