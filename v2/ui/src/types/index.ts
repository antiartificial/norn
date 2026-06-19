export interface RepoSpec {
  url: string
  branch?: string
  autoDeploy?: boolean
  repoWeb?: string
}

export interface Process {
  port?: number
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
}

export interface Endpoint {
  url: string
  region?: string
}

export interface InfraSpec {
  name: string
  deploy?: boolean
  processes: Record<string, Process>
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
  instances?: Array<{ node: string; address: string; port: number; status: string }>
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
