import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DeployHistory } from './DeployHistory.tsx'
import type { Deployment } from '../types/index.ts'

function deploy(id: string, app: string, startedAt: string): Deployment {
  return {
    id,
    app,
    commitSha: `${id}abcdef`,
    imageTag: id,
    sagaId: `saga-${id}`,
    status: 'deployed',
    startedAt,
    finishedAt: new Date(new Date(startedAt).getTime() + 10_000).toISOString(),
  }
}

function json(data: unknown) {
  return new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } })
}

describe('DeployHistory', () => {
  beforeEach(() => {
    const deployments = [
      deploy('api-1', 'api', '2026-01-01T00:00:00.000Z'),
      deploy('api-2', 'api', '2026-01-01T00:01:00.000Z'),
      deploy('worker-1', 'worker', '2026-01-01T00:02:00.000Z'),
    ]
    vi.stubGlobal('fetch', vi.fn(async () => json({ schemaVersion: 'norn.deployments/v1', deployments, count: deployments.length, offset: 0 })))
  })

  it('expands earlier deployments for a consecutive same-app run', async () => {
    render(<DeployHistory apps={['api', 'worker']} onClose={() => undefined} />)

    expect(await screen.findByText('api')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /\+1 earlier/i })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('api-2abc')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /\+1 earlier/i }))

    await waitFor(() => expect(screen.getByRole('button', { name: /\+1 earlier/i })).toHaveAttribute('aria-expanded', 'true'))
    expect(screen.getByText('api-2ab')).toBeInTheDocument()
  })
})
