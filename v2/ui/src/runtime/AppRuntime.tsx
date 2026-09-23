import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, apiAuthority, apiFetch } from '../lib/api.ts'
import { clearDurableIntent, durableIntent } from '../lib/durableIntent.ts'
import { appGroups } from '../lib/format.ts'
import { applyDurableOperationSnapshot, useDeployProgress } from '../hooks/useDeployProgress.ts'
import { useHubEvents } from '../hooks/useHubEvents.ts'
import { DeployPanel } from '../components/DeployPanel.tsx'
import { ScaleModal } from '../components/ScaleModal.tsx'
import { useToast } from '../components/ui/index.ts'
import type { AccessPattern, AccessPatternResponse, AppStatus, CapabilitiesResponse, Operation, ServiceManifest, VersionResponse } from '../types/index.ts'
import type { HubEvent } from '../types/ws.ts'

export type AppAction = 'preflight' | 'deploy' | 'restart'

export interface DeploymentStep { step?: string; kind?: string; status?: string; attempt?: number; durationMs?: number; message?: string }
export interface ActivityEntry { id: number; event: HubEvent; capturedAt: string }
interface AppActionAcceptance { sagaId?: string; operationId?: string; status?: string; replayed?: boolean; message?: string }

const acceptedStatuses = new Set(['queued', 'running', 'succeeded', 'failed', 'canceled'])

function validateAppActionAcceptance(value: AppActionAcceptance): AppActionAcceptance {
  if (!value.operationId || !value.status || !acceptedStatuses.has(value.status)) {
    throw new Error('Server returned an invalid durable operation acceptance')
  }
  return value
}

function isDefinitiveRejection(error: unknown): boolean {
  return error instanceof ApiError && [400, 401, 403, 404, 409, 422].includes(error.status)
}

export interface RuntimeContext {
  apps: AppStatus[]
  loading: boolean
  error: string | null
  refetch: () => void
  serviceManifest?: ServiceManifest
  accessPatterns: AccessPattern[]
  activeIngress: Set<string>
  activity: ActivityEntry[]
  deployState: ReturnType<typeof useDeployProgress>['deployState']
  setDeployState: ReturnType<typeof useDeployProgress>['setDeployState']
  mutations: ReturnType<typeof useAppMutations>
  toggleEndpoint: (appId: string, hostname: string, enabled: boolean) => void
  toggleDeployment: (appId: string, enabled: boolean) => Promise<void>
  fleetAvailable: boolean
  appRecoveryAvailable: boolean
  environment: { id: string; profile: string }
  releasePipelineAvailable: boolean
}

const DeployProgressContext = createContext<ReturnType<typeof useDeployProgress> | null>(null)
const RuntimeContextObject = createContext<RuntimeContext | null>(null)

export function useDeployProgressContext() {
  const value = useContext(DeployProgressContext)
  if (!value) throw new Error('deploy progress unavailable')
  return value
}

export function useRuntimeContext() {
  const value = useContext(RuntimeContextObject)
  if (!value) throw new Error('runtime context unavailable')
  return value
}

function useAppsQuery() {
  return useQuery({
    queryKey: ['apps'],
    queryFn: () => apiFetch<AppStatus[]>('/api/apps'),
    staleTime: 10_000,
    refetchInterval: 15_000,
  })
}

function useServiceManifestQuery() {
  return useQuery({
    queryKey: ['services', 'manifest'],
    queryFn: () => apiFetch<ServiceManifest>('/api/services/manifest'),
    staleTime: 20_000,
  })
}

function useAccessPatternsQuery() {
  return useQuery({
    queryKey: ['access', 'patterns'],
    queryFn: () => apiFetch<AccessPatternResponse>('/api/access/patterns'),
    staleTime: 60_000,
  })
}

function useCapabilitiesQuery() {
  return useQuery({
    queryKey: ['capabilities'],
    queryFn: () => apiFetch<CapabilitiesResponse>('/api/v1/capabilities'),
    staleTime: 60_000,
  })
}

function runKey(event: HubEvent): string | null {
  if (event.type !== 'deploy.step' && event.type !== 'preflight.step') return null
  const payload = event.payload as { sagaId?: string }
  const operation = event.type.startsWith('preflight') ? 'preflight' : 'deploy'
  const appId = event.appId ?? ''
  return `${appId}:${operation}:${payload.sagaId ?? 'unknown'}`
}

function eventAppId(event: HubEvent): string | undefined {
  return event.appId ?? ('payload' in event && event.payload && typeof event.payload === 'object' ? (event.payload as Record<string, unknown>).appId as string | undefined : undefined)
}

function isTerminalRunEvent(event: HubEvent): boolean {
  return event.type === 'deploy.completed' || event.type === 'deploy.succeeded' || event.type === 'deploy.failed' || event.type === 'preflight.completed' || event.type === 'preflight.failed'
}

export function useAppMutations(onScale: (state: { appId: string; groups: { name: string; current: number }[] }) => void) {
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const { setDeployState } = useDeployProgressContext()

  const invalidate = useCallback((appId: string) => {
    queryClient.invalidateQueries({ queryKey: ['apps'] })
    queryClient.invalidateQueries({ queryKey: ['app', appId] })
    queryClient.invalidateQueries({ queryKey: ['deployments'] })
  }, [queryClient])

  const runAppAction = useCallback(async (appId: string, action: AppAction) => {
    if (action === 'deploy' || action === 'preflight') {
      const body = { ref: 'HEAD' }
      const intent = durableIntent(`${apiAuthority()}:app-action:${appId}:${action}`, body)
      setDeployState({ appId, operation: action, steps: [], status: 'requesting', retryMode: 'same-intent' })
      try {
        const accepted = validateAppActionAcceptance(await apiFetch<AppActionAcceptance>(`/api/apps/${encodeURIComponent(appId)}/${action}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Idempotency-Key': intent.key },
          body: JSON.stringify(body),
        }))
        clearDurableIntent(intent)
        const terminalFailure = accepted.status === 'failed' || accepted.status === 'canceled'
        const terminalSuccess = accepted.status === 'succeeded'
        setDeployState({
          appId,
          operation: action,
          steps: [],
          status: terminalFailure ? 'failed' : terminalSuccess ? (action === 'deploy' ? 'deployed' : 'passed') : accepted.status ?? 'queued',
          sagaId: accepted.sagaId,
          operationId: accepted.operationId,
          replayed: accepted.replayed,
          retryMode: 'new-intent',
          error: terminalFailure ? accepted.message ?? `Accepted operation ${accepted.operationId ?? ''} ${accepted.status}`.trim() : undefined,
        })
        toast({
          kind: terminalFailure ? 'error' : terminalSuccess ? 'success' : 'info',
          title: terminalFailure ? `${action} ${accepted.status}` : terminalSuccess ? `${action} already complete` : `${action} accepted`,
          description: accepted.operationId ? `${appId} · operation ${accepted.operationId}` : appId,
        })
        invalidate(appId)
        return
      } catch (error) {
        const rejected = isDefinitiveRejection(error)
        const persistenceNote = intent.persistence === 'memory' ? ' Keep this tab open because browser storage is unavailable.' : ''
        setDeployState({
          appId,
          operation: action,
          steps: [],
          status: 'failed',
          retryMode: 'same-intent',
          error: rejected
            ? `Request rejected: ${error instanceof Error ? error.message : String(error)}. The same request key is retained because an earlier acceptance outcome may be unknown. Retry after resolving access or policy.${persistenceNote}`
            : `Acceptance outcome is unknown. Retry reuses the same request.${persistenceNote}`,
        })
        throw error
      }
    }
    await apiFetch(`/api/apps/${appId}/${action}`, {
      method: 'POST',
    })
    toast({ kind: 'info', title: 'Restart requested', description: appId })
    invalidate(appId)
  }, [invalidate, setDeployState, toast])

  const mutation = useMutation({
    mutationFn: ({ appId, action }: { appId: string; action: AppAction }) => runAppAction(appId, action),
    onError: (error, variables) => toast({ kind: 'error', title: `${variables.action} failed`, description: error instanceof Error ? error.message : variables.appId }),
  })

  return {
    run: (appId: string, action: AppAction) => mutation.mutate({ appId, action }),
    scale: (app: AppStatus) => onScale({ appId: app.spec.name, groups: appGroups(app) }),
    busy: mutation.isPending ? mutation.variables?.appId ?? null : null,
  }
}

function RuntimeInner({ children }: { children: (runtime: RuntimeContext & { connected: boolean; version: string }) => ReactNode }) {
  const appsQuery = useAppsQuery()
  const apps = appsQuery.data ?? []
  const serviceManifest = useServiceManifestQuery().data
  const accessPatterns = useAccessPatternsQuery().data?.patterns ?? []
  const capabilities = useCapabilitiesQuery()
  const version = useQuery({ queryKey: ['version'], queryFn: () => apiFetch<VersionResponse>('/api/version'), staleTime: 60_000 })
  const ingress = useQuery({ queryKey: ['cloudflared', 'ingress'], queryFn: () => apiFetch<{ hostnames?: string[] }>('/api/cloudflared/ingress'), staleTime: 30_000 })
  const [scaleState, setScaleState] = useState<{ appId: string; groups: { name: string; current: number }[] } | null>(null)
  const [activity, setActivity] = useState<ActivityEntry[]>([])
  const activityId = useRef(0)
  const lastToastedRun = useRef<string | null>(null)
  const { toast } = useToast()
  const queryClient = useQueryClient()
  const { deployState, setDeployState, applyDeployEvent } = useDeployProgressContext()

  useEffect(() => {
    const operationId = deployState?.operationId
    if (!operationId || ['deployed', 'passed', 'failed'].includes(deployState.status)) return
    let cancelled = false
    let timer: number | undefined
    const poll = async () => {
      try {
        const operation = await apiFetch<Operation>(`/api/v1/operations/${encodeURIComponent(operationId)}`)
        if (!cancelled) setDeployState((current) => applyDurableOperationSnapshot(current, operation))
      } catch {
        if (!cancelled) setDeployState((current) => current?.operationId === operationId ? { ...current, pollError: 'Operation status is temporarily unavailable; retrying.' } : current)
      } finally {
        if (!cancelled) timer = window.setTimeout(poll, 2_000)
      }
    }
    void poll()
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [deployState?.operationId, deployState?.status, setDeployState])

  const handleWsEvent = useCallback((event: HubEvent) => {
    setActivity((items) => [{ id: ++activityId.current, event, capturedAt: new Date().toISOString() }, ...items].slice(0, 20))
    if (event.type.startsWith('deploy.') || event.type.startsWith('preflight.')) applyDeployEvent(event)
    const appId = eventAppId(event)
    const key = runKey(event)
    if (key && key !== lastToastedRun.current) {
      lastToastedRun.current = key
      toast({ kind: 'info', title: event.type.startsWith('preflight') ? 'Preflight started' : 'Deploy started', description: appId })
    }
    if (event.type === 'deploy.completed' || event.type === 'deploy.succeeded') toast({ kind: 'success', title: 'Deploy succeeded', description: appId })
    if (event.type === 'deploy.failed') toast({ kind: 'error', title: 'Deploy failed', description: appId })
    if (isTerminalRunEvent(event)) lastToastedRun.current = null
    if (event.type === 'deploy.auto_rollback' || event.type.startsWith('rollback.')) toast({ kind: 'error', title: 'Rollback event', description: appId ?? event.type })
    if (event.type === 'canary.promoted') toast({ kind: 'success', title: 'Canary promoted', description: appId })
    if (event.type === 'beacon.event') {
      const payload = event.payload as { severity?: string; title?: string; body?: string; message?: string }
      if (payload.severity === 'critical') toast({ kind: 'error', title: payload.title ?? 'Critical incident', description: payload.body ?? payload.message })
    }
  }, [applyDeployEvent, toast])
  const { connected } = useHubEvents(handleWsEvent)

  const mutations = useAppMutations(setScaleState)
  const activeIngress = useMemo(() => new Set(ingress.data?.hostnames ?? []), [ingress.data?.hostnames])
  const fleetAvailable = ['fleet-v1', 'fleet-inventory', 'durable-fleet-capacity-plans'].every((feature) => capabilities.data?.features.includes(feature))
  const appRecoveryAvailable = capabilities.data?.features.includes('durable-app-recovery-v1') === true
  const environment = capabilities.data?.environment ?? { id: 'development', profile: 'development' }
	const releasePipelineAvailable = ['release-provenance-v1', 'release-qualifications-v2', 'release-promotions-v1'].every((feature) => capabilities.data?.features.includes(feature))

  const toggleEndpoint = useCallback(async (appId: string, hostname: string, enabled: boolean) => {
    await apiFetch(`/api/apps/${appId}/endpoints/toggle`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hostname, enabled }),
    })
    queryClient.invalidateQueries({ queryKey: ['cloudflared', 'ingress'] })
  }, [queryClient])
	const toggleDeployment = useCallback(async (appId: string, enabled: boolean) => {
		await apiFetch(`/api/v1/apps/${appId}/deployment`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }) })
		await queryClient.invalidateQueries({ queryKey: ['apps'] })
		toast({ kind: enabled ? 'success' : 'info', title: enabled ? 'Deployment enabled' : 'Deployment disabled', description: appId })
	}, [queryClient, toast])

  const runtime: RuntimeContext = {
    apps,
    loading: appsQuery.isLoading,
    error: appsQuery.error instanceof Error ? appsQuery.error.message : null,
    refetch: () => appsQuery.refetch(),
    serviceManifest,
    accessPatterns,
    activeIngress,
    activity,
    deployState,
    setDeployState,
    mutations,
    toggleEndpoint,
		toggleDeployment,
    fleetAvailable,
    appRecoveryAvailable,
    environment,
    releasePipelineAvailable,
  }

  return (
    <RuntimeContextObject.Provider value={runtime}>
      {children({ ...runtime, connected, version: version.data?.version ?? 'dev' })}
      {deployState && (
        <DeployPanel
          appId={deployState.appId}
          operation={deployState.operation}
          steps={deployState.steps}
          status={deployState.status}
          error={deployState.error}
          sagaId={deployState.sagaId}
          operationId={deployState.operationId}
          replayed={deployState.replayed}
          retryMode={deployState.retryMode}
          pollError={deployState.pollError}
          onClose={() => setDeployState(null)}
          onRetry={() => {
            const { appId, operation } = deployState
            setDeployState(null)
            mutations.run(appId, operation)
          }}
        />
      )}
      {scaleState && (
        <ScaleModal
          appId={scaleState.appId}
          groups={scaleState.groups}
          onClose={() => setScaleState(null)}
          onScaled={() => {
            setScaleState(null)
            queryClient.invalidateQueries({ queryKey: ['apps'] })
          }}
        />
      )}
    </RuntimeContextObject.Provider>
  )
}

export function AppRuntimeProvider({ children }: { children: (runtime: RuntimeContext & { connected: boolean; version: string }) => ReactNode }) {
  const deployProgress = useDeployProgress()
  return (
    <DeployProgressContext.Provider value={deployProgress}>
      <RuntimeInner>{children}</RuntimeInner>
    </DeployProgressContext.Provider>
  )
}
