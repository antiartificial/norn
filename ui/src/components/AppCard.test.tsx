import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { AppCard } from './AppCard.tsx'
import type { AppStatus } from '../types/index.ts'

function makeApp(overrides: Partial<AppStatus> = {}): AppStatus {
  return {
    spec: {
      app: 'test-app',
      role: 'webserver',
      port: 3000,
      healthcheck: '/health',
      hosts: { external: 'test.example.com', internal: 'test-svc' },
      services: {
        postgres: { database: 'test_db' },
        kv: { namespace: 'test' },
        events: { topics: ['user.created'] },
      },
      secrets: ['DB_URL', 'API_KEY'],
    },
    healthy: true,
    ready: '2/2',
    commitSha: 'abc123def456',
    deployedAt: new Date().toISOString(),
    pods: [{ name: 'test-pod-1', status: 'Running', ready: true, restarts: 0, startedAt: new Date().toISOString() }],
    forgeState: { app: 'test-app', status: 'forged', steps: [], resources: {} },
    ...overrides,
  }
}

const defaultHandlers = {
  onDeploy: vi.fn(),
  onForge: vi.fn(),
  onTeardown: vi.fn(),
  onRestart: vi.fn(),
  onRollback: vi.fn(),
  onViewLogs: vi.fn(),
}

describe('AppCard', () => {
  it('renders app name and role', () => {
    render(<AppCard app={makeApp()} {...defaultHandlers} />)
    expect(screen.getByText('test-app')).toBeInTheDocument()
    expect(screen.getByText('webserver')).toBeInTheDocument()
  })

  it('shows healthy state', () => {
    render(<AppCard app={makeApp({ healthy: true })} {...defaultHandlers} />)
    expect(screen.getByText('healthy')).toBeInTheDocument()
  })

  it('shows unhealthy state', () => {
    render(<AppCard app={makeApp({ healthy: false })} {...defaultHandlers} />)
    expect(screen.getByText('unhealthy')).toBeInTheDocument()
  })

  it('shows commit SHA truncated to 7 chars', () => {
    render(<AppCard app={makeApp({ commitSha: 'abc123def456' })} {...defaultHandlers} />)
    expect(screen.getByText('abc123d')).toBeInTheDocument()
  })

  it('shows "never deployed" when no commit/deploy', () => {
    render(<AppCard app={makeApp({ commitSha: '', deployedAt: '' })} {...defaultHandlers} />)
    expect(screen.getByText('never deployed')).toBeInTheDocument()
  })

  it('shows hostnames', () => {
    render(<AppCard app={makeApp()} {...defaultHandlers} />)
    expect(screen.getByText('test.example.com')).toBeInTheDocument()
    expect(screen.getByText('test-svc')).toBeInTheDocument()
  })

  it('shows service badges', () => {
    render(<AppCard app={makeApp()} {...defaultHandlers} />)
    expect(screen.getByText('PG')).toBeInTheDocument()
    expect(screen.getAllByText('KV').length).toBeGreaterThan(0)
  })

  it('calls onDeploy when Deploy clicked (forged state)', () => {
    const onDeploy = vi.fn()
    render(<AppCard app={makeApp()} {...defaultHandlers} onDeploy={onDeploy} />)
    fireEvent.click(screen.getByText('Deploy'))
    expect(onDeploy).toHaveBeenCalledWith('test-app')
  })

  it('calls onRestart when Restart clicked (forged + deployed)', () => {
    const onRestart = vi.fn()
    render(<AppCard app={makeApp()} {...defaultHandlers} onRestart={onRestart} />)
    fireEvent.click(screen.getByText('Restart'))
    expect(onRestart).toHaveBeenCalledWith('test-app')
  })

  it('calls onViewLogs when Logs clicked (has pods)', () => {
    const onViewLogs = vi.fn()
    render(<AppCard app={makeApp()} {...defaultHandlers} onViewLogs={onViewLogs} />)
    fireEvent.click(screen.getByText('Logs'))
    expect(onViewLogs).toHaveBeenCalledWith('test-app')
  })

  it('shows secret count', () => {
    render(<AppCard app={makeApp()} {...defaultHandlers} />)
    expect(screen.getByText('2')).toBeInTheDocument()
  })

  it('shows Forge button when unforged', () => {
    render(
      <AppCard
        app={makeApp({ forgeState: undefined, pods: [], ready: '0/0', commitSha: '', deployedAt: '' })}
        {...defaultHandlers}
      />
    )
    expect(screen.getByText('Forge')).toBeInTheDocument()
    expect(screen.queryByText('Deploy')).not.toBeInTheDocument()
    expect(screen.queryByText('Teardown')).not.toBeInTheDocument()
  })

  it('shows Resume Forge and Teardown when forge_failed', () => {
    render(
      <AppCard
        app={makeApp({ forgeState: { app: 'test-app', status: 'forge_failed', steps: [], resources: {}, error: 'step failed' } })}
        {...defaultHandlers}
      />
    )
    expect(screen.getByText('Resume Forge')).toBeInTheDocument()
    expect(screen.getByText('Teardown')).toBeInTheDocument()
    expect(screen.queryByText('Deploy')).not.toBeInTheDocument()
  })

  it('shows forged badge when forged', () => {
    render(<AppCard app={makeApp()} {...defaultHandlers} />)
    expect(screen.getByText('forged')).toBeInTheDocument()
  })

  it('calls onTeardown when Teardown clicked', () => {
    const onTeardown = vi.fn()
    render(<AppCard app={makeApp()} {...defaultHandlers} onTeardown={onTeardown} />)
    fireEvent.click(screen.getByText('Teardown'))
    expect(onTeardown).toHaveBeenCalledWith('test-app')
  })

  it('shows Deploy, Teardown, Restart, Rollback, Logs when forged with deploy', () => {
    render(<AppCard app={makeApp()} {...defaultHandlers} />)
    expect(screen.getByText('Deploy')).toBeInTheDocument()
    expect(screen.getByText('Teardown')).toBeInTheDocument()
    expect(screen.getByText('Restart')).toBeInTheDocument()
    expect(screen.getByText('Rollback')).toBeInTheDocument()
    expect(screen.getByText('Logs')).toBeInTheDocument()
    expect(screen.queryByText('Forge')).not.toBeInTheDocument()
  })

  it('shows partial badge when forge_failed', () => {
    render(
      <AppCard
        app={makeApp({ forgeState: { app: 'test-app', status: 'forge_failed', steps: [], resources: {} } })}
        {...defaultHandlers}
      />
    )
    expect(screen.getByText('partial')).toBeInTheDocument()
  })
})
