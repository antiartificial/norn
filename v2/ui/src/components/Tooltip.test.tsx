import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { Tooltip } from './Tooltip.tsx'

describe('Tooltip', () => {
  it('renders children', () => {
    render(
      <Tooltip text="Help text">
        <button>Hover me</button>
      </Tooltip>
    )
    expect(screen.getByText('Hover me')).toBeInTheDocument()
  })

  it('does not show tooltip text initially', () => {
    render(
      <Tooltip text="Help text">
        <button>Hover me</button>
      </Tooltip>
    )
    expect(screen.queryByText('Help text')).not.toBeInTheDocument()
  })

  it('shows tooltip after hover delay', async () => {
    vi.useFakeTimers()
    render(
      <Tooltip text="Help text">
        <button>Hover me</button>
      </Tooltip>
    )

    fireEvent.mouseEnter(screen.getByText('Hover me').closest('.tooltip-wrapper')!)
    act(() => { vi.advanceTimersByTime(500) })

    expect(screen.getByText('Help text')).toBeInTheDocument()
    vi.useRealTimers()
  })

  it('hides tooltip on mouse leave', async () => {
    vi.useFakeTimers()
    render(
      <Tooltip text="Help text">
        <button>Hover me</button>
      </Tooltip>
    )

    const wrapper = screen.getByText('Hover me').closest('.tooltip-wrapper')!
    fireEvent.mouseEnter(wrapper)
    act(() => { vi.advanceTimersByTime(500) })

    expect(screen.getByText('Help text')).toBeInTheDocument()

    fireEvent.mouseLeave(wrapper)

    expect(screen.queryByText('Help text')).not.toBeInTheDocument()
    vi.useRealTimers()
  })
})
