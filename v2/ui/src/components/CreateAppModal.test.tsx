import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { CreateAppModal } from './CreateAppModal.tsx'

describe('CreateAppModal', () => {
  it('creates a disabled-by-default endpoint template request', () => {
    const create = vi.fn()
    render(<CreateAppModal open busy={false} error={null} onClose={() => undefined} onCreate={create} />)
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Orders API' } })
    fireEvent.change(screen.getByLabelText('Container port'), { target: { value: '9090' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create draft' }))
    expect(create).toHaveBeenCalledWith({ name: 'ordersapi', kind: 'endpoint', port: 9090 })
    expect(screen.getByText('Deployment disabled')).toBeInTheDocument()
  })

  it('switches to a worker request without a port', () => {
    const create = vi.fn()
    render(<CreateAppModal open busy={false} error={null} onClose={() => undefined} onCreate={create} />)
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'email-worker' } })
    fireEvent.click(screen.getByLabelText('Worker'))
    fireEvent.click(screen.getByRole('button', { name: 'Create draft' }))
    expect(create).toHaveBeenCalledWith({ name: 'email-worker', kind: 'worker' })
  })
})
