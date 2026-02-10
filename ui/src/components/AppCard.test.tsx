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
    pods: [],
    ...overrides,
  }
}

describe('AppCard', () => {
  it('renders app name and role', () => {
    render(
      <AppCard
        app={makeApp()}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('test-app')).toBeInTheDocument()
    expect(screen.getByText('webserver')).toBeInTheDocument()
  })

  it('shows healthy state', () => {
    render(
      <AppCard
        app={makeApp({ healthy: true })}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('healthy')).toBeInTheDocument()
  })

  it('shows unhealthy state', () => {
    render(
      <AppCard
        app={makeApp({ healthy: false })}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('unhealthy')).toBeInTheDocument()
  })

  it('shows commit SHA truncated to 7 chars', () => {
    render(
      <AppCard
        app={makeApp({ commitSha: 'abc123def456' })}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('abc123d')).toBeInTheDocument()
  })

  it('shows "never deployed" when no commit/deploy', () => {
    render(
      <AppCard
        app={makeApp({ commitSha: '', deployedAt: '' })}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('never deployed')).toBeInTheDocument()
  })

  it('shows hostnames', () => {
    render(
      <AppCard
        app={makeApp()}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('test.example.com')).toBeInTheDocument()
    expect(screen.getByText('test-svc')).toBeInTheDocument()
  })

  it('shows service badges', () => {
    render(
      <AppCard
        app={makeApp()}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('PG')).toBeInTheDocument()
    expect(screen.getAllByText('KV').length).toBeGreaterThan(0)
  })

  it('calls onDeploy when Deploy clicked', () => {
    const onDeploy = vi.fn()
    render(
      <AppCard
        app={makeApp()}
        onDeploy={onDeploy}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    fireEvent.click(screen.getByText('Deploy'))
    expect(onDeploy).toHaveBeenCalledWith('test-app')
  })

  it('calls onRestart when Restart clicked', () => {
    const onRestart = vi.fn()
    render(
      <AppCard
        app={makeApp()}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={onRestart}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    fireEvent.click(screen.getByText('Restart'))
    expect(onRestart).toHaveBeenCalledWith('test-app')
  })

  it('calls onViewLogs when Logs clicked', () => {
    const onViewLogs = vi.fn()
    render(
      <AppCard
        app={makeApp()}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={onViewLogs}
      />
    )
    fireEvent.click(screen.getByText('Logs'))
    expect(onViewLogs).toHaveBeenCalledWith('test-app')
  })

  it('shows secret count', () => {
    render(
      <AppCard
        app={makeApp()}
        onDeploy={vi.fn()}
        onForge={vi.fn()}
        onRestart={vi.fn()}
        onRollback={vi.fn()}
        onViewLogs={vi.fn()}
      />
    )
    expect(screen.getByText('2')).toBeInTheDocument()
  })
})
