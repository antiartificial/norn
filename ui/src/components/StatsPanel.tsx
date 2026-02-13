import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'

interface Stats {
  totalBuilds: number
  totalDeploys: number
  totalFailures: number
  services: number
  containers: number
  mostPopularApp: string
  mostPopularN: number
  longestPod: string
  longestApp: string
  longestDuration: string
}

export function StatsPanel() {
  const [stats, setStats] = useState<Stats | null>(null)

  useEffect(() => {
    const load = () => {
      fetch(apiUrl('/api/stats'), fetchOpts)
        .then(r => r.json())
        .then(setStats)
        .catch(() => {})
    }
    load()
    const id = setInterval(load, 30_000)
    return () => clearInterval(id)
  }, [])

  if (!stats) return null

  return (
    <div className="stats-panel">
      <div className="stat-card">
        <div className="stat-card-label">Builds</div>
        <div className="stat-card-value">{stats.totalBuilds}</div>
        <div className="stat-card-detail">today</div>
      </div>
      <div className="stat-card">
        <div className="stat-card-label">Deploys</div>
        <div className="stat-card-value">{stats.totalDeploys}</div>
        <div className="stat-card-detail">today</div>
      </div>
      <div className="stat-card">
        <div className="stat-card-label">Failures</div>
        <div className={`stat-card-value${stats.totalFailures > 0 ? ' fail' : ''}`}>
          {stats.totalFailures}
        </div>
        <div className="stat-card-detail">today</div>
      </div>
      <div className="stat-card">
        <div className="stat-card-label">Services</div>
        <div className="stat-card-value">{stats.services}</div>
        <div className="stat-card-detail">discovered</div>
      </div>
      <div className="stat-card">
        <div className="stat-card-label">Containers</div>
        <div className="stat-card-value">{stats.containers}</div>
        <div className="stat-card-detail">running</div>
      </div>
      <div className="stat-card">
        <div className="stat-card-label">Most Active</div>
        <div className="stat-card-value">{stats.mostPopularN || '—'}</div>
        <div className="stat-card-detail">{stats.mostPopularApp || 'none'}</div>
      </div>
      <div className="stat-card">
        <div className="stat-card-label">Longest Up</div>
        <div className="stat-card-value">{stats.longestDuration || '—'}</div>
        <div className="stat-card-detail">{stats.longestApp || 'none'}</div>
      </div>
    </div>
  )
}
