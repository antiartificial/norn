import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { DeployPanel } from './DeployPanel.tsx'

describe('DeployPanel', () => {
  it('renders the app id in header', () => {
    render(
      <DeployPanel
        appId="my-app"
        steps={[]}
        status="deploying"
      />
    )
    expect(screen.getByText(/my-app/)).toBeInTheDocument()
  })

  it('renders pipeline steps', () => {
    const steps = [
      { step: 'build', status: 'completed' },
      { step: 'test', status: 'running' },
    ]
    render(
      <DeployPanel
        appId="my-app"
        steps={steps}
        status="deploying"
      />
    )
    expect(screen.getByText('build')).toBeInTheDocument()
    expect(screen.getByText('test')).toBeInTheDocument()
    expect(screen.getByText('completed')).toBeInTheDocument()
    expect(screen.getByText('running')).toBeInTheDocument()
  })

  it('shows step duration when present', () => {
    const steps = [
      { step: 'build', status: 'completed', durationMs: 2500 },
    ]
    render(
      <DeployPanel
        appId="my-app"
        steps={steps}
        status="deploying"
      />
    )
    expect(screen.getByText('2.5s')).toBeInTheDocument()
  })

  it('shows error message when failed', () => {
    render(
      <DeployPanel
        appId="my-app"
        steps={[{ step: 'build', status: 'failed' }]}
        status="failed"
        error="build command exited with code 1"
      />
    )
    expect(screen.getByText(/build command exited with code 1/)).toBeInTheDocument()
  })

  it('does not show error when status is not failed', () => {
    render(
      <DeployPanel
        appId="my-app"
        steps={[]}
        status="deploying"
        error="should not show"
      />
    )
    expect(screen.queryByText(/should not show/)).not.toBeInTheDocument()
  })
})
