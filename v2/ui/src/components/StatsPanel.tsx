import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface UptimeEntry {
  allocId: string
  jobId: string
  taskGroup: string
  uptime: string
  nodeName: string
  startedAt: string
}

interface StatsResponse {
  deploys: {
    total: number
    success: number
    failed: number
    mostPopularApp?: string
    mostPopularN?: number
  }
  appCount: number
  totalAllocs: number
  runningAllocs: number
  uptimeLeaderboard?: UptimeEntry[]
}

export function StatsPanel() {
  const [stats, setStats] = useState<StatsResponse | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    async function load() {
      try {
        const res = await fetch(apiUrl('/api/stats'), fetchOpts)
        if (res.ok) setStats(await res.json())
      } catch { /* */ }
      setLoading(false)
    }
    load()
    const interval = setInterval(load, 15000)
    return () => clearInterval(interval)
  }, [])

  if (loading) return <div className="stats-loading">Loading stats...</div>
  if (!stats) return <div className="stats-loading">Failed to load stats</div>

  const failRate = stats.deploys.total > 0
    ? Math.round((stats.deploys.failed / stats.deploys.total) * 100)
    : 0

  return (
    <div className="stats-panel">
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-value">{stats.appCount}</div>
          <div className="stat-label">Apps</div>
        </div>
        <div className="stat-card">
          <div className="stat-value">
            {stats.runningAllocs}<span className="stat-dim">/{stats.totalAllocs}</span>
          </div>
          <div className="stat-label">Allocations</div>
        </div>
        <div className="stat-card">
          <div className="stat-value">{stats.deploys.total}</div>
          <div className="stat-label">Deploys today</div>
        </div>
        <div className="stat-card">
          <div className="stat-value stat-green">{stats.deploys.success}</div>
          <div className="stat-label">Succeeded</div>
        </div>
        <div className="stat-card">
          <div className="stat-value stat-red">{stats.deploys.failed}</div>
          <div className="stat-label">Failed{failRate > 0 ? ` (${failRate}%)` : ''}</div>
        </div>
        {stats.deploys.mostPopularApp && (
          <div className="stat-card">
            <div className="stat-value stat-small">{stats.deploys.mostPopularApp}</div>
            <div className="stat-label">Most deployed ({stats.deploys.mostPopularN}x)</div>
          </div>
        )}
      </div>

      {stats.uptimeLeaderboard && stats.uptimeLeaderboard.length > 0 && (
        <div className="stats-leaderboard">
          <h4>Uptime Leaderboard</h4>
          <div className="leaderboard-list">
            <div className="leaderboard-header">
              <span className="lb-rank">#</span>
              <span className="lb-job">Job</span>
              <span className="lb-group">Group</span>
              <span className="lb-uptime">Uptime</span>
              <span className="lb-node">Node</span>
            </div>
            {stats.uptimeLeaderboard.map((entry, i) => (
              <div key={entry.allocId} className="leaderboard-row">
                <span className="lb-rank">{i + 1}</span>
                <span className="lb-job">{entry.jobId}</span>
                <span className="lb-group">{entry.taskGroup}</span>
                <span className="lb-uptime">{entry.uptime}</span>
                <span className="lb-node">{entry.nodeName}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
