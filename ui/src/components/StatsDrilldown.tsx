import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import type { DrilldownType } from './StatsPanel.tsx'
import type { Deployment } from '../types/index.ts'

interface Stats {
  mostPopularApp: string
}

interface LeaderboardEntry {
  rank: number
  pod: string
  app: string
  uptime: string
  startedAt: string
  restarts: number
  phase: string
}

interface Props {
  type: DrilldownType
  stats: Stats
  onClose: () => void
}

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const d = Math.floor(hr / 24)
  return `${d}d ago`
}

function duration(started: string, finished?: string): string {
  if (!finished) return '...'
  const ms = new Date(finished).getTime() - new Date(started).getTime()
  if (ms < 1000) return `${ms}ms`
  const sec = Math.floor(ms / 1000)
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60)
  const s = sec % 60
  return `${min}m ${s}s`
}

function statusClass(status: string): string {
  switch (status) {
    case 'deployed': return 'deployed'
    case 'failed':
    case 'rolled_back': return 'failed'
    case 'queued': return 'queued'
    default: return 'running'
  }
}

const titles: Record<DrilldownType, string> = {
  builds: 'Today\u2019s Builds',
  deploys: 'Successful Deploys',
  failures: 'Failed Deployments',
  mostActive: 'Most Active App',
  longestUp: 'Uptime Leaderboard',
}

export function StatsDrilldown({ type, stats, onClose }: Props) {
  return (
    <div className="stats-drilldown">
      <div className="stats-drilldown-header">
        <h4>{titles[type]}</h4>
        <button className="btn-close" onClick={onClose}>&times;</button>
      </div>
      {type === 'longestUp' ? (
        <UptimeLeaderboard />
      ) : (
        <DeploymentList type={type} stats={stats} />
      )}
    </div>
  )
}

function DeploymentList({ type, stats }: { type: DrilldownType; stats: Stats }) {
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const params = new URLSearchParams({ limit: '20' })
    switch (type) {
      case 'deploys':
        params.set('status', 'deployed')
        break
      case 'failures':
        params.set('status', 'failed')
        break
      case 'mostActive':
        if (stats.mostPopularApp) params.set('app', stats.mostPopularApp)
        break
    }

    fetch(apiUrl(`/api/deployments?${params}`), fetchOpts)
      .then(r => r.json())
      .then(data => {
        setDeployments(data.deployments || [])
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }, [type, stats.mostPopularApp])

  if (loading) return <div className="drilldown-loading">Loading...</div>
  if (deployments.length === 0) return <div className="drilldown-empty">No deployments found</div>

  return (
    <div className="stats-drilldown-list">
      {deployments.map(d => (
        <div className="drilldown-row" key={d.id}>
          <span className={`history-status ${statusClass(d.status)}`}>
            {d.status.replace('_', ' ')}
          </span>
          <span className="drilldown-app">{d.app}</span>
          <span className="drilldown-sha">{d.commitSha.slice(0, 7)}</span>
          {type === 'failures' && d.error ? (
            <span className="drilldown-error" title={d.error}>{d.error}</span>
          ) : (
            <span className="drilldown-error" />
          )}
          <span className="drilldown-time">
            {duration(d.startedAt, d.finishedAt)}
          </span>
          <span className="drilldown-time">{relativeTime(d.startedAt)}</span>
        </div>
      ))}
    </div>
  )
}

function rankClass(rank: number): string {
  if (rank === 1) return 'leaderboard-rank gold'
  if (rank === 2) return 'leaderboard-rank silver'
  if (rank === 3) return 'leaderboard-rank bronze'
  return 'leaderboard-rank'
}

function UptimeLeaderboard() {
  const [entries, setEntries] = useState<LeaderboardEntry[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch(apiUrl('/api/stats/uptime-leaderboard'), fetchOpts)
      .then(r => r.json())
      .then(data => {
        setEntries(data || [])
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }, [])

  if (loading) return <div className="drilldown-loading">Loading...</div>
  if (entries.length === 0) return <div className="drilldown-empty">No pods found</div>

  return (
    <div className="stats-drilldown-list">
      {entries.map(e => (
        <div className="leaderboard-row" key={e.pod}>
          <span className={rankClass(e.rank)}>{e.rank}</span>
          <div className="leaderboard-info">
            <div className="leaderboard-pod">{e.pod}</div>
            <div className="leaderboard-app">{e.app}</div>
          </div>
          <span className="leaderboard-uptime">{e.uptime}</span>
          <div className="leaderboard-meta">
            <div>{relativeTime(e.startedAt)}</div>
            {e.restarts > 0 && (
              <div className="leaderboard-restarts">{e.restarts} restart{e.restarts !== 1 ? 's' : ''}</div>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}
