import type { AppStatus } from '../types/index.ts'
import type { StatusTone } from '../components/ui/index.ts'

export function relativeTime(value?: string): string {
  if (!value) return 'unknown'
  const diff = Date.now() - new Date(value).getTime()
  const sec = Math.max(0, Math.floor(diff / 1000))
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  return `${Math.floor(hr / 24)}d ago`
}

export function statusTone(status?: string): StatusTone {
  const s = status?.toLowerCase() ?? ''
  if (['healthy', 'up', 'deployed', 'succeeded', 'complete', 'completed', 'running'].includes(s)) return 'success'
  if (['queued', 'building', 'deploying', 'testing', 'pending'].includes(s)) return 'info'
  if (['warning', 'idle', 'snoozed'].includes(s)) return 'warning'
  if (['failed', 'critical', 'down', 'unhealthy'].includes(s)) return 'danger'
  return 'neutral'
}

export function deploymentStatus(status?: string): string {
  return (status || 'unknown').replaceAll('_', ' ')
}

export function repoWebURL(url?: string, repoWeb?: string): string | null {
  if (repoWeb) return repoWeb
  if (!url) return null
  const gitea = url.match(/gitea\.default\.svc\.cluster\.local:\d+\/(.+?)(?:\.git)?$/)
  if (gitea) return `https://gitea.slopistry.com/${gitea[1]}`
  const gh = url.match(/github\.com[:/](.+?)(?:\.git)?$/)
  if (gh) return `https://github.com/${gh[1]}`
  return null
}

export function appGroups(app: AppStatus): { name: string; current: number }[] {
  const groupMap: Record<string, number> = {}
  for (const allocation of app.allocations ?? []) {
    if (allocation.status === 'running') groupMap[allocation.taskGroup] = (groupMap[allocation.taskGroup] ?? 0) + 1
  }
  const groups = Object.entries(groupMap).map(([name, current]) => ({ name, current }))
  if (groups.length === 0) {
    for (const name of Object.keys(app.spec.processes ?? {})) groups.push({ name, current: 0 })
  }
  return groups
}
