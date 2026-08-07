import type { StatusTone } from '../components/ui/index.ts'
import type { ActivityEntry } from '../runtime/AppRuntime.tsx'
import type { HubEvent } from '../types/ws.ts'

export interface FormattedActivity {
  id: number
  sentence: string
  tone: StatusTone
  href: string
  capturedAt: string
}

export interface CollapsedActivity extends FormattedActivity {
  count: number
}

function payload(event: HubEvent): Record<string, unknown> {
  return event.payload && typeof event.payload === 'object' ? event.payload as Record<string, unknown> : {}
}

function eventApp(event: HubEvent): string | undefined {
  const data = payload(event)
  return event.appId ?? (typeof data.appId === 'string' ? data.appId : undefined)
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function humanizeType(type: string): string {
  return type
    .replace(/[._-]+/g, ' ')
    .replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function withApp(sentence: string, app?: string): string {
  return app ? `${sentence} - ${app}` : sentence
}

function toneFor(event: HubEvent): StatusTone {
  const data = payload(event)
  const severity = text(data.severity)?.toLowerCase()
  const status = text(data.status)?.toLowerCase()
  if (severity === 'critical' || event.type.includes('failed') || status === 'failed' || status === 'error') return 'danger'
  if (severity === 'warning' || status === 'warning') return 'warning'
  if (event.type.includes('succeeded') || event.type.includes('completed') || event.type === 'canary.promoted') return 'success'
  return 'info'
}

function hrefFor(event: HubEvent): string {
  const app = eventApp(event)
  if (event.type.startsWith('deploy.')) return '/deploys'
  if (event.type === 'beacon.event') return '/incidents'
  if (event.type.startsWith('preflight.') || event.type.startsWith('rollback.')) return '/operations'
  if (app) return `/apps/${encodeURIComponent(app)}/overview`
  return '/overview'
}

export function formatActivity(entry: ActivityEntry): FormattedActivity {
  const event = entry.event
  const data = payload(event)
  const app = eventApp(event)
  const step = text(data.step) ?? 'step'
  const status = text(data.status) ?? 'updated'
  const title = text(data.title) ?? text(data.message) ?? humanizeType(event.type)
  const severity = text(data.severity)

  let sentence: string
  switch (event.type) {
    case 'deploy.step':
      sentence = withApp(`Deploy ${step} ${status}`, app)
      break
    case 'deploy.progress':
      sentence = withApp(`Deploy ${text(data.step) ?? text(data.message) ?? 'progress'}`, app)
      break
    case 'deploy.completed':
    case 'deploy.succeeded':
      sentence = withApp('Deploy succeeded', app)
      break
    case 'deploy.auto_rollback':
      sentence = withApp('Deploy auto rollback started', app)
      break
    case 'deploy.failed':
      sentence = withApp('Deploy failed', app)
      break
    case 'preflight.step':
      sentence = withApp(`Preflight ${step} ${status}`, app)
      break
    case 'preflight.progress':
      sentence = withApp(`Preflight ${text(data.step) ?? text(data.message) ?? 'progress'}`, app)
      break
    case 'preflight.completed':
      sentence = withApp('Preflight completed', app)
      break
    case 'preflight.failed':
      sentence = withApp('Preflight failed', app)
      break
    case 'beacon.event':
      sentence = severity ? `${severity}: ${title}` : title
      break
    case 'snapshot.restored':
      sentence = withApp('Snapshot restored', app)
      break
    case 'snapshot.retention':
      sentence = withApp('Snapshot retention applied', app)
      break
    case 'function.completed':
      sentence = withApp('Function run finished', app)
      break
    case 'app.restarted':
      sentence = withApp('App restarted', app)
      break
    case 'app.scaled':
      sentence = withApp('App scaled', app)
      break
    case 'rollback.started':
      sentence = withApp('Rollback started', app)
      break
    case 'rollback.completed':
      sentence = withApp('Rollback completed', app)
      break
    case 'rollback.failed':
      sentence = withApp('Rollback failed', app)
      break
    case 'canary.promoted':
      sentence = withApp('Canary promoted', app)
      break
    default:
      sentence = withApp(humanizeType(event.type), app)
  }

  return {
    id: entry.id,
    sentence,
    tone: toneFor(event),
    href: hrefFor(event),
    capturedAt: entry.capturedAt,
  }
}

export function collapseActivity(entries: ActivityEntry[]): CollapsedActivity[] {
  const rows: CollapsedActivity[] = []
  for (const entry of entries) {
    const formatted = formatActivity(entry)
    const current = rows[rows.length - 1]
    if (current?.sentence === formatted.sentence) {
      current.count += 1
      continue
    }
    rows.push({ ...formatted, count: 1 })
  }
  return rows.slice(0, 20)
}
