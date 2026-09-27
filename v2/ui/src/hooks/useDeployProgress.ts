import { useCallback, useState } from 'react'
import type { HubEvent } from '../types/ws.ts'

export interface StepEvent {
  message: string
  timestamp: number
  allocId?: string
  node?: string
  allocStatus?: string
}

export interface DeployStep {
  step: string
  status: string
  events?: StepEvent[]
}

export interface DeployProgressState {
  appId: string
  operation: 'deploy' | 'preflight'
  steps: DeployStep[]
  status: string
  sagaId?: string
  operationId?: string
  replayed?: boolean
  retryMode?: 'same-intent' | 'new-intent'
  pollError?: string
  error?: string
}

export interface DurableOperationSnapshot {
  id?: string
  status?: string
  message?: string
  lastError?: string
}

export function applyDurableOperationSnapshot(state: DeployProgressState | null, operation: DurableOperationSnapshot): DeployProgressState | null {
  if (!state || !state.operationId || operation.id !== state.operationId) return state
  if (operation.status === 'succeeded') return { ...state, status: state.operation === 'deploy' ? 'deployed' : 'passed', retryMode: 'new-intent', pollError: undefined }
  if (operation.status === 'failed' || operation.status === 'canceled') {
    return { ...state, status: 'failed', retryMode: 'new-intent', error: operation.lastError ?? operation.message ?? `Operation ${operation.status}`, pollError: undefined }
  }
  return { ...state, pollError: undefined }
}

export function upsertStep(steps: DeployStep[], incoming: DeployStep): DeployStep[] {
  const idx = steps.findIndex((s) => s.step === incoming.step)
  if (idx >= 0) {
    const updated = [...steps]
    updated[idx] = { ...updated[idx], ...incoming, events: updated[idx].events }
    return updated
  }
  return [...steps, incoming]
}

export function appendStepEvent(steps: DeployStep[], stepName: string, event: StepEvent): DeployStep[] {
  const idx = steps.findIndex((s) => s.step === stepName)
  if (idx < 0) return steps
  const updated = [...steps]
  updated[idx] = { ...updated[idx], events: [...(updated[idx].events ?? []), event] }
  return updated
}

export function deployProgressReducer(state: DeployProgressState | null, event: HubEvent, now = Date.now()): DeployProgressState | null {
  if (state?.appId && event.appId && state.appId !== event.appId) return state
  const eventPayload = event.payload && typeof event.payload === 'object' ? event.payload as Record<string, unknown> : {}
  const eventSagaId = typeof eventPayload.sagaId === 'string' ? eventPayload.sagaId : undefined
  const isRunEvent = event.type.startsWith('deploy.') || event.type.startsWith('preflight.')
  if (state?.sagaId && isRunEvent && eventSagaId !== state.sagaId) return state
  if (state && ['deployed', 'passed', 'failed'].includes(state.status)) return state
  if (event.type === 'deploy.step' || event.type === 'preflight.step') {
    const operation = event.type.startsWith('preflight') ? 'preflight' : 'deploy'
    const payload = event.payload as { step: string; status: string; sagaId?: string }
    if (state?.sagaId && payload.sagaId && state.sagaId !== payload.sagaId) return state
    return {
      ...state,
      appId: event.appId ?? state?.appId ?? '',
      operation,
      steps: upsertStep(state?.steps ?? [], {
        step: payload.step,
        status: payload.status,
      }),
      status: payload.status,
      sagaId: payload.sagaId || state?.sagaId,
    }
  }

  if (event.type === 'deploy.completed') return state ? { ...state, status: 'deployed' } : null
  if (event.type === 'preflight.completed') return state ? { ...state, status: 'passed' } : null
  if (event.type === 'deploy.failed' || event.type === 'preflight.failed') {
    const payload = event.payload as { error?: string; message?: string }
    return state ? { ...state, status: 'failed', retryMode: 'new-intent', error: payload.error ?? payload.message } : null
  }

  if (event.type === 'deploy.progress' || event.type === 'preflight.progress') {
    const payload = event.payload as { step?: string; message?: string; allocId?: string; node?: string; allocStatus?: string }
    if (!state || !payload.step) return state
    return {
      ...state,
      steps: appendStepEvent(state.steps, payload.step, {
        message: payload.message ?? '',
        timestamp: now,
        allocId: payload.allocId,
        node: payload.node,
        allocStatus: payload.allocStatus,
      }),
    }
  }

  return state
}

export function useDeployProgress() {
  const [deployState, setDeployState] = useState<DeployProgressState | null>(null)
  const applyDeployEvent = useCallback((event: HubEvent) => {
    setDeployState((prev) => deployProgressReducer(prev, event))
  }, [])
  return { deployState, setDeployState, applyDeployEvent }
}
