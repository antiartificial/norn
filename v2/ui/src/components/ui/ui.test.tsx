import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Button, ConfirmDialog, CopyButton, EmptyState, ErrorState, Modal, Tab, TabPanel, Tabs, TabsList, ToastProvider, useToast } from './index.ts'

function ToastProbe() {
  const { toast } = useToast()
  return <button type="button" onClick={() => toast({ kind: 'success', title: 'Saved', description: 'Changes persisted', durationMs: 1000 })}>Show toast</button>
}

function ModalProbe() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>Invoker</button>
      <Modal open={open} title="Scale app" onClose={() => setOpen(false)}>
        <button type="button">First</button>
        <button type="button">Last</button>
      </Modal>
    </>
  )
}

function StackedModalProbe() {
  const [outer, setOuter] = useState(false)
  const [inner, setInner] = useState(false)
  return (
    <>
      <button type="button" onClick={() => setOuter(true)}>Open outer</button>
      <Modal open={outer} title="Outer" onClose={() => setOuter(false)}>
        <button type="button" onClick={() => setInner(true)}>Open inner</button>
        <Modal open={inner} title="Inner" onClose={() => setInner(false)}>
          <button type="button">Inner action</button>
        </Modal>
      </Modal>
    </>
  )
}

describe('ui primitives', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('renders Button variants and loading state accessibly', () => {
    const onClick = vi.fn()
    render(<Button variant="primary" loading onClick={onClick}>Deploy</Button>)

    const button = screen.getByRole('button', { name: /deploy/i })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('aria-busy', 'true')
    expect(button).toHaveClass('ui-button-primary')
    fireEvent.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('Modal traps focus, closes with Escape, and returns focus', async () => {
    render(<ModalProbe />)

    const invoker = screen.getByRole('button', { name: 'Invoker' })
    invoker.focus()
    fireEvent.click(invoker)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Close modal' })).toHaveFocus())
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })
    expect(screen.getByRole('button', { name: 'Last' })).toHaveFocus()
    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(invoker).toHaveFocus()
  })

  it('Modal calls close on backdrop click', () => {
    const onClose = vi.fn()
    render(<Modal open title="Inspect" onClose={onClose}>Body</Modal>)
    fireEvent.mouseDown(screen.getByRole('dialog').parentElement!)
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-modal', 'true')
  })

  it('ConfirmDialog confirms and cancels with danger affordance', () => {
    const onClose = vi.fn()
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog
        open
        title="Restore snapshot"
        message="Restore this snapshot?"
        consequence="Current data will be replaced."
        danger
        confirmLabel="Restore"
        onClose={onClose}
        onConfirm={onConfirm}
      />,
    )

    expect(screen.getByText('Current data will be replaced.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Restore' })).toHaveClass('ui-button-danger')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByRole('button', { name: 'Restore' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('ToastProvider shows, pauses, resumes, and dismisses toasts', async () => {
    vi.useFakeTimers()
    render(
      <ToastProvider>
        <ToastProbe />
      </ToastProvider>,
    )

    act(() => fireEvent.click(screen.getByRole('button', { name: 'Show toast' })))
    expect(screen.getByRole('status')).toHaveTextContent('Saved')
    expect(screen.getByText('Changes persisted')).toBeInTheDocument()
    fireEvent.mouseEnter(screen.getByText('Saved').closest('.ui-toast')!)
    act(() => vi.advanceTimersByTime(1200))
    expect(screen.getByText('Saved')).toBeInTheDocument()
    fireEvent.mouseLeave(screen.getByText('Saved').closest('.ui-toast')!)
    act(() => vi.advanceTimersByTime(1000))
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  })

  it('ToastProvider pauses auto-dismiss while focused', () => {
    vi.useFakeTimers()
    render(
      <ToastProvider>
        <ToastProbe />
      </ToastProvider>,
    )

    act(() => fireEvent.click(screen.getByRole('button', { name: 'Show toast' })))
    const toast = screen.getByText('Saved').closest('.ui-toast')!
    fireEvent.focus(toast)
    act(() => vi.advanceTimersByTime(1200))
    expect(screen.getByText('Saved')).toBeInTheDocument()
    fireEvent.blur(toast)
    act(() => vi.advanceTimersByTime(1000))
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  })

  it('only the top focus layer handles Escape', async () => {
    render(<StackedModalProbe />)

    fireEvent.click(screen.getByRole('button', { name: 'Open outer' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Open inner' }))
    expect(screen.getByRole('dialog', { name: 'Outer' })).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: 'Inner' })).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Inner' })).not.toBeInTheDocument())
    expect(screen.getByRole('dialog', { name: 'Outer' })).toBeInTheDocument()
  })

  it('Tabs use roving tabindex and arrow-key activation', async () => {
    render(
      <Tabs defaultValue="overview">
        <TabsList aria-label="App sections">
          <Tab value="overview">Overview</Tab>
          <Tab value="logs">Logs</Tab>
          <Tab value="deploys">Deploys</Tab>
        </TabsList>
        <TabPanel value="overview">Overview panel</TabPanel>
        <TabPanel value="logs">Logs panel</TabPanel>
        <TabPanel value="deploys">Deploys panel</TabPanel>
      </Tabs>,
    )

    const overview = screen.getByRole('tab', { name: 'Overview' })
    const logs = screen.getByRole('tab', { name: 'Logs' })
    expect(overview).toHaveAttribute('aria-selected', 'true')
    expect(logs).toHaveAttribute('tabindex', '-1')
    overview.focus()
    fireEvent.keyDown(screen.getByRole('tablist'), { key: 'ArrowRight' })
    await act(async () => undefined)
    expect(logs).toHaveFocus()
    expect(logs).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByText('Overview panel')).not.toBeInTheDocument()
    expect(screen.getByText('Logs panel')).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole('tablist'), { key: 'End' })
    await act(async () => undefined)
    expect(screen.getByRole('tab', { name: 'Deploys' })).toHaveFocus()
  })

  it('Tabs keyboard navigation is scoped to the active tablist', async () => {
    render(
      <>
        <Tabs defaultValue="a">
          <TabsList aria-label="First">
            <Tab value="a">First A</Tab>
            <Tab value="b">First B</Tab>
          </TabsList>
          <TabPanel value="a">First A panel</TabPanel>
          <TabPanel value="b">First B panel</TabPanel>
        </Tabs>
        <Tabs defaultValue="a">
          <TabsList aria-label="Second">
            <Tab value="a">Second A</Tab>
            <Tab value="b">Second B</Tab>
          </TabsList>
          <TabPanel value="a">Second A panel</TabPanel>
          <TabPanel value="b">Second B panel</TabPanel>
        </Tabs>
      </>,
    )

    screen.getByRole('tab', { name: 'Second A' }).focus()
    fireEvent.keyDown(screen.getByRole('tablist', { name: 'Second' }), { key: 'ArrowRight' })
    await act(async () => undefined)
    expect(screen.getByRole('tab', { name: 'Second B' })).toHaveFocus()
    expect(screen.getByRole('tab', { name: 'First A' })).toHaveAttribute('aria-selected', 'true')
  })

  it('EmptyState and ErrorState expose expected content and retry action', () => {
    const onRetry = vi.fn()
    render(
      <>
        <EmptyState icon="*" title="No apps" hint="Create an infraspec." action={<button type="button">Create</button>} />
        <ErrorState message="API unavailable" onRetry={onRetry} />
      </>,
    )

    expect(screen.getByText('No apps')).toBeInTheDocument()
    expect(screen.getByText('Create an infraspec.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create' })).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('API unavailable')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('CopyButton writes to clipboard and shows feedback', async () => {
    vi.useFakeTimers()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    render(<CopyButton value="https://norn.local" label="Copy endpoint" />)

    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Copy endpoint to clipboard' })))
    expect(writeText).toHaveBeenCalledWith('https://norn.local')
    expect(screen.getByRole('status')).toHaveTextContent('Copied')
    act(() => vi.advanceTimersByTime(1300))
    expect(screen.queryByText('Copied')).not.toBeInTheDocument()
  })
})
