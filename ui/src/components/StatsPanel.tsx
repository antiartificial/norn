import { useState, useEffect } from 'react'
import { apiUrl, fetchOpts } from '../lib/api.ts'
import { StatsDrilldown } from './StatsDrilldown.tsx'

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

export type DrilldownType = 'builds' | 'deploys' | 'failures' | 'mostActive' | 'longestUp'

export function StatsPanel() {
  const [stats, setStats] = useState<Stats | null>(null)
  const [drilldown, setDrilldown] = useState<DrilldownType | null>(null)

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

  const toggle = (type: DrilldownType) => {
    setDrilldown(prev => prev === type ? null : type)
  }

  const clickable = (type: DrilldownType) =>
    `stat-card stat-card-clickable${drilldown === type ? ' active' : ''}`

  return (
    <>
      <div className="stats-panel">
        <div className={clickable('builds')} onClick={() => toggle('builds')}>
          <div className="stat-card-label">Builds</div>
          <div className="stat-card-value">{stats.totalBuilds}</div>
          <div className="stat-card-detail">today</div>
        </div>
        <div className={clickable('deploys')} onClick={() => toggle('deploys')}>
          <div className="stat-card-label">Deploys</div>
          <div className="stat-card-value">{stats.totalDeploys}</div>
          <div className="stat-card-detail">today</div>
        </div>
        <div className={clickable('failures')} onClick={() => toggle('failures')}>
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
        <div className={clickable('mostActive')} onClick={() => toggle('mostActive')}>
          <div className="stat-card-label">Most Active</div>
          <div className="stat-card-value">{stats.mostPopularN || '—'}</div>
          <div className="stat-card-detail">{stats.mostPopularApp || 'none'}</div>
        </div>
        <div className={clickable('longestUp')} onClick={() => toggle('longestUp')}>
          <div className="stat-card-label">Longest Up</div>
          <div className="stat-card-value">{stats.longestDuration || '—'}</div>
          <div className="stat-card-detail">{stats.longestApp || 'none'}</div>
        </div>
      </div>
      {drilldown && (
        <StatsDrilldown
          type={drilldown}
          stats={stats}
          onClose={() => setDrilldown(null)}
        />
      )}
    </>
  )
}
