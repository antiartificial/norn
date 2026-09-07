export interface RepoSpec {
  url: string
  branch?: string
  autoDeploy?: boolean
  repoWeb?: string
}

export interface Process {
  port?: number
	hostPort?: number
  command?: string
  schedule?: string
  function?: {
    timeout?: string
    memory?: number
  }
  health?: {
    path: string
    interval?: string
    timeout?: string
  }
  metrics?: {
    enabled?: boolean
    path?: string
    port?: number
  }
  scaling?: {
    min?: number
    max?: number
    perRegion?: number
  }
  resources?: {
    cpu?: number
    memory?: number
  }
	regions?: string[]
	singleton?: boolean
}

export interface Endpoint {
  url: string
  region?: string
}

export interface InfraSpec {
  name: string
  deploy?: boolean
  processes: Record<string, Process>
	primaryRegion?: string
	regions?: Record<string, { nomadRegion?: string; datacenters?: string[]; trafficWeight?: number }>
	placement?: { nodePool: string }
  services?: string[]
  secrets?: string[]
  migrations?: string
  env?: Record<string, string>
  repo?: RepoSpec
  build?: {
    dockerfile?: string
    test?: string
  }
  infrastructure?: {
    kafka?: { topics?: string[] }
    postgres?: { database: string }
    redis?: { namespace?: string }
    nats?: { streams?: string[] }
    objectStorage?: {
      provider?: string
      buckets?: Array<{
        name: string
        access?: string
        public?: boolean
        prefix?: string
        env?: string
      }>
    }
  }
  snapshots?: {
    keep?: number
    preRestore?: boolean
    retentionEnabled?: boolean
    exportBucket?: string
  }
  endpoints?: Endpoint[]
}

export interface Allocation {
  id: string
  taskGroup: string
  status: string
  lifecycle: 'active' | 'retained'
  healthy?: boolean
  nodeId?: string
  nodeAddress?: string
  nodeName?: string
  nodeProvider?: string // local, do, hz, remote
  nodeRegion?: string
  startedAt?: string
}

export interface ProcessAllocationCount {
  running: number
  active: number
  retained: number
  total: number
}

export interface AllocationSummary {
  running: number
  active: number
  retained: number
  total: number
  byProcess?: Record<string, ProcessAllocationCount>
  byStatus?: Record<string, number>
}

export interface AppStatus {
  spec: InfraSpec
  nomadStatus: string
  healthy: boolean
  allocations: Allocation[]
  allocationSummary: AllocationSummary
}

export interface SagaEvent {
  id: string
  sagaId: string
  timestamp: string
  source: string
  app: string
  category: string
  action: string
  message: string
  metadata?: Record<string, string>
}

export interface Deployment {
  id: string
  app: string
  commitSha: string
  imageTag: string
  sagaId: string
  status: string
  startedAt: string
  finishedAt?: string
  regions?: Array<{ region: string; nomadRegion: string; status: string; desiredWeight: number; activeWeight: number; evalId?: string; lastError?: string; updatedAt: string }>
}

export interface DeploymentListResponse {
  schemaVersion: 'norn.deployments/v1'
  deployments: Deployment[]
  count: number
  offset?: number
}

export interface DeploymentStepRecord {
  deploymentId: string
  app: string
  sagaId: string
  step: string
  status: 'running' | 'complete' | 'failed'
  kind?: 'readonly' | 'mutable'
  attempt?: number
  startedAt: string
  finishedAt?: string
  durationMs?: number
  message?: string
  metadata?: Record<string, unknown>
}

export interface DeploymentStepListResponse {
  schemaVersion: 'norn.deployment-steps/v1'
  deploymentId: string
  steps: DeploymentStepRecord[]
  count: number
}

export type EventSeverity = 'info' | 'warning' | 'critical'

export interface CorrelatedIncident {
  correlationKey: string
  source: string
  app: string
  environment?: string
  latestSeverity: EventSeverity
  latestType: string
  latestTitle: string
  eventCount: number
  firstSeen: string
  lastSeen: string
  openCount: number
  latestEventId: string
}

export interface BeaconEvent {
  id: string
  source?: string
  app: string
  environment?: string
  type: string
  severity: EventSeverity
  state: string
  title: string
  body?: string
  dedupeKey?: string
  occurredAt: string
  acknowledgedAt?: string
  acknowledgedBy?: string
  acknowledgementNote?: string
  snoozedUntil?: string
  metadata?: Record<string, unknown>
}

export interface Operation {
  id?: string
  sagaId?: string
  kind?: string
  app?: string
  ref?: string
  source?: string
  status?: string
  attempt?: number
  attempts?: number
  maxAttempts?: number
  risk?: string
  message?: string
  lastError?: string
  nextAttemptAt?: string
  createdAt?: string
  startedAt?: string
  updatedAt?: string
  finishedAt?: string
	payload?: Record<string, unknown>
	metadata?: Record<string, unknown>
}

export interface AppSnapshot {
  filename: string
  database: string
  commitSha?: string
  timestamp: string
  createdAt?: string
  size: number
}

export interface EventsResponse {
  events: BeaconEvent[]
  total?: number
}

export interface ActiveIncidentsResponse {
  incidents: CorrelatedIncident[]
}

export interface CorrelatedEventsResponse {
  correlationKey: string
  events: BeaconEvent[]
}

export interface OperationsResponse {
  count: number
  operations: Operation[]
}

export interface ValidationFinding {
  severity: 'error' | 'warning' | 'info'
  code: string
  field: string
  message: string
  remediation?: string
}

export interface FleetNodePool {
  size: string
  min: number
  desired: number
  max: number
  labels?: Record<string, string>
  replacement?: {
    strategy?: 'blueGreen' | 'rolling'
    requireCapacityHeadroom?: boolean
    drainTimeout?: string
    requireReadiness?: boolean
  }
}

export interface FleetInventory {
  schemaVersion: 'norn.fleet-inventory/v1'
  configured: boolean
  source?: string
  digest?: string
  document?: {
    apiVersion: 'norn.dev/fleet/v1'
    kind: 'Cluster'
    metadata?: { repository?: string; environment?: string; workflowUrl?: string }
    cluster: { name: string; provider: string; region: string }
  }
  validation?: { schemaVersion: string; documentKind: 'fleet'; name?: string; valid: boolean; findings: ValidationFinding[] }
  nodePools: Record<string, FleetNodePool>
}

export interface FleetPlansResponse {
  count: number
  plans: Operation[]
}

export interface FleetGitHubStatus {
  schemaVersion: 'norn.fleet-github-status/v1'
  configured: boolean
  connected: boolean
  repository?: string
  installationId?: number
  defaultBranch?: string
  configPath?: string
  planWorkflow?: string
  applyWorkflow?: string
  message?: string
}

export interface FleetReconciliationResponse {
  schemaVersion: 'norn.fleet-reconciliation/v1'
  planId: string
  count: number
  reconciliations: Operation[]
}

export type FleetRunnerAttemptStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'abandoned'

export interface FleetTimingRange {
  lowMs: number
  highMs: number
}

export interface FleetTimingProvenance {
  method: 'configured_range' | 'unavailable'
  configuredRange?: FleetTimingRange
  sampleCount: number
  successfulSampleCount: number
  exclusions: string[]
}

export interface FleetPhaseTiming {
  name: string
  state: 'active' | 'complete' | 'terminal'
  elapsedMs: number
  estimatedRemaining?: FleetTimingRange
}

export interface FleetRunnerTiming {
  schemaVersion: 'norn.fleet-timing/v1'
  scope: 'runner_attempt'
  asOf: string
  availability: 'available' | 'unavailable'
  operationClass: 'cold_start' | 'unknown'
  elapsedMs: number
  estimatedRemaining?: FleetTimingRange
  estimatedTotal?: FleetTimingRange
  estimatedCompletion?: { earliestAt: string; latestAt: string }
  confidence: 'low' | 'none'
  provenance: FleetTimingProvenance
  phases: FleetPhaseTiming[]
}

export interface FleetRunnerAttempt {
  schemaVersion: 'norn.fleet-runner-attempt/v1'
  id: string
  planId: string
  attempt: number
  rootAttemptId: string
  runnerAttemptId?: string
  sourceDispatchRunId: number
  pilotRunId: string
  recovery?: boolean
  status: FleetRunnerAttemptStatus
  currentPhase: string
  commitSha: string
  planSha256: string
  workflowUrl?: string
  retryOf?: string
  heartbeatSequence: number
  heartbeatTimeoutSeconds: number
  revision: number
  startedAt: string
  phaseStartedAt?: string
  heartbeatAt: string
  heartbeatExpiresAt: string
  updatedAt: string
  finishedAt?: string
  lastError?: string
  timing?: FleetRunnerTiming
}

export interface FleetRunnerAttemptResponse {
  schemaVersion: 'norn.fleet-runner-attempt/v1'
  planId: string
  attempts: FleetRunnerAttempt[]
  count: number
  serverTime: string
}

export interface VersionResponse {
  version: string
}

export interface CapabilitiesResponse {
  protocolVersion: number
  serverVersion: string
  features: string[]
  authority?: 'fleet-only' | string
  environment?: {
    id: 'development' | 'staging' | 'production' | string
    profile: 'development' | 'production' | string
  }
  auth?: {
    scopes: string[]
    principal?: {
      authenticated: boolean
      subject?: string
      deviceId?: string
      scopes: string[]
      expiresAt?: string
      legacy?: boolean
    }
  }
  endpoints?: Record<string, string>
}

/** Immutable staging evidence that may be promoted by a production control plane. */
export interface ReleaseQualification {
  schemaVersion: 'norn.release-qualification/v2'
  id: string
  deploymentId: string
  app: string
  sourceSha: string
  artifact: string
  environment: string
  issuedAt: string
  expiresAt: string
  keyId: string
  signature: string
  candidate: ReleaseCandidate
  dsse: DSSEEnvelope
}

export interface ReleaseCandidate {
  provider: 'github-actions' | string
  repository: string
  repositoryId: string
  ownerId: string
  repositoryVisibility?: 'public' | 'private' | 'internal' | string
  runId: string
  runAttempt?: string
  workflowRef: string
  workflowSha: string
  signerWorkflowRef: string
  signerWorkflowSha: string
  ref: string
  attestation: {
    mode?: 'github-public' | 'github-private' | 'norn-signed-private' | string
    issuer: string
    subjectDigest: string
    materialSha: string
    /** Display-only verifier label; never contains a credential or installation ID. */
    verifier?: string
    /** Backwards-compatible display-only verifier label. */
    verifierIdentity?: string
    provenanceUri?: string
    sbomUri?: string
    bundle?: {
      schemaVersion: 'norn.private-release-attestation/v1'
      keyId: string
      provenance: DSSEEnvelope
      sbom: DSSEEnvelope
    }
  }
}

export interface DSSEEnvelope {
  payloadType: string
  payload: string
  signatures: Array<{ keyid: string; sig: string }>
}

export interface ReleaseQualificationResponse {
  schemaVersion: 'norn.release-qualifications/v2'
  qualifications: ReleaseQualification[]
  count: number
}

export interface ReleaseActionRequest {
  sourceSha: string
  artifact?: string
}

export interface WSEvent {
  type: string
  appId: string
  payload: unknown
}

export interface NotificationChannel {
  id: string
  provider: string
  name: string
  url: string
  token?: string
  userKey?: string
  severities?: string[]
  createdAt: string
}

export interface DeployGroup {
  name: string
  apps: Array<{
    app: string
    waitReady?: boolean
  }>
}

export interface RemoteSnapshot {
  key: string
  size: number
  lastModified: string
}

export interface CanaryStatus {
  id?: string
  jobId?: string
  status: string
  statusDescription?: string
  isCanary?: boolean
}

export interface AccessGrant {
  id: string
  ip: string
  note: string
  createdBy: string
  createdAt: string
  expiresAt: string
}

export interface ServiceManifestEntry {
  name: string
  app: string
  process: string
  type: string
  status: string
  healthPath?: string
  reachability: {
    endpointScope: string
    instanceScope: string
    exposure: string
    routable: boolean
  }
  endpoints?: Array<{ url: string; region?: string }>
  instances?: Array<{
    id?: string
    allocationId?: string
    node: string
    address: string
    port: number
    status: string
    region?: string
    nodePool?: string
    placementSource?: 'consul-tags' | 'local-runtime' | 'unverified'
    placementVerified: boolean
  }>
  metadata?: Record<string, string>
}

export interface ServiceManifest {
  version: number
  generatedAt: string
  networkMode: string
  services: ServiceManifestEntry[]
}

export interface AccessPattern {
  app: string
  process: string
  type: string
  status: string
  endpoints?: string[]
  sources?: string[]
  windowHours: number
  totalRequests: number
  successes: number
  clientErrors: number
  serverErrors: number
  firstSeen?: string
  lastSeen?: string
  quietForHours?: number
  activeHours: number
  activeWeekdays?: number[]
  peakHourUtc?: number
  hourlyUtc: Record<string, number>
  weekdayUtc: Record<string, number>
  idleCandidate: boolean
  idleReason?: string
  recommendedAction: string
  confidence: string
}

export interface AccessPatternResponse {
  windowHours: number
  idleAfterHours: number
  patterns: AccessPattern[]
}

export interface EvaluatorNamespaceReadiness {
  namespace: string
  evaluator: string
  provider: string
  dryRun: boolean
  providerKeyRequired: boolean
  providerKeyConfigured: boolean
  mutationAllowed: boolean
  smokeOk?: boolean
  smokeError?: string
  ready: boolean
  blockers: string[]
}

export interface EvaluatorReadiness {
  generatedAt: string
  namespaces: EvaluatorNamespaceReadiness[]
  overallReady: boolean
  summary: string
}
