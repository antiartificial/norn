import { describe, expect, it } from 'vitest'
import { appendStepEvent, applyDurableOperationSnapshot, deployProgressReducer, upsertStep, type DeployProgressState } from './useDeployProgress.ts'

describe('deploy progress reducer', () => {
  it('upserts steps without dropping existing events', () => {
    const steps = upsertStep([{ step: 'build', status: 'running', events: [{ message: 'started', timestamp: 1 }] }], { step: 'build', status: 'complete' })
    expect(steps).toHaveLength(1)
    expect(steps[0].status).toBe('complete')
    expect(steps[0].events).toHaveLength(1)
  })

  it('appends events to existing steps only', () => {
    const appended = appendStepEvent([{ step: 'build', status: 'running' }], 'build', { message: 'log', timestamp: 2 })
    const missing = appendStepEvent(appended, 'test', { message: 'skip', timestamp: 3 })
    expect(appended[0].events?.[0].message).toBe('log')
    expect(missing).toBe(appended)
  })

  it('handles deploy step, progress, completion, and failure events', () => {
    const initial = deployProgressReducer(null, { type: 'deploy.step', appId: 'api', payload: { step: 'build', status: 'running', sagaId: 's1' } }, 10)
    expect(initial).toMatchObject({ appId: 'api', operation: 'deploy', status: 'running', sagaId: 's1' })
    expect(initial?.steps).toHaveLength(1)

	const progressed = deployProgressReducer(initial, { type: 'deploy.progress', appId: 'api', payload: { step: 'build', message: 'pulling', node: 'n1', sagaId: 's1' } }, 20)
    expect(progressed?.steps[0].events?.[0]).toMatchObject({ message: 'pulling', timestamp: 20, node: 'n1' })

	const completed = deployProgressReducer(progressed, { type: 'deploy.completed', appId: 'api', payload: { sagaId: 's1' } }, 30)
    expect(completed?.status).toBe('deployed')

	const failed = deployProgressReducer(progressed, { type: 'deploy.failed', appId: 'api', payload: { error: 'bad image', sagaId: 's1' } }, 40)
    expect(failed?.status).toBe('failed')
    expect(failed?.error).toBe('bad image')
  })

  it('handles preflight result states', () => {
    const state: DeployProgressState = { appId: 'web', operation: 'preflight', steps: [], status: 'queued' }
    expect(deployProgressReducer(state, { type: 'preflight.completed', appId: 'web', payload: {} })?.status).toBe('passed')
    expect(deployProgressReducer(state, { type: 'preflight.failed', appId: 'web', payload: { message: 'invalid' } })?.error).toBe('invalid')
  })

	it('ignores unrelated and stale events after a terminal accepted replay', () => {
	  const terminal: DeployProgressState = { appId: 'api', operation: 'deploy', steps: [], status: 'deployed', sagaId: 's1', operationId: 'op-1' }
	  expect(deployProgressReducer(terminal, { type: 'deploy.step', appId: 'worker', payload: { step: 'build', status: 'running', sagaId: 's2' } })).toBe(terminal)
	  expect(deployProgressReducer(terminal, { type: 'deploy.step', appId: 'api', payload: { step: 'build', status: 'running', sagaId: 's1' } })).toBe(terminal)

	  const running: DeployProgressState = { ...terminal, status: 'running' }
	  expect(deployProgressReducer(running, { type: 'deploy.failed', appId: 'api', payload: { error: 'old failure', sagaId: 'old-saga' } })).toBe(running)
	})

	it('uses the durable operation snapshot as the terminal authority', () => {
	  const running: DeployProgressState = { appId: 'api', operation: 'deploy', steps: [], status: 'running', operationId: 'op-1' }
	  expect(applyDurableOperationSnapshot(running, { id: 'op-1', status: 'succeeded' })?.status).toBe('deployed')
	  expect(applyDurableOperationSnapshot(running, { id: 'op-1', status: 'failed', lastError: 'build failed' })).toMatchObject({ status: 'failed', error: 'build failed', retryMode: 'new-intent' })
	  expect(applyDurableOperationSnapshot(running, { id: 'other', status: 'failed' })).toBe(running)
	})
})
