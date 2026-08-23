import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { setStoredTheme, type Theme } from '../lib/theme.ts'
import { StatusBar } from '../components/StatusBar.tsx'
import { Button } from '../components/ui/index.ts'
import type { AppAction, ActivityEntry } from '../runtime/AppRuntime.tsx'
import type { AppStatus } from '../types/index.ts'
import { CommandPalette } from './CommandPalette.tsx'

const navGroups = [
  { label: 'Operate', items: [['/overview', 'Overview', 'fa-gauge-high'], ['/apps', 'Apps', 'fa-grid'], ['/deploys', 'Deploys', 'fa-rocket-launch'], ['/incidents', 'Incidents', 'fa-circle-exclamation'], ['/operations', 'Operations', 'fa-clipboard-check']] },
  { label: 'Understand', items: [['/topology', 'Topology', 'fa-map'], ['/fleet', 'Fleet', 'fa-server']] },
  { label: 'Configure', items: [['/platform', 'Platform', 'fa-sliders']] },
] as const

export function Shell({ children, connected, version, apps, activity, runAction, fleetAvailable }: { children: ReactNode; connected: boolean; version: string; apps: AppStatus[]; activity: ActivityEntry[]; runAction: (appId: string, action: AppAction) => void; fleetAvailable: boolean }) {
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem('norn.sidebar.collapsed') === 'true')
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [theme, setTheme] = useState<Theme>(() => document.documentElement.dataset.theme === 'light' ? 'light' : 'dark')
  const location = useLocation()
  const pageTitle = useMemo(() => {
    if (location.pathname.startsWith('/apps/')) return location.pathname.split('/')[2] ?? 'App'
    if (location.pathname.startsWith('/apps')) return 'Apps'
    if (location.pathname.startsWith('/deploys')) return 'Deploys'
    if (location.pathname.startsWith('/incidents')) return 'Incidents'
    if (location.pathname.startsWith('/operations')) return 'Operations'
    if (location.pathname.startsWith('/topology')) return 'Topology'
    if (location.pathname.startsWith('/fleet')) return 'Fleet'
    if (location.pathname.startsWith('/platform')) return 'Platform'
    return 'Overview'
  }, [location.pathname])

  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen(true)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const updateCollapsed = (next: boolean) => {
    setCollapsed(next)
    localStorage.setItem('norn.sidebar.collapsed', String(next))
  }

  const switchTheme = () => {
    const next = theme === 'light' ? 'dark' : 'light'
    setTheme(next)
    setStoredTheme(next)
  }

  return (
    <div className={`norn-shell ${collapsed ? 'sidebar-collapsed' : ''}`}>
      <a className="skip-link" href="#main-content">Skip to content</a>
      <aside className="sidebar" aria-label="Primary">
        <div className="sidebar-brand">
          <span className="sidebar-mark">N</span>
          <span className="sidebar-title">NORN</span>
          <button className="sidebar-collapse" type="button" aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} onClick={() => updateCollapsed(!collapsed)}>
            <i className={`fawsb ${collapsed ? 'fa-angle-right' : 'fa-angle-left'}`} aria-hidden />
          </button>
        </div>
        <nav className="sidebar-nav">
          {navGroups.map((group) => (
            <div className="sidebar-section" key={group.label}>
              <div className="sidebar-section-label">{group.label}</div>
              {group.items.map(([to, label, icon]) => (
                to === '/fleet' && !fleetAvailable ? null :
                <NavLink key={to} to={to} className={({ isActive }) => `sidebar-link ${isActive ? 'active' : ''}`} title={label}>
                  <i className={`fawsb ${icon}`} aria-hidden />
                  <span>{label}</span>
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
        <div className="sidebar-footer">
          <span className="sidebar-version">norn {version}</span>
          <span className={`ws-status ${connected ? 'connected' : 'disconnected'}`}><span className={`ws-dot ${connected ? 'green' : 'red'}`} />{connected ? 'Live' : 'Reconnecting...'}</span>
        </div>
      </aside>
      <div className="shell-body">
        <header className="page-header">
          <div>
            <h1>{pageTitle}</h1>
          </div>
          <div className="page-header-actions">
            <button className="command-button" type="button" onClick={() => setPaletteOpen(true)}><i className="fawsb fa-magnifying-glass" aria-hidden /> Search <kbd>⌘K</kbd></button>
            <Button
              variant="ghost"
              size="sm"
              icon={theme === 'dark' ? 'fa-moon' : 'fa-sun'}
              aria-label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
              onClick={switchTheme}
            />
            <StatusBar />
          </div>
        </header>
        <main id="main-content" className="shell-main" tabIndex={-1}>{children}</main>
      </div>
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} apps={apps} activity={activity} runAction={runAction} fleetAvailable={fleetAvailable} />
    </div>
  )
}
