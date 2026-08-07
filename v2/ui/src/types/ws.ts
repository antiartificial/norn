export interface HubEventBase<TType extends string, TPayload = Record<string, unknown>> {
  id?: number
  timestamp?: string
  type: TType
  appId?: string
  payload: TPayload
}

export interface DeployStepPayload {
  step: string
  status: string
  sagaId?: string
}

export interface DeployProgressPayload {
  step?: string
  message?: string
  allocId?: string
  node?: string
  allocStatus?: string
}

export interface ErrorPayload {
  error?: string
  message?: string
}

export type HubEvent =
  | HubEventBase<'deploy.step', DeployStepPayload>
  | HubEventBase<'deploy.progress', DeployProgressPayload>
  | HubEventBase<'deploy.completed', Record<string, unknown>>
  | HubEventBase<'deploy.succeeded', Record<string, unknown>>
  | HubEventBase<'deploy.auto_rollback', Record<string, unknown>>
  | HubEventBase<'deploy.failed', ErrorPayload>
  | HubEventBase<'preflight.step', DeployStepPayload>
  | HubEventBase<'preflight.progress', DeployProgressPayload>
  | HubEventBase<'preflight.completed', Record<string, unknown>>
  | HubEventBase<'preflight.failed', ErrorPayload>
  | HubEventBase<'snapshot.restored' | 'snapshot.retention', Record<string, unknown>>
  | HubEventBase<'function.completed', Record<string, unknown>>
  | HubEventBase<'beacon.event', { severity?: string; title?: string; message?: string; appId?: string }>
  | HubEventBase<'rollback.started' | 'rollback.completed' | 'rollback.failed', Record<string, unknown>>
  | HubEventBase<'canary.promoted', Record<string, unknown>>
  | HubEventBase<'app.restarted' | 'app.scaled', Record<string, unknown>>
  | HubEventBase<string, unknown>
